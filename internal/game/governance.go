package game

import (
	"errors"
	"fmt"
	"sort"
)

// 游戏中治理机制领域规则（docs 游戏流程设计.md §解散 87-90、§投票踢人
// 92-95、§房主控制面板 97-98、§积分系统 100-104、§掉线处理 183-216）：
//   - 投票解散/投票踢人：仅存活玩家参与；超过三分之一同意即通过；发起
//     限制 = 局内每人限发起 1 次 + 每个阶段限发起 1 次；同意票制（发起者
//     计一票），未达阈值时投票保持开启，阶段切换时清空本轮投票；
//   - 踢人走掉线规则（判负移除语义）：被踢者标记死亡 + Left + 10 分钟
//     跨局加入冷却（LeaveReasonVoteKicked，复用 Task 38 契约）+
//     PersistGameLeave（退出玩家不能重入同一局）；
//   - 投票解散通过不扣分；房主强制解散需二次确认、扣 10 分、积分 ≤9
//     禁止（积分由接线层读取后经 HostDissolveCommand.HostScore 传入）；
//   - 房主控制面板按钮与普通游戏操作按钮分开呈现属 telegram 视图层
//     （views_host.go），本文件只做领域契约。
//
// 接线边界（以真实代码为准，不得伪造）：
//   - 房间实际关闭/回收（邀请链接失效等）由接线层收到 DissolveEffect 后
//     执行；积分读取与落库、投票超时关闭与「未通过」公告、AI 托管/判负
//     移除的定时接线属接线层/后续任务；
//   - game 核心只产出 DissolveEffect / ScorePenaltyEffect / CooldownEffect /
//     PersistEffect 契约，不触碰 SQLite（docs/技术选型.md §5.1）。

// GovernanceState 是游戏中治理状态的最小字段：投票解散/投票踢人的同意票、
// 每局发起人集合、每阶段发起标记、当前踢人目标与房主强制解散二次确认
// 待确认标记。
type GovernanceState struct {
	// PhaseVersion 记录每阶段限制对应的阶段版本：与 State.PhaseVersion
	// 不一致时（阶段切换）由 syncGovernancePhase 重置每阶段字段。
	PhaseVersion uint64

	// DissolveVotes 是投票解散同意票（键存在=已投同意票；发起者计一票）。
	DissolveVotes map[Seat]bool
	// DissolveBy 是本局已发起过投票解散的玩家（每人每局限 1 次）。
	DissolveBy map[Seat]bool
	// DissolveInitiated 标记本阶段是否已发起过投票解散（每阶段限 1 次）。
	DissolveInitiated bool

	// KickVotes 是投票踢人同意票。
	KickVotes map[Seat]bool
	// KickBy 是本局已发起过投票踢人的玩家（每人每局限 1 次）。
	KickBy map[Seat]bool
	// KickInitiated 标记本阶段是否已发起过投票踢人（每阶段限 1 次）。
	KickInitiated bool
	// KickTarget 是当前投票踢人目标（nil=未发起）。
	KickTarget *Seat

	// HostDissolvePending 标记房主强制解散是否已进入二次确认待确认态
	//（Confirm=false 置位；Confirm=true 校验并复位）。
	HostDissolvePending bool
}

// DissolveReason 是房间解散的原因（docs §解散：投票解散/房主强制解散）。
type DissolveReason int

const (
	DissolveReasonUnknown DissolveReason = iota
	DissolveVoted                        // 投票解散通过（不扣分）
	HostForced                           // 房主强制解散（扣 10 分）
)

// DissolveEffect 表示房间解散语义：由接线层执行真实房间关闭/回收
// （邀请链接失效等），game 核心只表达契约。
type DissolveEffect struct {
	Reason DissolveReason
}

func (DissolveEffect) effect() {}

// ScorePenaltyEffect 表示积分扣减语义（docs §积分系统 2：房主强制解散
// 扣 10 分）：由接线层落库，game 核心只表达契约（积分 ≤9 禁止由
// HostDissolveCommand.HostScore 入参校验）。
type ScorePenaltyEffect struct {
	Amount int
}

func (ScorePenaltyEffect) effect() {}

// 治理消息 key（docs 五.5 重大事件/治理公告写当前时间段主消息；
// governance.* 非敏感前缀，可 AudiencePublic；vote 类为个人确认反馈）。
const (
	GovernanceDissolveInitiatedMessageKey   = "governance.dissolve.initiated"
	GovernanceDissolveVoteMessageKey        = "governance.dissolve.vote"
	GovernanceDissolvePassedMessageKey      = "governance.dissolve.passed"
	GovernanceKickInitiatedMessageKey       = "governance.kick.initiated"
	GovernanceKickVoteMessageKey            = "governance.kick.vote"
	GovernanceKickPassedMessageKey          = "governance.kick.passed"
	GovernanceHostDissolveConfirmMessageKey = "governance.host_dissolve.confirm"
	GovernanceHostDissolvePassedMessageKey  = "governance.host_dissolve.passed"
)

// 治理领域规则的哨兵错误（与既有 lobby ErrNotHost 语义复用：不是房主）。
var (
	// ErrAlreadyInitiated 表示发起者本局已发起过同类治理投票
	//（局内每人限发起 1 次，docs §解散 3、§投票踢人 3）。
	ErrAlreadyInitiated = errors.New("game: already initiated this governance vote this game")
	// ErrPhaseAlreadyInitiated 表示本阶段已发起过同类治理投票
	//（每个阶段总共限发起 1 次）。
	ErrPhaseAlreadyInitiated = errors.New("game: governance vote already initiated this phase")
	// ErrNotInitiated 表示治理投票未发起即投票。
	ErrNotInitiated = errors.New("game: governance vote not initiated")
	// ErrAlreadyVoted 表示已投过同意票（每轮投票一次）。
	ErrAlreadyVoted = errors.New("game: already voted in this governance vote")
	// ErrGovernanceKickSelf 表示不能投票踢自己（目标必须是其他存活玩家）。
	ErrGovernanceKickSelf = errors.New("game: cannot vote to kick self")
	// ErrHostDissolveNotConfirmed 表示未先请求二次确认即确认强制解散。
	ErrHostDissolveNotConfirmed = errors.New("game: host dissolve not confirmed (two-step confirm required)")
	// ErrInsufficientScore 表示房主积分 ≤9，无法强制解散（docs §积分系统 2）。
	ErrInsufficientScore = errors.New("game: host score too low to force dissolve")
)

// governanceValidPhase 报告治理命令是否可在当前阶段受理：仅游戏中
// （发牌确认/夜间/白天发言/白天投票）；大厅与结算阶段走各自流程，
// 治理命令被拒（ErrWrongPhase）。
func governanceValidPhase(phase Phase) bool {
	switch phase {
	case PhaseDeal, PhaseNight, PhaseDaySpeech, PhaseDayVote:
		return true
	default:
		return false
	}
}

// governanceAliveSeats 返回治理参与玩家（存活且未离开，升序）。
func governanceAliveSeats(players []Player) []Seat {
	var seats []Seat
	for _, p := range players {
		if !p.Dead && !p.Left && p.Seat.Valid() {
			seats = append(seats, p.Seat)
		}
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	return seats
}

// syncGovernancePhase 在受理治理命令前同步每阶段限制：阶段版本变化时
// 清空本轮投票与「本阶段已发起」标记并记录当前阶段版本；每局发起人
// 集合（DissolveBy/KickBy）与 HostDissolvePending 不随阶段重置。
// 入参必须是副本（纯值语义，docs/技术选型.md §5.1）。
func syncGovernancePhase(st *State) {
	if st.Governance.PhaseVersion == st.PhaseVersion {
		return
	}
	st.Governance.PhaseVersion = st.PhaseVersion
	st.Governance.DissolveVotes = map[Seat]bool{}
	st.Governance.DissolveInitiated = false
	st.Governance.KickVotes = map[Seat]bool{}
	st.Governance.KickInitiated = false
	st.Governance.KickTarget = nil
}

// governanceDissolve 处理「发起投票解散」（docs §解散）：仅存活玩家参与
// （通用 validator 已校验），发起限制 = 每人每局 1 次 + 每阶段 1 次；
// 发起成功计本人一票同意并发出公共公告。
func (r reducer) governanceDissolve(st State, cmd GovernanceDissolveCommand) (State, []Effect, error) {
	if !governanceValidPhase(st.Phase) {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	syncGovernancePhase(&next)
	seat, ok := seatByUser(next.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if next.Governance.DissolveBy[seat] {
		return st, nil, ErrAlreadyInitiated
	}
	if next.Governance.DissolveInitiated {
		return st, nil, ErrPhaseAlreadyInitiated
	}
	next.Governance.DissolveBy[seat] = true
	next.Governance.DissolveInitiated = true
	next.Governance.DissolveVotes[seat] = true
	next.Processed[cmd.Meta.ID] = true

	init, err := NewMessageEffect(AudiencePublic, GovernanceDissolveInitiatedMessageKey, map[string]any{
		"initiator": seat,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance dissolve initiated: %w", err)
	}
	return r.settleGovernanceDissolve(next, []Effect{init})
}

// governanceDissolveVote 处理投票解散已发起后的同意票：未发起 →
// ErrNotInitiated；重复投票 → ErrAlreadyVoted；死亡玩家由 validator 拦截。
func (r reducer) governanceDissolveVote(st State, cmd GovernanceDissolveVoteCommand) (State, []Effect, error) {
	if !governanceValidPhase(st.Phase) {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	syncGovernancePhase(&next)
	seat, ok := seatByUser(next.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if !next.Governance.DissolveInitiated {
		return st, nil, ErrNotInitiated
	}
	if next.Governance.DissolveVotes[seat] {
		return st, nil, ErrAlreadyVoted
	}
	next.Governance.DissolveVotes[seat] = true
	next.Processed[cmd.Meta.ID] = true

	ack, err := NewMessageEffect(AudienceActor, GovernanceDissolveVoteMessageKey, map[string]any{"seat": seat})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance dissolve vote ack: %w", err)
	}
	return r.settleGovernanceDissolve(next, []Effect{ack})
}

// settleGovernanceDissolve 判定投票解散结果（docs §解散 2：超过三分之一
// 同意即通过）：通过则清空本轮投票并产出 DissolveEffect{DissolveVoted} +
// 公共公告（不扣分）；未达阈值保持开启（超时关闭属接线层）。
func (r reducer) settleGovernanceDissolve(st State, base []Effect) (State, []Effect, error) {
	alive := len(governanceAliveSeats(st.Players))
	yes := len(st.Governance.DissolveVotes)
	effects := append([]Effect{}, base...)
	if yes*3 <= alive {
		return st, effects, nil
	}
	passed, err := NewMessageEffect(AudiencePublic, GovernanceDissolvePassedMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance dissolve passed: %w", err)
	}
	effects = append(effects, passed, DissolveEffect{Reason: DissolveVoted})
	st.Governance.DissolveVotes = map[Seat]bool{}
	return st, effects, nil
}

// governanceKick 处理「发起投票踢人」（docs §投票踢人）：目标必须为
// 房间内存活玩家且非发起者本人；发起限制与投票解散一致（每人每局 1 次
// + 每阶段 1 次）；发起成功计本人一票同意并发出公共公告。
func (r reducer) governanceKick(st State, cmd GovernanceKickCommand) (State, []Effect, error) {
	if !governanceValidPhase(st.Phase) {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	syncGovernancePhase(&next)
	seat, ok := seatByUser(next.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if cmd.Target == seat {
		return st, nil, ErrGovernanceKickSelf
	}
	if next.Governance.KickBy[seat] {
		return st, nil, ErrAlreadyInitiated
	}
	if next.Governance.KickInitiated {
		return st, nil, ErrPhaseAlreadyInitiated
	}
	next.Governance.KickBy[seat] = true
	next.Governance.KickInitiated = true
	next.Governance.KickVotes[seat] = true
	target := cmd.Target
	next.Governance.KickTarget = &target
	next.Processed[cmd.Meta.ID] = true

	init, err := NewMessageEffect(AudiencePublic, GovernanceKickInitiatedMessageKey, map[string]any{
		"initiator": seat,
		"target":    target,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance kick initiated: %w", err)
	}
	return r.settleGovernanceKick(next, []Effect{init})
}

// governanceKickVote 处理投票踢人已发起后的同意票（错误语义同
// governanceDissolveVote）。
func (r reducer) governanceKickVote(st State, cmd GovernanceKickVoteCommand) (State, []Effect, error) {
	if !governanceValidPhase(st.Phase) {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	syncGovernancePhase(&next)
	seat, ok := seatByUser(next.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if !next.Governance.KickInitiated || next.Governance.KickTarget == nil {
		return st, nil, ErrNotInitiated
	}
	if next.Governance.KickVotes[seat] {
		return st, nil, ErrAlreadyVoted
	}
	next.Governance.KickVotes[seat] = true
	next.Processed[cmd.Meta.ID] = true

	ack, err := NewMessageEffect(AudienceActor, GovernanceKickVoteMessageKey, map[string]any{"seat": seat})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance kick vote ack: %w", err)
	}
	return r.settleGovernanceKick(next, []Effect{ack})
}

// settleGovernanceKick 判定投票踢人结果（超过三分之一同意即通过，docs
// §投票踢人 1）：通过则被踢者按掉线处理（判负移除语义）——标记死亡 +
// Left + CooldownEffect{LeaveCooldown, LeaveReasonVoteKicked} +
// PersistGameLeave + 公共公告；未达阈值保持开启。
func (r reducer) settleGovernanceKick(st State, base []Effect) (State, []Effect, error) {
	alive := len(governanceAliveSeats(st.Players))
	yes := len(st.Governance.KickVotes)
	effects := append([]Effect{}, base...)
	if yes*3 <= alive {
		return st, effects, nil
	}
	target := *st.Governance.KickTarget
	markPlayerDead(st.Players, target)
	markPlayerLeft(st.Players, target)
	passed, err := NewMessageEffect(AudiencePublic, GovernanceKickPassedMessageKey, map[string]any{"target": target})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance kick passed: %w", err)
	}
	effects = append(effects,
		passed,
		CooldownEffect{User: playerBySeat(st.Players, target).UserID, Duration: LeaveCooldown, Reason: LeaveReasonVoteKicked},
		PersistEffect{Kind: PersistGameLeave},
	)
	// I6：被投票踢出者若是房主 → 移交下一位（docs §房主移交）。
	effects = maybeTransferHostOnLeave(&st, target, effects)
	st.Governance.KickVotes = map[Seat]bool{}
	st.Governance.KickTarget = nil
	return st, effects, nil
}

// hostDissolve 处理房主强制解散（docs §解散 1、§积分系统 2）：仅房主、
// 二次确认（Confirm=false 请求确认 → Confirm=true 生效）、积分 ≤9 禁止
// （ErrInsufficientScore）；生效后产出 DissolveEffect{HostForced} +
// ScorePenaltyEffect{10} + 公共公告。
func (r reducer) hostDissolve(st State, cmd HostDissolveCommand) (State, []Effect, error) {
	if !governanceValidPhase(st.Phase) {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	syncGovernancePhase(&next)
	if _, ok := seatByUser(next.Players, cmd.Meta.Actor); !ok {
		return st, nil, ErrNotInRoom
	}
	if cmd.Meta.Actor != next.Lobby.Owner {
		return st, nil, ErrNotHost
	}
	if !cmd.Confirm {
		next.Governance.HostDissolvePending = true
		next.Processed[cmd.Meta.ID] = true
		prompt, err := NewMessageEffect(AudienceHost, GovernanceHostDissolveConfirmMessageKey, map[string]any{})
		if err != nil {
			return st, nil, fmt.Errorf("game: governance host dissolve confirm: %w", err)
		}
		return next, []Effect{prompt}, nil
	}
	if !next.Governance.HostDissolvePending {
		return st, nil, ErrHostDissolveNotConfirmed
	}
	if cmd.HostScore <= 9 {
		return st, nil, ErrInsufficientScore
	}
	next.Governance.HostDissolvePending = false
	next.Processed[cmd.Meta.ID] = true
	passed, err := NewMessageEffect(AudiencePublic, GovernanceHostDissolvePassedMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: governance host dissolve passed: %w", err)
	}
	effects := []Effect{
		passed,
		DissolveEffect{Reason: HostForced},
		ScorePenaltyEffect{Amount: 10},
	}
	return next, effects, nil
}
