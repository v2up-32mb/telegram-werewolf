package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/v2up-32mb/telegram-werewolf/migrations"
	_ "modernc.org/sqlite"
)

// openTestDB 打开一个临时 SQLite 数据库并启用外键约束
// （docs/技术选型.md §8.4）。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	return db
}

// runUp 从空库执行全部迁移的 Up。
func runUp(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose SetDialect: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.Up(db, "."); err != nil {
		t.Fatalf("goose Up: %v", err)
	}
}

// runDown 回滚全部迁移（Down 后清空）。
func runDown(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("goose SetDialect: %v", err)
	}
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.DownTo(db, ".", 0); err != nil {
		t.Fatalf("goose DownTo 0: %v", err)
	}
}

// tableNames 返回 sqlite_master 中的业务表集合（不含 sqlite_* 内部表与
// goose 迁移框架的元数据表 goose_db_version）。
func tableNames(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	return names
}

var wantTables = []string{
	"users", "rooms", "room_players",
	"games", "game_players",
	"role_stats", "battle_reports", "media_cache",
	"bot_update_cursor",
	"room_settings",
}

func wantTableSet() map[string]bool {
	set := make(map[string]bool, len(wantTables))
	for _, name := range wantTables {
		set[name] = true
	}
	return set
}

// TestInitialMigration 从空库执行 Up，检查 10 张表、外键与唯一约束，
// 执行 Down 后确认全部清空（docs/技术选型.md §13.4）。
func TestInitialMigration(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)

	// 10 张表全部存在。
	got := tableNames(t, db)
	for _, name := range wantTables {
		if !got[name] {
			t.Errorf("迁移后缺少表 %s", name)
		}
	}
	if len(got) != len(wantTables) {
		t.Errorf("表数量 = %d, want %d（got=%v）", len(got), len(wantTables), got)
	}

	// 种子数据：外键引用目标。
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(context.Background(), sql, args...); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}
	seed(`INSERT INTO users (telegram_id, nickname) VALUES (101, 'host'), (102, 'u102'), (103, 'u103')`)
	seed(`INSERT INTO rooms (room_code, host_user_id, phase) VALUES ('ABCDEF', 101, 'lobby')`)

	// 外键约束生效：rooms.host_user_id 必须引用已存在用户。
	if _, err := db.Exec(`INSERT INTO rooms (room_code, host_user_id, phase) VALUES ('ZZZZZZ', 9999, 'lobby')`); err == nil {
		t.Error("外键约束未生效：插入不存在 host 的 rooms 行未报错")
	}

	// 房间码唯一：重复 room_code 被拒绝。
	if _, err := db.Exec(`INSERT INTO rooms (room_code, host_user_id, phase) VALUES ('ABCDEF', 102, 'lobby')`); !isUniqueViolation(err) {
		t.Errorf("房间码唯一约束未生效：err=%v", err)
	}

	seed(`INSERT INTO room_players (room_code, user_id, seat) VALUES ('ABCDEF', 102, 1)`)

	// 同房用户唯一：同一房间同一用户重复加入被拒绝。
	if _, err := db.Exec(`INSERT INTO room_players (room_code, user_id, seat) VALUES ('ABCDEF', 102, 2)`); !isUniqueViolation(err) {
		t.Errorf("同房用户唯一约束未生效：err=%v", err)
	}
	// 同房座位号唯一：同一房间同一座位被拒绝。
	if _, err := db.Exec(`INSERT INTO room_players (room_code, user_id, seat) VALUES ('ABCDEF', 103, 1)`); !isUniqueViolation(err) {
		t.Errorf("同房座位号唯一约束未生效：err=%v", err)
	}

	seed(`INSERT INTO media_cache (cache_key, file_id, file_type) VALUES ('card-wolf', 'fid-1', 'photo')`)
	// 媒体缓存键唯一：重复缓存键被拒绝。
	if _, err := db.Exec(`INSERT INTO media_cache (cache_key, file_id, file_type) VALUES ('card-wolf', 'fid-2', 'photo')`); !isUniqueViolation(err) {
		t.Errorf("媒体缓存键唯一约束未生效：err=%v", err)
	}

	// Down 后业务表全部清空。
	runDown(t, db)
	after := tableNames(t, db)
	for _, name := range wantTables {
		if after[name] {
			t.Errorf("Down 后表 %s 仍存在", name)
		}
	}
}

// isUniqueViolation 判断错误是否为 SQLite UNIQUE/PRIMARY KEY 约束冲突。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "constraint failed: unique")
}
