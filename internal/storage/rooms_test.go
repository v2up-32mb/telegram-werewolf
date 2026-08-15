package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// mustUser 确保用户存在（建房/入房前置条件，docs/技术选型.md §8.3）。
func mustUser(t *testing.T, db *sql.DB, id int64, nick string) {
	t.Helper()
	if err := sqlc.New(db).UpsertUser(context.Background(), sqlc.UpsertUserParams{TelegramID: id, Nickname: nick}); err != nil {
		t.Fatalf("UpsertUser(%d): %v", id, err)
	}
}

// TestRoomsCreateAtomicAndHostSeat 验证原子建房：rooms 与 room_players 同时
// 出现、房主固定 1 号且 is_host=1；重复房间码被拒且不留中间态。
func TestRoomsCreateAtomicAndHostSeat(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	mustUser(t, db, 101, "host")
	repo := storage.NewRoomRepository(db)

	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 101, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var nRooms, nPlayers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ABCDEF'`).Scan(&nRooms); err != nil {
		t.Fatalf("count rooms: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM room_players WHERE room_code='ABCDEF'`).Scan(&nPlayers); err != nil {
		t.Fatalf("count players: %v", err)
	}
	if nRooms != 1 || nPlayers != 1 {
		t.Fatalf("建房后 rooms=%d players=%d, want 1/1", nRooms, nPlayers)
	}
	var seat, isHost int64
	if err := db.QueryRow(`SELECT seat, is_host FROM room_players WHERE room_code='ABCDEF' AND user_id=101`).Scan(&seat, &isHost); err != nil {
		t.Fatalf("host row: %v", err)
	}
	if seat != 1 || isHost != 1 {
		t.Errorf("房主 seat=%d is_host=%d, want 1/1", seat, isHost)
	}

	// 重复房间码：领域错误，无中间态。
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 102, "lobby"); !errors.Is(err, storage.ErrRoomCodeTaken) {
		t.Fatalf("重复建房 err = %v, want ErrRoomCodeTaken", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ABCDEF'`).Scan(&nRooms); err != nil {
		t.Fatalf("count rooms#2: %v", err)
	}
	if nRooms != 1 {
		t.Errorf("失败建房后 rooms = %d, want 1（无中间态）", nRooms)
	}

	// 房主用户未登记：外键拒绝映射领域错误且不留行。
	if err := repo.Create(ctx, game.RoomID("ZZZZZZ"), 999, "lobby"); !errors.Is(err, storage.ErrUserNotFound) {
		t.Fatalf("未登记房主建房 err = %v, want ErrUserNotFound", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ZZZZZZ'`).Scan(&nRooms); err != nil {
		t.Fatalf("count rooms#3: %v", err)
	}
	if nRooms != 0 {
		t.Errorf("未登记房主建房后 rooms = %d, want 0（无中间态）", nRooms)
	}

	// 房主座位写入失败：整个事务回滚，rooms 无残留行。
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER zz_fail_host_seat
		BEFORE INSERT ON room_players
		WHEN NEW.is_host = 1
		BEGIN
			SELECT RAISE(ABORT, 'forced host seat failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if err := repo.Create(ctx, game.RoomID("YYYYYY"), 101, "lobby"); err == nil {
		t.Fatal("房主座位写入应失败")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='YYYYYY'`).Scan(&nRooms); err != nil {
		t.Fatalf("count rooms#4: %v", err)
	}
	if nRooms != 0 {
		t.Errorf("房主座位失败后 rooms = %d, want 0（整体回滚）", nRooms)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER zz_fail_host_seat`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
}

// TestRoomsJoinSeatAllocation 验证入房座位分配（房主 1 号，其余 2/3/4…）、
// 同用户重复、满员与房间不存在语义。
func TestRoomsJoinSeatAllocation(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	for id := int64(101); id <= 107; id++ {
		mustUser(t, db, id, "u")
	}
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 101, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 未登记用户入房：用户外键拒绝映射领域错误。
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 999); !errors.Is(err, storage.ErrUserNotFound) {
		t.Fatalf("未登记用户入房 err = %v, want ErrUserNotFound", err)
	}

	for i, id := range []int64{102, 103, 104, 105, 106} {
		seat, err := repo.Join(ctx, game.RoomID("ABCDEF"), game.UserID(id))
		if err != nil {
			t.Fatalf("Join#%d(%d): %v", i, id, err)
		}
		if want := int64(i) + 2; seat != want {
			t.Errorf("Join(%d) seat = %d, want %d（按加入顺序 2/3/4…）", id, seat, want)
		}
	}
	// 已满（1 房主 + 5）→ 第 7 人被拒。
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 107); !errors.Is(err, storage.ErrRoomFull) {
		t.Fatalf("满员入房 err = %v, want ErrRoomFull", err)
	}
	// 同用户重复入房：唯一约束映射领域错误。
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 102); !errors.Is(err, storage.ErrUserAlreadyInRoom) {
		t.Fatalf("重复入房 err = %v, want ErrUserAlreadyInRoom", err)
	}
	// 不存在房间。
	if _, err := repo.Join(ctx, game.RoomID("NOPE99"), 107); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("不存在房间入房 err = %v, want ErrRoomNotFound", err)
	}
}

// TestRoomsLeaveKeepsRoomActive 验证退房：player 行删除、房间仍活跃、
// 可继续加入他人；退不在房用户与不存在房间返回领域错误。
func TestRoomsLeaveKeepsRoomActive(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	mustUser(t, db, 101, "host")
	mustUser(t, db, 102, "u2")
	mustUser(t, db, 103, "u3")
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 101, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 102); err != nil {
		t.Fatalf("Join 102: %v", err)
	}

	if err := repo.Leave(ctx, game.RoomID("ABCDEF"), 102); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM room_players WHERE room_code='ABCDEF' AND user_id=102`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("退房后 player 行 = %d, want 0", n)
	}
	var nRooms int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ABCDEF'`).Scan(&nRooms); err != nil {
		t.Fatalf("count rooms: %v", err)
	}
	if nRooms != 1 {
		t.Errorf("退房后房间数 = %d, want 1（房间保持活跃）", nRooms)
	}
	// 空出的座位可被后续玩家占据。
	seat, err := repo.Join(ctx, game.RoomID("ABCDEF"), 103)
	if err != nil {
		t.Fatalf("Join 103 复用座位: %v", err)
	}
	if seat != 2 {
		t.Errorf("复用座位 seat = %d, want 2", seat)
	}
	// 退不在房用户与不存在房间。
	if err := repo.Leave(ctx, game.RoomID("ABCDEF"), 102); !errors.Is(err, storage.ErrUserNotInRoom) {
		t.Fatalf("退不在房用户 err = %v, want ErrUserNotInRoom", err)
	}
	if err := repo.Leave(ctx, game.RoomID("NOPE99"), 101); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("退不存在房间 err = %v, want ErrRoomNotFound", err)
	}
}

// TestRoomsListActive 验证 active 扫描只返回活跃房间。
func TestRoomsListActive(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)
	for i, code := range []string{"AAAAAA", "BBBBBB"} {
		mustUser(t, db, int64(300+i), "h")
		if err := repo.Create(ctx, game.RoomID(code), game.UserID(300+i), "lobby"); err != nil {
			t.Fatalf("Create %s: %v", code, err)
		}
	}
	rooms, err := repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rooms) != 2 {
		t.Fatalf("活跃房间数 = %d, want 2", len(rooms))
	}
	if err := repo.Leave(ctx, game.RoomID("AAAAAA"), 300); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	rooms, err = repo.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive#2: %v", err)
	}
	if len(rooms) != 2 {
		t.Errorf("玩家退房不应让房间消失：活跃数 = %d, want 2", len(rooms))
	}
}

// TestRoomsSeatConflictMappedToErrSeatTaken 验证 Join 写入恰逢目标座位已被
// 占用时，UNIQUE(room_code, seat) 冲突被映射为领域错误 ErrSeatTaken
// （task15.md Step 1 覆盖清单）。
//
// 说明：SQLite WAL 下"先读后写"事务在快照过期时先以 SQLITE_BUSY 拒绝而
// 不会进入唯一约束检查，无法用并发手段确定性触发该分支；因此用 BEFORE
// INSERT 触发器在 Join 的真实 INSERT 语句内先把 2 号座位交给其他用户，
// 使外层 INSERT 命中 UNIQUE(room_code, seat)，走与生产一致的错误映射路径。
func TestRoomsSeatConflictMappedToErrSeatTaken(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)

	mustUser(t, db, 701, "host")
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 701, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 占用座位 3..6，唯一空位仅剩 2 号。
	for _, p := range []struct {
		id   int64
		seat int64
	}{
		{703, 3}, {704, 4}, {705, 5}, {706, 6},
	} {
		mustUser(t, db, p.id, "u")
		if err := sqlc.New(db).InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{
			RoomCode: "ABCDEF", UserID: p.id, Seat: p.seat, IsHost: 0,
		}); err != nil {
			t.Fatalf("occupy seat %d: %v", p.seat, err)
		}
	}
	mustUser(t, db, 702, "racer")
	mustUser(t, db, 777, "occupier")

	// 触发器仅在插入 702 时先把同座位交给 777（777 用户已登记，外键可达）；
	// 外层 INSERT 随即命中 UNIQUE(room_code, seat)。语句级原子性保证失败时
	// 触发器行与目标行一并回滚，不留中间态。
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER force_seat_conflict
		BEFORE INSERT ON room_players
		WHEN NEW.user_id = 702
		BEGIN
			INSERT INTO room_players (room_code, user_id, seat, is_host)
			VALUES (NEW.room_code, 777, NEW.seat, 0);
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 702); !errors.Is(err, storage.ErrSeatTaken) {
		t.Fatalf("座位冲突入房 err = %v, want ErrSeatTaken", err)
	}
	// join 事务整体回滚：702 与触发器写入的 777 均无残留（无中间态）。
	for _, id := range []int64{702, 777} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM room_players WHERE room_code='ABCDEF' AND user_id=?`, id).Scan(&n); err != nil {
			t.Fatalf("count %d: %v", id, err)
		}
		if n != 0 {
			t.Errorf("冲突回滚后 user %d 行数 = %d, want 0", id, n)
		}
	}
	// 原房间状态不变：房主 1 + 占用 3..6 = 5 行。
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM room_players WHERE room_code='ABCDEF'`).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 5 {
		t.Errorf("冲突回滚后 room_players = %d, want 5（房主+4 占用）", total)
	}
}

// openConcurrentTestDB 打开与生产对齐的临时库：WAL、外键、逐连接
// busy_timeout，并把连接池限制为生产默认值 4（docs/技术选型.md §8.4）。
func openConcurrentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conc.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(20000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(storage.DefaultMaxOpenConns)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	return db
}

// TestRoomsConcurrentJoinAllocatesUniqueSeats 验证并发入房的正确性：
// 9 人争抢 5 个空位，最终恰好 5 人成功且座位 2..6 各一（无碰撞），其余
// 4 人返回 ErrRoomFull，绝不泄漏 SQLITE_BUSY 等驱动文本（docs/技术选型.md
// §13.4 并发写入覆盖；回归单语句原子分配的并发语义）。
func TestRoomsConcurrentJoinAllocatesUniqueSeats(t *testing.T) {
	db := openConcurrentTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)
	for id := int64(801); id <= 810; id++ {
		mustUser(t, db, id, "u")
	}
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 801, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	seats := map[int64]int64{} // user -> 座位
	var unexpected []error
	for id := int64(802); id <= 810; id++ {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			seat, err := repo.Join(ctx, game.RoomID("ABCDEF"), game.UserID(uid))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !errors.Is(err, storage.ErrRoomFull) {
					unexpected = append(unexpected, err)
				}
				return
			}
			seats[uid] = seat
		}(id)
	}
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("存在非领域错误（BUSY/驱动文本泄漏）：%v", unexpected)
	}
	if len(seats) != 5 {
		t.Fatalf("成功入房数 = %d, want 5", len(seats))
	}
	seen := map[int64]bool{}
	for uid, seat := range seats {
		if seat < 2 || seat > 6 {
			t.Errorf("user %d 座位 = %d, want 2..6", uid, seat)
		}
		if seen[seat] {
			t.Errorf("座位 %d 被重复分配", seat)
		}
		seen[seat] = true
	}
}
