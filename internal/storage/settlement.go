package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// GameResult 描述一局正常结算需要持久化的全部输入
// （docs/技术选型.md §8.3 正常结束时写入战报、积分与统计）。
type GameResult struct {
	RoomCode   game.RoomID
	Phase      game.Phase
	WinnerCamp game.Camp
	Players    []PlayerResult
	Report     string // 战报内容（胜方、全员身份翻牌与关键事件）
}

// PlayerResult 描述一名玩家在本局的参与情况。
type PlayerResult struct {
	UserID        game.UserID
	Seat          game.Seat
	Role          game.Role
	Camp          game.Camp
	Died          bool // 对局中死亡（正常死亡后退出按正常结算）
	MaliciousExit bool // 恶意退出（存活时退出或连续超时被强制移除）
}

// SettlementRepository 提供对局结算的持久化操作；所有写操作在单事务内
// 完成，任一步失败整体回滚（docs/技术选型.md §8.4 关键结算使用显式事务）。
type SettlementRepository struct {
	db *sql.DB
}

// NewSettlementRepository 基于已打开并迁移的数据库创建结算 Repository。
func NewSettlementRepository(db *sql.DB) *SettlementRepository {
	return &SettlementRepository{db: db}
}

// SettleGame 原子保存对局、玩家结算、积分、角色统计与战报。
//
// 积分口径（docs/游戏流程设计.md §积分系统，按优先级）：
//  1. 恶意退出且阵营失败 → -5
//  2. 恶意退出且阵营胜利 → 0（不得分）
//  3. 对局中死亡但阵营胜利 → +2（死亡躺赢）
//  4. 阵营胜利 → +5
//  5. 其余（失败、死亡且失败）→ 0
//
// rooms/room_players 是"进行中房间"标记，仅服务于重启清场判定
// （docs/技术选型.md §8.2）：正常结算在同一事务内清除对应 active 标记
// （§8.3 正常结束时写入战报、积分与统计并清除 active 标记），避免重启时
// 把已结束对局误判为中止局；结束后回大厅由上层内存态处理（游戏流程设计.md
// §结算.5，返回大厅/再来一局属后续任务，Board 状态本就不持久化）。
func (r *SettlementRepository) SettleGame(ctx context.Context, result GameResult) error {
	if err := validateSettlement(result); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin settle game: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := sqlc.New(tx)
	gameID, err := q.CreateGame(ctx, sqlc.CreateGameParams{
		RoomCode: string(result.RoomCode),
		Phase:    result.Phase.String(),
	})
	if err != nil {
		return fmt.Errorf("storage: create game for room %s: %w", result.RoomCode, err)
	}
	// sqlc CreateGame 不含 winner_camp（000001 无默认值），此处补齐胜方
	//（不改动 queries/*.sql 与 sqlc/* 生成物）。
	if _, err := tx.ExecContext(ctx, `UPDATE games SET winner_camp = ? WHERE id = ?`, result.WinnerCamp.String(), gameID); err != nil {
		return fmt.Errorf("storage: set winner camp of game %d: %w", gameID, err)
	}

	for _, p := range result.Players {
		isWinner := p.Camp == result.WinnerCamp
		// sqlc 生成查询未含 is_winner（000001 默认 0），此处直接写入
		//（不改动 queries/*.sql 与 sqlc/* 生成物）。
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO game_players (game_id, user_id, seat, role, is_winner) VALUES (?, ?, ?, ?, ?)`,
			gameID, int64(p.UserID), int64(p.Seat), p.Role.String(), boolInt(isWinner)); err != nil {
			return fmt.Errorf("storage: insert game player %d: %w", p.UserID, err)
		}
		// 增量统计：单条写语句累加，避免"读后写"事务在 WAL 下的快照竞态
		//（sqlc UpsertRoleStat 为绝对值覆盖，不适用累加场景）。
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO role_stats (user_id, role, wins, losses, plays) VALUES (?, ?, ?, ?, 1)
			 ON CONFLICT(user_id, role) DO UPDATE SET
			   wins = wins + excluded.wins,
			   losses = losses + excluded.losses,
			   plays = plays + excluded.plays`,
			int64(p.UserID), p.Role.String(), boolInt(isWinner), boolInt(!isWinner)); err != nil {
			return fmt.Errorf("storage: upsert role stats %d: %w", p.UserID, err)
		}
		// 积分增量可为负（users.points 无 NOT NULL 之外的约束）。
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET points = points + ? WHERE telegram_id = ?`,
			pointsFor(p, isWinner), int64(p.UserID)); err != nil {
			return fmt.Errorf("storage: update points of %d: %w", p.UserID, err)
		}
	}

	if err := q.InsertBattleReport(ctx, sqlc.InsertBattleReportParams{
		GameID:  gameID,
		Content: result.Report,
	}); err != nil {
		return fmt.Errorf("storage: insert battle report of game %d: %w", gameID, err)
	}
	// 清除 active 标记：rooms 删除后 room_players 由外键 ON DELETE CASCADE
	// 一并清除（docs/技术选型.md §8.3）。
	if _, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE room_code = ?`, string(result.RoomCode)); err != nil {
		return fmt.Errorf("storage: clear active room %s: %w", result.RoomCode, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit settle game: %w", err)
	}
	return nil
}

// pointsFor 按游戏流程设计.md §积分系统 计算玩家积分增量。
func pointsFor(p PlayerResult, isWinner bool) int64 {
	switch {
	case p.MaliciousExit && !isWinner:
		return -5
	case p.MaliciousExit:
		return 0
	case p.Died && isWinner:
		return 2
	case isWinner:
		return 5
	default:
		return 0
	}
}

// validateSettlement 校验结算输入；非法输入返回错误且不落库。
func validateSettlement(result GameResult) error {
	if !result.WinnerCamp.Valid() {
		return errors.New("storage: settle game: 胜方阵营非法")
	}
	if len(result.Players) == 0 {
		return errors.New("storage: settle game: 玩家列表为空")
	}
	for _, p := range result.Players {
		if !p.Role.Valid() {
			return fmt.Errorf("storage: settle game: 玩家 %d 角色非法", p.UserID)
		}
		if !p.Camp.Valid() {
			return fmt.Errorf("storage: settle game: 玩家 %d 阵营非法", p.UserID)
		}
		if p.Role.Camp() != p.Camp {
			return fmt.Errorf("storage: settle game: 玩家 %d 角色与阵营不一致", p.UserID)
		}
		if !p.Seat.Valid() {
			return fmt.Errorf("storage: settle game: 玩家 %d 座位非法", p.UserID)
		}
	}
	return nil
}

// boolInt 把布尔值转为 SQLite 可存的 0/1。
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
