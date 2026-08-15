package storage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// TestQueriesUserUpsert 验证用户 upsert：同 telegram_id 重复写入更新昵称，
// 不产生重复行；可按键读取。
func TestQueriesUserUpsert(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 101, Nickname: "alice"}); err != nil {
		t.Fatalf("UpsertUser#1: %v", err)
	}
	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 101, Nickname: "alice2"}); err != nil {
		t.Fatalf("UpsertUser#2: %v", err)
	}
	u, err := q.GetUser(ctx, 101)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if u.Nickname != "alice2" {
		t.Errorf("nickname = %q, want alice2（upsert 应更新昵称）", u.Nickname)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE telegram_id = 101`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("users 行数 = %d, want 1（upsert 不得产生重复行）", n)
	}
}

// TestQueriesRoomAtomicJoin 验证房间原子加入：房间存在时可加入；房间不存在
// 时外键拒绝；同房用户/同房座位重复被唯一约束拒绝。
func TestQueriesRoomAtomicJoin(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 101, Nickname: "host"}); err != nil {
		t.Fatalf("upsert host: %v", err)
	}
	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 102, Nickname: "p2"}); err != nil {
		t.Fatalf("upsert p2: %v", err)
	}
	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 103, Nickname: "p3"}); err != nil {
		t.Fatalf("upsert p3: %v", err)
	}
	if err := q.CreateRoom(ctx, sqlc.CreateRoomParams{RoomCode: "ABCDEF", HostUserID: 101, Phase: "lobby"}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if err := q.InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{RoomCode: "ABCDEF", UserID: 102, Seat: 1, IsHost: 0}); err != nil {
		t.Fatalf("InsertRoomPlayer: %v", err)
	}

	// 房间不存在：外键拒绝，加入失败（原子性）。
	err := q.InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{RoomCode: "NOPE99", UserID: 103, Seat: 2, IsHost: 0})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed") {
		t.Errorf("不存在房间加入 err = %v, want foreign key constraint failed", err)
	}
	// 同房用户重复：唯一约束拒绝。
	err = q.InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{RoomCode: "ABCDEF", UserID: 102, Seat: 2, IsHost: 0})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
		t.Errorf("同房用户重复 err = %v, want unique constraint failed", err)
	}
	// 同房座位重复：唯一约束拒绝。
	err = q.InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{RoomCode: "ABCDEF", UserID: 103, Seat: 1, IsHost: 0})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unique constraint failed") {
		t.Errorf("同房座位重复 err = %v, want unique constraint failed", err)
	}
}

// TestQueriesListActiveRooms 验证 active 扫描：只返回活跃房间，删除不再返回。
func TestQueriesListActiveRooms(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	for i, code := range []string{"ABCDEF", "GHJKLM"} {
		if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: int64(200 + i), Nickname: "h"}); err != nil {
			t.Fatalf("upsert host: %v", err)
		}
		if err := q.CreateRoom(ctx, sqlc.CreateRoomParams{RoomCode: code, HostUserID: int64(200 + i), Phase: "lobby"}); err != nil {
			t.Fatalf("CreateRoom %s: %v", code, err)
		}
	}
	rooms, err := q.ListActiveRooms(ctx)
	if err != nil {
		t.Fatalf("ListActiveRooms: %v", err)
	}
	if len(rooms) != 2 {
		t.Fatalf("活跃房间数 = %d, want 2", len(rooms))
	}
	if err := q.DeleteRoom(ctx, "ABCDEF"); err != nil {
		t.Fatalf("DeleteRoom: %v", err)
	}
	rooms, err = q.ListActiveRooms(ctx)
	if err != nil {
		t.Fatalf("ListActiveRooms#2: %v", err)
	}
	if len(rooms) != 1 || rooms[0].RoomCode != "GHJKLM" {
		t.Errorf("删除后活跃房间 = %+v, want 仅 GHJKLM", rooms)
	}
}

// TestQueriesMediaCache 验证媒体缓存：按键写入与读取，重复键更新不产生重复行。
func TestQueriesMediaCache(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	if err := q.UpsertMediaCache(ctx, sqlc.UpsertMediaCacheParams{CacheKey: "card-wolf", FileID: "fid-1", FileType: "photo"}); err != nil {
		t.Fatalf("UpsertMediaCache#1: %v", err)
	}
	m, err := q.GetMediaCache(ctx, "card-wolf")
	if err != nil {
		t.Fatalf("GetMediaCache: %v", err)
	}
	if m.FileID != "fid-1" {
		t.Errorf("file_id = %q, want fid-1", m.FileID)
	}
	if err := q.UpsertMediaCache(ctx, sqlc.UpsertMediaCacheParams{CacheKey: "card-wolf", FileID: "fid-2", FileType: "photo"}); err != nil {
		t.Fatalf("UpsertMediaCache#2: %v", err)
	}
	m, err = q.GetMediaCache(ctx, "card-wolf")
	if err != nil {
		t.Fatalf("GetMediaCache#2: %v", err)
	}
	if m.FileID != "fid-2" {
		t.Errorf("file_id = %q, want fid-2（重复键应更新）", m.FileID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM media_cache WHERE cache_key = 'card-wolf'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("media_cache 行数 = %d, want 1", n)
	}
}

// TestQueriesRoleStatsAndReports 验证战绩读取：role_stats 写入与按用户读取，
// 战报写入与对局关联。
func TestQueriesRoleStatsAndReports(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	if err := q.UpsertUser(ctx, sqlc.UpsertUserParams{TelegramID: 303, Nickname: "p"}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if err := q.UpsertRoleStat(ctx, sqlc.UpsertRoleStatParams{UserID: 303, Role: "wolf", Wins: 2, Losses: 1, Plays: 3}); err != nil {
		t.Fatalf("UpsertRoleStat: %v", err)
	}
	if err := q.UpsertRoleStat(ctx, sqlc.UpsertRoleStatParams{UserID: 303, Role: "villager", Wins: 1, Losses: 0, Plays: 1}); err != nil {
		t.Fatalf("UpsertRoleStat#2: %v", err)
	}
	stats, err := q.GetRoleStats(ctx, 303)
	if err != nil {
		t.Fatalf("GetRoleStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("role_stats 行数 = %d, want 2", len(stats))
	}
	if stats[0].Role != "villager" || stats[1].Role != "wolf" {
		t.Errorf("stats 顺序 = %+v, want villagers→wolf（按角色排序）", stats)
	}
	if stats[1].Wins != 2 {
		t.Errorf("wolf wins = %d, want 2", stats[1].Wins)
	}

	// 对局与战报：创建对局后写入战报并读取。
	gid, err := q.CreateGame(ctx, sqlc.CreateGameParams{RoomCode: "ABCDEF", Phase: "day_vote"})
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if gid <= 0 {
		t.Fatalf("CreateGame id = %d, want > 0", gid)
	}
	if err := q.InsertBattleReport(ctx, sqlc.InsertBattleReportParams{GameID: gid, Content: "report-1"}); err != nil {
		t.Fatalf("InsertBattleReport: %v", err)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM battle_reports WHERE game_id = ?`, gid).Scan(&content); err != nil {
		t.Fatalf("read battle report: %v", err)
	}
	if content != "report-1" {
		t.Errorf("battle report content = %q, want report-1", content)
	}
}

// TestQueriesUpdateCursor 验证 bot_update_cursor 单行 upsert：重复写入仅
// 更新单行，读取返回最新游标。
func TestQueriesUpdateCursor(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	q := sqlc.New(db)
	ctx := context.Background()

	if err := q.UpsertUpdateCursor(ctx, 42); err != nil {
		t.Fatalf("UpsertUpdateCursor#1: %v", err)
	}
	cur, err := q.GetUpdateCursor(ctx)
	if err != nil {
		t.Fatalf("GetUpdateCursor: %v", err)
	}
	if cur.UpdateID != 42 {
		t.Errorf("update_id = %d, want 42", cur.UpdateID)
	}
	if err := q.UpsertUpdateCursor(ctx, 99); err != nil {
		t.Fatalf("UpsertUpdateCursor#2: %v", err)
	}
	cur, err = q.GetUpdateCursor(ctx)
	if err != nil {
		t.Fatalf("GetUpdateCursor#2: %v", err)
	}
	if cur.UpdateID != 99 {
		t.Errorf("update_id = %d, want 99（重复 upsert 应更新单行）", cur.UpdateID)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bot_update_cursor`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("bot_update_cursor 行数 = %d, want 1（单行游标）", n)
	}
}
