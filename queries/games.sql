-- name: CreateGame :one
INSERT INTO games (room_code, phase) VALUES (?, ?) RETURNING id;

-- name: InsertGamePlayer :exec
INSERT INTO game_players (game_id, user_id, seat, role) VALUES (?, ?, ?, ?);
