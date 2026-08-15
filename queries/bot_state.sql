-- name: UpsertUpdateCursor :exec
INSERT INTO bot_update_cursor (id, update_id) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET update_id = excluded.update_id, updated_at = datetime('now');

-- name: GetUpdateCursor :one
SELECT id, update_id, updated_at FROM bot_update_cursor WHERE id = 1;
