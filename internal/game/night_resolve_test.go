package game

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// resolveFixture 构造 PhaseNight 6 人局状态：狼 1/2、预言家 3、女巫 4、
// 平民 5/6，屠城胜负模式，夜间窗口全部开启（供结算后清理断言）。
func resolveFixture() State {
	kill := Seat(5)
	return State{
		RoomID:       "NIGHT01",
		Phase:        PhaseNight,
		PhaseVersion: 6,
		Players:      sixLiveFixture(),
		Night: NightState{
			WolfKillTarget: &kill,
			SeerChecked:    map[Seat]bool{},
			SeerResults:    map[Seat]Camp{},
			WolfVotes:      map[Seat]*Seat{},
			WolfLocked:     map[Seat]bool{},
			WolfRound:      0,
			WitchStage:     WitchStageClosed,
			SeerActive:     false,
		},
		Settings:  DefaultRoomSettings(), // 屠城（6 人局默认）
		Processed: map[string]bool{},
	}
}

// countResolveEffects 统计结算效果中的消息 key 数量。
func countResolveEffects(effects []Effect, keys ...string) map[string]int {
	out := map[string]int{}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok {
			for _, k := range keys {
				if m.Key == k {
					out[k]++
				}
			}
		}
	}
	return out
}

// victimsOf 返回 night.death 消息中的 victims 参数。
func victimsOf(effects []Effect) ([]Seat, bool) {
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok && m.Key == NightDeathMessageKey {
			v, ok := m.Params["victims"].([]Seat)
			return v, ok
		}
	}
	return nil, false
}

// deadSeats 返回状态中的死亡座位（升序）。
func deadSeats(st State) []Seat {
	var out []Seat
	for _, p := range st.Players {
		if p.Dead {
			out = append(out, p.Seat)
		}
	}
	return out
}

// TestResolveNightWolfKillNotSaved 验证刀未救：刀口死亡、发 night.death
// 消息、进入白天（PhaseDaySpeech）、PhaseVersion+1、夜间窗口清理。
func TestResolveNightWolfKillNotSaved(t *testing.T) {
	st := resolveFixture()

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if !reflect.DeepEqual(deadSeats(after), []Seat{5}) {
		t.Errorf("死亡座位 = %v, want [5]", deadSeats(after))
	}
	if after.Phase != PhaseDaySpeech {
		t.Errorf("Phase = %v, want PhaseDaySpeech", after.Phase)
	}
	if after.PhaseVersion != 7 {
		t.Errorf("PhaseVersion = %d, want 7", after.PhaseVersion)
	}
	victims, ok := victimsOf(effects)
	if !ok || !reflect.DeepEqual(victims, []Seat{5}) {
		t.Errorf("night.death victims = %v (ok=%v), want [5]", victims, ok)
	}
	got := countResolveEffects(effects, NightDeathMessageKey, NightPeaceMessageKey, SettlementVictoryMessageKey)
	if got[NightPeaceMessageKey] != 0 || got[SettlementVictoryMessageKey] != 0 {
		t.Errorf("效果 = %v, want 仅 night.death", got)
	}
}

// TestResolveNightWolfKillSaved 验证刀已救（当晚用解药）：刀口存活、
// 平安夜消息、进入白天。
func TestResolveNightWolfKillSaved(t *testing.T) {
	st := resolveFixture()
	st.Night.WitchUsedTonight = true
	st.Night.WitchSaveUsed = true

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if len(deadSeats(after)) != 0 {
		t.Errorf("死亡座位 = %v, want 无人死亡（解药救下刀口）", deadSeats(after))
	}
	got := countResolveEffects(effects, NightDeathMessageKey, NightPeaceMessageKey)
	if got[NightPeaceMessageKey] != 1 || got[NightDeathMessageKey] != 0 {
		t.Errorf("效果 = %v, want 仅 night.peace", got)
	}
}

// TestResolveNightPoisonOnly 验证毒药结算：毒药目标死亡。
func TestResolveNightPoisonOnly(t *testing.T) {
	st := resolveFixture()
	st.Night.WolfKillTarget = nil
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	target := Seat(3)
	st.Night.WitchPoisonTarget = &target

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if !reflect.DeepEqual(deadSeats(after), []Seat{3}) {
		t.Errorf("死亡座位 = %v, want [3]", deadSeats(after))
	}
	victims, ok := victimsOf(effects)
	if !ok || !reflect.DeepEqual(victims, []Seat{3}) {
		t.Errorf("night.death victims = %v (ok=%v), want [3]", victims, ok)
	}
}

// TestResolveNightKillAndPoisonDifferentTargets 验证多人死亡：
// 刀口与毒药目标不同且未救 → 两人均死亡，victims 顺序为刀口在前。
func TestResolveNightKillAndPoisonDifferentTargets(t *testing.T) {
	st := resolveFixture()
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	poison := Seat(3)
	st.Night.WitchPoisonTarget = &poison

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if !reflect.DeepEqual(deadSeats(after), []Seat{3, 5}) {
		t.Errorf("死亡座位 = %v, want [3 5]", deadSeats(after))
	}
	victims, ok := victimsOf(effects)
	if !ok || !reflect.DeepEqual(victims, []Seat{5, 3}) {
		t.Errorf("night.death victims = %v, want [5 3]（刀口在前毒药在后）", victims)
	}
}

// TestResolveNightKillAndPoisonSameTarget 验证同目标刀+毒且未救 →
// 死亡一次。
func TestResolveNightKillAndPoisonSameTarget(t *testing.T) {
	st := resolveFixture()
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	poison := Seat(5)
	st.Night.WitchPoisonTarget = &poison

	after, _, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if !reflect.DeepEqual(deadSeats(after), []Seat{5}) {
		t.Errorf("死亡座位 = %v, want [5]（刀毒同目标只死一次）", deadSeats(after))
	}
}

// TestResolveNightPeaceNight 验证平安夜：无刀口、无用药死亡 → night.peace。
func TestResolveNightPeaceNight(t *testing.T) {
	st := resolveFixture()
	st.Night.WolfKillTarget = nil

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if len(deadSeats(after)) != 0 {
		t.Errorf("平安夜死亡座位 = %v, want 无", deadSeats(after))
	}
	got := countResolveEffects(effects, NightDeathMessageKey, NightPeaceMessageKey)
	if got[NightPeaceMessageKey] != 1 || got[NightDeathMessageKey] != 0 {
		t.Errorf("效果 = %v, want 仅 night.peace", got)
	}
}

// TestResolveNightWolfKillTriggersFirst 验证「先触发者获胜、后续行动作废」：
// 仅剩狼 2 与好人 5；狼刀 5（好人全灭 → 狼胜）先触发，女巫毒 2 作废——
// 毒药目标 2 仍存活、Winner=CampWolf、Phase=Settlement。
func TestResolveNightWolfKillTriggersFirst(t *testing.T) {
	st := resolveFixture()
	// 只保留狼 2 与好人 5：1/3/4/6 已死。
	for i := range st.Players {
		switch st.Players[i].Seat {
		case 1, 3, 4, 6:
			st.Players[i].Dead = true
		}
	}
	kill := Seat(5)
	st.Night.WolfKillTarget = &kill
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	poison := Seat(2)
	st.Night.WitchPoisonTarget = &poison

	after, effects, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if after.Phase != PhaseSettlement {
		t.Fatalf("Phase = %v, want PhaseSettlement", after.Phase)
	}
	if after.Settled.Winner != CampWolf {
		t.Errorf("Winner = %v, want CampWolf（狼刀先触发，好人全灭）", after.Settled.Winner)
	}
	if after.PhaseVersion != 7 {
		t.Errorf("PhaseVersion = %d, want 7", after.PhaseVersion)
	}
	// 毒药作废证据：狼 2 仍存活。
	for _, p := range after.Players {
		if p.Seat == 2 && p.Dead {
			t.Error("毒药目标 2 号已死亡——后续行动未作废")
		}
	}
	// 预置死亡 1/3/4/6 + 刀口 5；毒药目标 2 存活（作废）。
	if !reflect.DeepEqual(deadSeats(after), []Seat{1, 3, 4, 5, 6}) {
		t.Errorf("死亡座位 = %v, want [1 3 4 5 6]（毒药作废）", deadSeats(after))
	}
	got := countResolveEffects(effects, NightDeathMessageKey, SettlementVictoryMessageKey)
	if got[SettlementVictoryMessageKey] != 1 {
		t.Errorf("胜利效果 = %v, want settlement.victory", got)
	}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok && m.Key == SettlementVictoryMessageKey {
			if m.Audience != AudiencePublic {
				t.Errorf("settlement.victory 受众 = %v, want Public", m.Audience)
			}
			if w, ok := m.Params["winner"].(Camp); !ok || w != CampWolf {
				t.Errorf("settlement.victory winner = %v, want CampWolf", m.Params["winner"])
			}
		}
	}
}

// TestResolveNightPoisonTriggersVictory 验证毒后触发：狼 1/神职已死，
// 刀口 6 死亡后仅剩狼 2 与好人 5（刀后不触发胜负），毒 2 → 狼人全灭 →
// 好人胜；5 存活、2/6 死亡。
func TestResolveNightPoisonTriggersVictory(t *testing.T) {
	st := resolveFixture()
	for i := range st.Players {
		switch st.Players[i].Seat {
		case 1, 3, 4:
			st.Players[i].Dead = true
		}
	}
	kill := Seat(6)
	st.Night.WolfKillTarget = &kill
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	poison := Seat(2)
	st.Night.WitchPoisonTarget = &poison

	after, _, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if after.Phase != PhaseSettlement {
		t.Fatalf("Phase = %v, want PhaseSettlement", after.Phase)
	}
	if after.Settled.Winner != CampGood {
		t.Errorf("Winner = %v, want CampGood（毒死最后狼人）", after.Settled.Winner)
	}
	// 唯一好人 5 幸存、毒死的 2 死亡、刀口 6 死亡。
	for _, p := range after.Players {
		switch p.Seat {
		case 2:
			if !p.Dead {
				t.Error("毒药目标 2 号未死亡")
			}
		case 5:
			if p.Dead {
				t.Error("刀口后唯一好人 5 号不应死亡")
			}
		case 6:
			if !p.Dead {
				t.Error("刀口 6 号未死亡")
			}
		}
	}
}

// TestResolveNightNoVictoryGoesToDay 验证未分胜负进入白天并清理夜间窗口。
func TestResolveNightNoVictoryGoesToDay(t *testing.T) {
	st := resolveFixture()
	st.Night.WitchUsedTonight = false // 无人死亡之外再叠加：狼 1 已死也不影响
	st.Players[0].Dead = true         // 狼 1 死

	after, _, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if after.Phase != PhaseDaySpeech {
		t.Errorf("Phase = %v, want PhaseDaySpeech", after.Phase)
	}
	if after.Night.WolfRound != 0 || after.Night.WitchStage != WitchStageClosed || after.Night.SeerActive {
		t.Error("夜间窗口未清理（WolfRound/WitchStage/SeerActive）")
	}
	if after.Night.WitchUsedTonight {
		t.Error("WitchUsedTonight 应在结算后清理")
	}
	if after.Night.WitchSaveChoice != nil || after.Night.WitchPoisonChoice != nil || after.Night.SeerPending != nil {
		t.Error("待确认选择未清理")
	}
}

// TestResolveNightLaterActionsVoided 验证胜利后后续行动作废：
// 结算返回 PhaseSettlement，任何 PhaseNight 命令再执行 → ErrWrongPhase。
func TestResolveNightLaterActionsVoided(t *testing.T) {
	st := resolveFixture()
	for i := range st.Players {
		switch st.Players[i].Seat {
		case 1, 3, 4, 6:
			st.Players[i].Dead = true
		}
	}
	kill := Seat(5)
	st.Night.WolfKillTarget = &kill
	st.Night.WitchUsedTonight = true
	st.Night.WitchPoisonUsed = true
	poison := Seat(2)
	st.Night.WitchPoisonTarget = &poison

	after, _, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if after.Phase != PhaseSettlement {
		t.Fatalf("前置失败：Phase = %v", after.Phase)
	}
	cmd := WitchSaveCommand{Meta: witchMeta("voided", 4), Use: false}
	if _, _, err := NewReducer().Reduce(after, cmd); !errors.Is(err, ErrWrongPhase) {
		t.Errorf("结算后 PhaseNight 命令 error = %v, want ErrWrongPhase", err)
	}
}

// TestDeadRoleStageDuration 验证死亡神职 2/3 假等待时长（docs §夜间 6）：
// 15s→10s、30s→20s、非正输入→0。
func TestDeadRoleStageDuration(t *testing.T) {
	cases := []struct {
		name   string
		normal time.Duration
		want   time.Duration
	}{
		{"默认其他角色夜间 15s", 15 * time.Second, 10 * time.Second},
		{"狼人夜间 30s", 30 * time.Second, 20 * time.Second},
		{"零值", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeadRoleStageDuration(tc.normal); got != tc.want {
				t.Errorf("DeadRoleStageDuration(%v) = %v, want %v", tc.normal, got, tc.want)
			}
		})
	}
}

// TestResolveNightDeadRoleStageNotSkipped 验证死亡角色阶段不跳过但技能
// 被拒：女巫已死后 BeginWitchPhase 仍进入解药窗口并产生阶段效果（固定
// 流程），但选择/确认命令经通用 validator 返回 ErrDeadPlayer。
func TestResolveNightDeadRoleStageNotSkipped(t *testing.T) {
	st := resolveFixture()
	st.Players[3].Dead = true // 4 号女巫死亡

	after, effects, err := BeginWitchPhase(st, true)
	if err != nil {
		t.Fatalf("BeginWitchPhase(死亡女巫) error = %v（阶段不应被跳过）", err)
	}
	if after.Night.WitchStage != WitchStageSave {
		t.Errorf("WitchStage = %d, want %d（仍进入解药窗口）", after.Night.WitchStage, WitchStageSave)
	}
	got := countResolveEffects(effects, WitchKillRevealMessageKey, WitchSavePromptMessageKey)
	if got[WitchKillRevealMessageKey] != 1 || got[WitchSavePromptMessageKey] != 1 {
		t.Errorf("死亡女巫阶段效果 = %v, want kill_reveal + save.prompt（固定流程）", got)
	}

	deadMeta := func(id string) CommandMeta {
		return CommandMeta{ID: id, Actor: 4, ExpectedPhase: PhaseNight, PhaseVersion: 6}
	}
	if _, _, err := NewReducer().Reduce(after, WitchSaveCommand{Meta: deadMeta("dead1"), Use: false}); !errors.Is(err, ErrDeadPlayer) {
		t.Errorf("死亡女巫选择 error = %v, want ErrDeadPlayer（不执行技能）", err)
	}
	if _, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: deadMeta("dead2")}); !errors.Is(err, ErrDeadPlayer) {
		t.Errorf("死亡女巫确认 error = %v, want ErrDeadPlayer", err)
	}
}

// TestResolveNightSeerWindowCleared 验证预言家窗口在结算时被关闭。
func TestResolveNightSeerWindowCleared(t *testing.T) {
	st := resolveFixture()
	st.Night.SeerActive = true
	pending := Seat(2)
	st.Night.SeerPending = &pending

	after, _, err := ResolveNight(st)
	if err != nil {
		t.Fatalf("ResolveNight error = %v", err)
	}
	if after.Night.SeerActive || after.Night.SeerPending != nil {
		t.Error("SeerActive/SeerPending 未清理")
	}
}
