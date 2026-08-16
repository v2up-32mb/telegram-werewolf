-- +goose Up
-- Task 24：房间配置快照与 bcrypt 密码哈希（明文不落库，
-- docs/游戏流程设计.md §密码；docs/技术选型.md §8.2 rooms 只保留中止清场
-- 最小数据，配置与密码哈希存入独立 room_settings 表，1:1 关联活跃房间）。
CREATE TABLE room_settings (
    room_code     TEXT PRIMARY KEY REFERENCES rooms(room_code) ON DELETE CASCADE,
    settings      TEXT NOT NULL DEFAULT '',   -- RoomSettings JSON 快照
    password_hash TEXT NOT NULL DEFAULT ''    -- bcrypt 哈希，绝不存明文
);

-- +goose Down
DROP TABLE IF EXISTS room_settings;
