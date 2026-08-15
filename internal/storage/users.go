package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
