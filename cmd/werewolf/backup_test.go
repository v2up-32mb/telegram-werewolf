package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestBackupCommandWritesConsistentSnapshot 通过临时 config.yaml 指定
// database_path，执行 werewolf backup --output 生成备份并离线核对行数。
func TestBackupCommandWritesConsistentSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	dbPath := filepath.Join(dir, "src.db")
	db, err := storage.Open(dbPath, storage.DefaultMaxOpenConns)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer db.Close()
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 预置两行用户，用于备份核对。
	for i, nick := range []string{"alice", "bob"} {
		if err := storage.NewUserRepository(db).Upsert(ctx, game.UserID(700+i), nick); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("database_path: "+dbPath+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	outPath := filepath.Join(dir, "backup.db")

	var stdout, stderr bytes.Buffer
	err = run(ctx, []string{"werewolf", "backup", "--config", cfgPath, "--output", outPath}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run backup: %v (stderr=%s)", err, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Error("backup 成功应输出提示信息")
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("备份文件未生成: %v", err)
	}

	restored, err := storage.Open(outPath, storage.DefaultMaxOpenConns)
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
	var n int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 2 {
		t.Errorf("恢复后 users 行数 = %d, want 2", n)
	}
}

// TestBackupCommandArgumentErrors 验证缺失 --output 与未知子命令返回错误。
func TestBackupCommandArgumentErrors(t *testing.T) {
	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"werewolf", "backup"}, &stdout, &stderr); err == nil {
		t.Error("缺 --output 应返回错误")
	}
	if err := run(ctx, []string{"werewolf", "frobnicate"}, &stdout, &stderr); err == nil {
		t.Error("未知子命令应返回错误")
	}
}
