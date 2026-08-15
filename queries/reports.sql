-- name: UpsertRoleStat :exec
INSERT INTO role_stats (user_id, role, wins, losses, plays) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, role) DO UPDATE SET wins = excluded.wins, losses = excluded.losses, plays = excluded.plays;

-- name: GetRoleStats :many
SELECT user_id, role, wins, losses, plays FROM role_stats WHERE user_id = ? ORDER BY role;

-- name: InsertBattleReport :exec
INSERT INTO battle_reports (game_id, content) VALUES (?, ?);
