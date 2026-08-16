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

-- name: UpsertRoomSettings :exec
INSERT INTO room_settings (room_code, settings, password_hash) VALUES (?, ?, ?)
ON CONFLICT(room_code) DO UPDATE SET settings = excluded.settings, password_hash = excluded.password_hash;

-- name: GetRoomSettings :one
SELECT settings, password_hash FROM room_settings WHERE room_code = ?;
