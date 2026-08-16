package game

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// 大厅生命周期的消息 key（渲染由后续任务按 key + params 产出
// MarkdownV2 文案；面板刷新统一 LobbyPanelMessageKey，由既有 Outbox
// Coalescer 按 CoalesceKey 合并——docs/技术选型.md §7.1）。
const (
	// LeaveConfirmedMessageKey 是玩家退出成功后发给本人的确认。
	LeaveConfirmedMessageKey = "lobby.left"
	// KickedMessageKey 是房主移除玩家后发给被移除者的通知。
	KickedMessageKey = "lobby.kicked"
	// HostTransferredMessageKey 是房主移交后发给新房主的通知
	//（旧房主不通知，docs「房主移交」2）。
	HostTransferredMessageKey = "lobby.host_transferred"
	// IdleReminderMessageKey 是闲置提醒（附续期/解散按钮语义，
	// docs「闲置回收」1）。
	IdleReminderMessageKey = "lobby.idle_reminder"
	// RenewedMessageKey 是房主续期成功的确认。
	RenewedMessageKey = "lobby.renewed"
	// RoomExpiredMessageKey 是房间超时自动回收通知。
	RoomExpiredMessageKey = "lobby.expired"
)

// 闲置回收时间常量（docs「闲置回收」1：未开始游戏超时 1 小时；
// 超时前 10 分钟提醒房主一次；续期 1 小时；游戏开始后不受影响）。
const (
	// IdleReminderLead 是到期前的提醒提前量（创建后 50 分钟 = 1 小时前 10 分钟）。
	IdleReminderLead = 10 * time.Minute
	// IdleExpireAfter 是默认超时时长（自创建起算，玩家进出不刷新）。
	IdleExpireAfter = 1 * time.Hour
	// RenewDuration 是房主续期时长（以续期时刻重新起算）。
	RenewDuration = 1 * time.Hour
)

// 大厅生命周期领域规则的哨兵错误。
var (
	// ErrNotHost 表示操作者不是房主（移除/续期仅房主可操作）。
	ErrNotHost = errors.New("game: only host may do this")
	// ErrRoomEmpty 表示房间已无玩家。
	ErrRoomEmpty = errors.New("game: room is empty")
	// ErrKickSelf 表示房主不能通过移除流程把自己移走（应走退出流）。
	ErrKickSelf = errors.New("game: host cannot kick self")
)

// LobbyLifetime 是大厅生命周期元数据（docs「闲置回收」1）：
// 超时自创建起算、玩家进出不刷新原始期限；续期以续期时刻重新计算。
// 由后续 room actor 接线持有，本任务以纯值语义表达规则。
type LobbyLifetime struct {
	// CreatedAt 是房间创建时刻（超时基准，续期不改动）。
	CreatedAt time.Time
	// ExpireAt 是当前到期时刻（初始 = CreatedAt + IdleExpireAfter；
	// 续期后 = 续期时刻 + RenewDuration）。
	ExpireAt time.Time
	// Reminded 标记当前周期是否已发送提醒（续期后重置，新周期再提醒）。
	Reminded bool
}

// LifecycleClock 是闲置回收规则的时间 seam（docs/技术选型.md §6.2：
// 可注入时钟；room.Clock 属 room 包不可被 game 反向依赖，故领域层
// 自持最小接口）。测试注入 Fake Clock，生产用实时钟。
type LifecycleClock interface {
	Now() time.Time
}

// realLifecycleClock 基于 time.Now 的生产时钟。
type realLifecycleClock struct{}

func (realLifecycleClock) Now() time.Time { return time.Now() }

// LeaveCommand 是玩家退出大厅的领域命令（docs §二.5 退出）。
type LeaveCommand struct {
	Meta CommandMeta
}

func (LeaveCommand) command() {}

// KickCommand 是房主移除玩家的领域命令（docs §二.9 踢人：游戏前房主
// 可移除任意玩家）。
type KickCommand struct {
	Meta   CommandMeta
	Target UserID
}

func (KickCommand) command() {}

// RenewCommand 是房主续期 1 小时的领域命令（docs「闲置回收」1）。
type RenewCommand struct {
	Meta CommandMeta
}

func (RenewCommand) command() {}

// LobbyLifecycleService 是大厅生命周期领域流程（docs §二.5/§房主移交/
// §闲置回收）。纯核心：进出/移交/到期规则在此计算，时间经 clock seam，
// 不触碰 Telegram/SQLite/系统时钟（docs/技术选型.md §5.1）。
type LobbyLifecycleService struct {
	clock LifecycleClock
}

// NewLobbyLifecycleService 构造生命周期服务；clock 为空时使用实时钟。
func NewLobbyLifecycleService(clock LifecycleClock) (LobbyLifecycleService, error) {
	if clock == nil {
		clock = realLifecycleClock{}
	}
	return LobbyLifecycleService{clock: clock}, nil
}

// LeaveRoom 处理玩家退出（docs §二.5）：
//  1. 操作者必须在房间（ErrNotInRoom 拒绝，无 Effects 无 State 变更）；
//  2. 移除玩家并给本人退出确认；
//  3. 普通玩家退出：房主面板刷新；
//  4. 房主退出且房间仍有人：按加入顺序（座位升序）最小在场座位成为
//     新房主，仅通知新房主（HostTransferred），旧房主不收到移交通知；
//     房主退出且是最后一人：只保留本人确认，不产面板/移交
//     （空房回收语义交给外层/到期评估）。
func (s LobbyLifecycleService) LeaveRoom(_ context.Context, st State, cmd LeaveCommand) (State, []Effect, error) {
	idx := findPlayerIndex(st.Players, cmd.Meta.Actor)
	if idx < 0 {
		return st, nil, ErrNotInRoom
	}
	oldOwner := st.Lobby.Owner
	players := removePlayerAt(st.Players, idx)

	left, err := NewMessageEffect(AudienceActor, LeaveConfirmedMessageKey, map[string]any{
		"room_code": string(st.RoomID),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: leave message: %w", err)
	}
	effects := []Effect{left}

	switch {
	case len(players) == 0:
		// 最后一人退出：空房，无面板/移交（回收语义由外层评估）。
	case cmd.Meta.Actor == oldOwner:
		newOwner := nextHostSeatUser(players)
		st.Lobby.Owner = newOwner
		transfer, err := NewMessageEffect(AudienceActor, HostTransferredMessageKey, map[string]any{
			"room_code": string(st.RoomID),
			"host":      newOwner,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: host transfer message: %w", err)
		}
		effects = append(effects, transfer)
		panel, err := NewMessageEffect(AudienceHost, LobbyPanelMessageKey, map[string]any{
			"room_code": string(st.RoomID),
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: lobby panel message: %w", err)
		}
		effects = append(effects, panel)
	default:
		panel, err := NewMessageEffect(AudienceHost, LobbyPanelMessageKey, map[string]any{
			"room_code": string(st.RoomID),
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: lobby panel message: %w", err)
		}
		effects = append(effects, panel)
	}

	st.Players = players
	return st, effects, nil
}

// KickPlayer 处理房主移除玩家（docs §二.9）：
//  1. 仅房主可操作（ErrNotHost）；空房 ErrRoomEmpty；
//  2. 房主不能经移除流程移走自己（ErrKickSelf，应走退出流）；
//  3. 目标必须在房间（ErrNotInRoom）；
//  4. 移除目标：发被移除通知（本人）并刷新房主面板。
func (s LobbyLifecycleService) KickPlayer(_ context.Context, st State, cmd KickCommand) (State, []Effect, error) {
	if cmd.Meta.Actor != st.Lobby.Owner {
		return st, nil, ErrNotHost
	}
	if len(st.Players) == 0 {
		return st, nil, ErrRoomEmpty
	}
	if cmd.Target == st.Lobby.Owner {
		return st, nil, ErrKickSelf
	}
	idx := findPlayerIndex(st.Players, cmd.Target)
	if idx < 0 {
		return st, nil, ErrNotInRoom
	}
	st.Players = removePlayerAt(st.Players, idx)

	kicked, err := NewMessageEffect(AudienceActor, KickedMessageKey, map[string]any{
		"room_code": string(st.RoomID),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: kicked message: %w", err)
	}
	panel, err := NewMessageEffect(AudienceHost, LobbyPanelMessageKey, map[string]any{
		"room_code": string(st.RoomID),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: lobby panel message: %w", err)
	}
	return st, []Effect{kicked, panel}, nil
}

// RenewRoom 处理房主续期（docs「闲置回收」1）：仅房主可续期；
// 以续期时刻重新计算 ExpireAt（+1 小时）并重置提醒标记
// （新周期超时前 10 分钟再次提醒）。成员列表不因续期改变。
func (s LobbyLifecycleService) RenewRoom(_ context.Context, st State, cmd RenewCommand, lt LobbyLifetime) (State, LobbyLifetime, []Effect, error) {
	if cmd.Meta.Actor != st.Lobby.Owner {
		return st, lt, nil, ErrNotHost
	}
	lt.ExpireAt = s.clock.Now().Add(RenewDuration)
	lt.Reminded = false

	renewed, err := NewMessageEffect(AudienceActor, RenewedMessageKey, map[string]any{
		"room_code": string(st.RoomID),
	})
	if err != nil {
		return st, lt, nil, fmt.Errorf("game: renew message: %w", err)
	}
	panel, err := NewMessageEffect(AudienceHost, LobbyPanelMessageKey, map[string]any{
		"room_code": string(st.RoomID),
	})
	if err != nil {
		return st, lt, nil, fmt.Errorf("game: lobby panel message: %w", err)
	}
	return st, lt, []Effect{renewed, panel}, nil
}

// EvaluateIdle 评估大厅闲置回收（docs「闲置回收」1）：
//   - 游戏开始后（非 PhaseLobby）不评估（进行中不受过期影响）；
//   - 空房不产通知（回收语义由外层处理）；
//   - 到期（now >= ExpireAt）：产出 RoomExpired 回收通知；
//   - 超时前 10 分钟且本周期未提醒：产出一次 IdleReminder 提醒，
//     标记 Reminded（续期后重置，新周期再提醒）。
//
// 玩家进出不刷新 ExpireAt：期限只由 CreatedAt/Renew 更新，
// 进出操作（LeaveRoom/KickPlayer）不触碰 LobbyLifetime。
func (s LobbyLifecycleService) EvaluateIdle(_ context.Context, lt LobbyLifetime, st State) (LobbyLifetime, []Effect, error) {
	if st.Phase != PhaseLobby || len(st.Players) == 0 {
		return lt, nil, nil
	}
	now := s.clock.Now()
	if !now.Before(lt.ExpireAt) {
		msg, err := NewMessageEffect(AudienceHost, RoomExpiredMessageKey, map[string]any{
			"room_code": string(st.RoomID),
		})
		if err != nil {
			return lt, nil, fmt.Errorf("game: room expired message: %w", err)
		}
		return lt, []Effect{msg}, nil
	}
	remindAt := lt.ExpireAt.Add(-IdleReminderLead)
	if !lt.Reminded && !now.Before(remindAt) {
		msg, err := NewMessageEffect(AudienceHost, IdleReminderMessageKey, map[string]any{
			"room_code": string(st.RoomID),
		})
		if err != nil {
			return lt, nil, fmt.Errorf("game: idle reminder message: %w", err)
		}
		lt.Reminded = true
		return lt, []Effect{msg}, nil
	}
	return lt, nil, nil
}

// findPlayerIndex 返回用户在成员列表中的下标；不在返回 -1。
func findPlayerIndex(players []Player, user UserID) int {
	for i := range players {
		if players[i].UserID == user {
			return i
		}
	}
	return -1
}

// removePlayerAt 返回移除指定下标后的成员列表副本。
func removePlayerAt(players []Player, idx int) []Player {
	out := make([]Player, 0, len(players)-1)
	out = append(out, players[:idx]...)
	out = append(out, players[idx+1:]...)
	return out
}

// nextHostSeatUser 返回按加入顺序（座位升序）最小在场座位的玩家：
// 房主 1 号让位后依次 2、3、…（docs「房主移交」1）。
func nextHostSeatUser(players []Player) UserID {
	best := players[0]
	for _, p := range players[1:] {
		if p.Seat < best.Seat {
			best = p
		}
	}
	return best.UserID
}
