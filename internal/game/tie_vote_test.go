package game

import (
	"errors"
	"testing"
)

// 平票流程测试（docs 游戏流程设计.md §投票 4：首次平票加时发言 → 第 2 次
// 缩圈投票（平票人投其他平票人）→ ≥3 人无发言投票循环（上限 2 轮、兜底
// 随机保留 2 人）→ 最终 2 人对决（禁止弃权、偶数投票人随机排除 1 人））。
// 测试复用 vote_test.go 的 voteReadyState/beginVote/voteCmd/voteConfirmCmd/
// voteTimeoutCmd/messageKeys/containsKey 等既有助手；RNG 用 deck_test.go
// 的 seqRNG 注入并保持可重放。

// eqSeats 比较两个座位切片（顺序敏感）。
func eqSeats(a, b []Seat) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// beginVoteTie 构造首次平票结算后的状态与效果：1/2/3 投 4，4/5/6 投 1
// → 3:3 平票 → 进入加时发言（Tie=Speech，候选 [1,4]）。
func beginVoteTie(t *testing.T, r Reducer, reveal bool) (State, []Effect) {
	t.Helper()
	st := beginVote(t, voteReadyState(reveal))
	after := st
	var effects []Effect
	var err error
	for _, actor := range []UserID{1, 2, 3} {
		if after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(4))); err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		if after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor)); err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	for _, actor := range []UserID{4, 5, 6} {
		if after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(1))); err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		if after, effects, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor)); err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	return after, effects
}

// speechToRunoff 从加时发言推进到第 2 次（缩圈）投票。
func speechToRunoff(t *testing.T, r Reducer, st State) State {
	t.Helper()
	after, _, err := r.Reduce(st, voteTimeoutCmd("ts1"))
	if err != nil {
		t.Fatalf("加时发言超时 error = %v", err)
	}
	return after
}

// voteRunoff 在缩圈/无发言轮为 actor 投目标并确认；ID 前缀区分轮次。
func voteRunoff(t *testing.T, r Reducer, st State, round int, actor UserID, target *Seat) State {
	t.Helper()
	id := string(rune('0' + round))
	after, _, err := r.Reduce(st, VoteCommand{
		Meta:   CommandMeta{ID: "x" + id + string(rune('0'+actor)), Actor: actor, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
		Target: target,
	})
	if err != nil {
		t.Fatalf("轮 %d 座位 %d VoteCommand error = %v", round, actor, err)
	}
	after, _, err = r.Reduce(after, VoteConfirmCommand{
		Meta: CommandMeta{ID: "y" + id + string(rune('0'+actor)), Actor: actor, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
	})
	if err != nil {
		t.Fatalf("轮 %d 座位 %d VoteConfirmCommand error = %v", round, actor, err)
	}
	return after
}

// TestTieVoteSpeechAfterFirstTie 验证首次平票进入加时发言：记录平票候选、
// 公共公告（无 vote.result）、投票命令被拒、平票候选人收到发言提示。
func TestTieVoteSpeechAfterFirstTie(t *testing.T) {
	r := NewReducer()
	after, effects := beginVoteTie(t, r, true)

	if after.Vote.Tie != TieSpeech {
		t.Fatalf("Tie = %v, want TieSpeech", after.Vote.Tie)
	}
	if !eqSeats(after.Vote.Candidates, []Seat{1, 4}) {
		t.Fatalf("Candidates = %v, want [1 4]", after.Vote.Candidates)
	}
	if after.Vote.Stage != VoteStageOpen {
		t.Fatalf("Stage = %v, want VoteStageOpen", after.Vote.Stage)
	}
	if after.Phase != PhaseDayVote {
		t.Fatalf("Phase = %v, want PhaseDayVote（平票流程不切换阶段）", after.Phase)
	}
	if !containsKey(messageKeys(effects), TieSpeechMessageKey) {
		t.Errorf("平票缺少 tie.speech 公共公告：%v", effects)
	}
	if containsKey(messageKeys(effects), VoteResultMessageKey) {
		t.Errorf("平票未落定不得输出 vote.result：%v", effects)
	}
	if got := countKey(messageKeys(effects), TieSpeechTurnMessageKey); got != 2 {
		t.Errorf("tie.speech_turn 数量 = %d, want 2（每名平票候选人一条）", got)
	}
	if !hasTimer(effects, PhaseDayVote) {
		t.Errorf("加时发言缺少计时器：%v", effects)
	}

	// 加时发言阶段投票命令被拒（含平票候选人本人）。
	if _, _, err := r.Reduce(after, voteCmd("tv1", 2, seatPtr(4))); !errors.Is(err, ErrVoteClosed) {
		t.Fatalf("加时发言阶段投票 error = %v, want ErrVoteClosed", err)
	}
	if _, _, err := r.Reduce(after, voteCmd("tv2", 1, seatPtr(4))); !errors.Is(err, ErrVoteClosed) {
		t.Fatalf("平票候选人加时发言阶段投票 error = %v, want ErrVoteClosed", err)
	}
}

// TestTieVoteSpeechTimeoutAdvancesRunoff 验证加时发言超时推进到第 2 次
// （缩圈）投票：候选仅平票玩家、轮次重置、重发提示与计时器。
func TestTieVoteSpeechTimeoutAdvancesRunoff(t *testing.T) {
	r := NewReducer()
	after, _ := beginVoteTie(t, r, true)

	runoff, effects, err := r.Reduce(after, voteTimeoutCmd("ts1"))
	if err != nil {
		t.Fatalf("加时发言超时 error = %v", err)
	}
	if runoff.Vote.Tie != TieRunoff {
		t.Fatalf("Tie = %v, want TieRunoff", runoff.Vote.Tie)
	}
	if runoff.Vote.TieRound != 0 {
		t.Fatalf("TieRound = %d, want 0（无发言轮计数从缩圈后开始）", runoff.Vote.TieRound)
	}
	if len(runoff.Vote.Ballots) != 0 || len(runoff.Vote.Pending) != 0 || len(runoff.Vote.Locked) != 0 {
		t.Fatalf("缩圈轮次未重置：%+v", runoff.Vote)
	}
	if !eqSeats(runoff.Vote.Candidates, []Seat{1, 4}) {
		t.Fatalf("Candidates = %v, want [1 4]", runoff.Vote.Candidates)
	}
	if !containsKey(messageKeys(effects), TieRunoffMessageKey) {
		t.Errorf("缺少 tie.runoff 公告：%v", effects)
	}
	if got := countKey(messageKeys(effects), TieRunoffPromptMessageKey); got != 6 {
		t.Errorf("tie.runoff_prompt 数量 = %d, want 6（全部存活玩家可投）", got)
	}
	if !hasTimer(effects, PhaseDayVote) {
		t.Errorf("缩圈轮缺少计时器：%v", effects)
	}
}

// TestTieVoteRunoffTargetRules 表格测试第 2 次投票边界：仅平票候选可投、
// 非候选不可投、平票人禁止投自己、平票人必须投其他平票人（禁止弃权）、
// 非平票人允许弃权（docs §投票 2/4）。
func TestTieVoteRunoffTargetRules(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true)
	st = speechToRunoff(t, r, st)

	cases := []struct {
		name   string
		actor  UserID
		target *Seat
		want   error
	}{
		{"非候选目标拒绝", 2, seatPtr(5), ErrInvalidTarget},
		{"平票人投自己被拒", 1, seatPtr(1), ErrVoteSelfInTie},
		{"平票人投其他平票人通过", 1, seatPtr(4), nil},
		{"平票人弃权被拒", 1, nil, ErrTieCandidateMustVote},
		{"非平票人投候选通过", 2, seatPtr(1), nil},
		{"非平票人弃权通过", 5, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := r.Reduce(st, voteCmd("tc_"+tc.name, tc.actor, tc.target))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestTieVoteRunoffResolvesWinner 验证缩圈轮唯一最高票落定放逐并进入黑夜
// （报身份模式无遗言；docs §投票 4：第 2 次投票后未平票直接落定）。
func TestTieVoteRunoffResolvesWinner(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true)
	st = speechToRunoff(t, r, st)

	// 1→4、4→1、2/3/5→4、6→1 → 4 号 4 票，1 号 2 票 → 4 号放逐。
	for _, actor := range []UserID{1, 2, 3, 5} {
		st = voteRunoff(t, r, st, 1, actor, seatPtr(4))
	}
	st = voteRunoff(t, r, st, 1, 4, seatPtr(1))
	st = voteRunoff(t, r, st, 1, 6, seatPtr(1))

	// 最后确认返回结算效果（取末尾一次 voteRunoff 的 effects 需重跑：
	// 简化：以最终状态断言）。
	if st.Vote.Exiled == nil || *st.Vote.Exiled != 4 {
		t.Fatalf("Exiled = %v, want 4", st.Vote.Exiled)
	}
	if !playerBySeat(st.Players, 4).Dead {
		t.Fatalf("4 号未被放逐")
	}
	if st.Vote.Tie != TieNone {
		t.Fatalf("Tie = %v, want TieNone（落定后清除）", st.Vote.Tie)
	}
	if st.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", st.Phase)
	}
}

// TestTieVoteRunoffTieGoesFinal 验证缩圈轮 2 名平票 → 直接进入最终对决。
func TestTieVoteRunoffTieGoesFinal(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true)
	st = speechToRunoff(t, r, st)

	// 1/2/3 投 4，4/5/6 投 1 → 3:3 再次平票 → 最终对决。
	for _, actor := range []UserID{1, 2, 3} {
		st = voteRunoff(t, r, st, 1, actor, seatPtr(4))
	}
	for _, actor := range []UserID{4, 5, 6} {
		st = voteRunoff(t, r, st, 1, actor, seatPtr(1))
	}

	if st.Vote.Tie != TieFinal {
		t.Fatalf("Tie = %v, want TieFinal", st.Vote.Tie)
	}
	if !eqSeats(st.Vote.Candidates, []Seat{1, 4}) {
		t.Fatalf("Candidates = %v, want [1 4]", st.Vote.Candidates)
	}
	if st.Vote.TieRound != 0 {
		t.Fatalf("TieRound = %d, want 0", st.Vote.TieRound)
	}
}

// noSpeechThreeWayTie 构造首次 3 方平票（1/4/5 各 2 票）并推进到缩圈轮。
func noSpeechThreeWayTie(t *testing.T, r Reducer, reveal bool) State {
	t.Helper()
	st := beginVote(t, voteReadyState(reveal))
	after := st
	// 1→4、2→4；3→5、4→5；5→1、6→1 → 1/4/5 各 2 票 → 3 方平票。
	for _, actor := range []UserID{1, 2} {
		after = voteRunoff(t, r, after, 7, actor, seatPtr(4))
	}
	for _, actor := range []UserID{3, 4} {
		after = voteRunoff(t, r, after, 7, actor, seatPtr(5))
	}
	for _, actor := range []UserID{5, 6} {
		after = voteRunoff(t, r, after, 7, actor, seatPtr(1))
	}
	if after.Vote.Tie != TieSpeech {
		t.Fatalf("前置：Tie = %v, want TieSpeech", after.Vote.Tie)
	}
	return speechToRunoff(t, r, after)
}

// castThreeWayTieRound 在缩圈/无发言轮投出 3 方平票（1/4/5 各 2 票）。
func castThreeWayTieRound(t *testing.T, r Reducer, st State, round int) State {
	t.Helper()
	for _, actor := range []UserID{1, 2} {
		st = voteRunoff(t, r, st, round, actor, seatPtr(4))
	}
	for _, actor := range []UserID{3, 4} {
		st = voteRunoff(t, r, st, round, actor, seatPtr(5))
	}
	for _, actor := range []UserID{5, 6} {
		st = voteRunoff(t, r, st, round, actor, seatPtr(1))
	}
	return st
}

// TestTieVoteNoSpeechLoopCapAndRandomKeepTwo 验证 ≥3 人无发言投票循环：
// 最多 2 轮，仍平票则由注入 RNG 随机保留 2 名候选（其余平票人转投票人，
// 不出局）→ 最终对决；同序列 RNG 重放结果一致。
func TestTieVoteNoSpeechLoopCapAndRandomKeepTwo(t *testing.T) {
	run := func(rng RNG) (State, []int) {
		r := NewReducerWithRNG(rng)
		st := noSpeechThreeWayTie(t, r, true)
		st = castThreeWayTieRound(t, r, st, 1)
		if st.Vote.Tie != TieNoSpeech || st.Vote.TieRound != 1 {
			t.Fatalf("第 1 个无发言轮：Tie=%v TieRound=%d, want TieNoSpeech/1", st.Vote.Tie, st.Vote.TieRound)
		}
		st = castThreeWayTieRound(t, r, st, 2)
		if st.Vote.Tie != TieNoSpeech || st.Vote.TieRound != 2 {
			t.Fatalf("第 2 个无发言轮：Tie=%v TieRound=%d, want TieNoSpeech/2", st.Vote.Tie, st.Vote.TieRound)
		}
		st = castThreeWayTieRound(t, r, st, 3)
		if st.Vote.Tie != TieFinal {
			t.Fatalf("第 2 轮后仍平票：Tie = %v, want TieFinal（RNG 保留 2 人）", st.Vote.Tie)
		}
		return st, r.(reducer).rng.(*seqRNG).bounds
	}

	sr := &seqRNG{seq: []int{0, 1}}
	st1, bounds1 := run(sr)
	if len(st1.Vote.Candidates) != 2 {
		t.Fatalf("最终候选数 = %d, want 2", len(st1.Vote.Candidates))
	}
	// seq Intn(3)=0 → 候选[0]=1，再 Intn(2)=1 → 候选[2]=5 → [1,5]。
	if !eqSeats(st1.Vote.Candidates, []Seat{1, 5}) {
		t.Fatalf("Candidates = %v, want [1 5]（RNG 保留）", st1.Vote.Candidates)
	}
	if len(bounds1) == 0 || len(bounds1) > 2 {
		t.Fatalf("RNG 调用次数/上限异常：bounds=%v", bounds1)
	}

	// 可重放：同序列 RNG 重跑结果一致。
	sr2 := &seqRNG{seq: []int{0, 1}}
	st2, _ := run(sr2)
	if !eqSeats(st1.Vote.Candidates, st2.Vote.Candidates) {
		t.Fatalf("重放不一致：%v vs %v", st1.Vote.Candidates, st2.Vote.Candidates)
	}
}

// finalFromRunoff 从缩圈 2 方平票进入最终对决。
func finalFromRunoff(t *testing.T, r Reducer, reveal bool) State {
	t.Helper()
	st, _ := beginVoteTie(t, r, reveal)
	st = speechToRunoff(t, r, st)
	for _, actor := range []UserID{1, 2, 3} {
		st = voteRunoff(t, r, st, 1, actor, seatPtr(4))
	}
	for _, actor := range []UserID{4, 5, 6} {
		st = voteRunoff(t, r, st, 1, actor, seatPtr(1))
	}
	if st.Vote.Tie != TieFinal {
		t.Fatalf("前置：Tie = %v, want TieFinal", st.Vote.Tie)
	}
	return st
}

// TestTieVoteFinalRestrictions 验证最终对决边界：2 名候选不投票、其余人
// 禁止弃权、只能投两名候选之一（docs §投票 4）。
func TestTieVoteFinalRestrictions(t *testing.T) {
	r := NewReducer()
	st := finalFromRunoff(t, r, true)

	cases := []struct {
		name   string
		actor  UserID
		target *Seat
		want   error
	}{
		{"候选本人投票被拒", 1, seatPtr(4), ErrDuelCandidateNoVote},
		{"禁止弃权", 2, nil, ErrDuelAbstainForbidden},
		{"非候选目标被拒", 2, seatPtr(3), ErrInvalidTarget},
		{"投候选一通过", 2, seatPtr(1), nil},
		{"投候选二通过", 3, seatPtr(4), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := r.Reduce(st, voteCmd("tf_"+tc.name, tc.actor, tc.target))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestTieVoteFinalEvenVotersExcludeOne 验证最终对决偶数投票人由注入 RNG
// 随机排除 1 人后必然分出结果；被排除者仅本轮失去投票权，不死亡。
func TestTieVoteFinalEvenVotersExcludeOne(t *testing.T) {
	sr := &seqRNG{seq: []int{1}} // Intn(4)=1 → 排除 4 名投票人中第 2 个（座位 3）
	r := NewReducerWithRNG(sr)
	st := finalFromRunoff(t, r, true)

	// 投票人 2/3/5/6（偶数 4 人）：2→1、3→1、5→4、6→4；排除座位 3
	//（其票作废）后 4 号以 2:1 胜出。
	st = carryDuelVote(t, r, st, 2, seatPtr(1))
	st = carryDuelVote(t, r, st, 3, seatPtr(1))
	st = carryDuelVote(t, r, st, 5, seatPtr(4))
	st, effects := carryDuelVoteEffects(t, r, st, 6, seatPtr(4))

	if !st.Vote.Excluded[3] {
		t.Fatalf("Excluded[3] = false, want true（RNG 排除座位 3）")
	}
	if _, has := st.Vote.Ballots[3]; has {
		t.Fatalf("被排除者票数应作废：Ballots[3] = %d", st.Vote.Ballots[3])
	}
	if !containsKey(messageKeys(effects), TieDuelExcludedMessageKey) {
		t.Errorf("缺少 tie.duel_excluded 通知：%v", effects)
	}
	if st.Vote.Exiled == nil || *st.Vote.Exiled != 4 {
		t.Fatalf("Exiled = %v, want 4", st.Vote.Exiled)
	}
	for _, p := range st.Players {
		if p.Seat == 3 && p.Dead {
			t.Fatalf("被排除投票人不得死亡：座位 3")
		}
	}
	if st.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", st.Phase)
	}
}

// carryDuelVote 在最终对决中投票并确认。
func carryDuelVote(t *testing.T, r Reducer, st State, actor UserID, target *Seat) State {
	t.Helper()
	after, _, err := r.Reduce(st, voteCmd("d"+string(rune('0'+actor)), actor, target))
	if err != nil {
		t.Fatalf("最终对决 VoteCommand %d error = %v", actor, err)
	}
	after, _, err = r.Reduce(after, voteConfirmCmd("dc"+string(rune('0'+actor)), actor))
	if err != nil {
		t.Fatalf("最终对决 VoteConfirmCommand %d error = %v", actor, err)
	}
	return after
}

func carryDuelVoteEffects(t *testing.T, r Reducer, st State, actor UserID, target *Seat) (State, []Effect) {
	t.Helper()
	after, _, err := r.Reduce(st, voteCmd("d"+string(rune('0'+actor)), actor, target))
	if err != nil {
		t.Fatalf("最终对决 VoteCommand %d error = %v", actor, err)
	}
	after, effects, err := r.Reduce(after, voteConfirmCmd("dc"+string(rune('0'+actor)), actor))
	if err != nil {
		t.Fatalf("最终对决 VoteConfirmCommand %d error = %v", actor, err)
	}
	return after, effects
}

// TestTieVoteFinalOddVotersNoExclusion 验证奇数投票人不排除、直接决胜。
func TestTieVoteFinalOddVotersNoExclusion(t *testing.T) {
	r := NewReducer()
	st := beginVote(t, voteReadyState(true))
	st.Players[5].Dead = true // 6 号提前死亡（缩小投票人集合）

	// 首次投票：1/2→4、4/5→1、3 弃权 → 2:2 平票 → 加时发言。
	for _, actor := range []UserID{1, 2} {
		st = voteRunoff(t, r, st, 7, actor, seatPtr(4))
	}
	for _, actor := range []UserID{4, 5} {
		st = voteRunoff(t, r, st, 7, actor, seatPtr(1))
	}
	st = voteRunoff(t, r, st, 7, 3, nil) // 弃权
	if st.Vote.Tie != TieSpeech {
		t.Fatalf("前置：Tie = %v, want TieSpeech", st.Vote.Tie)
	}
	st = speechToRunoff(t, r, st)

	// 缩圈轮：1→4、2→1、4→1、5→4、3 弃权 → 2:2 → 最终对决。
	for _, act := range []struct {
		a UserID
		t *Seat
	}{{1, seatPtr(4)}, {2, seatPtr(1)}, {4, seatPtr(1)}, {5, seatPtr(4)}} {
		st = voteRunoff(t, r, st, 1, act.a, act.t)
	}
	st = voteRunoff(t, r, st, 1, 3, nil)
	if st.Vote.Tie != TieFinal {
		t.Fatalf("前置：Tie = %v, want TieFinal", st.Vote.Tie)
	}

	// 最终对决投票人 2/3/5（奇数 3 人，6 号死亡）：2→1、3→4、5→1 → 1 胜。
	st = carryDuelVote(t, r, st, 2, seatPtr(1))
	st = carryDuelVote(t, r, st, 3, seatPtr(4))
	st = carryDuelVote(t, r, st, 5, seatPtr(1))

	if len(st.Vote.Excluded) != 0 {
		t.Fatalf("奇数投票人不得排除：Excluded = %v", st.Vote.Excluded)
	}
	if st.Vote.Exiled == nil || *st.Vote.Exiled != 1 {
		t.Fatalf("Exiled = %v, want 1", st.Vote.Exiled)
	}
	if st.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", st.Phase)
	}
}

// TestTieVoteRunoffAllTimeoutEndsDay 验证缩圈轮全员超时（无人投票）：按
// 弃权处理 → 无人被放逐、不进平票流程、直接结束白天（MVP 裁决：平票
// 无法裁决时当日不流放；平票候选人禁止主动弃权，超时按弃权覆盖）。
func TestTieVoteRunoffAllTimeoutEndsDay(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true)
	st = speechToRunoff(t, r, st)

	after, _, err := r.Reduce(st, voteTimeoutCmd("tr0"))
	if err != nil {
		t.Fatalf("缩圈全员超时 error = %v", err)
	}
	if after.Vote.Exiled != nil {
		t.Fatalf("全员超时 Exiled = %v, want nil", *after.Vote.Exiled)
	}
	if after.Vote.Tie != TieNone {
		t.Fatalf("Tie = %v, want TieNone", after.Vote.Tie)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestTieVoteRunoffTimeoutAbstains 验证缩圈轮超时未确认者按弃权结算。
func TestTieVoteRunoffTimeoutAbstains(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true)
	st = speechToRunoff(t, r, st)

	// 2/3/5/6 确认投 4；1/4 超时 → 弃权 → 4 号 4 票胜出。
	for _, actor := range []UserID{2, 3, 5, 6} {
		st = carryDuelVote(t, r, st, actor, seatPtr(4))
	}
	after, _, err := r.Reduce(st, voteTimeoutCmd("tr1"))
	if err != nil {
		t.Fatalf("缩圈超时 error = %v", err)
	}
	if after.Vote.Ballots[1] != 0 || after.Vote.Ballots[4] != 0 {
		t.Fatalf("超时玩家未按弃权：%+v", after.Vote.Ballots)
	}
	if after.Vote.Exiled == nil || *after.Vote.Exiled != 4 {
		t.Fatalf("Exiled = %v, want 4", after.Vote.Exiled)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestTieVoteFinalTimeoutForfeits 验证最终对决超时者失去本轮投票权
// （与随机排除同一语义），其余票结算。
func TestTieVoteFinalTimeoutForfeits(t *testing.T) {
	r := NewReducer()
	st := finalFromRunoff(t, r, true)

	// 2→1、3→4、5→1 确认；6 超时失权 → 投票人 3 人（奇数）→ 1 胜。
	st = carryDuelVote(t, r, st, 2, seatPtr(1))
	st = carryDuelVote(t, r, st, 3, seatPtr(4))
	st = carryDuelVote(t, r, st, 5, seatPtr(1))
	after, _, err := r.Reduce(st, voteTimeoutCmd("df1"))
	if err != nil {
		t.Fatalf("最终对决超时 error = %v", err)
	}
	if !after.Vote.Excluded[6] {
		t.Fatalf("Excluded[6] = false, want true（超时失权）")
	}
	if _, has := after.Vote.Ballots[6]; has {
		t.Fatalf("失权者票数应作废：Ballots[6] = %d", after.Vote.Ballots[6])
	}
	if after.Vote.Exiled == nil || *after.Vote.Exiled != 1 {
		t.Fatalf("Exiled = %v, want 1", after.Vote.Exiled)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestTieVoteLastWordsAfterDuel 验证对决落定后被票死者进入遗言窗口
// （默认不报身份 30 秒，docs §结算 4），遗言后进入黑夜。
func TestTieVoteLastWordsAfterDuel(t *testing.T) {
	r := NewReducer()
	st := finalFromRunoff(t, r, false) // 不报身份 → 有遗言

	// 2→1、3→1、5→4、6→4（偶数 4 人）→ seqRNG{seq:[0]} → 排除座位 2 → 1 胜。
	r = NewReducerWithRNG(&seqRNG{seq: []int{0}})
	st2 := st
	st2 = carryDuelVote(t, r, st2, 2, seatPtr(1))
	st2 = carryDuelVote(t, r, st2, 3, seatPtr(1))
	st2 = carryDuelVote(t, r, st2, 5, seatPtr(4))
	st2, effects := carryDuelVoteEffects(t, r, st2, 6, seatPtr(4))

	if st2.Vote.Stage != VoteStageLastWords {
		t.Fatalf("Stage = %v, want VoteStageLastWords", st2.Vote.Stage)
	}
	if !containsKey(messageKeys(effects), LastWordsPromptMessageKey) {
		t.Errorf("缺少 last_words.prompt：%v", effects)
	}
	exiled := *st2.Vote.Exiled
	st2, effects, err := r.Reduce(st2, lastWordsCmd("lw1", UserID(exiled), "遗言内容"))
	if err != nil {
		t.Fatalf("LastWordsCommand error = %v", err)
	}
	if !containsKey(messageKeys(effects), LastWordsPublishedMessageKey) {
		t.Errorf("缺少 last_words.published：%v", effects)
	}
	if st2.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", st2.Phase)
	}
}

// TestTieVoteRNGErrorPropagates 验证注入 RNG 错误显式传播（兜底随机与
// 对决排除共用同一错误路径）。
func TestTieVoteRNGErrorPropagates(t *testing.T) {
	rr := NewReducerWithRNG(&errRNG{}).(reducer)
	if _, err := rr.keepTwoCandidates([]Seat{1, 4, 5}); err == nil {
		t.Fatalf("keepTwoCandidates RNG 错误应传播，got nil")
	}
	if _, err := rr.excludeDuelVoter([]Seat{2, 3}); err == nil {
		t.Fatalf("excludeDuelVoter RNG 错误应传播，got nil")
	}
}

// TestTieVoteFinalAllTimeoutFallback 验证最终对决全员超时/无人投票的极端
// 情形：注入 RNG 兜底选出胜者（MVP 裁决，docs §投票 4「必然分出结果」
// 不变量；代码注释见 tie_vote.go finalFallback）。
func TestTieVoteFinalAllTimeoutFallback(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{0}})
	st := finalFromRunoff(t, r, true)

	// 投票人 2/3/5/6 全部超时失权 → 无人投票 → RNG Intn(2)=0 选出候选 1。
	after, _, err := r.Reduce(st, voteTimeoutCmd("dfall"))
	if err != nil {
		t.Fatalf("最终对决全员超时 error = %v", err)
	}
	for _, seat := range []Seat{2, 3, 5, 6} {
		if !after.Vote.Excluded[seat] {
			t.Fatalf("Excluded[%d] = false, want true（超时失权）", seat)
		}
	}
	if after.Vote.Exiled == nil || *after.Vote.Exiled != 1 {
		t.Fatalf("兜底 Exiled = %v, want 1（RNG 选出候选 1）", after.Vote.Exiled)
	}
	if !playerBySeat(after.Players, 1).Dead {
		t.Fatalf("1 号未被放逐")
	}
	if after.Vote.Tie != TieNone {
		t.Fatalf("Tie = %v, want TieNone", after.Vote.Tie)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}
