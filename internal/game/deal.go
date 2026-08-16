package game

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// 发牌与身份确认阶段的消息 key（docs §发牌；渲染由后续任务按
// key + params 产出 MarkdownV2 文案）。
const (
	// DealRoleCardMessageKey 是每玩家的私有身份卡消息（图片 + Caption）。
	DealRoleCardMessageKey = "deal.role_card"
	// DealConfirmPromptMessageKey 是每玩家的独立临时确认消息（未确认态）。
	DealConfirmPromptMessageKey = "deal.confirm_prompt"
	// DealConfirmDoneMessageKey 是玩家确认后发给本人的「已确认」文案。
	DealConfirmDoneMessageKey = "deal.confirm_done"
	// DealConfirmDeleteMessageKey 是全员确认/超时后删除每玩家确认消息
	// 的语义 key（文本渲染由适配层决定）。
	DealConfirmDeleteMessageKey = "deal.confirm_delete"
	// PhaseNightStartMessageKey 是「第 1 夜」时间段主消息（公共）。
	// 发牌确认不计入第 1 夜主消息，且主消息在确认消息删除后才创建。
	PhaseNightStartMessageKey = "phase.night.start"
)

// DealConfirmTimeout 是发牌确认阶段的限时（docs/游戏流程设计.md §发牌：
// 全部确认或 10 秒超时后删除确认消息并创建第 1 夜主消息）。
const DealConfirmTimeout = 10 * time.Second

// 发牌确认阶段领域规则的哨兵错误（与 join.go 的 ErrRoomFull、
// settings.go 的 ErrSettingsLocked 语义区分，不重复）。
var (
	// ErrRoomNotFull 表示房间尚未满员，不能开始游戏。
	ErrRoomNotFull = errors.New("game: room is not full")
	// ErrAlreadyConfirmed 表示玩家已确认过身份（重复确认拒绝）。
	ErrAlreadyConfirmed = errors.New("game: actor already confirmed role")
)

// startGame 处理房主在满员大厅开始游戏（docs §开始游戏、§发牌）：
//  1. 仅房主可开始（ErrNotHost）；
//  2. 房间必须满员（ErrRoomNotFull）；
//  3. 配置必须通过 MVP 校验；
//  4. 标准牌组副本经无偏 Fisher–Yates 洗牌后按座位升序分配
//     （洗牌作用于副本，绝不修改 Lobby.Config.Roles——配置锁定语义）；
//  5. PhaseLobby → PhaseDeal、PhaseVersion+1、DealState.Confirmed 为空、
//     已受理命令标记 Processed；
//  6. Effects 顺序 = 每玩家私有角色卡（AudienceActor，狼人含 wolf_mates）
//     → 每玩家独立确认提示（AudienceActor）→ 10 秒 TimerEffect。
//
// 所有私密角色信息只出现在 AudienceActor 效果的 params 中，
// 不产生包含全员身份列表的公共对象（docs/技术选型.md §5.1 敏感视图）。
func (r reducer) startGame(st State, cmd StartGameCommand) (State, []Effect, error) {
	if cmd.Meta.Actor != st.Lobby.Owner {
		return st, nil, ErrNotHost
	}
	if len(st.Players) < st.Lobby.Config.PlayerCount {
		return st, nil, ErrRoomNotFull
	}
	if err := st.Lobby.Config.Validate(); err != nil {
		return st, nil, fmt.Errorf("game: start game config: %w", err)
	}

	seats, err := dealSeats(st.Players)
	if err != nil {
		return st, nil, err
	}
	roles := append([]Role(nil), st.Lobby.Config.Roles...)
	if err := Shuffle(roles, r.rng); err != nil {
		return st, nil, fmt.Errorf("game: deal shuffle: %w", err)
	}

	next := st.Copy()
	next.Phase = PhaseDeal
	next.PhaseVersion++
	next.Processed[cmd.Meta.ID] = true

	bySeat := make(map[Seat]int, len(next.Players))
	for i, p := range next.Players {
		bySeat[p.Seat] = i
	}
	for i, seat := range seats {
		next.Players[bySeat[seat]].Role = roles[i]
	}

	effects := make([]Effect, 0, 2*len(seats)+1)
	// 第一组：每玩家私有角色卡（座位序，AudienceActor，狼人含 wolf_mates）。
	for _, seat := range seats {
		p := next.Players[bySeat[seat]]
		params := map[string]any{
			"seat": seat,
			"role": p.Role.String(),
			"camp": p.Role.Camp().String(),
		}
		if p.Role == RoleWolf {
			params["wolf_mates"] = wolfMates(next.Players, seat)
		}
		card, err := NewMessageEffect(AudienceActor, DealRoleCardMessageKey, params)
		if err != nil {
			return st, nil, fmt.Errorf("game: deal role card: %w", err)
		}
		effects = append(effects, card)
	}
	// 第二组：每玩家独立确认提示（座位序，AudienceActor）。
	for _, seat := range seats {
		prompt, err := NewMessageEffect(AudienceActor, DealConfirmPromptMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: deal confirm prompt: %w", err)
		}
		effects = append(effects, prompt)
	}
	// 第三组：发牌确认限时 10 秒计时器。
	effects = append(effects, TimerEffect{Phase: PhaseDeal, Duration: DealConfirmTimeout})
	return next, effects, nil
}

// confirmRole 处理玩家确认已查看身份（docs §发牌）：
//   - 记录本人座位；重复确认拒绝（ErrAlreadyConfirmed）；
//   - 本人收到 deal.confirm_done；
//   - 全员确认后：Timer Cancel → 每玩家 deal.confirm_delete（删除在前）
//     → AudiencePublic phase.night.start（phase_number=1，创建在后），
//     PhaseDeal → PhaseNight、PhaseVersion+1。
func (r reducer) confirmRole(st State, cmd ConfirmRoleCommand) (State, []Effect, error) {
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	for _, confirmed := range st.Deal.Confirmed {
		if confirmed == seat {
			return st, nil, ErrAlreadyConfirmed
		}
	}

	next := st.Copy()
	next.Deal.Confirmed = append(next.Deal.Confirmed, seat)
	next.Processed[cmd.Meta.ID] = true

	done, err := NewMessageEffect(AudienceActor, DealConfirmDoneMessageKey, map[string]any{"seat": seat})
	if err != nil {
		return st, nil, fmt.Errorf("game: deal confirm done: %w", err)
	}
	effects := []Effect{done}

	if len(next.Deal.Confirmed) >= len(next.Players) {
		after, transition, err := completeDealTransition(next)
		if err != nil {
			return st, nil, err
		}
		return after, append(effects, transition...), nil
	}
	return next, effects, nil
}

// timeoutDeal 处理发牌确认阶段超时（10 秒）：自动确认所有未确认玩家，
// 并走与全员确认相同的过渡（Timer Cancel → 删除 → 第 1 夜主消息）。
func (r reducer) timeoutDeal(st State, cmd TimeoutCommand) (State, []Effect, error) {
	seats, err := dealSeats(st.Players)
	if err != nil {
		return st, nil, err
	}
	next := st.Copy()
	next.Deal.Confirmed = append([]Seat(nil), seats...)
	next.Processed[cmd.Meta.ID] = true
	return completeDealTransition(next)
}

// completeDealTransition 执行发牌确认完成后的统一过渡。先构造全部
// Effects（原子性：任一构造失败不部分修改状态），再切换阶段：
//  1. TimerEffect Cancel（停止发牌确认计时器）；
//  2. 每玩家 AudienceActor deal.confirm_delete（确认消息删除在前）；
//  3. AudiencePublic phase.night.start（第 1 夜主消息创建在后）；
//  4. Phase=PhaseNight、PhaseVersion+1。
func completeDealTransition(st State) (State, []Effect, error) {
	seats, err := dealSeats(st.Players)
	if err != nil {
		return st, nil, err
	}
	effects := make([]Effect, 0, len(seats)+2)
	effects = append(effects, TimerEffect{Phase: PhaseDeal, Cancel: true})
	for _, seat := range seats {
		del, err := NewMessageEffect(AudienceActor, DealConfirmDeleteMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: deal confirm delete: %w", err)
		}
		effects = append(effects, del)
	}
	night, err := NewMessageEffect(AudiencePublic, PhaseNightStartMessageKey, map[string]any{"phase_number": 1})
	if err != nil {
		return st, nil, fmt.Errorf("game: phase night start: %w", err)
	}
	effects = append(effects, night)

	st.Phase = PhaseNight
	st.PhaseVersion++
	return st, effects, nil
}

// dealSeats 返回按座位升序排列的有效座位（发牌按座位序分配，
// docs §开始游戏：座位 1、2、3…按加入顺序固定）。存在非法座位时报错。
func dealSeats(players []Player) ([]Seat, error) {
	seats := make([]Seat, 0, len(players))
	for _, p := range players {
		if !p.Seat.Valid() {
			return nil, fmt.Errorf("game: invalid seat %v during deal", p.Seat)
		}
		seats = append(seats, p.Seat)
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	return seats, nil
}

// seatByUser 返回用户在成员列表中的座位；不在房间返回 ok=false。
func seatByUser(players []Player, user UserID) (Seat, bool) {
	for _, p := range players {
		if p.UserID == user {
			return p.Seat, true
		}
	}
	return 0, false
}

// wolfMates 返回指定狼人座位的狼队友座位列表（升序，不含自己）。
func wolfMates(players []Player, self Seat) []Seat {
	var mates []Seat
	for _, p := range players {
		if p.Role == RoleWolf && p.Seat != self {
			mates = append(mates, p.Seat)
		}
	}
	sort.Slice(mates, func(i, j int) bool { return mates[i] < mates[j] })
	return mates
}
