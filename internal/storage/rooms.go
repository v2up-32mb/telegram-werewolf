package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
	"golang.org/x/crypto/bcrypt"
)

// 房间领域错误：SQLite 驱动文本只在 storage 内部解析，向上层只暴露
// 领域错误（不向 Telegram 层泄漏驱动细节）。
var (
	// ErrRoomNotFound 表示房间不存在（入房/退房/清场目标缺失）。
	ErrRoomNotFound = errors.New("storage: room not found")
	// ErrRoomCodeTaken 表示房间码已被占用（建房唯一冲突）。
	ErrRoomCodeTaken = errors.New("storage: room code already taken")
	// ErrUserAlreadyInRoom 表示用户已在该房间（重复入房唯一冲突）。
	ErrUserAlreadyInRoom = errors.New("storage: user already in room")
	// ErrSeatTaken 表示目标座位被占用（显式冲突路径，防御唯一约束兜底）。
	ErrSeatTaken = errors.New("storage: seat already taken")
	// ErrRoomFull 表示房间已满（MVP 6 席，docs/游戏流程设计.md §二.3）。
	ErrRoomFull = errors.New("storage: room is full")
	// ErrUserNotInRoom 表示用户不在该房间（退房目标缺失）。
	ErrUserNotInRoom = errors.New("storage: user not in room")
)

// RoomRepository 提供建房、入房、退房与活跃房间扫描的持久化操作
// （docs/技术选型.md §8.3）。所有写操作在单事务内完成，失败不留中间态。
type RoomRepository struct {
	db *sql.DB
}

// NewRoomRepository 基于已打开并迁移的数据库创建房间 Repository。
func NewRoomRepository(db *sql.DB) *RoomRepository {
	return &RoomRepository{db: db}
}

// joinRoomPlayerStmt 以单条语句原子完成"校验房间存在 → 分配最小空座位
// （房主固定 1 号，其余 2..6）→ 写入参与者"。
//
// 选择单语句的理由：SQLite WAL 下"先读后写"的事务在快照过期时升级写锁会
// 直接失败（SQLITE_BUSY_SNAPSHOT），busy_timeout 不处理该错误；单语句在
// 写锁争用下由驱动以新快照整体重启，天然收敛到正确座位且不泄漏驱动文本。
// 房主 1 号不在候选集；目标座位被并发占用时唯一约束仍作为兜底映射。
const joinRoomPlayerStmt = `INSERT INTO room_players (room_code, user_id, seat, is_host)
SELECT ?, ?, s.seat, 0
FROM rooms
CROSS JOIN (SELECT 2 AS seat UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6) s
WHERE rooms.room_code = ?
  AND NOT EXISTS (SELECT 1 FROM room_players p WHERE p.room_code = ? AND p.seat = s.seat)
ORDER BY s.seat
LIMIT 1
RETURNING seat`

// Create 原子建房：rooms 与房主 1 号座位（is_host=1，docs/游戏流程设计.md
// §一.3 房主也是玩家）同时写入；重复房间码返回 ErrRoomCodeTaken，房主
// 用户未登记返回 ErrUserNotFound，失败回滚不留中间态。
func (r *RoomRepository) Create(ctx context.Context, code game.RoomID, host game.UserID, phase string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin create room: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := sqlc.New(tx)
	if err := q.CreateRoom(ctx, sqlc.CreateRoomParams{
		RoomCode:   string(code),
		HostUserID: int64(host),
		Phase:      phase,
	}); err != nil {
		if isUniqueViolation(err) {
			return ErrRoomCodeTaken
		}
		if isForeignKeyViolation(err) {
			return ErrUserNotFound
		}
		return fmt.Errorf("storage: create room %q: %w", code, err)
	}
	if err := q.InsertRoomPlayer(ctx, sqlc.InsertRoomPlayerParams{
		RoomCode: string(code),
		UserID:   int64(host),
		Seat:     int64(game.HostSeat),
		IsHost:   1,
	}); err != nil {
		return fmt.Errorf("storage: insert host seat of room %q: %w", code, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit create room %q: %w", code, err)
	}
	return nil
}

// Join 把玩家加入房间并返回分配的座位号（房主固定 1 号，其余按加入顺序
// 2/3/4…，docs/游戏流程设计.md §三.2；MVP 上限 6 席）。房间不存在返回
// ErrRoomNotFound，已满返回 ErrRoomFull；重复入房优先返回
// ErrUserAlreadyInRoom，座位唯一冲突（防御路径）映射为 ErrSeatTaken，
// 用户未登记返回 ErrUserNotFound。
func (r *RoomRepository) Join(ctx context.Context, code game.RoomID, user game.UserID) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("storage: begin join room: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seat int64
	err = tx.QueryRowContext(ctx, joinRoomPlayerStmt, string(code), int64(user), string(code), string(code)).Scan(&seat)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 无可用座位：按"重复入房优先于满员"区分领域原因。此处分支为
		// 只读判断（不写），不会引入读后写升级竞态。
		var inRoom int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_players WHERE room_code = ? AND user_id = ?`, string(code), int64(user)).Scan(&inRoom); err != nil {
			return 0, fmt.Errorf("storage: check membership of room %q: %w", code, err)
		}
		if inRoom > 0 {
			return 0, ErrUserAlreadyInRoom
		}
		var exists int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE room_code = ?`, string(code)).Scan(&exists); err != nil {
			return 0, fmt.Errorf("storage: check room %q: %w", code, err)
		}
		if exists == 0 {
			return 0, ErrRoomNotFound
		}
		return 0, ErrRoomFull
	case err != nil:
		if isUniqueViolation(err) {
			return 0, mapJoinConflict(err)
		}
		if isForeignKeyViolation(err) {
			return 0, ErrUserNotFound
		}
		return 0, fmt.Errorf("storage: join room %q user %d: %w", code, user, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("storage: commit join room %q: %w", code, err)
	}
	return seat, nil
}

// Leave 删除用户的房内座位行；房间保持活跃（docs/游戏流程设计.md §二.5
// 玩家可随时退出，技术选型.md §8.3 退房只更新活跃记录）。房间不存在
// 返回 ErrRoomNotFound，用户不在房返回 ErrUserNotInRoom。
func (r *RoomRepository) Leave(ctx context.Context, code game.RoomID, user game.UserID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin leave room: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	q := sqlc.New(tx)
	if _, err := q.GetRoomByCode(ctx, string(code)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoomNotFound
		}
		return fmt.Errorf("storage: get room %q: %w", code, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM room_players WHERE room_code = ? AND user_id = ?`, string(code), int64(user))
	if err != nil {
		return fmt.Errorf("storage: leave room %q user %d: %w", code, user, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: leave room %q rows affected: %w", code, err)
	}
	if n == 0 {
		return ErrUserNotInRoom
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit leave room %q: %w", code, err)
	}
	return nil
}

// ListActive 返回全部活跃房间（rooms 表仅保存进行中的活跃房间，
// docs/技术选型.md §8.2）。
func (r *RoomRepository) ListActive(ctx context.Context) ([]sqlc.Room, error) {
	rooms, err := sqlc.New(r.db).ListActiveRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: list active rooms: %w", err)
	}
	return rooms, nil
}

// mapJoinConflict 把入房唯一约束冲突映射为领域错误：约束文本含 user_id
// 为重复入房（主键），其余视为座位唯一约束（防御路径）。
func mapJoinConflict(err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "user_id") {
		return ErrUserAlreadyInRoom
	}
	return ErrSeatTaken
}

// isUniqueViolation 报告错误是否为 SQLite 唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

// isForeignKeyViolation 报告错误是否为 SQLite 外键约束冲突。
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

// UpdateRoomSettings 持久化房间设置快照（JSON 文本）与 bcrypt 密码哈希。
// 本层只接收已哈希值并用 bcrypt.Cost 严格校验哈希本身，绝不接收明文密码
// （docs/游戏流程设计.md §密码：明文不得入库）。房间不存在返回 ErrRoomNotFound。
func (r *RoomRepository) UpdateRoomSettings(ctx context.Context, code game.RoomID, settings string, passwordHash string) error {
	if passwordHash != "" {
		if _, err := bcrypt.Cost([]byte(passwordHash)); err != nil {
			return fmt.Errorf("storage: refusing non-bcrypt password hash for room %q: %w", code, err)
		}
	}
	if err := sqlc.New(r.db).UpsertRoomSettings(ctx, sqlc.UpsertRoomSettingsParams{
		RoomCode:     string(code),
		Settings:     settings,
		PasswordHash: passwordHash,
	}); err != nil {
		if isForeignKeyViolation(err) {
			return ErrRoomNotFound
		}
		return fmt.Errorf("storage: upsert room settings %q: %w", code, err)
	}
	return nil
}

// roomSettingsRow 读取设置行；无设置行时区分「房间不存在」与
// 「房间存在但从未保存过设置」（后者为空设置/空密码状态）。
func (r *RoomRepository) roomSettingsRow(ctx context.Context, code game.RoomID) (sqlc.GetRoomSettingsRow, error) {
	row, err := sqlc.New(r.db).GetRoomSettings(ctx, string(code))
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqlc.GetRoomSettingsRow{}, fmt.Errorf("storage: get room settings %q: %w", code, err)
	}
	var exists int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms WHERE room_code = ?`, string(code)).Scan(&exists); err != nil {
		return sqlc.GetRoomSettingsRow{}, fmt.Errorf("storage: check room %q: %w", code, err)
	}
	if exists == 0 {
		return sqlc.GetRoomSettingsRow{}, ErrRoomNotFound
	}
	return sqlc.GetRoomSettingsRow{}, nil
}

// RoomPasswordHash 返回房间当前 bcrypt 密码哈希（空串=未设密码；
// 房间存在但从未保存过设置同样为空串）；房间不存在返回 ErrRoomNotFound。
func (r *RoomRepository) RoomPasswordHash(ctx context.Context, code game.RoomID) (string, error) {
	row, err := r.roomSettingsRow(ctx, code)
	if err != nil {
		return "", err
	}
	return row.PasswordHash, nil
}

// RoomSettings 返回房间设置快照 JSON（空串=未设置过）；
// 房间不存在返回 ErrRoomNotFound。
func (r *RoomRepository) RoomSettings(ctx context.Context, code game.RoomID) (string, error) {
	row, err := r.roomSettingsRow(ctx, code)
	if err != nil {
		return "", err
	}
	return row.Settings, nil
}
