package game

import (
	"errors"
	"testing"
	"time"
)

// seerNightFixture 构造 PhaseNight 6 人局状态：预言家 3 号存活，
// 查验窗口已开启（SeerActive=true），其他角色与药品全未使用。
func seerNightFixture() State {
	players := []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 3, Seat: 3, Role: RoleSeer},
		{UserID: 4, Seat: 4, Role: RoleWitch},
		{UserID: 5, Seat: 5, Role: RoleVillager},
		{UserID: 6, Seat: 6, Role: RoleVillager},
	}
	return State{
		RoomID:       "SEER01",
		Phase:        PhaseNight,
		PhaseVersion: 5,
		Players:      players,
		Night: NightState{
			SeerChecked: map[Seat]bool{},
			SeerResults: map[Seat]Camp{},
			WolfVotes:   map[Seat]*Seat{},
			WolfLocked:  map[Seat]bool{},
			SeerActive:  true,
		},
		Settings:  DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
}

// seerMeta 构造 PhaseNight/v5 的命令 Meta（预言家窗口用）。
func seerMeta(id string, actor UserID) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseNight, PhaseVersion: 5}
}

// countSeerEffects 统计效果列表中各 seer.* 消息 key 的数量。
func countSeerEffects(effects []Effect) map[string]int {
	out := map[string]int{}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok {
			out[m.Key]++
		}
	}
	return out
}

// TestSeerBeginPhaseEffects 验证进入预言家阶段时 beginSeerPhase 产出：
// 查验提示（seer.prompt，AudienceSeer，含存活目标列表）、15 秒
// PhaseNight TimerEffect；SeerActive=true、SeerPending=nil；
// 已有查验历史（SeerResults/SeerChecked）跨夜保留不清空；
// 不存在 AudiencePublic 的 seer.* 消息。
func TestSeerBeginPhaseEffects(t *testing.T) {
	st := seerNightFixture()
	st.Night.SeerActive = false
	st.Night.SeerResults = map[Seat]Camp{1: CampWolf}
	st.Night.SeerChecked = map[Seat]bool{1: true}

	after, effects, err := beginSeerPhase(st)
	if err != nil {
		t.Fatalf("beginSeerPhase error = %v, want nil", err)
	}
	if !after.Night.SeerActive {
		t.Error("beginSeerPhase 未开启查验窗口")
	}
	if after.Night.SeerPending != nil {
		t.Errorf("SeerPending = %v, want nil", after.Night.SeerPending)
	}
	if after.Night.SeerResults[1] != CampWolf {
		t.Error("beginSeerPhase 清空了查验历史（应跨夜保留）")
	}
	if !after.Night.SeerChecked[1] {
		t.Error("beginSeerPhase 清空了 SeerChecked（应跨夜保留）")
	}

	got := countSeerEffects(effects)
	if got[SeerPromptMessageKey] != 1 {
		t.Errorf("seer.prompt 数量 = %d, want 1", got[SeerPromptMessageKey])
	}
	var timer *TimerEffect
	for _, e := range effects {
		switch e := e.(type) {
		case MessageEffect:
			if e.Audience == AudiencePublic {
				t.Errorf("%s 以 Public 受众产生（查验结果泄漏）", e.Key)
			}
			if e.Key == SeerPromptMessageKey {
				if e.Audience != AudienceSeer {
					t.Errorf("seer.prompt 受众 = %v, want Seer", e.Audience)
				}
				if _, ok := e.Params["targets"]; !ok {
					t.Error("seer.prompt 缺少 targets 参数")
				}
			}
		case TimerEffect:
			timer = &e
		}
	}
	if timer == nil {
		t.Fatal("缺少 TimerEffect")
	}
	if timer.Phase != PhaseNight || timer.Duration != time.Duration(DefaultOtherNightSeconds)*time.Second {
		t.Errorf("Timer = %+v, want PhaseNight %d 秒", timer, DefaultOtherNightSeconds)
	}
}

// TestSeerSelectChangeableBeforeConfirm 验证「选择 → 确认」：确认前可
// 反复修改目标；确认后立即写入二分结果、SeerChecked 同步、阶段提前
// 结束（SeerActive=false），并收到 seer.result（AudienceSeer）。
func TestSeerSelectChangeableBeforeConfirm(t *testing.T) {
	st := seerNightFixture()

	after, _, err := NewReducer().Reduce(st, SeerCheckCommand{Meta: seerMeta("s1", 3), Target: 5})
	if err != nil {
		t.Fatalf("选择 5 号 error = %v", err)
	}
	if after.Night.SeerPending == nil || *after.Night.SeerPending != 5 {
		t.Errorf("SeerPending = %v, want &Seat(5)", after.Night.SeerPending)
	}

	after, _, err = NewReducer().Reduce(after, SeerCheckCommand{Meta: seerMeta("s2", 3), Target: 2})
	if err != nil {
		t.Fatalf("改选 2 号 error = %v", err)
	}
	if after.Night.SeerPending == nil || *after.Night.SeerPending != 2 {
		t.Errorf("改选后 SeerPending = %v, want &Seat(2)（确认前可修改）", after.Night.SeerPending)
	}
	if _, ok := after.Night.SeerResults[2]; ok {
		t.Error("确认前不应产生查验结果")
	}

	final, effects, err := NewReducer().Reduce(after, SeerConfirmCommand{Meta: seerMeta("s3", 3)})
	if err != nil {
		t.Fatalf("确认查验 error = %v", err)
	}
	if final.Night.SeerResults[2] != CampWolf {
		t.Errorf("SeerResults[2] = %v, want CampWolf（狼人二分结果）", final.Night.SeerResults[2])
	}
	if !final.Night.SeerChecked[2] {
		t.Error("确认后 SeerChecked[2] 应为 true")
	}
	if final.Night.SeerActive {
		t.Error("确认后阶段应提前结束（SeerActive=false）")
	}
	if final.Night.SeerPending != nil {
		t.Errorf("确认后 SeerPending = %v, want nil", final.Night.SeerPending)
	}
	got := countSeerEffects(effects)
	if got[SeerResultMessageKey] != 1 {
		t.Errorf("确认效果 = %v, want seer.result", got)
	}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok && m.Key == SeerResultMessageKey {
			if m.Audience != AudienceSeer {
				t.Errorf("seer.result 受众 = %v, want Seer", m.Audience)
			}
			if p, ok := m.Params["target"].(Seat); !ok || p != 2 {
				t.Errorf("seer.result target = %v, want Seat(2)", m.Params["target"])
			}
			if c, ok := m.Params["camp"].(Camp); !ok || c != CampWolf {
				t.Errorf("seer.result camp = %v, want CampWolf", m.Params["camp"])
			}
		}
	}
}

// TestSeerBinaryResults 表格验证二分结果：狼人返回狼人、好人阵营
// （女巫/预言家/平民，含查验自己）全部返回好人。
func TestSeerBinaryResults(t *testing.T) {
	cases := []struct {
		name   string
		target Seat
		want   Camp
	}{
		{"查验狼人", 1, CampWolf},
		{"查验另一狼人", 2, CampWolf},
		{"查验女巫", 4, CampGood},
		{"查验自己", 3, CampGood},
		{"查验平民", 5, CampGood},
		{"查验另一平民", 6, CampGood},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := seerNightFixture()
			after, _, err := NewReducer().Reduce(st, SeerCheckCommand{Meta: seerMeta("c1", 3), Target: tc.target})
			if err != nil {
				t.Fatalf("选择目标 error = %v", err)
			}
			final, _, err := NewReducer().Reduce(after, SeerConfirmCommand{Meta: seerMeta("c2", 3)})
			if err != nil {
				t.Fatalf("确认 error = %v", err)
			}
			if final.Night.SeerResults[tc.target] != tc.want {
				t.Errorf("SeerResults[%d] = %v, want %v", tc.target, final.Night.SeerResults[tc.target], tc.want)
			}
		})
	}
}

// TestSeerConfirmWithoutSelection 验证未选择即确认被拒绝。
func TestSeerConfirmWithoutSelection(t *testing.T) {
	st := seerNightFixture()
	if _, _, err := NewReducer().Reduce(st, SeerConfirmCommand{Meta: seerMeta("m1", 3)}); !errors.Is(err, ErrSeerNoSelection) {
		t.Errorf("未选择即确认 error = %v, want ErrSeerNoSelection", err)
	}
}

// TestSeerTimeoutEmptyCheck 验证超时空验：不随机查验、不产生任何结果、
// 窗口关闭并收到 seer.none 提示。
func TestSeerTimeoutEmptyCheck(t *testing.T) {
	st := seerNightFixture()

	after, effects, err := NewReducer().Reduce(st, TimeoutCommand{Meta: seerMeta("t1", 0)})
	if err != nil {
		t.Fatalf("预言家超时 error = %v, want nil", err)
	}
	if len(after.Night.SeerResults) != 0 {
		t.Errorf("超时不应产生查验结果：%v", after.Night.SeerResults)
	}
	if after.Night.SeerActive {
		t.Error("超时后窗口应关闭（SeerActive=false）")
	}
	got := countSeerEffects(effects)
	if got[SeerNoneMessageKey] != 1 {
		t.Errorf("超时效果 = %v, want seer.none", got)
	}
}

// TestSeerHistoryPersistsOnNewNight 验证查验历史跨夜持续携带：
// 已有结果在 beginSeerPhase 后仍保留（下一夜可引用历史私密标记）。
func TestSeerHistoryPersistsOnNewNight(t *testing.T) {
	st := seerNightFixture()
	st.Night.SeerActive = false
	st.Night.SeerResults = map[Seat]Camp{5: CampGood}
	st.Night.SeerChecked = map[Seat]bool{5: true}

	after, _, err := beginSeerPhase(st)
	if err != nil {
		t.Fatalf("beginSeerPhase error = %v", err)
	}
	if after.Night.SeerResults[5] != CampGood || !after.Night.SeerChecked[5] {
		t.Error("新夜开启后历史查验结果丢失")
	}
}

// TestSeerDeadRejected 验证死亡预言家不执行技能：Dead=true 时通用
// validator 以 ErrDeadPlayer 拒绝选择与确认，且不被视为超时处理。
func TestSeerDeadRejected(t *testing.T) {
	st := seerNightFixture()
	st.Players[2].Dead = true // 3 号预言家死亡

	if _, _, err := NewReducer().Reduce(st, SeerCheckCommand{Meta: seerMeta("d1", 3), Target: 5}); !errors.Is(err, ErrDeadPlayer) {
		t.Errorf("死亡预言家选择查验 error = %v, want ErrDeadPlayer", err)
	}
	if _, _, err := NewReducer().Reduce(st, SeerConfirmCommand{Meta: seerMeta("d2", 3)}); !errors.Is(err, ErrDeadPlayer) {
		t.Errorf("死亡预言家确认 error = %v, want ErrDeadPlayer", err)
	}
}

// TestSeerNonSeerRejected 验证只有预言家本人可操作查验。
func TestSeerNonSeerRejected(t *testing.T) {
	st := seerNightFixture()

	if _, _, err := NewReducer().Reduce(st, SeerCheckCommand{Meta: seerMeta("n1", 4), Target: 5}); !errors.Is(err, ErrNotSeer) {
		t.Errorf("非预言家选择查验 error = %v, want ErrNotSeer", err)
	}
	if _, _, err := NewReducer().Reduce(st, SeerConfirmCommand{Meta: seerMeta("n2", 4)}); !errors.Is(err, ErrNotSeer) {
		t.Errorf("非预言家确认 error = %v, want ErrNotSeer", err)
	}
}

// TestSeerWindowClosedRejected 验证窗口未开启时选择/确认被拒；
// Reduce(Timeout) 保持既有未实现语义（reducer_test 契约），
// handler 层直接校验关闭守卫返回 ErrSeerActionClosed。
func TestSeerWindowClosedRejected(t *testing.T) {
	st := seerNightFixture()
	st.Night.SeerActive = false

	if _, _, err := NewReducer().Reduce(st, SeerCheckCommand{Meta: seerMeta("cl1", 3), Target: 5}); !errors.Is(err, ErrSeerActionClosed) {
		t.Errorf("窗口关闭时选择 error = %v, want ErrSeerActionClosed", err)
	}
	if _, _, err := NewReducer().Reduce(st, SeerConfirmCommand{Meta: seerMeta("cl2", 3)}); !errors.Is(err, ErrSeerActionClosed) {
		t.Errorf("窗口关闭时确认 error = %v, want ErrSeerActionClosed", err)
	}
	if _, _, err := NewReducer().Reduce(st, TimeoutCommand{Meta: seerMeta("cl3", 0)}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("窗口关闭时 Reduce(Timeout) error = %v, want ErrNotImplemented（既有未实现契约）", err)
	}
	if _, _, err := NewReducer().(reducer).seerTimeout(st, TimeoutCommand{Meta: seerMeta("cl4", 0)}); !errors.Is(err, ErrSeerActionClosed) {
		t.Errorf("seerTimeout 关闭守卫 error = %v, want ErrSeerActionClosed", err)
	}
}

// TestSeerSensitiveMessagesNeverPublic 验证 seer.* 私密前缀白名单：
// 查验相关消息不能以 AudiencePublic 受众构造，只能 Seer/GodView。
func TestSeerSensitiveMessagesNeverPublic(t *testing.T) {
	for _, key := range []string{
		SeerPromptMessageKey,
		SeerResultMessageKey,
		SeerNoneMessageKey,
	} {
		if _, err := NewMessageEffect(AudiencePublic, key, nil); err == nil {
			t.Errorf("NewMessageEffect(Public, %q) error = nil, want 拒绝", key)
		}
		if _, err := NewMessageEffect(AudienceSeer, key, nil); err != nil {
			t.Errorf("NewMessageEffect(Seer, %q) error = %v, want nil", key, err)
		}
	}
}
