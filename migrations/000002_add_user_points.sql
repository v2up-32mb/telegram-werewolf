-- +goose Up
-- 用户积分：新玩家初始 0，积分可以为负（docs/游戏流程设计.md §积分系统；
-- 000001 遗漏该列，docs/技术选型.md §8.2 明确积分为长期持久化数据）。
ALTER TABLE users ADD COLUMN points INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN points;
