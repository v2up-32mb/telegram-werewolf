package game

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 白天投票与遗言效果原语（docs 游戏流程设计.md §投票、§结算 4 遗言、
// 阶段消息设计.md §8.4 白天投票、§3.3 时间文案、§4.3 长度不变量）：
//   - 投票私聊匿名收集：先选目标或弃权，再点击「确认投票」；确认前可
//     改票、确认后锁定；全部有票权玩家确认后可提前结束；超时未确认者
//     按弃权处理；
//   - 投票期间任何效果不得携带实时票数，结束后按「逐人明细 → 票数统计
//     → 放逐结果」统一公布（AudiencePublic，docs §投票 1）；
//   - 投票阶段静默由阶段切换实现：进入 PhaseDayVote 后 PhaseDaySpeech
//     的发言命令因 ErrWrongPhase 被拒（docs §投票 3）；
//   - 投票临时消息结束后删除（vote.delete，AudienceActor）；明细/统计/
//     结果写入当天主消息由接线层按效果渲染追加；
//   - 遗言绑定「不报身份」模式：默认（RevealRoleOnDeath=false）被票死者
//     有 30 秒遗言（last_words.* 并正常转播）；房主选择「报身份」时无
//     遗言；狼人自爆永远无遗言属 Task 38 自爆路径，本任务不实现自爆；
//   - 白天结束 Phase=PhaseNight、PhaseVersion+1；夜间主消息与夜序号由
//     接线层负责（game 核心不维护夜序号，与 resolveNight 不产出
//     day-start 键一致）。
//
// 已知缺口（如实记录，不阻塞本任务）：
//  1. 平票（含全员弃权）不裁决、不放逐，完整平票流程（加时发言/缩圈/
//     最终对决）属 Task 37；
//  2. 白天放逐后的胜负即时判定由后续结算任务/接线层调用 EvaluateVictory
//     （docs §结算 1：白天为投票先触发者获胜）；
//  3. 遗言窗口真实 30 秒计时与删除消息的定时执行属接线层任务（reducer
//     只产出 TimerEffect/DelayEffect 等效果）。

// 白天投票消息 key（docs §8.4 白天投票；vote.* 与 last_words.* 均为
// 公共安全前缀，不在 NewMessageEffect 敏感前缀列表中）。
const (
	// VotePromptMessageKey 是投票 UI（AudienceActor）：params
	// seat/candidates/deadline。
	VotePromptMessageKey = "vote.prompt"
	// VoteLockedMessageKey 是确认锁定反馈（AudienceActor）：params
	// seat/target（nil=弃权）。
	VoteLockedMessageKey = "vote.locked"
	// VoteDetailMessageKey 是逐人明细（AudiencePublic）：params ballots
	//（投票人 → 目标座位，0=弃权）。
	VoteDetailMessageKey = "vote.detail"
	// VoteTallyMessageKey 是票数统计（AudiencePublic）：params
	// counts/abstain。
	VoteTallyMessageKey = "vote.tally"
	// VoteResultMessageKey 是放逐结果（AudiencePublic）：params exiled
	//（nil=平票/未放逐，平票流程属 Task 37）。
	VoteResultMessageKey = "vote.result"
	// VoteDeleteMessageKey 是投票临时消息删除（AudienceActor）：params
	// seat。
	VoteDeleteMessageKey = "vote.delete"
	// LastWordsPromptMessageKey 是遗言提示（AudienceActor）：params
	// seat/deadline。
	LastWordsPromptMessageKey = "last_words.prompt"
	// LastWordsPublishedMessageKey 是遗言转播（AudiencePublic）：params
	// seat/text。
	LastWordsPublishedMessageKey = "last_words.published"
)

// 白天投票与遗言领域规则的哨兵错误。
var (
	// ErrVoteClosed 表示投票窗口已关闭（阶段未开始或已结束）。
	ErrVoteClosed = errors.New("game: day vote is closed")
	// ErrVoteLocked 表示投票选择已确认锁定，不能修改/重复确认。
	ErrVoteLocked = errors.New("game: vote already confirmed and locked")
	// ErrVoteNoSelection 表示未选择目标或弃权即确认。
	ErrVoteNoSelection = errors.New("game: must select a target or abstain before confirming")
	// ErrLastWordsClosed 表示遗言窗口已关闭。
	ErrLastWordsClosed = errors.New("game: last words window is closed")
	// ErrLastWordsNotExiled 表示只有被票死者能发表遗言。
	ErrLastWordsNotExiled = errors.New("game: only the exiled player may speak last words")
	// ErrEmptyLastWords 表示遗言正文为空（去首尾空白后）。
	ErrEmptyLastWords = errors.New("game: last words must not be empty")
)

// VoteConfirmSeconds 是白天投票限时（秒）。docs「6 人局默认配置总表」
// 未列明投票时长，本值为 MVP 常量；不新增 RoomSettings 字段。
const VoteConfirmSeconds = 30

// LastWordsSeconds 是被票死者遗言限时（docs §结算 4：30 秒遗言）。
const LastWordsSeconds = 30

// BeginVote 从 PhaseDaySpeech 进入 PhaseDayVote（接线层在白天发言结束
// 后调用）：
//   - 前置校验：仅 PhaseDaySpeech 可进入，否则 ErrWrongPhase；
//   - Phase=PhaseDayVote、PhaseVersion+1，Vote 状态初始化为收票窗口
//     （Stage=Open，集合清零）；
//   - 每名存活玩家收到 vote.prompt（AudienceActor：候选存活列表 +
//     UTC+8 截止时刻）并启动投票计时器（TimerEffect PhaseDayVote）。
//
// 投票阶段静默由阶段切换实现：PhaseDaySpeech 的发言命令此后因
// ErrWrongPhase 被拒（docs §投票 3）。
func BeginVote(st State, at time.Time) (State, []Effect, error) {
	if st.Phase != PhaseDaySpeech {
		return st, nil, ErrWrongPhase
	}
	next := st.Copy()
	next.Phase = PhaseDayVote
	next.PhaseVersion++
	next.Vote = VoteState{
		Stage:   VoteStageOpen,
		Ballots: map[Seat]Seat{},
		Pending: map[Seat]*Seat{},
		Locked:  map[Seat]bool{},
	}

	voters := aliveSeats(next.Players)
	deadline := at.Add(time.Duration(VoteConfirmSeconds) * time.Second)
	effects := make([]Effect, 0, len(voters)+1)
	for _, seat := range voters {
		prompt, err := NewMessageEffect(AudienceActor, VotePromptMessageKey, map[string]any{
			"seat":       seat,
			"candidates": voters,
			"deadline":   deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: vote prompt: %w", err)
		}
		effects = append(effects, prompt)
	}
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(VoteConfirmSeconds) * time.Second})
	return next, effects, nil
}

// vote 处理投票人选择（docs §投票 1：先选择目标或弃权，确认前可改票）：
//   - 仅收票窗口（ErrVoteClosed）与未锁定（ErrVoteLocked）可改选；
//   - 写入 Pending[seat]：键存在且 nil=弃权待确认（与 Night.WolfVotes 的
//     nil 键语义一致）；键不存在=未选择；
//   - 目标存活校验由通用 validator 保证（VoteCommand.Target=nil 豁免=
//     弃权）。
func (r reducer) vote(st State, cmd VoteCommand) (State, []Effect, error) {
	if st.Vote.Stage != VoteStageOpen {
		return st, nil, ErrVoteClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if st.Vote.Locked[seat] {
		return st, nil, ErrVoteLocked
	}

	next := st.Copy()
	if cmd.Target == nil {
		next.Vote.Pending[seat] = nil
	} else {
		target := *cmd.Target
		next.Vote.Pending[seat] = &target
	}
	next.Processed[cmd.Meta.ID] = true
	return next, nil, nil
}

// voteConfirm 处理投票确认（docs §投票 1、§8.4：确认前可改、确认后锁定；
// 弃权也需要确认；全部有票权玩家确认后可提前结束）：
//   - 仅收票窗口（ErrVoteClosed）；已锁定 → ErrVoteLocked；
//   - 未选择即确认 → ErrVoteNoSelection；
//   - 确认后写入 Ballots（目标座位或 0=弃权）、Locked=true，并产出
//     AudienceActor vote.locked；
//   - 全部存活玩家确认后立即结算（settleVote，不等待超时）。
func (r reducer) voteConfirm(st State, cmd VoteConfirmCommand) (State, []Effect, error) {
	if st.Vote.Stage != VoteStageOpen {
		return st, nil, ErrVoteClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if st.Vote.Locked[seat] {
		return st, nil, ErrVoteLocked
	}
	if _, has := st.Vote.Pending[seat]; !has {
		return st, nil, ErrVoteNoSelection
	}

	next := st.Copy()
	target := next.Vote.Pending[seat]
	if target == nil {
		next.Vote.Ballots[seat] = 0 // 弃权
	} else {
		next.Vote.Ballots[seat] = *target
	}
	next.Vote.Locked[seat] = true
	next.Processed[cmd.Meta.ID] = true

	ack, err := NewMessageEffect(AudienceActor, VoteLockedMessageKey, map[string]any{
		"seat":   seat,
		"target": target,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: vote confirm ack: %w", err)
	}
	effects := []Effect{ack}

	if allAliveVotersLocked(next) {
		after, resolved, err := r.settleVote(next, cmd.Meta.ReceivedAt)
		if err != nil {
			return st, nil, err
		}
		return after, append(effects, resolved...), nil
	}
	return next, effects, nil
}

// voteTimeout 处理收票窗口超时（docs「超时与默认选择」：所有人投票
// 超时 → 弃票）：未确认存活玩家按弃权结算（Ballots=0、Locked=true），
// 随后走与全员确认相同的结算路径。
func (r reducer) voteTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Vote.Stage != VoteStageOpen {
		return st, nil, ErrVoteClosed
	}
	next := st.Copy()
	for _, seat := range aliveSeats(next.Players) {
		if !next.Vote.Locked[seat] {
			next.Vote.Ballots[seat] = 0
			next.Vote.Locked[seat] = true
		}
	}
	next.Processed[cmd.Meta.ID] = true
	return r.settleVote(next, cmd.Meta.ReceivedAt)
}

// settleVote 结算白天投票（docs §投票 1：结束后按「逐人明细 → 票数统计
// → 放逐结果」统一公布；期间任何效果不携带实时票数）：
//   - 唯一最高票 → 放逐（Vote.Exiled、玩家 Dead=true）；
//   - 平票（含全员弃权）→ 不放逐（Exiled=nil，结果说明平票；完整平票
//     流程属 Task 37，见文件头已知缺口 1）；
//   - 效果顺序：TimerEffect Cancel → 逐人 vote.delete（投票临时消息删除
//     在前，与 deal.go completeDealTransition 的 delete 在前一致）→
//     vote.detail → vote.tally → vote.result；
//   - 遗言绑定「不报身份」：默认（RevealRoleOnDeath=false）且有人被票死
//     时进入 Stage=LastWords（30 秒遗言窗口：last_words.prompt +
//     TimerEffect），遗言结束或超时后再进入黑夜；报身份（=true）时无
//     遗言直接进入黑夜（finishDayVote）。
func (r reducer) settleVote(st State, at time.Time) (State, []Effect, error) {
	next := st.Copy()
	next.Vote.Stage = VoteStageClosed

	counts, abstain := tallyVotes(next.Vote.Ballots)
	if exiled := topVoteTarget(counts); exiled != nil {
		seat := *exiled
		next.Vote.Exiled = &seat
		markPlayerDead(next.Players, seat)
	}

	effects := make([]Effect, 0, len(st.Players)+5)
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Cancel: true})
	for _, seat := range aliveSeats(st.Players) {
		del, err := NewMessageEffect(AudienceActor, VoteDeleteMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: vote delete: %w", err)
		}
		effects = append(effects, del)
	}
	detail, err := NewMessageEffect(AudiencePublic, VoteDetailMessageKey, map[string]any{
		"ballots": next.Vote.Ballots,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: vote detail: %w", err)
	}
	tally, err := NewMessageEffect(AudiencePublic, VoteTallyMessageKey, map[string]any{
		"counts":  counts,
		"abstain": abstain,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: vote tally: %w", err)
	}
	result, err := NewMessageEffect(AudiencePublic, VoteResultMessageKey, map[string]any{
		"exiled": next.Vote.Exiled,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: vote result: %w", err)
	}
	effects = append(effects, detail, tally, result)

	if next.Vote.Exiled != nil && !next.Settings.RevealRoleOnDeath {
		// 默认「不报身份」：被票死者有 30 秒遗言（docs §结算 4）。
		next.Vote.Stage = VoteStageLastWords
		deadline := at.Add(time.Duration(LastWordsSeconds) * time.Second)
		prompt, err := NewMessageEffect(AudienceActor, LastWordsPromptMessageKey, map[string]any{
			"seat":     *next.Vote.Exiled,
			"deadline": deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: last words prompt: %w", err)
		}
		effects = append(effects, prompt, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(LastWordsSeconds) * time.Second})
		return next, effects, nil
	}

	after, transition, err := finishDayVote(next)
	if err != nil {
		return st, nil, err
	}
	return after, append(effects, transition...), nil
}

// lastWords 处理被票死者遗言（docs §结算 4：仅「不报身份」模式有 30 秒
// 遗言并正常转播 AudiencePublic；狼人自爆永远无遗言属 Task 38）：
//   - 仅遗言窗口（ErrLastWordsClosed）；仅被票死者本人
//     （ErrLastWordsNotExiled；通用 validator 已豁免 actor 存活校验，
//     见 reducer.go validate）；
//   - 正文去首尾空白后必须非空（ErrEmptyLastWords）；
//   - 转播遗言后关闭窗口并进入黑夜（finishDayVote）。
func (r reducer) lastWords(st State, cmd LastWordsCommand) (State, []Effect, error) {
	if st.Vote.Stage != VoteStageLastWords {
		return st, nil, ErrLastWordsClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if st.Vote.Exiled == nil || seat != *st.Vote.Exiled {
		return st, nil, ErrLastWordsNotExiled
	}
	text := strings.TrimSpace(cmd.Text)
	if text == "" {
		return st, nil, ErrEmptyLastWords
	}

	next := st.Copy()
	next.Vote.LastWords = text
	next.Vote.Stage = VoteStageClosed
	next.Processed[cmd.Meta.ID] = true

	published, err := NewMessageEffect(AudiencePublic, LastWordsPublishedMessageKey, map[string]any{
		"seat": seat,
		"text": text,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: last words published: %w", err)
	}
	after, transition, err := finishDayVote(next)
	if err != nil {
		return st, nil, err
	}
	return after, append([]Effect{published}, transition...), nil
}

// lastWordsTimeout 处理遗言 30 秒超时：无正文直接进入黑夜
// （docs「超时与默认选择」之外的标准流程；遗言不发表即无内容）。
func (r reducer) lastWordsTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Vote.Stage != VoteStageLastWords {
		return st, nil, ErrLastWordsClosed
	}
	next := st.Copy()
	next.Vote.Stage = VoteStageClosed
	next.Processed[cmd.Meta.ID] = true
	return finishDayVote(next)
}

// finishDayVote 结束白天并进入黑夜：Phase=PhaseNight、PhaseVersion+1。
// 夜间主消息与 phase.night.start 的夜序号由接线层负责（game 核心不维护
// 夜序号；与 resolveNight 切入白天时不产出 day-start 键一致）。
func finishDayVote(st State) (State, []Effect, error) {
	st.Phase = PhaseNight
	st.PhaseVersion++
	return st, nil, nil
}

// allAliveVotersLocked 报告所有存活玩家是否都已确认（提前结束条件，
// docs §投票 1：所有有投票权玩家确认后可提前结束）。
func allAliveVotersLocked(st State) bool {
	for _, seat := range aliveSeats(st.Players) {
		if !st.Vote.Locked[seat] {
			return false
		}
	}
	return true
}

// tallyVotes 统计确认票：counts 为目标座位 → 票数；abstain 为弃权票数
// （Ballots=0）。
func tallyVotes(ballots map[Seat]Seat) (counts map[Seat]int, abstain int) {
	counts = make(map[Seat]int, len(ballots))
	for _, target := range ballots {
		if target == 0 {
			abstain++
			continue
		}
		counts[target]++
	}
	return counts, abstain
}

// topVoteTarget 返回唯一最高票目标；平票（含全员弃权/零票）返回 nil
// （完整平票流程属 Task 37，本任务不裁决，见文件头已知缺口 1）。
func topVoteTarget(counts map[Seat]int) *Seat {
	var best Seat
	bestN := 0
	unique := true
	for target, n := range counts {
		if n > bestN {
			best, bestN, unique = target, n, true
		} else if n == bestN {
			unique = false
		}
	}
	if !unique || bestN == 0 {
		return nil
	}
	return &best
}
