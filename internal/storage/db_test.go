package storage_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestOpenPerConnectionPragmas 验证每条 database/sql 连接都启用
// foreign_keys=ON、journal_mode=WAL 与 busy_timeout（docs/技术选型.md §8.4），
// 防止只在单条初始化连接上执行一次 PRAGMA。
func TestOpenPerConnectionPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.Open(path, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// 强制同时持有多条连接（池上限 4），逐条验证 Pragma。
	conns := make([]*sqlConn, 0, 4)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn#%d: %v", i, err)
		}
		conns = append(conns, &sqlConn{c: c, t: t})
	}
	if open := db.Stats().OpenConnections; open < 2 {
		t.Errorf("打开连接数 = %d, want >= 2（应建立多条真实连接）", open)
	}
	for _, c := range conns {
		c.verifyPragma("foreign_keys", "1")
		c.verifyPragma("journal_mode", "wal")
		c.verifyPragma("busy_timeout", strconv.Itoa(storage.BusyTimeoutMS))
	}
}

// sqlConn 包装 database/sql 连接并验证 PRAGMA 查询结果。
type sqlConn struct {
	c *sql.Conn
	t *testing.T
}

func (w *sqlConn) Close() error { return w.c.Close() }

func (w *sqlConn) verifyPragma(name, want string) {
	w.t.Helper()
	var got string
	if err := w.c.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&got); err != nil {
		w.t.Fatalf("PRAGMA %s: %v", name, err)
	}
	if got != want {
		w.t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
	}
}

// TestOpenClose 验证连接关闭语义：Close 后连接池不可再用，Close 可重复调用。
func TestOpenClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.Open(path, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("重复 Close err = %v, want nil（幂等）", err)
	}
	if err := db.Ping(); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Errorf("Close 后 Ping err = %v, want closed", err)
	}
}

// TestMigrateIdempotent 验证迁移幂等：连续两次 Migrate 不报错，10 张业务表存在。
func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.Open(path, 4)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate#1: %v", err)
	}
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate#2（幂等）: %v", err)
	}
	got := tableNames(t, db)
	for _, name := range wantTables {
		if !got[name] {
			t.Errorf("迁移后缺少表 %s", name)
		}
	}
	if len(got) != len(wantTables) {
		t.Errorf("表数量 = %d, want %d（got=%v）", len(got), len(wantTables), got)
	}
}
