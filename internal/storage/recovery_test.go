package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// TestRecoveryListInterruptedRoomsOnStartup 验证启动扫描：返回全部遗留
// 活跃房间及其参与者快照（含座位与是否房主，docs/技术选型.md §8.3）。
func TestRecoveryListInterruptedRoomsOnStartup(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)

	mustUser(t, db, 501, "host1")
	if err := repo.Create(ctx, game.RoomID("AAAAAA"), 501, "lobby"); err != nil {
		t.Fatalf("Create AAAAAA: %v", err)
	}
	mustUser(t, db, 502, "p2")
	if _, err := repo.Join(ctx, game.RoomID("AAAAAA"), 502); err != nil {
		t.Fatalf("Join 502: %v", err)
	}
	mustUser(t, db, 503, "host2")
	if err := repo.Create(ctx, game.RoomID("BBBBBB"), 503, "lobby"); err != nil {
		t.Fatalf("Create BBBBBB: %v", err)
	}

	rec := storage.NewRecoveryRepository(db)
	got, err := rec.ListInterruptedRoomsOnStartup(ctx)
	if err != nil {
		t.Fatalf("ListInterruptedRoomsOnStartup: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("遗留活跃房间数 = %d, want 2", len(got))
	}
	byCode := map[string]storage.InterruptedRoom{}
	for _, r := range got {
		byCode[r.Room.RoomCode] = r
	}

	a := byCode["AAAAAA"]
	if a.Room.RoomCode != "AAAAAA" || a.Room.HostUserID != 501 || a.Room.Phase != "lobby" {
		t.Errorf("房间 AAAAAA 字段 = %+v, want host=501 phase=lobby", a.Room)
	}
	players := map[int64]sqlc.RoomPlayer{}
	for _, p := range a.Players {
		players[p.UserID] = p
	}
	if len(players) != 2 {
		t.Errorf("房间 AAAAAA 玩家数 = %d, want 2", len(players))
	}
	if h := players[501]; h.Seat != 1 || h.IsHost != 1 {
		t.Errorf("房主 player = %+v, want seat=1 is_host=1", h)
	}
	if p := players[502]; p.Seat != 2 || p.IsHost != 0 {
		t.Errorf("加入者 player = %+v, want seat=2 is_host=0", p)
	}

	b := byCode["BBBBBB"]
	if b.Room.RoomCode != "BBBBBB" || b.Room.HostUserID != 503 {
		t.Errorf("房间 BBBBBB 字段 = %+v, want host=503", b.Room)
	}
	if len(b.Players) != 1 || b.Players[0].UserID != 503 || b.Players[0].Seat != 1 {
		t.Errorf("房间 BBBBBB 玩家 = %+v, want 仅房主 seat=1", b.Players)
	}
}

// TestRecoveryMarkInterrupted 验证重启中止清场：games 出现 aborted=1
// 记录（phase 保留）、rooms/room_players 清空；房间不存在返回领域错误。
func TestRecoveryMarkInterrupted(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)
	mustUser(t, db, 601, "host")
	mustUser(t, db, 602, "p")
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 601, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 602); err != nil {
		t.Fatalf("Join 602: %v", err)
	}

	rec := storage.NewRecoveryRepository(db)
	if err := rec.MarkInterrupted(ctx, game.RoomID("ABCDEF")); err != nil {
		t.Fatalf("MarkInterrupted: %v", err)
	}

	for table, want := range map[string]int{"rooms": 0, "room_players": 0} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE room_code='ABCDEF'`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != want {
			t.Errorf("%s 清场后行数 = %d, want %d", table, n, want)
		}
	}
	var phase string
	var aborted int64
	if err := db.QueryRow(`SELECT phase, aborted FROM games WHERE room_code='ABCDEF'`).Scan(&phase, &aborted); err != nil {
		t.Fatalf("select games: %v", err)
	}
	if phase != "lobby" || aborted != 1 {
		t.Errorf("games 记录 phase=%q aborted=%d, want lobby/1", phase, aborted)
	}
	// 房间已清场，重复标注按不存在处理。
	if err := rec.MarkInterrupted(ctx, game.RoomID("ABCDEF")); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("重复标注 err = %v, want ErrRoomNotFound", err)
	}
	// 清场后启动扫描无遗留房间（返回非 nil 空切片）。
	left, err := rec.ListInterruptedRoomsOnStartup(ctx)
	if err != nil {
		t.Fatalf("ListInterruptedRoomsOnStartup#2: %v", err)
	}
	if left == nil || len(left) != 0 {
		t.Errorf("清场后遗留房间 = %v, want 非 nil 空切片", left)
	}
}
