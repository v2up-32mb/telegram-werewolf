package game

import (
	"errors"
	"reflect"
	"testing"
)

// 白天狼人自爆测试（docs 游戏流程设计.md §狼人自爆、§投票 5）：
// 仅狼人在白天任意时刻可自爆；无遗言；已有投票作废；直接进入黑夜；
// 结果写当前白天主消息（wolves.explode），不额外发送永久事件消息。
// daySpeechReady 与 leave_test.go 共用（vote_test.go 之外的公共助手）。

func explodeCmd(id string, actor UserID, phase Phase, version uint64) ExplodeCommand {
	return ExplodeCommand{
		Meta: CommandMeta{ID: id, Actor: actor, ExpectedPhase: phase, PhaseVersion: version},
	}
}

// TestExplodeDuringSpeech 验证白天发言阶段自爆：直接黑夜、无遗言、
// 取消发言阶段计时器、公共公告、不发 vote.delete。
func TestExplodeDuringSpeech(t *testing.T) {
	st := daySpeechReady()
	r := NewReducer()

	after, effects, err := r.Reduce(st, explodeCmd("e1", 1, PhaseDaySpeech, 3))
	if err != nil {
		t.Fatalf("ExplodeCommand error = %v", err)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
	if after.PhaseVersion != st.PhaseVersion+1 {
		t.Fatalf("PhaseVersion = %d, want %d", after.PhaseVersion, st.PhaseVersion+1)
	}
	if !playerBySeat(after.Players, 1).Dead {
		t.Fatalf("自爆狼人未死亡")
	}
	if playerBySeat(after.Players, 1).Left {
		t.Fatalf("自爆不是退出：Left 不得置位")
	}
	if !reflect.DeepEqual(after.Vote, VoteState{}) {
		t.Fatalf("自爆后 Vote = %+v, want 清空", after.Vote)
	}
	if after.Vote.Stage != VoteStageClosed || after.Vote.Stage == VoteStageLastWords {
		t.Fatalf("自爆后不得进入遗言窗口")
	}
	if !hasTimerCancel(effects, PhaseDaySpeech) {
		t.Fatalf("缺少 PhaseDaySpeech 计时器取消：%v", effects)
	}
	if !containsKey(messageKeys(effects), WolfExplodeMessageKey) {
		t.Errorf("缺少 wolves.explode 公共公告：%v", effects)
	}
	if containsKey(messageKeys(effects), VoteDeleteMessageKey) {
		t.Errorf("发言阶段自爆不得输出 vote.delete：%v", effects)
	}
}

// TestExplodeDuringVoteVoidsVotes 验证投票阶段自爆：已有投票作废（含
// 已确认票）、删除投票临时消息、直接黑夜（docs §投票 5）。
func TestExplodeDuringVoteVoidsVotes(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 2, 3} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(4)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	if len(after.Vote.Ballots) == 0 {
		t.Fatalf("前置：应已有已确认票")
	}

	after, effects, err := r.Reduce(after, explodeCmd("e2", 1, PhaseDayVote, voteVersion))
	if err != nil {
		t.Fatalf("投票阶段自爆 error = %v", err)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
	if !reflect.DeepEqual(after.Vote, VoteState{}) {
		t.Fatalf("投票作废后 Vote = %+v, want 清空", after.Vote)
	}
	if !hasTimerCancel(effects, PhaseDayVote) {
		t.Fatalf("缺少 PhaseDayVote 计时器取消：%v", effects)
	}
	if !containsKey(messageKeys(effects), VoteDeleteMessageKey) {
		t.Errorf("投票阶段自爆缺少 vote.delete：%v", effects)
	}
	if !containsKey(messageKeys(effects), WolfExplodeMessageKey) {
		t.Errorf("缺少 wolves.explode 公告：%v", effects)
	}
	if after.Vote.Exiled != nil {
		t.Fatalf("自爆优先：不得放逐任何人")
	}
}

// TestExplodeVoidsTieFlow 验证平票流程中自爆清空平票状态（Tie/Candidates
// 等全部作废，docs §投票 5：已投出的票全部作废）。
func TestExplodeVoidsTieFlow(t *testing.T) {
	r := NewReducer()
	st, _ := beginVoteTie(t, r, true) // TieSpeech, Candidates [1,4]
	if st.Vote.Tie != TieSpeech {
		t.Fatalf("前置：Tie = %v, want TieSpeech", st.Vote.Tie)
	}

	after, _, err := r.Reduce(st, explodeCmd("e3", 1, PhaseDayVote, voteVersion))
	if err != nil {
		t.Fatalf("平票轮自爆 error = %v", err)
	}
	if after.Vote.Tie != TieNone || len(after.Vote.Candidates) != 0 {
		t.Fatalf("平票状态未作废：%+v", after.Vote)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestExplodeDuringLastWordsNoLastWords 验证遗言窗口内自爆：无遗言、
// 直接黑夜（docs §自爆 1：无遗言）。
func TestExplodeDuringLastWordsNoLastWords(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		var err error
		st, _, err = r.Reduce(st, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		st, _, err = r.Reduce(st, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	st, _, err := r.Reduce(st, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	st, _, err = r.Reduce(st, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}
	if st.Vote.Stage != VoteStageLastWords {
		t.Fatalf("前置：Stage = %v, want VoteStageLastWords", st.Vote.Stage)
	}

	after, effects, err := r.Reduce(st, explodeCmd("e4", 1, PhaseDayVote, voteVersion))
	if err != nil {
		t.Fatalf("遗言窗口自爆 error = %v", err)
	}
	if containsKey(messageKeys(effects), LastWordsPromptMessageKey) {
		t.Errorf("自爆后不得出现 last_words.prompt：%v", effects)
	}
	if after.Vote.Stage == VoteStageLastWords || after.Vote.Stage == VoteStageOpen {
		t.Fatalf("自爆后遗言/收票窗口状态 = %v, want 关闭", after.Vote.Stage)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestExplodeRejectsNonWolf 验证非狼人自爆被拒（docs §狼人自爆：
// 仅狼人可自爆）。
func TestExplodeRejectsNonWolf(t *testing.T) {
	st := daySpeechReady()
	if _, _, err := NewReducer().Reduce(st, explodeCmd("e5", 3, PhaseDaySpeech, 3)); !errors.Is(err, ErrNotWolf) {
		t.Fatalf("非狼人自爆 error = %v, want ErrNotWolf", err)
	}
}

// TestExplodeRejectsWrongPhase 验证夜间/大厅等阶段自爆被拒。
func TestExplodeRejectsWrongPhase(t *testing.T) {
	st := voteReadyState(true)
	st.Phase = PhaseNight
	st.PhaseVersion = 3
	if _, _, err := NewReducer().Reduce(st, explodeCmd("e6", 1, PhaseNight, 3)); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("夜间自爆 error = %v, want ErrWrongPhase", err)
	}
}

// TestExplodeDuplicateCommand 验证重复命令 ID 被拒（防重复自爆结算）。
func TestExplodeDuplicateCommand(t *testing.T) {
	st := daySpeechReady()
	st.Processed["e1"] = true
	if _, _, err := NewReducer().Reduce(st, explodeCmd("e1", 1, PhaseDaySpeech, 3)); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("重复自爆 error = %v, want ErrDuplicateCommand", err)
	}
}
