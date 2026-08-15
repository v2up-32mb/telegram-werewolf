-- name: UpsertUser :exec
INSERT INTO users (telegram_id, nickname) VALUES (?, ?)
ON CONFLICT(telegram_id) DO UPDATE SET nickname = excluded.nickname;

-- name: GetUser :one
SELECT telegram_id, nickname, created_at FROM users WHERE telegram_id = ?;
