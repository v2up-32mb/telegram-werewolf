package telegram

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

func TestDeduperAcceptAndHighWatermark(t *testing.T) {
	d := NewDeduper(8)
	if !d.Accept(1) {
		t.Fatal("Accept(1) = false, want true")
	}
	if !d.Accept(2) {
		t.Fatal("Accept(2) = false, want true")
	}
	if d.Accept(1) {
		t.Fatal("Accept(1) duplicate = true, want false（已处理 ID 不得重入）")
	}
	if !d.Accept(3) {
		t.Fatal("Accept(3) = false, want true")
	}
	if got := d.HighWatermark(); got != 3 {
		t.Fatalf("HighWatermark = %d, want 3", got)
	}
}

func TestDeduperBoundedWindowAndMonotonicWatermark(t *testing.T) {
	d := NewDeduper(2)
	for _, id := range []int64{1, 2} {
		if !d.Accept(id) {
			t.Fatalf("Accept(%d) = false, want true", id)
		}
	}
	// 容量 2：Accept(3) 淘汰最旧 1，但水位推至 3。
	if !d.Accept(3) {
		t.Fatal("Accept(3) = false, want true（新 ID 正常接受）")
	}
	if got := d.HighWatermark(); got != 3 {
		t.Fatalf("HighWatermark = %d, want 3（水位单调推进）", got)
	}
	// 历史 ID（含被淘汰的最旧 1）因水位仍拒绝：不重放。
	if d.Accept(1) {
		t.Fatal("Accept(1) = true after eviction, want false（历史 ID 仍拒绝，不重放）")
	}
	if d.Accept(2) {
		t.Fatal("Accept(2) = true, want false（窗口内重复拒绝）")
	}
}

func TestDeduperRestoreHighWatermark(t *testing.T) {
	d := NewDeduper(8)
	d.Restore(100)
	if got := d.HighWatermark(); got != 100 {
		t.Fatalf("HighWatermark after Restore = %d, want 100", got)
	}
	if d.Accept(99) {
		t.Fatal("Accept(99) = true, want false（恢复水位内已处理判定正确）")
	}
	if !d.Accept(101) {
		t.Fatal("Accept(101) = false, want true（新 ID 可接受）")
	}
	if d.Accept(100) {
		t.Fatal("Accept(100) = true, want false（水位本身不重放）")
	}
}

func TestDeduperConcurrentAcceptExactlyOnce(t *testing.T) {
	d := NewDeduper(16)
	const goroutines = 32
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.Accept(42) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := accepted.Load(); got != 1 {
		t.Fatalf("concurrent Accept(42) accepted %d times, want 1（并发重复 update ID 恰好一次）", got)
	}
}

// sqliteCursorStore 把 bot_update_cursor 表适配为 CursorStore（测试内实现，
// 与 Task 13 的 migrations/queries/sqlc 语义一致：单行 id=1 + update_id）。
type sqliteCursorStore struct {
	db *sql.DB
}

func (s sqliteCursorStore) Load(ctx context.Context) (int64, error) {
	row, err := sqlc.New(s.db).GetUpdateCursor(ctx)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.UpdateID, nil
}

func (s sqliteCursorStore) Save(ctx context.Context, updateID int64) error {
	return sqlc.New(s.db).UpsertUpdateCursor(ctx, updateID)
}

func openCursorDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `CREATE TABLE IF NOT EXISTS bot_update_cursor (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		update_id INTEGER NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestDeduperSqliteRestoreHighWatermark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor.db")
	ctx := context.Background()

	db1 := openCursorDB(t, path)
	store1 := sqliteCursorStore{db: db1}
	if err := store1.Save(ctx, 42); err != nil {
		db1.Close()
		t.Fatalf("Save: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	// 模拟重启：重新打开同一文件，Load 恢复 high-watermark。
	db2 := openCursorDB(t, path)
	defer db2.Close()
	store2 := sqliteCursorStore{db: db2}
	hw, err := store2.Load(ctx)
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if hw != 42 {
		t.Fatalf("restored high-watermark = %d, want 42（重启从 SQLite bot_update_cursor 恢复）", hw)
	}

	d := NewDeduper(8)
	d.Restore(hw)
	if d.Accept(42) {
		t.Fatal("Accept(42) after Restore = true, want false（已 ACK 的 update 不得重放）")
	}
	if !d.Accept(43) {
		t.Fatal("Accept(43) = false, want true（新 update 正常接受）")
	}
}
