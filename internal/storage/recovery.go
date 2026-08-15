package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// InterruptedRoom 是一次服务重启后遗留的活跃房间及其参与者快照
// （docs/技术选型.md §8.2：rooms 仅保存进行中的活跃房间）。
type InterruptedRoom struct {
	Room    sqlc.Room
	Players []sqlc.RoomPlayer
}

// RecoveryRepository 提供服务重启清场的持久化操作：扫描遗留活跃房间，
// 并事务化地把房间标记为"服务重启中止"（docs/技术选型.md §8.3）。
type RecoveryRepository struct {
	db *sql.DB
}

// NewRecoveryRepository 基于已打开并迁移的数据库创建启动中止 Repository。
func NewRecoveryRepository(db *sql.DB) *RecoveryRepository {
	return &RecoveryRepository{db: db}
}

// ListInterruptedRoomsOnStartup 返回启动时全部遗留活跃房间及其参与者
// （含座位与是否房主快照），供上层生成中止记录并通知可联系参与者。
// 返回非 nil 空切片表示无遗留房间。
func (r *RecoveryRepository) ListInterruptedRoomsOnStartup(ctx context.Context) ([]InterruptedRoom, error) {
	rooms, err := sqlc.New(r.db).ListActiveRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: list interrupted rooms: %w", err)
	}
	out := make([]InterruptedRoom, 0, len(rooms))
	for _, room := range rooms {
		players, err := r.listPlayers(ctx, room.RoomCode)
		if err != nil {
			return nil, fmt.Errorf("storage: list players of room %q: %w", room.RoomCode, err)
		}
		out = append(out, InterruptedRoom{Room: room, Players: players})
	}
	return out, nil
}

// MarkInterrupted 事务化地把遗留活跃房间清场为"服务重启中止"：写入
// aborted=1 的 games 记录（保留阶段，docs/技术选型.md §8.3 中止局不判
// 胜负/不加减积分）→ 删除 room_players → 删除 rooms。通知由上层 Effect
// 执行。房间不存在或已被并发清场返回 ErrRoomNotFound。
func (r *RecoveryRepository) MarkInterrupted(ctx context.Context, code game.RoomID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin mark interrupted: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := sqlc.New(tx)
	room, err := q.GetRoomByCode(ctx, string(code))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoomNotFound
		}
		return fmt.Errorf("storage: get room %q: %w", code, err)
	}
	// sqlc 未生成 games 中止记录查询，此处直接在事务内写入
	//（不改动 queries/*.sql 与 sqlc/* 生成物）。
	if _, err := tx.ExecContext(ctx, `INSERT INTO games (room_code, phase, aborted) VALUES (?, ?, 1)`, string(code), room.Phase); err != nil {
		return fmt.Errorf("storage: insert aborted game of room %q: %w", code, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM room_players WHERE room_code = ?`, string(code)); err != nil {
		return fmt.Errorf("storage: clear players of room %q: %w", code, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE room_code = ?`, string(code))
	if err != nil {
		return fmt.Errorf("storage: delete aborted room %q: %w", code, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: delete aborted room %q rows affected: %w", code, err)
	}
	if n == 0 {
		// 并发下房间已被其他清场事务删除：整体回滚，不留 games 记录。
		return ErrRoomNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit mark interrupted room %q: %w", code, err)
	}
	return nil
}

// listPlayers 返回房间内全部参与者（按座位排序）；sqlc 未生成
// room_players 列表查询，此处直接查询（不改动 queries/*.sql）。
func (r *RecoveryRepository) listPlayers(ctx context.Context, roomCode string) ([]sqlc.RoomPlayer, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT room_code, user_id, seat, is_host, joined_at
		FROM room_players WHERE room_code = ? ORDER BY seat`, roomCode)
	if err != nil {
		return nil, fmt.Errorf("storage: query players of room %q: %w", roomCode, err)
	}
	defer func() { _ = rows.Close() }()

	players := []sqlc.RoomPlayer{}
	for rows.Next() {
		var p sqlc.RoomPlayer
		if err := rows.Scan(&p.RoomCode, &p.UserID, &p.Seat, &p.IsHost, &p.JoinedAt); err != nil {
			return nil, fmt.Errorf("storage: scan player of room %q: %w", roomCode, err)
		}
		players = append(players, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate players of room %q: %w", roomCode, err)
	}
	return players, nil
}
