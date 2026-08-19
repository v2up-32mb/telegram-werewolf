-- +goose Up
-- 房主强制解散扣分幂等账本。该表不引用 rooms：解散流程会先清理房间，
-- 但重试仍必须能凭 room_code + user_id 识别已经应用的扣分。
CREATE TABLE score_penalties (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    room_code  TEXT NOT NULL,
    user_id    INTEGER NOT NULL,
    amount     INTEGER NOT NULL CHECK (amount > 0),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (room_code, user_id)
);

CREATE INDEX idx_score_penalties_user_id ON score_penalties(user_id);

-- +goose Down
DROP TABLE score_penalties;
