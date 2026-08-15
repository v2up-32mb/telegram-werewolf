-- name: CreateRoom :exec
INSERT INTO rooms (room_code, host_user_id, phase) VALUES (?, ?, ?);

-- name: GetRoomByCode :one
SELECT room_code, host_user_id, phase, created_at, started_at FROM rooms WHERE room_code = ?;

-- name: InsertRoomPlayer :exec
INSERT INTO room_players (room_code, user_id, seat, is_host) VALUES (?, ?, ?, ?);

-- name: ListActiveRooms :many
SELECT room_code, host_user_id, phase, created_at, started_at FROM rooms ORDER BY created_at, room_code;

-- name: DeleteRoom :exec
DELETE FROM rooms WHERE room_code = ?;
