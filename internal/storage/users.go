package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// ErrUserNotFound 表示用户不存在（Load 命中空行或写操作外键缺失）。
var ErrUserNotFound = errors.New("storage: user not found")

// UserRepository 提供用户的读取与 upsert（docs/技术选型.md §8.3：
// 建房/入房前确保用户存在）。
type UserRepository struct {
	db *sql.DB
}

// NewUserRepository 基于已打开并迁移的数据库创建用户 Repository。
func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Load 返回用户；不存在返回 ErrUserNotFound。
func (r *UserRepository) Load(ctx context.Context, id game.UserID) (sqlc.User, error) {
	u, err := sqlc.New(r.db).GetUser(ctx, int64(id))
	if errors.Is(err, sql.ErrNoRows) {
		return sqlc.User{}, ErrUserNotFound
	}
	if err != nil {
		return sqlc.User{}, fmt.Errorf("storage: get user %d: %w", id, err)
	}
	return u, nil
}

// Upsert 写入或更新用户昵称（telegram_id 为主键，重复写入幂等）。
func (r *UserRepository) Upsert(ctx context.Context, id game.UserID, nickname string) error {
	if err := sqlc.New(r.db).UpsertUser(ctx, sqlc.UpsertUserParams{
		TelegramID: int64(id),
		Nickname:   nickname,
	}); err != nil {
		return fmt.Errorf("storage: upsert user %d: %w", id, err)
	}
	return nil
}

// SetCooldown 设置跨局加入冷却截止时刻（UTC RFC3339；docs 游戏流程设计.md
// §退出约束）。零值 until 清除冷却。
func (r *UserRepository) SetCooldown(ctx context.Context, id game.UserID, until time.Time) error {
	var raw sql.NullString
	if !until.IsZero() {
		raw = sql.NullString{String: until.UTC().Format(time.RFC3339), Valid: true}
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE users SET cooldown_until = ? WHERE telegram_id = ?`, raw, int64(id)); err != nil {
		return fmt.Errorf("storage: set cooldown of %d: %w", id, err)
	}
	return nil
}

// CooldownUntil 返回用户冷却截止时刻（零值=不在冷却；用户不存在返回
// ErrUserNotFound）。
func (r *UserRepository) CooldownUntil(ctx context.Context, id game.UserID) (time.Time, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT cooldown_until FROM users WHERE telegram_id = ?`, int64(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrUserNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("storage: load cooldown of %d: %w", id, err)
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("storage: parse cooldown of %d: %w", id, err)
	}
	return t, nil
}
