package game

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// wolfNightFixture 构造 PhaseNight 6 人局状态：狼人 1/2 号存活，
// 第 1 轮投票窗口开启（WolfRound=1），必须刀人（Settings 显式默认）。
func wolfNightFixture() State {
	players := []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 3, Seat: 3, Role: RoleSeer},
		{UserID: 4, Seat: 4, Role: RoleWitch},
		{UserID: 5, Seat: 5, Role: RoleVillager},
		{UserID: 6, Seat: 6, Role: RoleVillager},
	}
	return State{
		RoomID:       "WOLF01",
		Phase:        PhaseNight,
		PhaseVersion: 3,
		Players:      players,
		Night: NightState{
			WolfRound:   1,
			WolfVotes:   map[Seat]*Seat{},
			WolfLocked:  map[Seat]bool{},
			SeerChecked: map[Seat]bool{},
		},
		Settings:  DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
}

// nightMeta 构造 PhaseNight/v3 的命令 Meta（狼人投票与确认）。
func nightMeta(id string, actor UserID) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseNight, PhaseVersion: 3}
}

// seatPtr 返回指向 seat 的指针。
func seatPtr(seat Seat) *Seat { return &seat }

// countWolfEffects 统计效果列表中各 wolf.* 消息 key 的数量。
func countWolfEffects(effects []Effect) map[string]int {
	out := map[string]int{}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok {
			out[m.Key]++
		}
	}
	return out
}

// wolfNightEffects 校验狼人开始的讨论/投票消息集合。
// TestWolfNightBeginEffects 验证进入夜间时 beginWolfPhase（经 deal.go
// 钩子）产出：每只存活狼人收到 wolf.discuss（AudienceWolf）与 wolf.vote
// （AudienceWolf，含 round/目标存活座位/wolf_mates）；已死亡玩家收到
// 同内容讨论副本（AudienceGodView）；TimerEffect 为 30 秒 PhaseNight；
// WolfRound=1；不存在 AudiencePublic 的 wolf.* 消息。
func TestWolfNightBeginEffects(t *testing.T) {
	st := wolfNightFixture()
	st.Night.WolfRound = 0
	st.Night.WolfVotes = nil
	st.Night.WolfLocked = nil
	st.Players[2] = Player{UserID: 3, Seat: Seat(3), Role: RoleSeer, Dead: true} // 上帝视角
	st.Night.SeerChecked = map[Seat]bool{}

	after, effects, err := beginWolfPhase(st)
	if err != nil {
		t.Fatalf("beginWolfPhase error = %v, want nil", err)
	}
	if after.Night.WolfRound != 1 {
		t.Errorf("WolfRound = %d, want 1", after.Night.WolfRound)
	}
	if after.Night.WolfVotes == nil || after.Night.WolfLocked == nil {
		t.Error("WolfVotes/WolfLocked 未初始化为空 map")
	}

	got := countWolfEffects(effects)
	if got[WolfDiscussMessageKey] != 2 {
		t.Errorf("wolf.discuss 数量 = %d, want 2（狼人群 + 上帝视角副本）", got[WolfDiscussMessageKey])
	}
	if got[WolfVoteMessageKey] != 1 {
		t.Errorf("wolf.vote 数量 = %d, want 1（狼人群投票 UI）", got[WolfVoteMessageKey])
	}

	var timer *TimerEffect
	audiences := map[string]Audience{}
	for _, e := range effects {
		switch e := e.(type) {
		case MessageEffect:
			audiences[e.Key] = e.Audience
			if e.Key == WolfDiscussMessageKey || e.Key == WolfVoteMessageKey {
				if e.Audience != AudienceWolf && e.Audience != AudienceGodView {
					t.Errorf("%s 受众 = %v, want Wolf/GodView", e.Key, e.Audience)
				}
				if e.Key == WolfVoteMessageKey && e.Audience == AudienceGodView {
					t.Errorf("wolf.vote 不应发给上帝视角")
				}
			}
			if e.Audience == AudiencePublic {
				t.Errorf("%s 以 Public 受众产生（无公共泄漏）", e.Key)
			}
			// 每只狼人各自收到讨论与投票效果：AudienceWolf 群发即涵盖全部存活狼人
			if e.Key == WolfVoteMessageKey {
				if _, ok := e.Params["round"]; !ok {
					t.Error("wolf.vote 缺少 round 参数")
				}
				if _, ok := e.Params["targets"]; !ok {
					t.Error("wolf.vote 缺少 targets 参数")
				}
				if _, ok := e.Params["wolf_mates"]; !ok {
					t.Error("wolf.vote 缺少 wolf_mates 参数")
				}
			}
		case TimerEffect:
			timer = &e
		}
	}
	if timer == nil || timer.Phase != PhaseNight || timer.Duration != 30*time.Second || timer.Cancel {
		t.Errorf("TimerEffect = %+v, want Phase=Night Duration=30s Cancel=false", timer)
	}
	if len(effects) != 4 {
		t.Errorf("effects 数量 = %d, want 4（2×讨论 + 投票 + Timer）", len(effects))
	}
}

// TestWolfVoteRejectsNonWolf 验证非狼人（存活神职）不能投票（ErrNotWolf）。
func TestWolfVoteRejectsNonWolf(t *testing.T) {
	st := wolfNightFixture()
	target := Seat(3)
	cmd := WolfVoteCommand{Meta: nightMeta("w1", 3), Target: &target}
	before := st
	after, effects, err := NewReducerWithRNG(&seqRNG{seq: []int{}}).Reduce(st, cmd)
	if !errors.Is(err, ErrNotWolf) {
		t.Fatalf("Reduce error = %v, want ErrNotWolf", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestWolfVoteAllowsSelfAndMate 验证狼人可选择自己与狼队友（任意存活玩家）。
func TestWolfVoteAllowsSelfAndMate(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()

	after, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(1)})
	if err != nil {
		t.Fatalf("狼人 1 投自己 error = %v, want nil", err)
	}
	if got := after.Night.WolfVotes[1]; got == nil || *got != 1 {
		t.Errorf("WolfVotes[1] = %v, want 1（自己）", got)
	}

	after, _, err = r.Reduce(after, WolfVoteCommand{Meta: nightMeta("w2", 2), Target: seatPtr(1)})
	if err != nil {
		t.Fatalf("狼人 2 投狼队友（1 号）error = %v, want nil", err)
	}
	if got := after.Night.WolfVotes[2]; got == nil || *got != 1 {
		t.Errorf("WolfVotes[2] = %v, want 1（狼队友）", got)
	}
}

// TestWolfVoteRejectsDeadTarget 验证目标死亡被拒（ErrInvalidTarget，通用 validator）。
func TestWolfVoteRejectsDeadTarget(t *testing.T) {
	st := wolfNightFixture()
	st.Players[2] = Player{UserID: 3, Seat: Seat(3), Role: RoleSeer, Dead: true}
	target := Seat(3)
	before := st
	after, effects, err := NewReducerWithRNG(&seqRNG{seq: []int{}}).Reduce(
		st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: &target})
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("Reduce error = %v, want ErrInvalidTarget", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestWolfVoteOverwriteThenLocked 验证确认前最终选择可覆盖，确认后锁定。
func TestWolfVoteOverwriteThenLocked(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()

	st, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("第一次选择 error = %v", err)
	}
	st, _, err = r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1b", 1), Target: seatPtr(4)})
	if err != nil {
		t.Fatalf("覆盖选择 error = %v", err)
	}
	if got := st.Night.WolfVotes[1]; got == nil || *got != 4 {
		t.Fatalf("最终选择 = %v, want 4（确认前以最后一次为准）", got)
	}

	st, _, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if err != nil {
		t.Fatalf("确认 error = %v, want nil", err)
	}
	if !st.Night.WolfLocked[1] {
		t.Fatal("WolfLocked[1] = false, want true")
	}

	after, effects, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1c", 1), Target: seatPtr(5)})
	if !errors.Is(err, ErrWolfVoteLocked) {
		t.Fatalf("确认后再投票 error = %v, want ErrWolfVoteLocked", err)
	}
	assertStateUnchanged(t, st, after, effects)

	after, effects, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1b", 1)})
	if !errors.Is(err, ErrWolfVoteLocked) {
		t.Fatalf("重复确认 error = %v, want ErrWolfVoteLocked", err)
	}
	assertStateUnchanged(t, st, after, effects)
}

// TestWolfConfirmRequiresSelection 验证必须刀人时未选择即确认被拒。
func TestWolfConfirmRequiresSelection(t *testing.T) {
	st := wolfNightFixture()
	before := st
	after, effects, err := NewReducerWithRNG(&seqRNG{seq: []int{}}).Reduce(
		st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if !errors.Is(err, ErrWolfNoSelection) {
		t.Fatalf("Reduce error = %v, want ErrWolfNoSelection", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestWolfMustKillRejectsEmptyKill 验证默认必须刀人时主动空刀被拒。
func TestWolfMustKillRejectsEmptyKill(t *testing.T) {
	st := wolfNightFixture()
	before := st
	after, effects, err := NewReducerWithRNG(&seqRNG{seq: []int{}}).Reduce(
		st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: nil})
	if !errors.Is(err, ErrWolfMustKill) {
		t.Fatalf("Reduce error = %v, want ErrWolfMustKill", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestWolfEmptyKillAllowedWhenConfigured 验证 Settings.WolfMustKill=false
// 时双狼空刀 → 平安夜（WolfKillTarget=nil）并结束狼人阶段。
func TestWolfEmptyKillAllowedWhenConfigured(t *testing.T) {
	st := wolfNightFixture()
	st.Settings.WolfMustKill = false
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})

	st, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: nil})
	if err != nil {
		t.Fatalf("狼人 1 空刀 error = %v, want nil", err)
	}
	st, _, err = r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w2", 2), Target: nil})
	if err != nil {
		t.Fatalf("狼人 2 空刀 error = %v, want nil", err)
	}
	st, _, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if err != nil {
		t.Fatalf("狼人 1 确认 error = %v", err)
	}
	after, effects, err := r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c2", 2)})
	if err != nil {
		t.Fatalf("狼人 2 确认 error = %v, want nil（全员确认提前结束）", err)
	}
	if after.Night.WolfKillTarget != nil {
		t.Errorf("WolfKillTarget = %v, want nil（空刀平安夜）", *after.Night.WolfKillTarget)
	}
	if after.Night.WolfRound != 0 {
		t.Errorf("WolfRound = %d, want 0（狼人阶段结束）", after.Night.WolfRound)
	}
	got := countWolfEffects(effects)
	if got[WolfDiscussDeleteMessageKey] != 1 || got[WolfVoteDeleteMessageKey] != 1 {
		t.Errorf("结束删除效果 = %v, want wolf.discuss_delete=1 wolf.vote_delete=1", got)
	}
	if got[WolfTieMessageKey] != 0 {
		t.Errorf("不应出现平票重开（wolf.tie = %d）", got[WolfTieMessageKey])
	}
}

// TestWolfConfirmAllResolvesMajority 验证全员确认后多数目标落定。
func TestWolfConfirmAllResolvesMajority(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()

	st, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("狼人 1 投票 error = %v", err)
	}
	st, _, err = r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w2", 2), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("狼人 2 投票 error = %v", err)
	}
	st, _, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if err != nil {
		t.Fatalf("狼人 1 确认 error = %v", err)
	}
	after, effects, err := r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c2", 2)})
	if err != nil {
		t.Fatalf("狼人 2 确认 error = %v", err)
	}
	if after.Night.WolfKillTarget == nil || *after.Night.WolfKillTarget != 3 {
		t.Errorf("WolfKillTarget = %v, want 3", after.Night.WolfKillTarget)
	}
	if after.Night.WolfRound != 0 {
		t.Errorf("WolfRound = %d, want 0", after.Night.WolfRound)
	}
	got := countWolfEffects(effects)
	if got[WolfDiscussDeleteMessageKey] != 1 || got[WolfVoteDeleteMessageKey] != 1 {
		t.Errorf("结束删除效果 = %v", got)
	}
	if got[WolfTieMessageKey] != 0 {
		t.Errorf("一致票不应触发平票重开")
	}
}

// TestWolfRound1TieReopensRound2 验证首次平票：清空确认状态、保留投票选择、
// WolfRound=2、重发投票 UI（round=2）与 30 秒 Timer；WolfKillTarget 未落定。
func TestWolfRound1TieReopensRound2(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()

	st, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("狼人 1 投 3 error = %v", err)
	}
	st, _, err = r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w2", 2), Target: seatPtr(4)})
	if err != nil {
		t.Fatalf("狼人 2 投 4 error = %v", err)
	}
	st, _, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if err != nil {
		t.Fatalf("狼人 1 确认 error = %v", err)
	}
	after, effects, err := r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c2", 2)})
	if err != nil {
		t.Fatalf("狼人 2 确认 error = %v（平票进入第二轮）", err)
	}

	if after.Night.WolfRound != 2 {
		t.Errorf("WolfRound = %d, want 2", after.Night.WolfRound)
	}
	if len(after.Night.WolfLocked) != 0 {
		t.Errorf("WolfLocked = %v, want 清空（平票清空确认状态）", after.Night.WolfLocked)
	}
	if after.Night.WolfVotes[1] == nil || *after.Night.WolfVotes[1] != 3 ||
		after.Night.WolfVotes[2] == nil || *after.Night.WolfVotes[2] != 4 {
		t.Errorf("WolfVotes = %v, want 保留 {1:3, 2:4}", after.Night.WolfVotes)
	}
	if after.Night.WolfKillTarget != nil {
		t.Errorf("WolfKillTarget = %v, want 未落定", after.Night.WolfKillTarget)
	}

	got := countWolfEffects(effects)
	if got[WolfTieMessageKey] != 1 {
		t.Errorf("wolf.tie 数量 = %d, want 1", got[WolfTieMessageKey])
	}
	if got[WolfVoteMessageKey] != 1 {
		t.Errorf("第二轮 wolf.vote 数量 = %d, want 1", got[WolfVoteMessageKey])
	}
	var timer *TimerEffect
	for _, e := range effects {
		if t, ok := e.(TimerEffect); ok {
			timer = &t
		}
	}
	if timer == nil || timer.Duration != 30*time.Second || timer.Cancel {
		t.Errorf("第二轮 TimerEffect = %+v, want 30s 不取消", timer)
	}
}

// TestWolfRound2TieResolvesByRNG 验证第二轮平票由注入 RNG 随机落定。
func TestWolfRound2TieResolvesByRNG(t *testing.T) {
	st := wolfNightFixture()
	st.Night.WolfRound = 2
	st.Night.WolfVotes = map[Seat]*Seat{1: seatPtr(3), 2: seatPtr(4)}
	st.Night.WolfLocked = map[Seat]bool{}

	// 两狼保持 3/4 平票并重新确认；RNG Intn(2) 返回 0 → 选 3
	r := NewReducerWithRNG(&seqRNG{seq: []int{0}})
	st, _, err := r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if err != nil {
		t.Fatalf("第二轮狼人 1 确认 error = %v", err)
	}
	after, effects, err := r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c2", 2)})
	if err != nil {
		t.Fatalf("第二轮狼人 2 确认 error = %v, want 平票由 RNG 落定", err)
	}
	if after.Night.WolfKillTarget == nil || *after.Night.WolfKillTarget != 3 {
		t.Errorf("WolfKillTarget = %v, want 3（RNG 平票随机落定）", after.Night.WolfKillTarget)
	}
	if after.Night.WolfRound != 0 {
		t.Errorf("WolfRound = %d, want 0", after.Night.WolfRound)
	}
	if got := countWolfEffects(effects); got[WolfTieMessageKey] != 0 {
		t.Errorf("第二轮平票落定不应再重开：wolf.tie=%d", got[WolfTieMessageKey])
	}
}

// TestWolfTimeoutAbandonsKill 验证超时弃刀：WolfKillTarget 保持 nil、
// WolfRound=0、讨论/投票删除效果发出，Phase 仍为 Night。
func TestWolfTimeoutAbandonsKill(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()
	st, _, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("投票 error = %v", err)
	}

	after, effects, err := r.Reduce(st, TimeoutCommand{Meta: nightMeta("t1", 0)})
	if err != nil {
		t.Fatalf("Timeout error = %v, want nil（超时弃刀）", err)
	}
	if after.Phase != PhaseNight {
		t.Errorf("Phase = %v, want PhaseNight（狼人阶段是夜间子阶段）", after.Phase)
	}
	if after.Night.WolfKillTarget != nil {
		t.Errorf("WolfKillTarget = %v, want nil（超时弃刀）", after.Night.WolfKillTarget)
	}
	if after.Night.WolfRound != 0 {
		t.Errorf("WolfRound = %d, want 0", after.Night.WolfRound)
	}
	got := countWolfEffects(effects)
	if got[WolfDiscussDeleteMessageKey] != 1 || got[WolfVoteDeleteMessageKey] != 1 {
		t.Errorf("结束删除效果 = %v", got)
	}
}

// TestWolfPhaseClosedAfterDone 验证狼人阶段结束后投票/确认被拒。
func TestWolfPhaseClosedAfterDone(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: []int{}})
	st := wolfNightFixture()
	st.Night.WolfRound = 0

	after, effects, err := r.Reduce(st, WolfVoteCommand{Meta: nightMeta("w1", 1), Target: seatPtr(3)})
	if !errors.Is(err, ErrWolfVoteClosed) {
		t.Fatalf("结束后投票 error = %v, want ErrWolfVoteClosed", err)
	}
	assertStateUnchanged(t, st, after, effects)

	after, effects, err = r.Reduce(st, WolfConfirmCommand{Meta: nightMeta("c1", 1)})
	if !errors.Is(err, ErrWolfVoteClosed) {
		t.Fatalf("结束后确认 error = %v, want ErrWolfVoteClosed", err)
	}
	assertStateUnchanged(t, st, after, effects)
}

// TestWolfStateValueSemantics 验证 WolfVotes/WolfLocked 经 State.Copy
// 深拷贝：修改副本不影响原状态。
func TestWolfStateValueSemantics(t *testing.T) {
	st := wolfNightFixture()
	st.Night.WolfVotes = map[Seat]*Seat{1: seatPtr(3), 2: nil}
	st.Night.WolfLocked = map[Seat]bool{1: true}

	c := st.Copy()
	*c.Night.WolfVotes[1] = 9
	c.Night.WolfVotes[2] = seatPtr(7)
	delete(c.Night.WolfVotes, 1)
	c.Night.WolfLocked[2] = true
	delete(c.Night.WolfLocked, 1)

	if got := st.Night.WolfVotes[1]; got == nil || *got != 3 {
		t.Errorf("原 WolfVotes[1] = %v, want 3（深拷贝）", got)
	}
	if got := st.Night.WolfVotes[2]; got != nil {
		t.Errorf("原 WolfVotes[2] = %v, want nil", got)
	}
	if _, ok := c.Night.WolfVotes[1]; ok {
		t.Error("副本删除 WolfVotes[1] 未生效")
	}
	if !st.Night.WolfLocked[1] || st.Night.WolfLocked[2] {
		t.Errorf("原 WolfLocked = %v, want {1:true}", st.Night.WolfLocked)
	}
	_ = reflect.DeepEqual
}
