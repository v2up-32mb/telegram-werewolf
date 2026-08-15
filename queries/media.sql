-- name: UpsertMediaCache :exec
INSERT INTO media_cache (cache_key, file_id, file_type) VALUES (?, ?, ?)
ON CONFLICT(cache_key) DO UPDATE SET file_id = excluded.file_id, file_type = excluded.file_type;

-- name: GetMediaCache :one
SELECT cache_key, file_id, file_type, created_at FROM media_cache WHERE cache_key = ?;
