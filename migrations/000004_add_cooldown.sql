-- +goose Up
-- 跨局加入冷却（docs 游戏流程设计.md §退出约束：存活主动退出/连续 3 次
-- 超时强制移除/投票踢出触发 10 分钟冷却，冷却期间不能创建或加入房间）。
-- 存 UTC RFC3339 截止时刻；NULL 表示不在冷却。
ALTER TABLE users ADD COLUMN cooldown_until TEXT;

-- +goose Down
ALTER TABLE users DROP COLUMN cooldown_until;
