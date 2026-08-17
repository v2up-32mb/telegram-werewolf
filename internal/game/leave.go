package game

import (
	"fmt"
	"time"
)

// 游戏内退出/恶意退出领域规则（docs 游戏流程设计.md §恶意退出判定、
// §狼人自爆 2、§退出约束、§五.5 重大事件）：
//   - LeaveGameCommand 仅游戏进行中（PhaseDaySpeech / PhaseDayVote /
//     PhaseNight）受理；大厅等其他阶段 → ErrWrongPhase（大厅退出走接线层
//     既有大厅流程，本命令只覆盖游戏内退出）；
//   - 狼人白天退出 → 按自爆处理（复用 explode.applyWolfExplode）：直接
//     黑夜、无遗言、已投票作废；区别于 ExplodeCommand：这是主动退出，
//     置 Left 并触发加入冷却（LeaveReasonWolfExplode，docs §自爆 2）；
//   - 夜间退出（任意角色，含狼人）→ 不算自爆；标记死亡；公告为
//     「恶意退出死亡」（leave.malicious，不误导身份，docs §自爆 2）；
//   - 白天非狼人存活主动退出 → 标记死亡 + leave.malicious + 冷却
//     （LeaveReasonMaliciousActive）；
//   - 所有游戏内退出/强制移除一律产出 PersistGameLeave（供接线层写入
//     JoinStore.HasLeft 记录：退出玩家不能重入同一局，docs §退出约束）；
//   - 重大事件只写当前时间段主消息，不额外发送永久事件消息（docs §五.5）。
//
// 已知边界（以真实代码为准，不得伪造）：
//   - 跨局 10 分钟加入冷却只以 CooldownEffect + CooldownFor(LeaveReason)
//     领域契约表达；冷却持久化与「冷却期间不能创建/加入房间」的跨局拦截
//     属接线/存储层（internal/storage 无相关列），本文件不修改
//     lobby.go/join.go/storage 的拦截实现；
//   - 同局重入拦截沿用既有 JoinStore.HasLeft seam（internal/game/join.go，
//     ErrAlreadyLeft 契约与测试已存在）。

// 恶意退出/超时消息 key（docs §恶意退出判定、§退出约束、§五.5）。
const (
	// LeaveMaliciousMessageKey 是恶意退出死亡公共公告（写当前时间段
	// 主消息）：params seat。
	LeaveMaliciousMessageKey = "leave.malicious"
	// LeaveTimeoutWarningMessageKey 是连续超时私聊预警（AudienceActor，
	// 不全局广播，docs §恶意退出判定 ③）：params streak/remaining。
	LeaveTimeoutWarningMessageKey = "leave.timeout_warning"
	// LeaveRemovedMessageKey 是连续超时强制移除公共公告（写当前时间段
	// 主消息）：params seat。
	LeaveRemovedMessageKey = "leave.removed"
)

// LeaveReason 是退出/移除的原因，用于跨局加入冷却（CooldownFor）判定
// 与接线层记录（docs §退出约束）。
type LeaveReason int

const (
	LeaveReasonUnknown         LeaveReason = iota
	LeaveReasonMaliciousActive             // 游戏进行中存活主动退出
	LeaveReasonWolfExplode                 // 狼人白天退出按自爆
	LeaveReasonMaliciousNight              // 夜间恶意退出死亡（不误导身份）
	LeaveReasonForcedTimeout               // 连续 3 次超时强制移除
	LeaveReasonVoteKicked                  // 游戏中被投票踢出（Task 39 复用，本任务只留冷却契约）
	LeaveReasonNormalDeath                 // 正常死亡后退出
	LeaveReasonPreGame                     // 游戏开始前退出
	LeaveReasonGameEnd                     // 正常完成一局/再来一局
	LeaveReasonRoomClosed                  // 房主强制解散/投票解散
	LeaveReasonAborted                     // Bot 重启导致对局中止
)

// LeaveCooldown 是存活主动退出/强制移除/投票踢出触发的跨局加入
// 冷却时长（docs §退出约束：10 分钟）。
const LeaveCooldown = 600 * time.Second

// CooldownFor 报告指定退出原因是否触发 10 分钟跨局加入冷却
// （docs §退出约束）：触发 = 游戏进行中存活主动退出（含狼人白天退出
// 按自爆）/连续 3 次超时强制移除/游戏中被投票踢出；不触发 = 正常死亡
// 后退出/游戏前退出/正常完成一局/再来一局/投票解散/房主强制解散/
// Bot 重启中止。
func CooldownFor(reason LeaveReason) bool {
	switch reason {
	case LeaveReasonMaliciousActive, LeaveReasonWolfExplode, LeaveReasonMaliciousNight,
		LeaveReasonForcedTimeout, LeaveReasonVoteKicked:
		return true
	default:
		return false
	}
}

// leaveGame 处理游戏进行中的主动退出（通用 validator 已校验重复 ID/
// 阶段/版本/在场/存活）。
func (r reducer) leaveGame(st State, cmd LeaveGameCommand) (State, []Effect, error) {
	if st.Phase != PhaseDaySpeech && st.Phase != PhaseDayVote && st.Phase != PhaseNight {
		return st, nil, ErrWrongPhase
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}

	// 狼人白天退出按自爆处理（docs §自爆 2）：复用 explode 状态转换，
	// 再补主动退出的 Left 标记与加入冷却。
	if st.Phase != PhaseNight && roleAtSeat(st.Players, seat) == RoleWolf {
		after, effects, err := r.applyWolfExplode(st, seat)
		if err != nil {
			return st, nil, err
		}
		after.Processed[cmd.Meta.ID] = true
		markPlayerLeft(after.Players, seat)
		effects = append(effects,
			CooldownEffect{User: cmd.Meta.Actor, Duration: LeaveCooldown, Reason: LeaveReasonWolfExplode},
			PersistEffect{Kind: PersistGameLeave},
		)
		// I6：房主游戏内离开 → 移交给下一位在场玩家（docs §房主移交）。
		effects = maybeTransferHostOnLeave(&after, seat, effects)
		return after, effects, nil
	}

	reason := LeaveReasonMaliciousActive
	if st.Phase == PhaseNight {
		reason = LeaveReasonMaliciousNight
	}
	next := st.Copy()
	markPlayerDead(next.Players, seat)
	markPlayerLeft(next.Players, seat)
	markPlayerMalicious(next.Players, seat) // 恶意退出（结算积分为 0/-5）
	next.Processed[cmd.Meta.ID] = true

	ann, err := NewMessageEffect(AudiencePublic, LeaveMaliciousMessageKey, map[string]any{"seat": seat})
	if err != nil {
		return st, nil, fmt.Errorf("game: malicious leave announce: %w", err)
	}
	effects := []Effect{
		ann,
		CooldownEffect{User: cmd.Meta.Actor, Duration: LeaveCooldown, Reason: reason},
		PersistEffect{Kind: PersistGameLeave},
	}
	// I6：房主游戏内退出 → 移交给下一位在场玩家。
	effects = maybeTransferHostOnLeave(&next, seat, effects)
	return next, effects, nil
}

// markPlayerLeft 把指定座位标记为已离开本局（无该座位时静默）。
func markPlayerLeft(players []Player, seat Seat) {
	for i := range players {
		if players[i].Seat == seat {
			players[i].Left = true
			return
		}
	}
}

// markPlayerMalicious 把指定座位标记为恶意退出（仅「游戏进行中存活主动
// 退出」路径调用；连续超时强制移除在 advanceTimeoutStreaks 内直接置位；
// 无该座位时静默）。
func markPlayerMalicious(players []Player, seat Seat) {
	for i := range players {
		if players[i].Seat == seat {
			players[i].MaliciousExit = true
			return
		}
	}
}

// maybeTransferHostOnLeave 在玩家离开/被移除后处理房主移交（docs §房主移交）：
// 离开者是房主且仍有在场存活玩家时，按加入顺序（座位升序）移交给下一位，
// 仅通知新房主（lobby.host_transferred，AudienceActor）。无存活玩家或离开者
// 非房主时原样返回。base 为既有效果序列，追加移交通知后返回。
func maybeTransferHostOnLeave(st *State, leavingSeat Seat, base []Effect) []Effect {
	leaver := playerBySeat(st.Players, leavingSeat)
	if leaver.UserID == 0 || st.Lobby.Owner != leaver.UserID {
		return base
	}
	alive := aliveSeats(st.Players)
	if len(alive) == 0 {
		return base
	}
	newHost := playerBySeat(st.Players, alive[0]).UserID
	st.Lobby.Owner = newHost
	msg, err := NewMessageEffect(AudienceActor, HostTransferredMessageKey, map[string]any{
		"room_code": string(st.RoomID),
		"host":      newHost,
	})
	if err != nil {
		return base
	}
	return append(base, msg)
}

// advanceTimeoutStreaks 处理一次超时事件的连续超时计数（docs §恶意退出
// 判定 ②③、§退出约束）：
//   - unresponsive 为本轮有明确确认窗口但未操作的存活玩家座位；每人
//     TimeoutStreak+1；
//   - 本轮已操作（不在 unresponsive 集合）的存活玩家 TimeoutStreak 清零
//     （「中间操作过则重置」由超时结算实现，docs §恶意退出判定 ②）；
//     死亡/已离开玩家跳过；
//   - 达到 2 → 私聊预警（AudienceActor，不全局广播）：leave.timeout_warning
//     params streak/remaining；
//   - 达到 3 → 强制移除：死亡 + Left + leave.removed 公共公告（写当前
//     时间段主消息）+ CooldownEffect{ForcedTimeout} + PersistGameLeave。
//
// MVP 边界（如实注释，不扩实现）：
//   - 当前由 reducer 在白天投票收票超时分派接入（TestVoteTimeoutWiresStreak
//     真实端到端验证）；发牌确认/夜间狼人/女巫/预言家窗口与白天发言麦位
//     的超时计数接线、以及「操作成功即时清零」的逐操作实现属后续任务/
//     接线层，本文件不扩实现；
//   - at 保留给未来计时语义扩展（如按截止时间区分窗口），当前无时间
//     相关分支。
func (r reducer) advanceTimeoutStreaks(st State, at time.Time, unresponsive []Seat) (State, []Effect, error) {
	_ = at
	next := st.Copy()
	set := make(map[Seat]bool, len(unresponsive))
	for _, s := range unresponsive {
		if s.Valid() {
			set[s] = true
		}
	}
	var effects []Effect
	for i := range next.Players {
		p := &next.Players[i]
		if p.Dead || p.Left || !p.Seat.Valid() {
			continue
		}
		if !set[p.Seat] {
			p.TimeoutStreak = 0
			continue
		}
		p.TimeoutStreak++
		switch p.TimeoutStreak {
		case 2:
			warn, err := NewMessageEffect(AudienceActor, LeaveTimeoutWarningMessageKey, map[string]any{
				"streak":    p.TimeoutStreak,
				"remaining": 1,
			})
			if err != nil {
				return st, nil, fmt.Errorf("game: timeout warning: %w", err)
			}
			effects = append(effects, warn)
		case 3:
			p.Dead = true
			p.Left = true
			p.MaliciousExit = true // 连续 3 次超时强制移除（积分按恶意退出口径）
			removed, err := NewMessageEffect(AudiencePublic, LeaveRemovedMessageKey, map[string]any{"seat": p.Seat})
			if err != nil {
				return st, nil, fmt.Errorf("game: timeout removal: %w", err)
			}
			effects = append(effects,
				removed,
				CooldownEffect{User: p.UserID, Duration: LeaveCooldown, Reason: LeaveReasonForcedTimeout},
				PersistEffect{Kind: PersistGameLeave},
			)
			// I6：被强制移除者若是房主 → 移交下一位。
			effects = maybeTransferHostOnLeave(&next, p.Seat, effects)
		}
	}
	return next, effects, nil
}

// unconfirmedVoters 返回白天投票收票窗口内未确认锁定的存活玩家座位
// （供 reducer 在 voteTimeout 前维护连续超时计数）。
func unconfirmedVoters(st State) []Seat {
	var seats []Seat
	for _, s := range aliveSeats(st.Players) {
		if !st.Vote.Locked[s] {
			seats = append(seats, s)
		}
	}
	return seats
}
