-- +goose Up
-- 初始 schema：用户、活跃房间、对局、统计、战报和媒体缓存。
-- rooms 只保存进行中的活跃房间；对局结束历史进入 games
--（docs/技术选型.md §8.2 数据边界）。

CREATE TABLE users (
    telegram_id INTEGER PRIMARY KEY,             -- Telegram 用户 ID
    nickname    TEXT    NOT NULL,                -- 游戏昵称
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE rooms (
    room_code    TEXT PRIMARY KEY,               -- 6 位房间码（唯一）
    host_user_id INTEGER NOT NULL REFERENCES users(telegram_id),
    phase        TEXT    NOT NULL,               -- 当前阶段
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    started_at   TEXT                             -- 开局时间，未开始为 NULL
);

CREATE TABLE room_players (
    room_code TEXT    NOT NULL REFERENCES rooms(room_code) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(telegram_id),
    seat      INTEGER NOT NULL,                  -- 座位号（房间内唯一）
    is_host   INTEGER NOT NULL DEFAULT 0,
    joined_at TEXT    NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (room_code, user_id),            -- 同房用户唯一
    UNIQUE (room_code, seat)                     -- 同房座位号唯一
);

CREATE TABLE games (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    room_code   TEXT    NOT NULL,                -- 来源房间码（结束历史记录，不引用活跃 rooms）
    phase       TEXT    NOT NULL,
    winner_camp TEXT,
    aborted     INTEGER NOT NULL DEFAULT 0,      -- 1=服务重启中止，不判胜负
    settled_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE game_players (
    game_id   INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    user_id   INTEGER NOT NULL REFERENCES users(telegram_id),
    seat      INTEGER NOT NULL,
    role      TEXT    NOT NULL,
    is_winner INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (game_id, user_id)
);

CREATE TABLE role_stats (
    user_id INTEGER NOT NULL REFERENCES users(telegram_id),
    role    TEXT    NOT NULL,
    wins    INTEGER NOT NULL DEFAULT 0,
    losses  INTEGER NOT NULL DEFAULT 0,
    plays   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, role)
);

CREATE TABLE battle_reports (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    game_id    INTEGER NOT NULL REFERENCES games(id),
    content    TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE media_cache (
    cache_key  TEXT PRIMARY KEY,                 -- 媒体缓存键（唯一）
    file_id    TEXT NOT NULL,                    -- Telegram file_id
    file_type  TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE bot_update_cursor (
    id         INTEGER PRIMARY KEY CHECK (id = 1), -- 单行游标
    update_id  INTEGER NOT NULL,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- +goose Down
DROP TABLE IF EXISTS bot_update_cursor;
DROP TABLE IF EXISTS media_cache;
DROP TABLE IF EXISTS battle_reports;
DROP TABLE IF EXISTS role_stats;
DROP TABLE IF EXISTS game_players;
DROP TABLE IF EXISTS games;
DROP TABLE IF EXISTS room_players;
DROP TABLE IF EXISTS rooms;
DROP TABLE IF EXISTS users;
