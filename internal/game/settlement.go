package game

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// 结算、积分、战报数据与「再来一局」领域规则（docs 游戏流程设计.md
// §结算 5-8、§积分系统 1、§恶意退出判定、阶段消息设计.md §15/§16）：
//   - 结算由 Reducer 路径在进入 PhaseSettlement 时触发（夜间 settleNight、
//     白天放逐落定后即时判定 docs §结算 1），产出 PersistSettlementEffect
//     供接线层映射 internal/storage.GameResult 调用 Task 16 单事务
//     SettleGame（docs/技术选型.md §8.3/§8.4）；game 核心不触碰 SQLite；
//   - 积分口径（docs §积分系统 1，与 storage.PlayerResult 注释一致）：
//     胜 +5 / 对局中死亡且阵营胜利 +2 / 失败 0 / 恶意退出且阵营胜利 0 /
//     恶意退出且阵营失败 -5；恶意退出仅含「存活时主动退出」与「连续 3 次
//     超时强制移除」（Player.MaliciousExit，leave.go 置位）；
//   - 战报单独永久发送（settlement.report），当前时间段主消息先定稿
//     （settleNight 先产出死讯/胜利公告，再追加战报效果）；
//   - 不评选最佳玩家（docs §结算 8：只记胜负，后期再考虑）；
//   - 房主点击「再来一局」回等待大厅：保留成员与配置、从点击起至少
//     15 秒退出窗口（RematchWindow），窗口内禁止开始新对局。

// SettlementReportMessageKey 是独立结算战报（AudiencePublic，永久发送，
// docs 阶段消息设计.md §15/§16）：params room_code/winner/players/
// key_events。
const SettlementReportMessageKey = "settlement.report"

// RematchMessageKey 是房主「再来一局」回到等待大厅的公共公告
// （AudiencePublic，docs §结算 6）：params window_seconds。
const RematchMessageKey = "lobby.rematch"

// RematchWindow 是「再来一局」后的玩家退出窗口（docs §结算 6：
// 从点击起至少留足 15 秒）。
const RematchWindow = 15 * time.Second

// 结算/再来一局领域规则的哨兵错误。
var (
	// ErrNotSettled 表示状态尚未完成合法结算（不可产出战报/持久化）。
	ErrNotSettled = errors.New("game: game is not settled")
	// ErrRematchWindowOpen 表示「再来一局」退出窗口尚未结束，期间不允许
	// 开始新对局（docs §结算 6：至少留足 15 秒）。
	ErrRematchWindowOpen = errors.New("game: rematch exit window still open")
)

// PlayerResult 是结算中一名玩家的结果（身份翻牌 + 积分变化，seat 升序），
// 与 internal/storage/settlement.go 的 PlayerResult 口径一致。
type PlayerResult struct {
	UserID        UserID
	Seat          Seat
	Role          Role
	Camp          Camp
	Died          bool
	MaliciousExit bool
	// Score 是本次对局积分变化（胜 +5 / 死亡躺赢 +2 / 失败 0 /
	// 恶意退出且胜 0 / 恶意退出且败 -5）。
	Score int
}

// KeyEvent 是战报中的一条关键事件（由最终状态可推导：狼人袭击、女巫
// 毒杀、白天放逐等）。战报不伪装完整回放，只列可推导条目（docs §结算 7、
// §记录 243）。
type KeyEvent struct {
	Phase Phase
	Text  string
}

// Settlement 是一次结算的完整结果（持久化与战报共用）。
type Settlement struct {
	RoomID    RoomID
	Phase     Phase
	Winner    Camp
	Players   []PlayerResult
	KeyEvents []KeyEvent
}

// PersistSettlementEffect 表示结算结果需持久化：由接线层映射为
// internal/storage.GameResult 调用 SettlementRepository.SettleGame
// （Task 16 单事务内写战报/积分/统计并清 active 标记）；game 核心不
// 触碰 SQLite（docs/技术选型.md §5.1/§8.3/§8.4）。
type PersistSettlementEffect struct {
	Result Settlement
}

func (PersistSettlementEffect) effect() {}

// settle 执行结算领域流程（仅 PhaseSettlement 且胜方已判定）：
//  1. 全员身份翻牌与逐人积分；
//  2. 关键事件（最终状态可推导，不伪装完整回放）；
//  3. 写出 SettledState（Revealed/KeyEvents）并产出
//     PersistSettlementEffect + settlement.report（AudiencePublic）。
//
// 幂等：已结算状态直接返回且不追加效果（防止重复持久化/战报）。
// base 为既有效果序列（死讯/放逐/胜利公告先定稿当前时间段主消息，
// 独立战报随后永久发送，docs §五.5、阶段消息设计.md §15）。
func settle(st State, base []Effect) (State, []Effect, error) {
	if st.Phase != PhaseSettlement {
		return st, base, ErrNotSettled
	}
	if st.Settled.Winner != CampWolf && st.Settled.Winner != CampGood {
		return st, base, ErrNotSettled
	}
	if len(st.Settled.Revealed) > 0 {
		return st, base, nil // 幂等
	}

	players, events := computeSettlement(st)
	next := st.Copy()
	next.Settled.Revealed = players
	next.Settled.KeyEvents = events

	persist := PersistSettlementEffect{Result: Settlement{
		RoomID:    st.RoomID,
		Phase:     st.Phase,
		Winner:    st.Settled.Winner,
		Players:   players,
		KeyEvents: events,
	}}
	report, err := NewMessageEffect(AudiencePublic, SettlementReportMessageKey, map[string]any{
		"room_code":  string(st.RoomID),
		"winner":     st.Settled.Winner,
		"players":    players,
		"key_events": events,
	})
	if err != nil {
		return st, base, fmt.Errorf("game: settlement report message: %w", err)
	}
	effects := make([]Effect, 0, len(base)+2)
	effects = append(effects, base...)
	effects = append(effects, persist, report)
	return next, effects, nil
}

// computeSettlement 计算全员身份翻牌（含积分变化，seat 升序）与可推导
// 关键事件。不评选最佳玩家（docs §结算 8：只记胜负与积分）。
func computeSettlement(st State) ([]PlayerResult, []KeyEvent) {
	players := make([]PlayerResult, 0, len(st.Players))
	for _, p := range st.Players {
		if !p.Seat.Valid() {
			continue
		}
		camp := p.Role.Camp()
		isWinner := camp == st.Settled.Winner
		var score int
		switch {
		case p.MaliciousExit && !isWinner:
			score = -5 // 恶意退出且阵营失败
		case p.MaliciousExit:
			score = 0 // 恶意退出且阵营胜利：不得分
		case p.Dead && isWinner:
			score = 2 // 对局中死亡但阵营胜利（死亡躺赢）
		case isWinner:
			score = 5 // 阵营胜利
		default:
			score = 0 // 失败（含死亡且失败）
		}
		players = append(players, PlayerResult{
			UserID:        p.UserID,
			Seat:          p.Seat,
			Role:          p.Role,
			Camp:          camp,
			Died:          p.Dead,
			MaliciousExit: p.MaliciousExit,
			Score:         score,
		})
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Seat < players[j].Seat })
	return players, keyEventsFromState(st)
}

// keyEventsFromState 由最终 State 推导关键事件：每名死亡玩家至多一条，
// 死因优先级为白天放逐 → 女巫毒杀 → 狼人袭击 → 未知出局；战报不伪装
// 完整回放（不维护逐阶段完整历史，docs §结算 7）。
func keyEventsFromState(st State) []KeyEvent {
	players := append([]Player(nil), st.Players...)
	sort.Slice(players, func(i, j int) bool { return players[i].Seat < players[j].Seat })
	var events []KeyEvent
	for _, p := range players {
		if p.Dead && p.Seat.Valid() {
			events = append(events, deathKeyEvent(st, p.Seat))
		}
	}
	return events
}

// deathKeyEvent 为座位 seat 推导一条死亡关键事件。
func deathKeyEvent(st State, seat Seat) KeyEvent {
	if st.Vote.Exiled != nil && *st.Vote.Exiled == seat {
		return KeyEvent{Phase: PhaseDayVote, Text: fmt.Sprintf("白天投票放逐了 %d 号", seat)}
	}
	if st.Night.WitchPoisonTarget != nil && *st.Night.WitchPoisonTarget == seat {
		return KeyEvent{Phase: PhaseNight, Text: fmt.Sprintf("女巫毒杀了 %d 号", seat)}
	}
	if st.Night.WolfKillTarget != nil && *st.Night.WolfKillTarget == seat {
		return KeyEvent{Phase: PhaseNight, Text: fmt.Sprintf("狼人袭击了 %d 号", seat)}
	}
	return KeyEvent{Phase: PhaseNight, Text: fmt.Sprintf("%d 号出局", seat)}
}

// rematch 处理 RematchCommand（docs §结算 5/6）：仅房主、仅结算阶段
// （通用 validator 已校验阶段/版本/在场/存活）；回等待大厅保留未缺席
// 成员并复位 Dead，沿用 Lobby.Config 与 Settings（配置可继续修改），
// 对局数据复位，并记录从点击起至少 15 秒的退出窗口（窗口期内
// startGame 拒绝）。Meta.ReceivedAt 为窗口计算时间源（reducer 不读取
// 系统时间，docs/技术选型.md §5.1）。
func (r reducer) rematch(st State, cmd RematchCommand) (State, []Effect, error) {
	if cmd.Meta.Actor != st.Lobby.Owner {
		return st, nil, ErrNotHost
	}

	next := st.Copy()
	kept := make([]Player, 0, len(next.Players))
	for _, p := range next.Players {
		if p.Left {
			continue // 缺席玩家不保留：回大厅后可等新人/AI 补位
		}
		p.Dead = false
		p.MaliciousExit = false // 新对局重新判定
		p.TimeoutStreak = 0     // 整局累计连续超时随对局重置
		kept = append(kept, p)
	}
	next.Players = kept
	next.Phase = PhaseLobby
	next.PhaseVersion++
	next.Lobby.RematchReadyAt = cmd.Meta.ReceivedAt.Add(RematchWindow)
	next.Deal = DealState{}
	next.Night = NightState{}
	next.Day = DayState{}
	next.Vote = VoteState{}
	next.Settled = SettledState{}
	next.Governance = GovernanceState{}
	next.Processed[cmd.Meta.ID] = true

	msg, err := NewMessageEffect(AudiencePublic, RematchMessageKey, map[string]any{
		"window_seconds": int(RematchWindow / time.Second),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: rematch message: %w", err)
	}
	return next, []Effect{msg}, nil
}
