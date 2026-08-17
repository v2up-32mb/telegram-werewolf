package game

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// 狼人夜间阶段的消息 key（docs §夜间 2、§13.4 狼人讨论：讨论为独立
// 消息，存活狼人副本在狼人阶段结束后删除，上帝视角副本永久保留）。
const (
	// WolfDiscussMessageKey 是狼人讨论消息（AudienceWolf 群发存活狼人、
	// AudienceGodView 群发上帝视角玩家；狼人标识只对这两类可见）。
	WolfDiscussMessageKey = "wolf.discuss"
	// WolfVoteMessageKey 是狼人投票 UI（AudienceWolf，含轮次与候选目标）。
	WolfVoteMessageKey = "wolf.vote"
	// WolfVoteLockedMessageKey 是狼人确认锁定文案（AudienceWolf）。
	WolfVoteLockedMessageKey = "wolf.vote.locked"
	// WolfTieMessageKey 是首次平票进入第二轮的通知（AudienceWolf）。
	WolfTieMessageKey = "wolf.tie"
	// WolfDiscussDeleteMessageKey 是狼人阶段结束后删除存活狼人讨论副本
	// 的语义 key（无文本渲染；上帝视角副本不删除）。
	WolfDiscussDeleteMessageKey = "wolf.discuss_delete"
	// WolfVoteDeleteMessageKey 是狼人阶段结束后删除投票消息的语义 key。
	WolfVoteDeleteMessageKey = "wolf.vote_delete"
)

// 狼人投票领域规则的哨兵错误（docs §夜间 2；与 deal.go 的
// ErrAlreadyConfirmed、join/settings 既有哨兵语义区分，不重复）。
var (
	// ErrNotWolf 表示操作者不是狼人（只有存活狼人可讨论/投票）。
	ErrNotWolf = errors.New("game: only wolves may do this")
	// ErrWolfVoteLocked 表示狼人选择已确认锁定，本轮不能修改/重复确认。
	ErrWolfVoteLocked = errors.New("game: wolf vote already locked")
	// ErrWolfMustKill 表示默认必须刀人时主动空刀被拒（docs「狼人空刀」）。
	ErrWolfMustKill = errors.New("game: wolves must kill (empty kill disabled)")
	// ErrWolfNoSelection 表示必须刀人时未选择目标即确认。
	ErrWolfNoSelection = errors.New("game: wolf must select a target before confirming")
	// ErrWolfVoteClosed 表示狼人投票窗口已关闭（阶段未开始或已结束）。
	ErrWolfVoteClosed = errors.New("game: wolf vote is closed")
)

// BeginWolfPhase 在夜间开始（后续 P0 接线层收到 phase.night.start 时调用，
// 与 MediaCache/SendRoleCard 的接线延期同理；本任务提供领域函数与测试，
// 不改动 deal.go 与 Task 28 既有过渡效果契约）初始化狼人投票窗口：
//   - WolfRound=1、WolfVotes/WolfLocked 初始化为空；
//   - 每名存活狼人收到 wolf.discuss 讨论副本与 wolf.vote 投票 UI
//     （AudienceWolf 群发）；
//   - 每个狼人阶段开始时已在上帝视角（已死亡）的玩家收到同内容讨论
//     副本（AudienceGodView，永久保留）；死亡玩家不补发死亡前错过的讨论；
//   - TimerEffect{Phase:PhaseNight, Duration=Settings 生效狼人时长}。
//
// 狼人标识与队友名单只出现在 wolf.* 消息 params（AudienceWolf/
// AudienceGodView），绝不进入 AudiencePublic（docs §狼人标识 双保险）。
func BeginWolfPhase(st State) (State, []Effect, error) {
	effects := make([]Effect, 0, 4)
	mates := aliveWolfSeats(st.Players)

	discuss, err := NewMessageEffect(AudienceWolf, WolfDiscussMessageKey, map[string]any{
		"round":      1,
		"wolf_mates": mates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf discuss message: %w", err)
	}
	effects = append(effects, discuss)

	vote, err := NewMessageEffect(AudienceWolf, WolfVoteMessageKey, map[string]any{
		"round":      1,
		"targets":    aliveSeats(st.Players),
		"wolf_mates": mates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf vote message: %w", err)
	}
	effects = append(effects, vote)

	for _, p := range st.Players {
		if p.Dead && p.Seat.Valid() {
			god, err := NewMessageEffect(AudienceGodView, WolfDiscussMessageKey, map[string]any{
				"round":      1,
				"wolf_mates": mates,
			})
			if err != nil {
				return st, nil, fmt.Errorf("game: wolf god view discuss: %w", err)
			}
			effects = append(effects, god)
			break // AudienceGodView 群发给所有上帝视角玩家
		}
	}

	next := st.Copy()
	next.Night.WolfRound = 1
	next.Night.WolfVotes = map[Seat]*Seat{}
	next.Night.WolfLocked = map[Seat]bool{}
	effects = append(effects, TimerEffect{Phase: PhaseNight, Duration: wolfNightDuration(next.Settings)})
	return next, effects, nil
}

// wolfNightDuration 返回狼人夜间投票限时（Settings 生效时长，默认 30 秒，
// docs「夜间限时」/「狼人夜间时长」）；零值设置防御性回退默认值。
func wolfNightDuration(s RoomSettings) time.Duration {
	_, wolfSeconds, _ := s.EffectiveDurations()
	if wolfSeconds <= 0 {
		wolfSeconds = DefaultWolfNightSeconds
	}
	return time.Duration(wolfSeconds) * time.Second
}

// wolfVote 处理狼人选择刀人目标（docs §夜间 2：讨论与投票并行、可选择
// 任意存活玩家包括自己和狼队友、确认前最终选择可覆盖）：
//   - 仅存活狼人（ErrNotWolf）且投票窗口开启（ErrWolfVoteClosed）；
//   - 确认后本轮不能修改（ErrWolfVoteLocked）；
//   - 目标存活校验由通用 validator 保证（死亡/越界 ErrInvalidTarget）；
//   - Target=nil 为主动空刀，仅 Settings.WolfMustKill=false 允许
//     （默认必须刀人 ErrWolfMustKill）。
func (r reducer) wolfVote(st State, cmd WolfVoteCommand) (State, []Effect, error) {
	if st.Night.WolfRound < 1 {
		return st, nil, ErrWolfVoteClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if roleAtSeat(st.Players, seat) != RoleWolf {
		return st, nil, ErrNotWolf
	}
	if st.Night.WolfLocked[seat] {
		return st, nil, ErrWolfVoteLocked
	}
	if cmd.Target == nil && st.Settings.WolfMustKill {
		return st, nil, ErrWolfMustKill
	}

	next := st.Copy()
	if cmd.Target == nil {
		next.Night.WolfVotes[seat] = nil
	} else {
		target := *cmd.Target
		next.Night.WolfVotes[seat] = &target
	}
	next.Processed[cmd.Meta.ID] = true
	return next, nil, nil
}

// wolfConfirm 处理狼人确认选择（docs §夜间 2：每名存活狼人选择后须点击
// 「确认选择」，确认后本轮不能修改；所有存活狼人确认后可提前结束）：
//   - 仅存活狼人（ErrNotWolf）且窗口开启（ErrWolfVoteClosed）；
//   - 确认后不能修改/重复确认（ErrWolfVoteLocked）；
//   - 必须刀人时未选择即确认 → ErrWolfNoSelection；空刀配置下未选择
//     视为空刀并锁定；
//   - 所有存活狼人确认后立即结算（resolveWolfVotes）：多数落定、
//     首次平票重开第二轮、第二轮平票由注入 RNG 随机落定。
func (r reducer) wolfConfirm(st State, cmd WolfConfirmCommand) (State, []Effect, error) {
	if st.Night.WolfRound < 1 {
		return st, nil, ErrWolfVoteClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if roleAtSeat(st.Players, seat) != RoleWolf {
		return st, nil, ErrNotWolf
	}
	if st.Night.WolfLocked[seat] {
		return st, nil, ErrWolfVoteLocked
	}
	if _, has := st.Night.WolfVotes[seat]; !has && st.Settings.WolfMustKill {
		return st, nil, ErrWolfNoSelection
	}

	next := st.Copy()
	if _, has := next.Night.WolfVotes[seat]; !has {
		next.Night.WolfVotes[seat] = nil // 空刀配置下未选择 = 空刀
	}
	next.Night.WolfLocked[seat] = true
	next.Processed[cmd.Meta.ID] = true

	ackParams := map[string]any{
		"seat":   seat,
		"round":  next.Night.WolfRound,
		"target": next.Night.WolfVotes[seat],
	}
	ack, err := NewMessageEffect(AudienceWolf, WolfVoteLockedMessageKey, ackParams)
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf confirm ack: %w", err)
	}
	effects := []Effect{ack}

	if allAliveWolvesLocked(next) {
		after, resolved, err := r.resolveWolfVotes(next)
		if err != nil {
			return st, nil, err
		}
		return after, append(effects, resolved...), nil
	}
	return next, effects, nil
}

// wolfTimeout 处理狼人投票超时（docs「超时默认」：狼人夜晚刀人超时 →
// 弃刀）：WolfKillTarget 保持 nil、WolfRound=0、删除存活狼人讨论/投票
// 副本（上帝视角副本不删除）。
// I4：未锁定存活狼人计入整局连续超时计数（docs §恶意退出判定②；死亡/已
// 离开者由 advanceTimeoutStreaks 跳过）。
func (r reducer) wolfTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Night.WolfRound < 1 {
		return st, nil, ErrWolfVoteClosed
	}
	st1, fx1, err := r.advanceTimeoutStreaks(st, cmd.Meta.ReceivedAt, unresponsiveWolves(st))
	if err != nil {
		return st, nil, err
	}
	next := st1.Copy()
	next.Processed[cmd.Meta.ID] = true
	after, fx2, err := endWolfPhase(next, nil)
	if err != nil {
		return st, nil, err
	}
	return after, append(fx1, fx2...), nil
}

// unresponsiveWolves 返回狼人超时时未确认锁定的存活狼人座位。
func unresponsiveWolves(st State) []Seat {
	var out []Seat
	for _, seat := range aliveWolfSeats(st.Players) {
		if !st.Night.WolfLocked[seat] {
			out = append(out, seat)
		}
	}
	return out
}

// resolveWolfVotes 在全部存活狼人确认后结算票数（docs §夜间 2）：
//   - 多数目标得票 → WolfKillTarget 落定并结束狼人阶段；
//   - 首次平票（WolfRound=1）→ 清空确认状态、保留投票选择、WolfRound=2，
//     重发 wolf.tie + 投票 UI（round=2）+ 30 秒计时器；
//   - 第二轮平票 → 从平票目标（含空刀）中由注入 RNG 随机落定；
//   - 结束统一走 endWolfPhase：WolfRound=0、存活狼人收到讨论/投票删除
//     （AudienceWolf），上帝视角副本不删除。
func (r reducer) resolveWolfVotes(st State) (State, []Effect, error) {
	emptyCount := 0
	counts := map[Seat]int{}
	for _, seat := range aliveWolfSeats(st.Players) {
		target, ok := st.Night.WolfVotes[seat]
		if !ok {
			return st, nil, fmt.Errorf("game: wolf %d locked without selection", seat)
		}
		if target == nil {
			emptyCount++
			continue
		}
		counts[*target]++
	}

	maxVotes := emptyCount
	winners := []*Seat(nil) // nil 表示空刀（空刀可参与平票并可能被随机选中）
	if emptyCount > 0 {
		winners = append(winners, nil)
	}
	for target, n := range counts {
		switch {
		case n > maxVotes:
			maxVotes = n
			t := target
			winners = []*Seat{&t}
		case n == maxVotes && maxVotes > 0:
			t := target
			winners = append(winners, &t)
		}
	}

	// 首次平票：清空确认状态、保留选择、进入第二轮。
	if len(winners) > 1 && st.Night.WolfRound == 1 {
		return r.reopenWolfRound(st)
	}

	// 第二轮（或唯一多数）：平票由注入 RNG 随机落定。
	if len(winners) > 1 {
		// 候选先按座位（空刀 nil 视为最小）稳定排序：Go map 迭代顺序
		// 随机，若不排序会使同一注入 RNG 值选出不同目标，测试与日志
		// 无法复现；排序不改变「均匀随机选中」的领域语义（docs §夜间 2
		// 第二轮平票随机落定）。
		sort.Slice(winners, func(i, j int) bool {
			if winners[i] == nil {
				return true
			}
			if winners[j] == nil {
				return false
			}
			return *winners[i] < *winners[j]
		})
		idx, err := r.rng.Intn(len(winners))
		if err != nil {
			return st, nil, fmt.Errorf("game: wolf tie rng: %w", err)
		}
		winners = winners[idx : idx+1]
	}
	if len(winners) == 0 {
		// 防御：理论上不可达（全员锁定必有投票记录），按弃刀处理。
		return endWolfPhase(st, nil)
	}
	kill := winners[0]
	if kill != nil {
		v := *kill
		kill = &v
	}
	return endWolfPhase(st, kill)
}

// reopenWolfRound 处理首次平票：WolfRound=2、WolfLocked 清空（确认状态
// 清空）、WolfVotes 保留（可继续覆盖）、重发 wolf.tie 与投票 UI（round
// =2）以及 30 秒计时器（docs §夜间 2）。
func (r reducer) reopenWolfRound(st State) (State, []Effect, error) {
	next := st.Copy()
	next.Night.WolfRound = 2
	next.Night.WolfLocked = map[Seat]bool{}

	tie, err := NewMessageEffect(AudienceWolf, WolfTieMessageKey, map[string]any{"round": 2})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf tie message: %w", err)
	}
	vote, err := NewMessageEffect(AudienceWolf, WolfVoteMessageKey, map[string]any{
		"round":      2,
		"targets":    aliveSeats(next.Players),
		"wolf_mates": aliveWolfSeats(next.Players),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf vote round 2 message: %w", err)
	}
	return next, []Effect{tie, vote, TimerEffect{Phase: PhaseNight, Duration: wolfNightDuration(next.Settings)}}, nil
}

// endWolfPhase 结束狼人阶段：WolfRound=0、落定 WolfKillTarget（nil=弃刀/
// 空刀平安夜）、每名存活狼人收到讨论与投票删除效果（AudienceWolf），
// 上帝视角副本永久保留不发删除（docs §13.4）。
func endWolfPhase(st State, kill *Seat) (State, []Effect, error) {
	next := st.Copy()
	next.Night.WolfRound = 0
	if kill != nil {
		v := *kill
		next.Night.WolfKillTarget = &v
	} else {
		next.Night.WolfKillTarget = nil
	}

	discussDel, err := NewMessageEffect(AudienceWolf, WolfDiscussDeleteMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf discuss delete: %w", err)
	}
	voteDel, err := NewMessageEffect(AudienceWolf, WolfVoteDeleteMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf vote delete: %w", err)
	}
	return next, []Effect{discussDel, voteDel}, nil
}

// allAliveWolvesLocked 报告所有存活狼人是否均已确认锁定。
func allAliveWolvesLocked(st State) bool {
	for _, seat := range aliveWolfSeats(st.Players) {
		if !st.Night.WolfLocked[seat] {
			return false
		}
	}
	return true
}

// aliveWolfSeats 返回存活狼人座位（升序）。
func aliveWolfSeats(players []Player) []Seat {
	var out []Seat
	for _, p := range players {
		if p.Role == RoleWolf && !p.Dead && p.Seat.Valid() {
			out = append(out, p.Seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// aliveSeats 返回全部存活玩家座位（升序），供投票目标列表使用。
func aliveSeats(players []Player) []Seat {
	var out []Seat
	for _, p := range players {
		if !p.Dead && p.Seat.Valid() {
			out = append(out, p.Seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// playerBySeat 返回指定座位的玩家；不存在返回零值 Player。
func playerBySeat(players []Player, seat Seat) Player {
	for _, p := range players {
		if p.Seat == seat {
			return p
		}
	}
	return Player{}
}

// roleAtSeat 返回指定座位的角色；非法/不存在返回 RoleUnknown。
func roleAtSeat(players []Player, seat Seat) Role {
	return playerBySeat(players, seat).Role
}
