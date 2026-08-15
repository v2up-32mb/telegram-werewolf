package storage

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/v2up-32mb/telegram-werewolf/migrations"
)

// Migrate 通过 Goose library mode 从嵌入的 migrations.FS 执行全部
// 迁移（docs/技术选型.md §8.1：迁移为可审查的纯 SQL migration 文件）。
// 重复调用幂等：无待执行迁移时返回 nil。
func Migrate(db *sql.DB) error {
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("storage: set goose dialect: %w", err)
	}
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("storage: run migrations: %w", err)
	}
	return nil
}
