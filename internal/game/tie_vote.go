package game

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// 白天平票流程（docs 游戏流程设计.md §投票 4、阶段消息设计.md §8.5 平票、
// 技术选型.md §5.2 注入 RNG）：
//   - 首次平票（≥2 名候选并列最高票且票数 >0）→ 平票玩家依次加时发言
//     （TieSpeech），投票命令被拒；超时推进第 2 次（缩圈）投票；
//   - 第 2 次投票（TieRunoff）：只在平票候选间投；平票人必须投其他
//     平票人（投自己/主动弃权被拒），非平票人可弃权（docs §投票 2：
//     仅最终 2 人对决禁止弃权）；
//   - 缩圈后仍 ≥3 人平票 → 无发言投票循环（TieNoSpeech，上限 2 轮）；
//     第 2 轮后仍平票 → 注入 RNG 随机保留 2 名候选，其余平票人转为投票人
//     （不出局、照常游戏）→ 最终对决；缩圈后恰为 2 人平票 → 直接对决；
//   - 最终对决（TieFinal）：2 名候选本人不投票，其余存活玩家必须投其中
//     一人（禁止弃权）；投票人数为偶数 → 注入 RNG 随机排除 1 名投票人
//     （仅本轮失去投票权，不死亡、不影响后续进程）→ 必然分出结果；
//     全员超时/无人投票的极端情形注入 RNG 兜底选出胜者（MVP 裁决，
//     保证「必然分出结果」不变量）；
//   - 超时语义（docs「超时与默认选择」）：加时发言超时 → 推进缩圈；
//     缩圈/无发言轮超时 → 未确认者按弃权结算；最终对决超时 → 未确认者
//     失去本轮投票权（与随机排除同一语义）；
//   - 每轮平票过渡输出 vote.delete（上轮临时投票消息删除），最终落定走
//     Task 36 既有放逐 →（遗言/报身份）→ 进入黑夜路径（resolveExile）。

// TieStage 是平票流程内的子阶段；TieNone 表示当前不在平票流程。
type TieStage int

const (
	// TieNone 表示当前不在平票流程。
	TieNone TieStage = iota
	// TieSpeech 是首次平票后的加时发言阶段（投票命令被拒，超时推进缩圈）。
	TieSpeech
	// TieRunoff 是第 2 次（缩圈）投票：仅在平票候选间投。
	TieRunoff
	// TieNoSpeech 是无发言投票轮：≥3 人平票时循环，上限 2 轮。
	TieNoSpeech
	// TieFinal 是最终 2 人对决：禁止弃权，偶数投票人随机排除 1 人。
	TieFinal
)

// Valid 报告平票子阶段是否为已知取值。
func (s TieStage) Valid() bool {
	return s >= TieNone && s <= TieFinal
}

// TieSpeechSeconds 是平票加时发言限时（秒）。docs「6 人局默认配置总表」
// 未列明平票加时发言时长，本值为 MVP 常量，与 VoteConfirmSeconds 对齐。
const TieSpeechSeconds = 30

// 平票流程消息 key（docs §8.5 平票；tie.* 为公共安全前缀，不在
// NewMessageEffect 敏感前缀列表中）。平票过程全部追加当天白天主消息，
// 每轮过渡删除上轮临时投票消息。
const (
	// TieSpeechMessageKey 是平票公共公告（AudiencePublic）：params
	// candidates。
	TieSpeechMessageKey = "tie.speech"
	// TieSpeechTurnMessageKey 是平票候选人加时发言提示（AudienceActor）：
	// params seat/deadline。
	TieSpeechTurnMessageKey = "tie.speech_turn"
	// TieRunoffMessageKey 是第 2 次（缩圈）投票公告（AudiencePublic）：
	// params candidates。
	TieRunoffMessageKey = "tie.runoff"
	// TieRunoffPromptMessageKey 是缩圈/无发言轮投票 UI（AudienceActor）：
	// params seat/candidates/deadline。
	TieRunoffPromptMessageKey = "tie.runoff_prompt"
	// TieNoSpeechMessageKey 是无发言投票轮公告（AudiencePublic）：params
	// round/candidates。
	TieNoSpeechMessageKey = "tie.no_speech"
	// TieFinalMessageKey 是最终对决公告（AudiencePublic）：params
	// candidates。
	TieFinalMessageKey = "tie.final"
	// TieDuelPromptMessageKey 是最终对决投票 UI（AudienceActor）：params
	// seat/candidates/deadline。
	TieDuelPromptMessageKey = "tie.duel_prompt"
	// TieDuelExcludedMessageKey 是被排除投票人通知（AudienceActor）：
	// params seat。
	TieDuelExcludedMessageKey = "tie.duel_excluded"
)

// 平票流程的哨兵错误（docs §投票 4 边界与 §13.1 哨兵错误）。
var (
	// ErrVoteSelfInTie 表示平票候选人在第 2 次投票/无发言轮投了自己
	//（平票人必须投其他平票人）。
	ErrVoteSelfInTie = errors.New("game: tied player cannot vote for self in tie")
	// ErrTieCandidateMustVote 表示平票候选人在缩圈/无发言轮主动弃权被拒
	//（平票人也要投票）。
	ErrTieCandidateMustVote = errors.New("game: tied candidate must vote for another candidate")
	// ErrDuelCandidateNoVote 表示最终对决候选人本人投票被拒
	//（docs §投票 4：这 2 人不投票）。
	ErrDuelCandidateNoVote = errors.New("game: duel candidates do not vote")
	// ErrDuelAbstainForbidden 表示最终对决禁止弃权（必须投 2 人之一）。
	ErrDuelAbstainForbidden = errors.New("game: abstain is forbidden in the final duel")
)

// tieVote 处理平票流程各投票轮（缩圈/无发言轮/最终对决）的选择：
//   - 加时发言阶段投票命令一律被拒（ErrVoteClosed）；
//   - 最终对决：候选人不投票（ErrDuelCandidateNoVote）、禁止弃权
//     （ErrDuelAbstainForbidden）、只能投 2 名候选之一（ErrInvalidTarget）；
//   - 缩圈/无发言轮：平票人必须投其他平票人（投自己 ErrVoteSelfInTie、
//     弃权 ErrTieCandidateMustVote），非平票人可投候选或弃权。
func (r reducer) tieVote(st State, cmd VoteCommand) (State, []Effect, error) {
	if st.Vote.Tie == TieSpeech || st.Vote.Stage != VoteStageOpen {
		return st, nil, ErrVoteClosed
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if st.Vote.Locked[seat] {
		return st, nil, ErrVoteLocked
	}
	if err := validateTieVoteTarget(st, seat, cmd.Target); err != nil {
		return st, nil, err
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

// validateTieVoteTarget 校验平票轮次的目标与投票人边界（见 tieVote）。
func validateTieVoteTarget(st State, seat Seat, target *Seat) error {
	switch st.Vote.Tie {
	case TieFinal:
		if isTieCandidate(st, seat) {
			return ErrDuelCandidateNoVote
		}
		if target == nil {
			return ErrDuelAbstainForbidden
		}
		if !isTieCandidate(st, *target) {
			return ErrInvalidTarget
		}
	case TieRunoff, TieNoSpeech:
		if target == nil {
			if isTieCandidate(st, seat) {
				return ErrTieCandidateMustVote
			}
			return nil
		}
		if isTieCandidate(st, seat) && *target == seat {
			return ErrVoteSelfInTie
		}
		if !isTieCandidate(st, *target) {
			return ErrInvalidTarget
		}
	default:
		return ErrVoteClosed
	}
	return nil
}

// isTieCandidate 报告座位是否为当前平票候选。
func isTieCandidate(st State, seat Seat) bool {
	for _, c := range st.Vote.Candidates {
		if c == seat {
			return true
		}
	}
	return false
}

// tieVoteConfirm 处理平票轮次的确认锁定（复用收票窗口语义：确认前可改、
// 确认后锁定）；全部有投票权玩家确认后立即结算该轮（缩圈/无发言轮走
// settleTieRound，最终对决走 resolveFinal）。
func (r reducer) tieVoteConfirm(st State, cmd VoteConfirmCommand) (State, []Effect, error) {
	if st.Vote.Tie == TieSpeech || st.Vote.Stage != VoteStageOpen {
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
		next.Vote.Ballots[seat] = 0
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
		return st, nil, fmt.Errorf("game: tie vote confirm ack: %w", err)
	}
	effects := []Effect{ack}

	if !allTieVotersLocked(next) {
		return next, effects, nil
	}
	if next.Vote.Tie == TieFinal {
		after, resolved, err := r.resolveFinal(next, cmd.Meta.ReceivedAt)
		if err != nil {
			return st, nil, err
		}
		return after, append(effects, resolved...), nil
	}
	after, resolved, err := r.settleTieRound(next, cmd.Meta.ReceivedAt)
	if err != nil {
		return st, nil, err
	}
	return after, append(effects, resolved...), nil
}

// tieVoters 返回当前平票轮的投票人集合：缩圈/无发言轮为全部存活玩家
// （docs §投票 4：平票人也要投票）；最终对决为存活非候选玩家。
func tieVoters(st State) []Seat {
	if st.Vote.Tie == TieFinal {
		voters := make([]Seat, 0, len(st.Players))
		for _, seat := range aliveSeats(st.Players) {
			if !isTieCandidate(st, seat) {
				voters = append(voters, seat)
			}
		}
		return voters
	}
	return aliveSeats(st.Players)
}

// allTieVotersLocked 报告当前平票轮的所有投票人是否都已确认（提前结束
// 条件，与 allAliveVotersLocked 同一语义；docs §投票 1）。
func allTieVotersLocked(st State) bool {
	for _, seat := range tieVoters(st) {
		if !st.Vote.Locked[seat] {
			return false
		}
	}
	return true
}

// tieSpeechTimeout 处理加时发言超时：推进到第 2 次（缩圈）投票
// （docs §投票 4；超时语义见文件头）。
func (r reducer) tieSpeechTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Vote.Tie != TieSpeech {
		return st, nil, ErrVoteClosed
	}
	next := st.Copy()
	next.Processed[cmd.Meta.ID] = true
	return r.enterTieRunoff(next, cmd.Meta.ReceivedAt, nil)
}

// tieRoundTimeout 处理缩圈/无发言轮超时：未确认者按弃权结算（Ballots=0、
// Locked=true），随后与全员确认走同一结算路径（docs「超时与默认选择」）。
func (r reducer) tieRoundTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Vote.Tie != TieRunoff && st.Vote.Tie != TieNoSpeech {
		return st, nil, ErrVoteClosed
	}
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
	return r.settleTieRound(next, cmd.Meta.ReceivedAt)
}

// tieFinalTimeout 处理最终对决超时：未确认者失去本轮投票权（与随机排除
// 同一语义，仅本轮不投票、不死亡），随后立即结算对决。
func (r reducer) tieFinalTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Vote.Tie != TieFinal {
		return st, nil, ErrVoteClosed
	}
	if st.Vote.Stage != VoteStageOpen {
		return st, nil, ErrVoteClosed
	}
	next := st.Copy()
	if next.Vote.Excluded == nil {
		next.Vote.Excluded = map[Seat]bool{}
	}
	for _, seat := range tieVoters(next) {
		if !next.Vote.Locked[seat] {
			next.Vote.Excluded[seat] = true
		}
	}
	next.Processed[cmd.Meta.ID] = true
	return r.resolveFinal(next, cmd.Meta.ReceivedAt)
}

// settleTieRound 结算缩圈/无发言轮的投票结果：
//   - 唯一最高票 → 放逐（resolveExile：明细/统计/结果 → 遗言或进入黑夜）；
//   - 全员弃权/零票 → 无人被放逐，直接结束白天（MVP 裁决：平票无法
//     裁决时当日不流放，与首次全员弃权同路径）；
//   - 2 人平票 → 进入最终对决；≥3 人平票 → 无发言轮循环（上限 2 轮，
//     第 2 轮后仍平票由注入 RNG 随机保留 2 名候选）。
func (r reducer) settleTieRound(st State, at time.Time) (State, []Effect, error) {
	counts, abstain := tallyVotes(st.Vote.Ballots)

	effects := make([]Effect, 0, len(st.Players)+6)
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Cancel: true})
	for _, seat := range aliveSeats(st.Players) {
		del, err := NewMessageEffect(AudienceActor, VoteDeleteMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie round vote delete: %w", err)
		}
		effects = append(effects, del)
	}
	detail, err := NewMessageEffect(AudiencePublic, VoteDetailMessageKey, map[string]any{
		"ballots": st.Vote.Ballots,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie round detail: %w", err)
	}
	tally, err := NewMessageEffect(AudiencePublic, VoteTallyMessageKey, map[string]any{
		"counts":  counts,
		"abstain": abstain,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie round tally: %w", err)
	}
	effects = append(effects, detail, tally)

	if exiled := topVoteTarget(counts); exiled != nil {
		return r.resolveExile(st, at, effects, *exiled)
	}
	if len(counts) == 0 {
		next := st.Copy()
		next.Vote.Stage = VoteStageClosed
		next.Vote.Tie = TieNone
		next.Vote.TieRound = 0
		next.Vote.Candidates = nil
		next.Vote.Excluded = nil
		result, err := NewMessageEffect(AudiencePublic, VoteResultMessageKey, map[string]any{
			"exiled": nil,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie round result: %w", err)
		}
		effects = append(effects, result)
		after, transition, err := finishDayVote(next)
		if err != nil {
			return st, nil, err
		}
		return after, append(effects, transition...), nil
	}

	tied := topTiedTargets(counts)
	switch {
	case len(tied) == 2:
		return r.enterTieFinal(st, at, effects, tied)
	case st.Vote.TieRound >= 2:
		kept, err := r.keepTwoCandidates(tied)
		if err != nil {
			return st, nil, err
		}
		return r.enterTieFinal(st, at, effects, kept)
	default:
		return r.enterTieNoSpeech(st, at, effects, tied)
	}
}

// topTiedTargets 返回并列最高票（票数 >0）的目标座位，升序。
func topTiedTargets(counts map[Seat]int) []Seat {
	best := 0
	for _, n := range counts {
		if n > best {
			best = n
		}
	}
	tied := make([]Seat, 0, len(counts))
	for seat, n := range counts {
		if n == best && n > 0 {
			tied = append(tied, seat)
		}
	}
	sort.Slice(tied, func(i, j int) bool { return tied[i] < tied[j] })
	return tied
}

// enterTieSpeech 从首次平票进入加时发言：记录平票候选，产出公共公告、
// 每名候选的加时发言提示与计时器（docs §投票 4）。收票窗口保持
// VoteStageOpen，投票由各阶段 reducer 按 Tie 子阶段拒绝。
func (r reducer) enterTieSpeech(st State, at time.Time, base []Effect, candidates []Seat) (State, []Effect, error) {
	next := st.Copy()
	next.Vote.Tie = TieSpeech
	next.Vote.TieRound = 0
	next.Vote.Candidates = append([]Seat(nil), candidates...)
	next.Vote.Excluded = nil

	effects := append([]Effect{}, base...)
	speech, err := NewMessageEffect(AudiencePublic, TieSpeechMessageKey, map[string]any{
		"candidates": next.Vote.Candidates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie speech announcement: %w", err)
	}
	effects = append(effects, speech)

	deadline := at.Add(time.Duration(TieSpeechSeconds) * time.Second)
	for _, seat := range next.Vote.Candidates {
		turn, err := NewMessageEffect(AudienceActor, TieSpeechTurnMessageKey, map[string]any{
			"seat":     seat,
			"deadline": deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie speech turn: %w", err)
		}
		effects = append(effects, turn)
	}
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(TieSpeechSeconds) * time.Second})
	return next, effects, nil
}

// enterTieRunoff 进入第 2 次（缩圈）投票：清空上轮收票集合、重发候选
// 公告、投票 UI 与计时器（docs §投票 4）。
func (r reducer) enterTieRunoff(st State, at time.Time, base []Effect) (State, []Effect, error) {
	next := st.Copy()
	next.Vote.Tie = TieRunoff
	next.Vote.TieRound = 0
	next.Vote.Ballots = map[Seat]Seat{}
	next.Vote.Pending = map[Seat]*Seat{}
	next.Vote.Locked = map[Seat]bool{}
	next.Vote.Excluded = nil

	effects := append([]Effect{}, base...)
	announce, err := NewMessageEffect(AudiencePublic, TieRunoffMessageKey, map[string]any{
		"candidates": next.Vote.Candidates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie runoff announcement: %w", err)
	}
	effects = append(effects, announce)

	deadline := at.Add(time.Duration(VoteConfirmSeconds) * time.Second)
	for _, seat := range aliveSeats(next.Players) {
		prompt, err := NewMessageEffect(AudienceActor, TieRunoffPromptMessageKey, map[string]any{
			"seat":       seat,
			"candidates": next.Vote.Candidates,
			"deadline":   deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie runoff prompt: %w", err)
		}
		effects = append(effects, prompt)
	}
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(VoteConfirmSeconds) * time.Second})
	return next, effects, nil
}

// enterTieNoSpeech 进入下一个无发言投票轮（上限 2 轮）：轮次 +1、重开
// 收票窗口、删除上轮临时投票消息并重发公告/提示/计时器。
func (r reducer) enterTieNoSpeech(st State, at time.Time, base []Effect, tied []Seat) (State, []Effect, error) {
	next := st.Copy()
	if next.Vote.TieRound < 2 {
		next.Vote.TieRound++
	}
	next.Vote.Tie = TieNoSpeech
	next.Vote.Candidates = append([]Seat(nil), tied...)
	next.Vote.Ballots = map[Seat]Seat{}
	next.Vote.Pending = map[Seat]*Seat{}
	next.Vote.Locked = map[Seat]bool{}
	next.Vote.Excluded = nil

	effects := append([]Effect{}, base...)
	announce, err := NewMessageEffect(AudiencePublic, TieNoSpeechMessageKey, map[string]any{
		"round":      next.Vote.TieRound,
		"candidates": next.Vote.Candidates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie no-speech announcement: %w", err)
	}
	effects = append(effects, announce)

	deadline := at.Add(time.Duration(VoteConfirmSeconds) * time.Second)
	for _, seat := range aliveSeats(next.Players) {
		prompt, err := NewMessageEffect(AudienceActor, TieRunoffPromptMessageKey, map[string]any{
			"seat":       seat,
			"candidates": next.Vote.Candidates,
			"deadline":   deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie no-speech prompt: %w", err)
		}
		effects = append(effects, prompt)
	}
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(VoteConfirmSeconds) * time.Second})
	return next, effects, nil
}

// enterTieFinal 进入最终 2 人对决：候选人不投票，其余存活玩家必须投
// 2 名候选之一（禁止弃权）；投票人数为偶数时由 resolveFinal 注入 RNG
// 随机排除 1 人（docs §投票 4）。
func (r reducer) enterTieFinal(st State, at time.Time, base []Effect, candidates []Seat) (State, []Effect, error) {
	next := st.Copy()
	next.Vote.Tie = TieFinal
	next.Vote.TieRound = 0
	next.Vote.Candidates = append([]Seat(nil), candidates...)
	next.Vote.Ballots = map[Seat]Seat{}
	next.Vote.Pending = map[Seat]*Seat{}
	next.Vote.Locked = map[Seat]bool{}
	next.Vote.Excluded = map[Seat]bool{}

	effects := append([]Effect{}, base...)
	announce, err := NewMessageEffect(AudiencePublic, TieFinalMessageKey, map[string]any{
		"candidates": next.Vote.Candidates,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: tie final announcement: %w", err)
	}
	effects = append(effects, announce)

	deadline := at.Add(time.Duration(VoteConfirmSeconds) * time.Second)
	for _, seat := range aliveSeats(next.Players) {
		if isTieCandidate(next, seat) {
			continue
		}
		prompt, err := NewMessageEffect(AudienceActor, TieDuelPromptMessageKey, map[string]any{
			"seat":       seat,
			"candidates": next.Vote.Candidates,
			"deadline":   deadline,
		})
		if err != nil {
			return st, nil, fmt.Errorf("game: tie duel prompt: %w", err)
		}
		effects = append(effects, prompt)
	}
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Duration: time.Duration(VoteConfirmSeconds) * time.Second})
	return next, effects, nil
}

// resolveFinal 结算最终对决：先处理偶数投票人的随机排除（与超时失权
// 同一语义，见 tieFinalTimeout），再按剩余票数放逐胜者；无人投票的
// 极端情形由注入 RNG 兜底选出胜者。
func (r reducer) resolveFinal(st State, at time.Time) (State, []Effect, error) {
	next := st.Copy()
	effects := make([]Effect, 0, len(st.Players)+6)
	effects = append(effects, TimerEffect{Phase: PhaseDayVote, Cancel: true})
	for _, seat := range tieVoters(st) {
		del, err := NewMessageEffect(AudienceActor, VoteDeleteMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: duel vote delete: %w", err)
		}
		effects = append(effects, del)
	}
	// 超时已失权者逐个通知（tieFinalTimeout 写入 Excluded）。
	for _, seat := range sortedSetKeys(next.Vote.Excluded) {
		msg, err := NewMessageEffect(AudienceActor, TieDuelExcludedMessageKey, map[string]any{"seat": seat})
		if err != nil {
			return st, nil, fmt.Errorf("game: duel excluded notice: %w", err)
		}
		effects = append(effects, msg)
	}

	// 偶数投票人 → 注入 RNG 随机排除 1 人（必然分出结果）。
	if next.Vote.Excluded == nil {
		next.Vote.Excluded = map[Seat]bool{}
	}
	var voters []Seat
	for _, seat := range aliveSeats(next.Players) {
		if !isTieCandidate(next, seat) && !next.Vote.Excluded[seat] {
			voters = append(voters, seat)
		}
	}
	if len(voters) >= 2 && len(voters)%2 == 0 {
		remaining, err := r.excludeDuelVoter(voters)
		if err != nil {
			return st, nil, err
		}
		remainingSet := make(map[Seat]bool, len(remaining))
		for _, s := range remaining {
			remainingSet[s] = true
		}
		var excluded Seat
		for _, s := range voters {
			if !remainingSet[s] {
				excluded = s
				break
			}
		}
		next.Vote.Excluded[excluded] = true
		delete(next.Vote.Ballots, excluded)
		msg, err := NewMessageEffect(AudienceActor, TieDuelExcludedMessageKey, map[string]any{"seat": excluded})
		if err != nil {
			return st, nil, fmt.Errorf("game: duel excluded notice: %w", err)
		}
		effects = append(effects, msg)
	}

	counts, abstain := tallyVotes(next.Vote.Ballots)
	detail, err := NewMessageEffect(AudiencePublic, VoteDetailMessageKey, map[string]any{
		"ballots": next.Vote.Ballots,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: duel detail: %w", err)
	}
	tally, err := NewMessageEffect(AudiencePublic, VoteTallyMessageKey, map[string]any{
		"counts":  counts,
		"abstain": abstain,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: duel tally: %w", err)
	}
	effects = append(effects, detail, tally)

	if exiled := topVoteTarget(counts); exiled != nil {
		return r.resolveExile(next, at, effects, *exiled)
	}
	return r.finalFallback(next, at, effects)
}

// finalFallback 处理最终对决无人投票/全部失权的极端情形：注入 RNG 兜底
// 选出胜者（MVP 裁决，保证 docs §投票 4「必然分出结果」不变量）。
func (r reducer) finalFallback(st State, at time.Time, base []Effect) (State, []Effect, error) {
	if len(st.Vote.Candidates) != 2 {
		return st, nil, fmt.Errorf("game: final duel requires exactly 2 candidates, got %d", len(st.Vote.Candidates))
	}
	idx, err := r.rng.Intn(len(st.Vote.Candidates))
	if err != nil {
		return st, nil, fmt.Errorf("game: final duel fallback: %w", err)
	}
	return r.resolveExile(st, at, base, st.Vote.Candidates[idx])
}

// keepTwoCandidates 从 ≥3 名平票候选中注入 RNG 保留 2 人（docs §投票 4
// 兜底：其余平票人转为投票人，不出局、照常游戏）；结果升序，调用方可
// 重放同一 RNG 序列得到一致结果。RNG 错误显式返回且不部分修改状态。
func (r reducer) keepTwoCandidates(candidates []Seat) ([]Seat, error) {
	if len(candidates) < 2 {
		return nil, fmt.Errorf("game: tie keep-two requires at least 2 candidates, got %d", len(candidates))
	}
	first, err := r.rng.Intn(len(candidates))
	if err != nil {
		return nil, fmt.Errorf("game: tie keep-two first draw: %w", err)
	}
	rest := make([]Seat, 0, len(candidates)-1)
	rest = append(rest, candidates[:first]...)
	rest = append(rest, candidates[first+1:]...)
	second, err := r.rng.Intn(len(rest))
	if err != nil {
		return nil, fmt.Errorf("game: tie keep-two second draw: %w", err)
	}
	kept := []Seat{candidates[first], rest[second]}
	sort.Slice(kept, func(i, j int) bool { return kept[i] < kept[j] })
	return kept, nil
}

// excludeDuelVoter 从最终对决投票人中注入 RNG 排除 1 人（docs §投票 4：
// 偶数投票人随机排除 1 名，仅本轮失去投票权、不死亡）。RNG 错误显式
// 返回且不部分修改状态。
func (r reducer) excludeDuelVoter(voters []Seat) ([]Seat, error) {
	if len(voters) <= 1 {
		return voters, nil
	}
	idx, err := r.rng.Intn(len(voters))
	if err != nil {
		return nil, fmt.Errorf("game: duel voter exclusion: %w", err)
	}
	remaining := make([]Seat, 0, len(voters)-1)
	remaining = append(remaining, voters[:idx]...)
	remaining = append(remaining, voters[idx+1:]...)
	return remaining, nil
}

// sortedSetKeys 返回 map[Seat]bool 中为 true 的键，升序（nil map 视为空）。
func sortedSetKeys(m map[Seat]bool) []Seat {
	keys := make([]Seat, 0, len(m))
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
