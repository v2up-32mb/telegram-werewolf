package storage_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	_ "modernc.org/sqlite"
)

// openBackupSrcDB 打开与生产对齐的临时源库：WAL、外键、逐连接
// busy_timeout、连接池上限 4（docs/技术选型.md §8.4）。
func openBackupSrcDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(storage.DefaultMaxOpenConns)
	return db
}

// tableCounts 返回关键业务表的行数（离线恢复核对口径）。
func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT 'users' UNION ALL SELECT 'rooms' UNION ALL SELECT 'room_players' UNION ALL SELECT 'games' UNION ALL SELECT 'game_players' UNION ALL SELECT 'role_stats' UNION ALL SELECT 'battle_reports'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	out := map[string]int{}
	for _, name := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + name).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// seedBackupData 写入可供备份核对的多表数据。
func seedBackupData(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	mustUser(t, db, 501, "host")
	mustUser(t, db, 502, "p2")
	if err := storage.NewUserRepository(db).Upsert(ctx, 503, "p3"); err != nil {
		t.Fatalf("upsert 503: %v", err)
	}
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("ABCDEF"), 501, "lobby"); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := repo.Join(ctx, game.RoomID("ABCDEF"), 502); err != nil {
		t.Fatalf("join: %v", err)
	}
	if err := storage.NewSettlementRepository(db).SettleGame(ctx, storage.GameResult{
		RoomCode:   game.RoomID("ABCDEF"),
		Phase:      game.PhaseSettlement,
		WinnerCamp: game.CampWolf,
		Players: []storage.PlayerResult{
			{UserID: 501, Seat: 1, Role: game.RoleWolf, Camp: game.CampWolf},
			{UserID: 502, Seat: 2, Role: game.RoleVillager, Camp: game.CampGood},
		},
		Report: "report",
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
}

// TestBackupConsistentUnderConcurrentWALWrites 验证 WAL 并发读写期间
// 备份仍可完成且一致：备份文件 integrity_check=ok，离线恢复后关键行数
// 与源库一致（并发写入只更新已有行，行数保持确定）。
func TestBackupConsistentUnderConcurrentWALWrites(t *testing.T) {
	src := openBackupSrcDB(t)
	runUp(t, src)
	seedBackupData(t, src)
	ctx := context.Background()

	// 备份期间持续对已有行做 UPDATE（WAL 并发读写）。
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); ; i++ {
			select {
			case <-done:
				return
			default:
				if _, err := src.ExecContext(ctx, `UPDATE users SET points = points + 1 WHERE telegram_id = ?`, 501+i%3); err != nil {
					return
				}
			}
		}
	}()

	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := storage.Backup(ctx, src, dst); err != nil {
		close(done)
		wg.Wait()
		t.Fatalf("Backup: %v", err)
	}
	close(done)
	wg.Wait()

	restored, err := storage.Open(dst, storage.DefaultMaxOpenConns)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	var check string
	if err := restored.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Fatalf("integrity_check = %q, want ok", check)
	}
	want := tableCounts(t, src)
	got := tableCounts(t, restored)
	if len(got) != len(want) {
		t.Fatalf("备份表数量 = %d, want %d", len(got), len(want))
	}
	for table, n := range want {
		if got[table] != n {
			t.Errorf("恢复后 %s 行数 = %d, want %d", table, got[table], n)
		}
	}
}

// TestBackupReplacesExistingOutput 验证目标文件已存在时被原子替换为
// 合法备份，且失败路径不残留临时文件。
func TestBackupReplacesExistingOutput(t *testing.T) {
	src := openBackupSrcDB(t)
	runUp(t, src)
	seedBackupData(t, src)
	ctx := context.Background()

	dir := t.TempDir()
	dst := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(dst, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	if err := storage.Backup(ctx, src, dst); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	restored, err := storage.Open(dst, storage.DefaultMaxOpenConns)
	if err != nil {
		t.Fatalf("open replaced backup: %v", err)
	}
	defer restored.Close()
	var check string
	if err := restored.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if check != "ok" {
		t.Fatalf("替换后 integrity_check = %q, want ok", check)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		// 只检查快照临时文件残留；backup.db-wal/shm 是恢复参数
		// storage.Open（WAL）造成的副文件，不属于备份产物。
		if strings.HasPrefix(e.Name(), ".werewolf-backup-") {
			t.Errorf("目录残留临时快照文件 %s", e.Name())
		}
	}
}

// TestBackupRejectsMissingParent 验证目标父目录不存在时返回明确错误。
func TestBackupRejectsMissingParent(t *testing.T) {
	src := openBackupSrcDB(t)
	runUp(t, src)
	ctx := context.Background()
	dst := filepath.Join(t.TempDir(), "no-such-dir", "backup.db")
	if err := storage.Backup(ctx, src, dst); err == nil {
		t.Fatal("父目录不存在应返回错误")
	}
}
