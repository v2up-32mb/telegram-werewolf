package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup 使用 SQLite 一致性快照能力把 src 在线备份到 dst
// （docs/技术选型.md §8.5）：禁止直接复制正在运行的 WAL 主文件。
//
// 流程：目标目录生成唯一临时快照 → VACUUM INTO 生成一致性快照 →
// 重新打开临时库执行 PRAGMA integrity_check（必须返回 ok）→
// os.Rename 原子改名到 dst；任何失败路径都会清理临时文件。
func Backup(ctx context.Context, db *sql.DB, dst string) error {
	if dst == "" {
		return errors.New("storage: backup: 目标路径为空")
	}
	dir := filepath.Dir(dst)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("storage: backup: 目标目录不可用 (%s): %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("storage: backup: 目标目录不是目录 (%s)", dir)
	}

	// VACUUM INTO 要求目标文件不存在：CreateTemp 独占占位后删除以保留唯一名。
	f, err := os.CreateTemp(dir, ".werewolf-backup-*.db.tmp")
	if err != nil {
		return fmt.Errorf("storage: backup: 创建临时快照文件: %w", err)
	}
	tmpPath := f.Name()
	_ = f.Close()
	_ = os.Remove(tmpPath)
	defer func() { _ = os.Remove(tmpPath) }()

	escaped := strings.ReplaceAll(tmpPath, "'", "''")
	if _, err := db.ExecContext(ctx, `VACUUM INTO '`+escaped+`'`); err != nil {
		return fmt.Errorf("storage: backup: 生成一致性快照: %w", err)
	}

	// 完整性检查用裸连接打开（不注入 WAL pragma），避免改变快照文件的
	// journal_mode 或制造 -wal/-shm 副文件，保持备份产物为单文件可移植。
	snap, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return fmt.Errorf("storage: backup: 打开快照: %w", err)
	}
	var check string
	err = snap.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check)
	closeErr := snap.Close()
	if err != nil {
		return fmt.Errorf("storage: backup: 完整性检查: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("storage: backup: 关闭快照: %w", closeErr)
	}
	if check != "ok" {
		return fmt.Errorf("storage: backup: integrity_check = %q, want ok", check)
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("storage: backup: 原子改名到 %s: %w", dst, err)
	}
	return nil
}
