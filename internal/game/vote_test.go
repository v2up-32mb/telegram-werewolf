package game

import (
	"errors"
	"testing"
	"time"
)

// voteReadyState 构造可进入白天投票的领域状态：PhaseDaySpeech、6 名
// 存活玩家、默认设置；reveal 控制死讯是否报身份（遗言子阶段绑定该配置，
// docs 游戏流程设计.md §结算 4）。
func voteReadyState(reveal bool) State {
	st := State{
		RoomID:       "R1",
		GameID:       "G1",
		Phase:        PhaseDaySpeech,
		PhaseVersion: 3,
		Players: []Player{
			{UserID: 1, Seat: 1, Role: RoleWolf},
			{UserID: 2, Seat: 2, Role: RoleWolf},
			{UserID: 3, Seat: 3, Role: RoleSeer},
			{UserID: 4, Seat: 4, Role: RoleWitch},
			{UserID: 5, Seat: 5, Role: RoleVillager},
			{UserID: 6, Seat: 6, Role: RoleVillager},
		},
		Day:      DayState{Speaker: 1, SpeechOrder: []Seat{1, 2, 3, 4, 5, 6}},
		Settings: DefaultRoomSettings(),
	}
	st.Settings.RevealRoleOnDeath = reveal
	st.Processed = map[string]bool{}
	return st
}

// beginVote 便捷封装：PhaseDaySpeech → PhaseDayVote。
func beginVote(t *testing.T, st State) State {
	t.Helper()
	after, _, err := BeginVote(st, time.Now())
	if err != nil {
		t.Fatalf("BeginVote error = %v", err)
	}
	return after
}

// voteVersion 是 BeginVote 之后的期望阶段版本（PhaseDaySpeech v3 → v4）。
const voteVersion uint64 = 4

func voteCmd(id string, actor UserID, target *Seat) VoteCommand {
	return VoteCommand{
		Meta:   CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
		Target: target,
	}
}

func voteConfirmCmd(id string, actor UserID) VoteConfirmCommand {
	return VoteConfirmCommand{
		Meta: CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
	}
}

func lastWordsCmd(id string, actor UserID, text string) LastWordsCommand {
	return LastWordsCommand{
		Meta: CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
		Text: text,
	}
}

func voteTimeoutCmd(id string) TimeoutCommand {
	return TimeoutCommand{
		Meta: CommandMeta{ID: id, ExpectedPhase: PhaseDayVote, PhaseVersion: voteVersion},
	}
}

// messageKeys 提取 Effect 序列中的 MessageEffect key（保持顺序）。
func messageKeys(effects []Effect) []string {
	keys := make([]string, 0, len(effects))
	for _, e := range effects {
		if me, ok := e.(MessageEffect); ok {
			keys = append(keys, me.Key)
		}
	}
	return keys
}

func indexOf(keys []string, key string) int {
	for i, k := range keys {
		if k == key {
			return i
		}
	}
	return -1
}

// TestBeginVoteTransitionsAndPrompts 验证 BeginVote：仅 PhaseDaySpeech 可
// 进入；切换 PhaseDayVote、PhaseVersion+1、Stage=Open 且集合初始化；
// 每名存活玩家收到 vote.prompt（AudienceActor）并启动投票计时器。
func TestBeginVoteTransitionsAndPrompts(t *testing.T) {
	st := voteReadyState(false)
	after, effects, err := BeginVote(st, time.Now())
	if err != nil {
		t.Fatalf("BeginVote error = %v", err)
	}
	if after.Phase != PhaseDayVote {
		t.Fatalf("Phase = %v, want PhaseDayVote", after.Phase)
	}
	if after.PhaseVersion != st.PhaseVersion+1 {
		t.Fatalf("PhaseVersion = %d, want %d", after.PhaseVersion, st.PhaseVersion+1)
	}
	if after.Vote.Stage != VoteStageOpen {
		t.Fatalf("Vote.Stage = %v, want VoteStageOpen", after.Vote.Stage)
	}
	if after.Vote.Ballots == nil || after.Vote.Pending == nil || after.Vote.Locked == nil {
		t.Fatalf("Vote 集合未初始化: %+v", after.Vote)
	}

	keys := messageKeys(effects)
	if got := countKey(keys, VotePromptMessageKey); got != 6 {
		t.Errorf("vote.prompt 数量 = %d, want 6（每名存活玩家一条）", got)
	}
	for _, e := range effects {
		if me, ok := e.(MessageEffect); ok && me.Key == VotePromptMessageKey {
			if me.Audience != AudienceActor {
				t.Errorf("vote.prompt audience = %v, want AudienceActor", me.Audience)
			}
		}
	}
	if !hasTimer(effects, PhaseDayVote) {
		t.Errorf("缺少 PhaseDayVote 计时器效果：%v", effects)
	}
}

func countKey(keys []string, key string) int {
	n := 0
	for _, k := range keys {
		if k == key {
			n++
		}
	}
	return n
}

func hasTimer(effects []Effect, phase Phase) bool {
	for _, e := range effects {
		if te, ok := e.(TimerEffect); ok && te.Phase == phase && !te.Cancel {
			return true
		}
	}
	return false
}

// TestBeginVoteRejectsWrongPhase 验证 BeginVote 只在 PhaseDaySpeech 可用。
func TestBeginVoteRejectsWrongPhase(t *testing.T) {
	st := voteReadyState(false)
	st.Phase = PhaseNight
	if _, _, err := BeginVote(st, time.Now()); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("BeginVote on PhaseNight error = %v, want ErrWrongPhase", err)
	}
}

// TestVotePhaseSilence 验证投票阶段静默：发言命令因阶段不匹配被拒
// （docs 游戏流程设计.md §投票 3：进入投票后关闭发言）。
func TestVotePhaseSilence(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	cmd := SpeakCommand{
		Meta: CommandMeta{ID: "s1", Actor: 1, ExpectedPhase: PhaseDaySpeech, PhaseVersion: 3},
		Text: "再投一票",
	}
	if _, _, err := NewReducer().Reduce(st, cmd); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("SpeakCommand during vote error = %v, want ErrWrongPhase", err)
	}
}

// TestVoteSelectChangeConfirm 验证选择→改票→确认→锁定（docs §投票 1：
// 先选择目标或弃权，确认前可改票，确认后锁定）。
func TestVoteSelectChangeConfirm(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()

	after, effects, err := r.Reduce(st, voteCmd("v1", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand select 3 error = %v", err)
	}
	if got := *after.Vote.Pending[2]; got != 3 {
		t.Fatalf("Pending[2] = %d, want 3", got)
	}

	after, _, err = r.Reduce(after, voteCmd("v2", 2, seatPtr(4)))
	if err != nil {
		t.Fatalf("VoteCommand change to 4 error = %v", err)
	}
	if got := *after.Vote.Pending[2]; got != 4 {
		t.Fatalf("改票后 Pending[2] = %d, want 4", got)
	}

	after, effects, err = r.Reduce(after, voteConfirmCmd("c1", 2))
	if err != nil {
		t.Fatalf("VoteConfirmCommand error = %v", err)
	}
	if after.Vote.Ballots[2] != 4 {
		t.Fatalf("Ballots[2] = %d, want 4", after.Vote.Ballots[2])
	}
	if !after.Vote.Locked[2] {
		t.Fatalf("Locked[2] = false, want true")
	}
	if !containsKey(messageKeys(effects), VoteLockedMessageKey) {
		t.Errorf("确认后缺少 vote.locked 效果：%v", effects)
	}

	if _, _, err := r.Reduce(after, voteCmd("v3", 2, seatPtr(5))); !errors.Is(err, ErrVoteLocked) {
		t.Fatalf("锁定后 VoteCommand error = %v, want ErrVoteLocked", err)
	}
	if _, _, err := r.Reduce(after, voteConfirmCmd("c2", 2)); !errors.Is(err, ErrVoteLocked) {
		t.Fatalf("锁定后重复确认 error = %v, want ErrVoteLocked", err)
	}
}

// TestVoteAbstainNeedsConfirm 验证弃权选择与确认（docs §投票 2、§8.4：
// 弃权也需要确认；确认后 Ballots 记 0=弃权）。
func TestVoteAbstainNeedsConfirm(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()

	after, _, err := r.Reduce(st, voteCmd("v1", 2, nil))
	if err != nil {
		t.Fatalf("VoteCommand abstain error = %v", err)
	}
	if _, has := after.Vote.Pending[2]; !has {
		t.Fatalf("Pending[2] 缺失，want 弃权待确认（nil 键保留）")
	}
	if after.Vote.Pending[2] != nil {
		t.Fatalf("Pending[2] = %v, want nil（弃权）", *after.Vote.Pending[2])
	}

	after, effects, err := r.Reduce(after, voteConfirmCmd("c1", 2))
	if err != nil {
		t.Fatalf("弃权确认 error = %v", err)
	}
	if after.Vote.Ballots[2] != 0 {
		t.Fatalf("Ballots[2] = %d, want 0（弃权）", after.Vote.Ballots[2])
	}
	if !after.Vote.Locked[2] {
		t.Fatalf("Locked[2] = false, want true")
	}
	if !containsKey(messageKeys(effects), VoteLockedMessageKey) {
		t.Errorf("弃权确认后缺少 vote.locked 效果：%v", effects)
	}
}

// TestVoteConfirmWithoutSelection 验证未选择即确认被拒。
func TestVoteConfirmWithoutSelection(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	if _, _, err := NewReducer().Reduce(st, voteConfirmCmd("c1", 2)); !errors.Is(err, ErrVoteNoSelection) {
		t.Fatalf("未选择即确认 error = %v, want ErrVoteNoSelection", err)
	}
}

// TestVoteNoLiveTally 验证投票期间任何效果都不携带实时票数
// （docs §投票 1：结束后才统一公布）。
func TestVoteNoLiveTally(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()

	after, effects, err := r.Reduce(st, voteCmd("v1", 1, seatPtr(2)))
	if err != nil {
		t.Fatalf("VoteCommand error = %v", err)
	}
	after, effects, err = r.Reduce(after, voteConfirmCmd("c1", 1))
	if err != nil {
		t.Fatalf("VoteConfirmCommand error = %v", err)
	}
	for _, key := range []string{VoteDetailMessageKey, VoteTallyMessageKey, VoteResultMessageKey} {
		if containsKey(messageKeys(effects), key) {
			t.Errorf("投票期间不得出现 %s：%v", key, effects)
		}
	}
}

// TestVoteAllConfirmedSettlesEarly 验证全部有票权玩家确认后提前结束并
// 统一公布（报身份模式：无遗言，直接进入黑夜）：逐人明细→票数统计→
// 放逐结果顺序、唯一最高票被放逐、临时投票消息删除、计时器取消。
func TestVoteAllConfirmedSettlesEarly(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	// 1/3/4/5/6 投 2，2 投 3 → 2 号 5 票，3 号 1 票，2 号被放逐。
	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, _, err = r.Reduce(after, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	after, effects, err := r.Reduce(after, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}

	keys := messageKeys(effects)
	if !containsKey(keys, VoteDetailMessageKey) || !containsKey(keys, VoteTallyMessageKey) ||
		!containsKey(keys, VoteResultMessageKey) {
		t.Fatalf("结算缺少明细/统计/结果效果：%v", keys)
	}
	if i, j, k := indexOf(keys, VoteDetailMessageKey), indexOf(keys, VoteTallyMessageKey), indexOf(keys, VoteResultMessageKey); !(i < j && j < k) {
		t.Errorf("结果顺序错误：detail=%d tally=%d result=%d，want detail<tally<result", i, j, k)
	}
	if got := countKey(keys, VoteDeleteMessageKey); got != 6 {
		t.Errorf("vote.delete 数量 = %d, want 6（全部投票临时消息）", got)
	}
	if !hasTimerCancel(effects, PhaseDayVote) {
		t.Errorf("结算缺少计时器取消：%v", effects)
	}

	if after.Vote.Exiled == nil || *after.Vote.Exiled != 2 {
		t.Fatalf("Exiled = %v, want 2", after.Vote.Exiled)
	}
	if !playerBySeat(after.Players, 2).Dead {
		t.Fatalf("2 号未被放逐标记死亡")
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight（报身份模式无遗言直接进入黑夜）", after.Phase)
	}
	if after.PhaseVersion != voteVersion+1 {
		t.Fatalf("PhaseVersion = %d, want %d", after.PhaseVersion, voteVersion+1)
	}
}

// TestVoteTimeoutAbstains 验证超时未确认者按弃权处理并统一结算
// （docs 游戏流程设计.md §超时默认：所有人投票超时 → 弃票）。
func TestVoteTimeoutAbstains(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}

	after, effects, err := r.Reduce(after, voteTimeoutCmd("t1"))
	if err != nil {
		t.Fatalf("TimeoutCommand error = %v", err)
	}
	if after.Vote.Ballots[2] != 0 {
		t.Fatalf("超时玩家 Ballots[2] = %d, want 0（弃权）", after.Vote.Ballots[2])
	}
	if !containsKey(messageKeys(effects), VoteResultMessageKey) {
		t.Errorf("超时结算缺少 vote.result：%v", effects)
	}
	if after.Vote.Exiled == nil || *after.Vote.Exiled != 2 {
		t.Fatalf("Exiled = %v, want 2", after.Vote.Exiled)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestVoteDeadCannotVote 验证死亡玩家无投票权（docs §死亡玩家 5）。
func TestVoteDeadCannotVote(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	st.Players[2].Dead = true // 3 号死亡
	if _, _, err := NewReducer().Reduce(st, voteCmd("v1", 3, seatPtr(1))); !errors.Is(err, ErrDeadPlayer) {
		t.Fatalf("死亡玩家投票 error = %v, want ErrDeadPlayer", err)
	}
}

// TestVoteTargetMustBeAlive 验证投票目标必须为存活玩家。
func TestVoteTargetMustBeAlive(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	st.Players[3].Dead = true // 4 号死亡
	if _, _, err := NewReducer().Reduce(st, voteCmd("v1", 1, seatPtr(4))); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("投死亡玩家 error = %v, want ErrInvalidTarget", err)
	}
}

// TestVoteDuplicateCommand 验证重复命令 ID 被拒绝（防重复结算）。
func TestVoteDuplicateCommand(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	st.Processed["v1"] = true
	if _, _, err := NewReducer().Reduce(st, voteCmd("v1", 1, seatPtr(2))); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("重复命令 error = %v, want ErrDuplicateCommand", err)
	}
}

// TestLastWordsDefaultMode 验证遗言绑定「不报身份」默认模式：被票死者
// 有 30 秒遗言，遗言正常转播（docs §结算 4、§死亡玩家 4）。
func TestLastWordsDefaultMode(t *testing.T) {
	st := beginVote(t, voteReadyState(false)) // 默认不报身份 → 有遗言
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, effects, err := r.Reduce(after, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	after, effects, err = r.Reduce(after, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}

	if after.Vote.Stage != VoteStageLastWords {
		t.Fatalf("Stage = %v, want VoteStageLastWords（默认不报身份有遗言）", after.Vote.Stage)
	}
	// 遗言窗口内投票命令被拒（收票窗口已关闭）。
	if _, _, err := r.Reduce(after, voteCmd("v9", 1, seatPtr(3))); !errors.Is(err, ErrVoteClosed) {
		t.Fatalf("遗言窗口内投票 error = %v, want ErrVoteClosed", err)
	}
	if after.Phase != PhaseDayVote {
		t.Fatalf("Phase = %v, want PhaseDayVote（遗言窗口内不切换阶段）", after.Phase)
	}
	if !containsKey(messageKeys(effects), LastWordsPromptMessageKey) {
		t.Errorf("遗言窗口缺少 last_words.prompt：%v", effects)
	}
	if !hasTimer(effects, PhaseDayVote) {
		t.Errorf("遗言窗口缺少 30 秒计时器：%v", effects)
	}

	// 被票死者（已死亡）发表遗言 → 转播并进入黑夜。
	after, effects, err = r.Reduce(after, lastWordsCmd("lw1", 2, "我怀疑 3 号"))
	if err != nil {
		t.Fatalf("LastWordsCommand error = %v", err)
	}
	if after.Vote.LastWords != "我怀疑 3 号" {
		t.Fatalf("LastWords = %q, want 遗言正文", after.Vote.LastWords)
	}
	if !containsKey(messageKeys(effects), LastWordsPublishedMessageKey) {
		t.Errorf("缺少 last_words.published 转播效果：%v", effects)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight（遗言结束进入黑夜）", after.Phase)
	}
	if after.Vote.Stage != VoteStageClosed {
		t.Fatalf("Stage = %v, want VoteStageClosed", after.Vote.Stage)
	}

	// 遗言结束后已进入黑夜（Phase=PhaseNight），再发遗言因阶段不匹配
	// 被通用 validator 拒绝（ErrWrongPhase；窗口关闭后命令一律作废）。
	if _, _, err := r.Reduce(after, lastWordsCmd("lw2", 2, "再补一句")); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("白天结束后再发遗言 error = %v, want ErrWrongPhase", err)
	}
}

// TestLastWordsRejectsNonExiled 验证只有被票死者能发表遗言。
func TestLastWordsRejectsNonExiled(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, _, err = r.Reduce(after, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	after, _, err = r.Reduce(after, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}

	if _, _, err := r.Reduce(after, lastWordsCmd("lw1", 3, "替他说")); !errors.Is(err, ErrLastWordsNotExiled) {
		t.Fatalf("非被票死者发遗言 error = %v, want ErrLastWordsNotExiled", err)
	}
}

// TestLastWordsTimeoutEndsDay 验证遗言 30 秒超时后直接进入黑夜（无正文）。
func TestLastWordsTimeoutEndsDay(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, _, err = r.Reduce(after, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	after, _, err = r.Reduce(after, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}
	if after.Vote.Stage != VoteStageLastWords {
		t.Fatalf("前置：Stage = %v, want VoteStageLastWords", after.Vote.Stage)
	}

	after, effects, err := r.Reduce(after, voteTimeoutCmd("t1"))
	if err != nil {
		t.Fatalf("遗言超时 error = %v", err)
	}
	if after.Vote.LastWords != "" {
		t.Fatalf("遗言超时 LastWords = %q, want 空", after.Vote.LastWords)
	}
	if containsKey(messageKeys(effects), LastWordsPublishedMessageKey) {
		t.Errorf("遗言超时不得转播：%v", effects)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestVoteRevealModeSkipsLastWords 验证房主选择「报身份」时无遗言，
// 结算后直接进入黑夜（docs §结算 4）。
func TestVoteRevealModeSkipsLastWords(t *testing.T) {
	st := beginVote(t, voteReadyState(true)) // 报身份 → 无遗言
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(2)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, _, err = r.Reduce(after, voteCmd("v2", 2, seatPtr(3)))
	if err != nil {
		t.Fatalf("VoteCommand 2 error = %v", err)
	}
	after, effects, err := r.Reduce(after, voteConfirmCmd("c2", 2))
	if err != nil {
		t.Fatalf("最后确认 error = %v", err)
	}

	if containsKey(messageKeys(effects), LastWordsPromptMessageKey) {
		t.Errorf("报身份模式不得进入遗言窗口：%v", effects)
	}
	if after.Vote.Stage != VoteStageClosed {
		t.Fatalf("Stage = %v, want VoteStageClosed", after.Vote.Stage)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestVoteTieNoExile 验证平票（Task 37 完整平票流程前的最小契约）：
// 不放逐、不死亡、结果注明平票；代码注释记录已知缺口。
func TestVoteTieNoExile(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	// 1/2/3 投 4，4/5/6 投 1 → 3:3 平票。
	after := st
	var err error
	var effects []Effect
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
	for _, actor := range []UserID{4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(1)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, effects, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	// 第二轮循环中座位 6 的确认即全员确认，结算在此时完成（
	// effects 取第二轮最后一次 Reduce 的结果）。
	if after.Vote.Exiled != nil {
		t.Fatalf("平票 Exiled = %v, want nil（不放逐，Task 37 处理）", *after.Vote.Exiled)
	}
	for _, p := range after.Players {
		if p.Dead {
			t.Fatalf("平票不得有人死亡：座位 %d", p.Seat)
		}
	}
	if !containsKey(messageKeys(effects), VoteResultMessageKey) {
		t.Errorf("平票结算缺少 vote.result：%v", effects)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestVoteAllAbstainNoExile 验证全员弃权：无人被放逐，正常进入黑夜。
func TestVoteAllAbstainNoExile(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	after := st
	var err error
	for _, actor := range []UserID{1, 2, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, nil))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	if after.Vote.Exiled != nil {
		t.Fatalf("全员弃权 Exiled = %v, want nil", *after.Vote.Exiled)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight", after.Phase)
	}
}

// TestVoteTimeoutOutsideWindow 验证投票窗口关闭后超时命令被拒。
func TestVoteTimeoutOutsideWindow(t *testing.T) {
	st := beginVote(t, voteReadyState(false))
	st.Vote.Stage = VoteStageClosed
	if _, _, err := NewReducer().Reduce(st, voteTimeoutCmd("t1")); !errors.Is(err, ErrVoteClosed) {
		t.Fatalf("窗口关闭后超时 error = %v, want ErrVoteClosed", err)
	}
}

func voteID(prefix string, actor UserID) string {
	return prefix + string(rune('0'+actor))
}

func containsKey(keys []string, key string) bool {
	return indexOf(keys, key) >= 0
}

func hasTimerCancel(effects []Effect, phase Phase) bool {
	for _, e := range effects {
		if te, ok := e.(TimerEffect); ok && te.Phase == phase && te.Cancel {
			return true
		}
	}
	return false
}
