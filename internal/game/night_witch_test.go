package game

import (
	"errors"
	"testing"
	"time"
)

// witchNightFixture 构造 PhaseNight 6 人局状态：女巫 4 号存活，
// 狼人刀口为 5 号，解药窗口已开启（WitchStage=1），首夜自救启用
// （Settings 显式默认 + WitchFirstNight=true）。
func witchNightFixture() State {
	kill := Seat(5)
	players := []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 3, Seat: 3, Role: RoleSeer},
		{UserID: 4, Seat: 4, Role: RoleWitch},
		{UserID: 5, Seat: 5, Role: RoleVillager},
		{UserID: 6, Seat: 6, Role: RoleVillager},
	}
	return State{
		RoomID:       "WITCH1",
		Phase:        PhaseNight,
		PhaseVersion: 4,
		Players:      players,
		Night: NightState{
			WolfKillTarget:  &kill,
			SeerChecked:     map[Seat]bool{},
			WolfVotes:       map[Seat]*Seat{},
			WolfLocked:      map[Seat]bool{},
			WitchStage:      WitchStageSave,
			WitchFirstNight: true,
		},
		Settings:  DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
}

// witchMeta 构造 PhaseNight/v4 的命令 Meta（女巫窗口用）。
func witchMeta(id string, actor UserID) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseNight, PhaseVersion: 4}
}

// countWitchEffects 统计效果列表中各 witch.* 消息 key 的数量。
func countWitchEffects(effects []Effect) map[string]int {
	out := map[string]int{}
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok {
			out[m.Key]++
		}
	}
	return out
}

// TestWitchBeginPhaseEffects 验证进入女巫阶段时 BeginWitchPhase 产出：
// 刀口告知（witch.kill_reveal，AudienceActor，含 kill_target）、解药窗口
// 提示（witch.save.prompt，AudienceActor，含药品状态）、15 秒 PhaseNight
// TimerEffect；WitchStage=1、WitchUsedTonight 重置为 false、
// WitchFirstNight 记录首夜语义；不存在 AudiencePublic 的 witch.* 消息。
func TestWitchBeginPhaseEffects(t *testing.T) {
	st := witchNightFixture()
	st.Night.WitchStage = WitchStageClosed

	after, effects, err := BeginWitchPhase(st, true)
	if err != nil {
		t.Fatalf("BeginWitchPhase error = %v, want nil", err)
	}
	if after.Night.WitchStage != WitchStageSave {
		t.Errorf("WitchStage = %d, want %d（解药窗口）", after.Night.WitchStage, WitchStageSave)
	}
	if after.Night.WitchUsedTonight {
		t.Error("BeginWitchPhase 未重置 WitchUsedTonight")
	}
	if !after.Night.WitchFirstNight {
		t.Error("BeginWitchPhase(firstNight=true) 未记录首夜语义")
	}

	got := countWitchEffects(effects)
	if got[WitchKillRevealMessageKey] != 1 {
		t.Errorf("witch.kill_reveal 数量 = %d, want 1", got[WitchKillRevealMessageKey])
	}
	if got[WitchSavePromptMessageKey] != 1 {
		t.Errorf("witch.save.prompt 数量 = %d, want 1", got[WitchSavePromptMessageKey])
	}

	var timer *TimerEffect
	for _, e := range effects {
		switch e := e.(type) {
		case MessageEffect:
			if e.Audience == AudiencePublic {
				t.Errorf("%s 以 Public 受众产生（刀口/用药泄漏）", e.Key)
			}
			if e.Key == WitchKillRevealMessageKey {
				if e.Audience != AudienceActor {
					t.Errorf("witch.kill_reveal 受众 = %v, want Actor", e.Audience)
				}
				target, ok := e.Params["kill_target"].(*Seat)
				if !ok || target == nil || *target != 5 {
					t.Errorf("witch.kill_reveal kill_target = %v, want &Seat(5)", e.Params["kill_target"])
				}
			}
			if e.Key == WitchSavePromptMessageKey {
				if e.Audience != AudienceActor {
					t.Errorf("witch.save.prompt 受众 = %v, want Actor", e.Audience)
				}
				if _, ok := e.Params["save_used"]; !ok {
					t.Error("witch.save.prompt 缺少 save_used 参数")
				}
				if _, ok := e.Params["poison_used"]; !ok {
					t.Error("witch.save.prompt 缺少 poison_used 参数")
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

// TestWitchSaveSelectChangeableBeforeConfirm 验证解药窗口「选择 → 确认」：
// 确认前可反复修改（使用解药 → 不使用解药），确认「不使用解药」后解药
// 不消耗并进入毒药窗口（WitchStage=2），并收到确认文案 + 毒药提示。
func TestWitchSaveSelectChangeableBeforeConfirm(t *testing.T) {
	st := witchNightFixture()

	after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("w1", 4), Use: true})
	if err != nil {
		t.Fatalf("选择使用解药 error = %v", err)
	}
	if after.Night.WitchSaveChoice == nil || !*after.Night.WitchSaveChoice {
		t.Error("选择使用解药后 WitchSaveChoice 应为 true")
	}

	after, _, err = NewReducer().Reduce(after, WitchSaveCommand{Meta: witchMeta("w2", 4), Use: false})
	if err != nil {
		t.Fatalf("改选不使用解药 error = %v", err)
	}
	if after.Night.WitchSaveChoice == nil || *after.Night.WitchSaveChoice {
		t.Error("改选不使用解药后 WitchSaveChoice 应为 false（确认前可修改）")
	}

	final, effects, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("w3", 4)})
	if err != nil {
		t.Fatalf("确认 error = %v", err)
	}
	if final.Night.WitchSaveUsed {
		t.Error("确认不使用解药后解药不应消耗")
	}
	if final.Night.WitchStage != WitchStagePoison {
		t.Errorf("WitchStage = %d, want %d（毒药窗口）", final.Night.WitchStage, WitchStagePoison)
	}
	got := countWitchEffects(effects)
	if got[WitchSaveLockedMessageKey] != 1 || got[WitchPoisonPromptMessageKey] != 1 {
		t.Errorf("确认效果 = %v, want witch.save.locked + witch.poison.prompt", got)
	}
}

// TestWitchSaveUseConsumesAndEnds 验证确认使用解药后：立即永久消耗解药、
// 本夜已用一瓶、本夜不能再用毒药、阶段提前结束（WitchStage=0）。
func TestWitchSaveUseConsumesAndEnds(t *testing.T) {
	st := witchNightFixture()

	after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("s1", 4), Use: true})
	if err != nil {
		t.Fatalf("选择使用解药 error = %v", err)
	}
	final, effects, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("s2", 4)})
	if err != nil {
		t.Fatalf("确认使用解药 error = %v", err)
	}
	if !final.Night.WitchSaveUsed {
		t.Error("确认使用解药后 WitchSaveUsed 应为 true（永久消耗）")
	}
	if !final.Night.WitchUsedTonight {
		t.Error("确认使用解药后 WitchUsedTonight 应为 true（本夜已用一瓶）")
	}
	if final.Night.WitchStage != WitchStageClosed {
		t.Errorf("WitchStage = %d, want 0（确认后提前结束）", final.Night.WitchStage)
	}
	got := countWitchEffects(effects)
	if got[WitchSaveLockedMessageKey] != 1 || got[WitchPoisonPromptMessageKey] != 0 {
		t.Errorf("确认使用解药效果 = %v, want 仅 witch.save.locked（不使用解药才开毒药窗口）", got)
	}

	// 阶段结束后任何女巫操作均被拒绝。
	if _, _, err := NewReducer().Reduce(final, WitchPoisonCommand{Meta: witchMeta("s3", 4), Target: seatPtr(3)}); !errors.Is(err, ErrWitchActionClosed) {
		t.Errorf("阶段结束后的毒药选择 error = %v, want ErrWitchActionClosed", err)
	}
}

// TestWitchNightOnePotionOnly 验证 reducer 保证「一夜一瓶」：本夜已用药
// 后（WitchUsedTonight=true），任何新的选择/确认都被拒绝。
func TestWitchNightOnePotionOnly(t *testing.T) {
	st := witchNightFixture()
	st.Night.WitchUsedTonight = true

	if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("p1", 4), Use: false}); !errors.Is(err, ErrWitchUsedTonight) {
		t.Errorf("已用药后的解药选择 error = %v, want ErrWitchUsedTonight", err)
	}
	// 毒药/确认断言需在毒药窗口下（解药窗口内毒药选择先命中
	// ErrWitchActionClosed，顺序校验正确）。
	poisonStage := st
	poisonStage.Night.WitchStage = WitchStagePoison
	if _, _, err := NewReducer().Reduce(poisonStage, WitchPoisonCommand{Meta: witchMeta("p2", 4), Target: seatPtr(3)}); !errors.Is(err, ErrWitchUsedTonight) {
		t.Errorf("已用药后的毒药选择 error = %v, want ErrWitchUsedTonight", err)
	}
	if _, _, err := NewReducer().Reduce(poisonStage, WitchConfirmCommand{Meta: witchMeta("p3", 4)}); !errors.Is(err, ErrWitchUsedTonight) {
		t.Errorf("已用药后的确认 error = %v, want ErrWitchUsedTonight", err)
	}
}

// TestWitchPotionPermanentConsumption 验证药品一局内永久消耗：
// 解药已用（WitchSaveUsed=true）后不能再次使用解药，可继续选择不使用并
// 进入毒药窗口；毒药已用（WitchPoisonUsed=true）后不能毒任何人。
func TestWitchPotionPermanentConsumption(t *testing.T) {
	t.Run("解药永久消耗", func(t *testing.T) {
		st := witchNightFixture()
		st.Night.WitchSaveUsed = true

		if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("a1", 4), Use: true}); !errors.Is(err, ErrWitchSaveUnavailable) {
			t.Errorf("解药用尽后选择使用 error = %v, want ErrWitchSaveUnavailable", err)
		}
		after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("a2", 4), Use: false})
		if err != nil {
			t.Fatalf("选择不使用解药 error = %v", err)
		}
		final, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("a3", 4)})
		if err != nil {
			t.Fatalf("确认不使用解药 error = %v", err)
		}
		if final.Night.WitchStage != WitchStagePoison {
			t.Errorf("WitchStage = %d, want %d", final.Night.WitchStage, WitchStagePoison)
		}
	})

	t.Run("毒药永久消耗", func(t *testing.T) {
		st := witchNightFixture()
		st.Night.WitchStage = WitchStagePoison
		st.Night.WitchPoisonUsed = true

		if _, _, err := NewReducer().Reduce(st, WitchPoisonCommand{Meta: witchMeta("b1", 4), Target: seatPtr(3)}); !errors.Is(err, ErrWitchPoisonUnavailable) {
			t.Errorf("毒药用尽后选择毒人 error = %v, want ErrWitchPoisonUnavailable", err)
		}
		after, _, err := NewReducer().Reduce(st, WitchPoisonCommand{Meta: witchMeta("b2", 4), Target: nil})
		if err != nil {
			t.Fatalf("选择不使用毒药 error = %v（毒药用尽仍可确认不使用）", err)
		}
		final, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("b3", 4)})
		if err != nil {
			t.Fatalf("确认不使用毒药 error = %v", err)
		}
		if final.Night.WitchPoisonTarget != nil || final.Night.WitchStage != WitchStageClosed {
			t.Errorf("毒药用尽后确认结束状态 = %+v，want 不记录新目标且 Stage=0（WitchPoisonUsed 保持 true=永久已用）", final.Night)
		}
	})
}

// TestWitchPoisonSelectConfirm 验证毒药窗口：选择存活目标 → 确认 →
// 永久消耗毒药、记录毒药目标、阶段提前结束。
func TestWitchPoisonSelectConfirm(t *testing.T) {
	st := witchNightFixture()
	st.Night.WitchStage = WitchStagePoison

	after, _, err := NewReducer().Reduce(st, WitchPoisonCommand{Meta: witchMeta("q1", 4), Target: seatPtr(3)})
	if err != nil {
		t.Fatalf("选择毒药目标 error = %v", err)
	}
	if after.Night.WitchPoisonChoice == nil || *after.Night.WitchPoisonChoice != 3 {
		t.Errorf("WitchPoisonChoice = %v, want &Seat(3)", after.Night.WitchPoisonChoice)
	}
	final, effects, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("q2", 4)})
	if err != nil {
		t.Fatalf("确认毒药目标 error = %v", err)
	}
	if !final.Night.WitchPoisonUsed {
		t.Error("确认毒药目标后 WitchPoisonUsed 应为 true")
	}
	if final.Night.WitchPoisonTarget == nil || *final.Night.WitchPoisonTarget != 3 {
		t.Errorf("WitchPoisonTarget = %v, want &Seat(3)", final.Night.WitchPoisonTarget)
	}
	if !final.Night.WitchUsedTonight || final.Night.WitchStage != WitchStageClosed {
		t.Errorf("确认毒药后状态 = %+v，want 已用一瓶且阶段结束", final.Night)
	}
	got := countWitchEffects(effects)
	if got[WitchPoisonLockedMessageKey] != 1 {
		t.Errorf("确认毒药效果 = %v, want witch.poison.locked", got)
	}
}

// TestWitchPoisonSkipConfirm 验证「不使用毒药」：选择 nil → 确认 →
// 不消耗毒药、不记录目标、阶段提前结束。
func TestWitchPoisonSkipConfirm(t *testing.T) {
	st := witchNightFixture()
	st.Night.WitchStage = WitchStagePoison

	after, _, err := NewReducer().Reduce(st, WitchPoisonCommand{Meta: witchMeta("n1", 4), Target: nil})
	if err != nil {
		t.Fatalf("选择不使用毒药 error = %v", err)
	}
	if !after.Night.WitchPoisonSkip {
		t.Error("选择不使用毒药后 WitchPoisonSkip 应为 true")
	}
	final, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("n2", 4)})
	if err != nil {
		t.Fatalf("确认不使用毒药 error = %v", err)
	}
	if final.Night.WitchPoisonUsed || final.Night.WitchPoisonTarget != nil {
		t.Error("不使用毒药不应消耗毒药或记录目标")
	}
	if final.Night.WitchStage != WitchStageClosed {
		t.Errorf("WitchStage = %d, want 0（确认后提前结束）", final.Night.WitchStage)
	}
}

// TestWitchConfirmWithoutSelection 验证未选择即确认被拒绝。
func TestWitchConfirmWithoutSelection(t *testing.T) {
	t.Run("解药窗口未选择", func(t *testing.T) {
		st := witchNightFixture()
		if _, _, err := NewReducer().Reduce(st, WitchConfirmCommand{Meta: witchMeta("m1", 4)}); !errors.Is(err, ErrWitchNoSelection) {
			t.Errorf("解药窗口直接确认 error = %v, want ErrWitchNoSelection", err)
		}
	})
	t.Run("毒药窗口未选择", func(t *testing.T) {
		st := witchNightFixture()
		st.Night.WitchStage = WitchStagePoison
		if _, _, err := NewReducer().Reduce(st, WitchConfirmCommand{Meta: witchMeta("m2", 4)}); !errors.Is(err, ErrWitchNoSelection) {
			t.Errorf("毒药窗口直接确认 error = %v, want ErrWitchNoSelection", err)
		}
	})
}

// TestWitchSelfSaveFirstNightConfig 验证首夜自救配置：仅首夜且配置开启时
// 才能使用解药自救（刀口=女巫自己）；非首夜或配置关闭时拒绝自救。
func TestWitchSelfSaveFirstNightConfig(t *testing.T) {
	t.Run("首夜且配置开启可自救", func(t *testing.T) {
		st := witchNightFixture()
		kill := Seat(4)
		st.Night.WolfKillTarget = &kill
		st.Night.WitchFirstNight = true
		st.Settings.WitchSelfSaveFirstNight = true

		after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("c1", 4), Use: true})
		if err != nil {
			t.Fatalf("首夜自救选择 error = %v, want nil", err)
		}
		final, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("c2", 4)})
		if err != nil {
			t.Fatalf("首夜自救确认 error = %v, want nil", err)
		}
		if !final.Night.WitchSaveUsed || final.Night.WitchStage != WitchStageClosed {
			t.Errorf("首夜自救结果 = %+v, want 解药消耗且阶段结束", final.Night)
		}
	})

	t.Run("首夜但配置关闭拒绝自救", func(t *testing.T) {
		st := witchNightFixture()
		kill := Seat(4)
		st.Night.WolfKillTarget = &kill
		st.Night.WitchFirstNight = true
		st.Settings.WitchSelfSaveFirstNight = false

		if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("c3", 4), Use: true}); !errors.Is(err, ErrWitchCannotSelfSave) {
			t.Errorf("配置关闭自救 error = %v, want ErrWitchCannotSelfSave", err)
		}
	})

	t.Run("非首夜拒绝自救", func(t *testing.T) {
		st := witchNightFixture()
		kill := Seat(4)
		st.Night.WolfKillTarget = &kill
		st.Night.WitchFirstNight = false
		st.Settings.WitchSelfSaveFirstNight = true

		if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("c4", 4), Use: true}); !errors.Is(err, ErrWitchCannotSelfSave) {
			t.Errorf("非首夜自救 error = %v, want ErrWitchCannotSelfSave", err)
		}
	})
}

// TestWitchKilledCannotPoison 验证死亡限制：女巫当夜被刀且不能自救时，
// 不能用毒（毒人选择被拒），但可选择不使用毒药并确认提前结束；
// 救别人（刀口不是女巫）不受此限制。
func TestWitchKilledCannotPoison(t *testing.T) {
	st := witchNightFixture()
	kill := Seat(4)
	st.Night.WolfKillTarget = &kill
	st.Night.WitchFirstNight = false // 非首夜 → 不能自救
	st.Settings.WitchSelfSaveFirstNight = true

	if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("d1", 4), Use: true}); !errors.Is(err, ErrWitchCannotSelfSave) {
		t.Fatalf("被刀且不能自救时使用解药 error = %v, want ErrWitchCannotSelfSave", err)
	}
	after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("d2", 4), Use: false})
	if err != nil {
		t.Fatalf("被刀且不能自救时选择不使用解药 error = %v", err)
	}
	poisonStage, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("d3", 4)})
	if err != nil {
		t.Fatalf("确认不使用解药 error = %v", err)
	}
	if poisonStage.Night.WitchStage != WitchStagePoison {
		t.Fatalf("WitchStage = %d, want %d", poisonStage.Night.WitchStage, WitchStagePoison)
	}

	if _, _, err := NewReducer().Reduce(poisonStage, WitchPoisonCommand{Meta: witchMeta("d4", 4), Target: seatPtr(3)}); !errors.Is(err, ErrWitchPoisonUnavailable) {
		t.Errorf("被刀且不能自救时选择毒人 error = %v, want ErrWitchPoisonUnavailable", err)
	}
	skip, _, err := NewReducer().Reduce(poisonStage, WitchPoisonCommand{Meta: witchMeta("d5", 4), Target: nil})
	if err != nil {
		t.Fatalf("被刀且不能自救时选择不使用毒药 error = %v", err)
	}
	final, _, err := NewReducer().Reduce(skip, WitchConfirmCommand{Meta: witchMeta("d6", 4)})
	if err != nil {
		t.Fatalf("确认不使用毒药 error = %v", err)
	}
	if final.Night.WitchPoisonUsed || final.Night.WitchStage != WitchStageClosed {
		t.Errorf("被刀且不能自救时的结束状态 = %+v, want 不用毒且阶段结束", final.Night)
	}
}

// TestWitchNoKillTargetCannotSave 验证平安夜：无刀口时使用解药被拒，
// 不使用解药可进入毒药窗口（毒人不受平安夜限制）。
func TestWitchNoKillTargetCannotSave(t *testing.T) {
	st := witchNightFixture()
	st.Night.WolfKillTarget = nil

	if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("e1", 4), Use: true}); !errors.Is(err, ErrWitchNothingToSave) {
		t.Errorf("平安夜选择使用解药 error = %v, want ErrWitchNothingToSave", err)
	}
	after, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("e2", 4), Use: false})
	if err != nil {
		t.Fatalf("平安夜选择不使用解药 error = %v", err)
	}
	final, _, err := NewReducer().Reduce(after, WitchConfirmCommand{Meta: witchMeta("e3", 4)})
	if err != nil {
		t.Fatalf("平安夜确认 error = %v", err)
	}
	if final.Night.WitchStage != WitchStagePoison {
		t.Errorf("WitchStage = %d, want %d（平安夜仍可进入毒药窗口）", final.Night.WitchStage, WitchStagePoison)
	}
}

// TestWitchTimeoutNoPotion 验证超时默认：女巫超时不使用任何药
// （不用解药、不用毒药），窗口关闭并收到 witch.none 提示。
func TestWitchTimeoutNoPotion(t *testing.T) {
	st := witchNightFixture()

	after, effects, err := NewReducer().Reduce(st, TimeoutCommand{Meta: witchMeta("t1", 0)})
	if err != nil {
		t.Fatalf("女巫超时 error = %v, want nil", err)
	}
	if after.Night.WitchSaveUsed || after.Night.WitchPoisonUsed || after.Night.WitchUsedTonight {
		t.Errorf("女巫超时不应使用任何药：%+v", after.Night)
	}
	if after.Night.WitchStage != WitchStageClosed {
		t.Errorf("WitchStage = %d, want 0（超时后窗口关闭）", after.Night.WitchStage)
	}
	got := countWitchEffects(effects)
	if got[WitchNoneMessageKey] != 1 {
		t.Errorf("女巫超时效果 = %v, want witch.none", got)
	}
}

// TestWitchNonWitchRejected 验证只有女巫本人可使用女巫技能。
func TestWitchNonWitchRejected(t *testing.T) {
	st := witchNightFixture()

	if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("nw1", 5), Use: true}); !errors.Is(err, ErrNotWitch) {
		t.Errorf("非女巫选择用药 error = %v, want ErrNotWitch", err)
	}
	if _, _, err := NewReducer().Reduce(st, WitchConfirmCommand{Meta: witchMeta("nw2", 5)}); !errors.Is(err, ErrNotWitch) {
		t.Errorf("非女巫确认 error = %v, want ErrNotWitch", err)
	}
}

// TestWitchWindowClosedRejected 验证窗口未开启（WitchStage=0）时所有
// 女巫命令与超时均以 ErrWitchActionClosed 拒绝。
func TestWitchWindowClosedRejected(t *testing.T) {
	st := witchNightFixture()
	st.Night.WitchStage = WitchStageClosed

	if _, _, err := NewReducer().Reduce(st, WitchSaveCommand{Meta: witchMeta("cl1", 4), Use: false}); !errors.Is(err, ErrWitchActionClosed) {
		t.Errorf("窗口关闭时解药选择 error = %v, want ErrWitchActionClosed", err)
	}
	if _, _, err := NewReducer().Reduce(st, WitchPoisonCommand{Meta: witchMeta("cl2", 4), Target: nil}); !errors.Is(err, ErrWitchActionClosed) {
		t.Errorf("窗口关闭时毒药选择 error = %v, want ErrWitchActionClosed", err)
	}
	if _, _, err := NewReducer().Reduce(st, WitchConfirmCommand{Meta: witchMeta("cl3", 4)}); !errors.Is(err, ErrWitchActionClosed) {
		t.Errorf("窗口关闭时确认 error = %v, want ErrWitchActionClosed", err)
	}
	// 窗口关闭（狼人轮与女巫窗口均未开启）时 Reduce 的 Timeout 保持既有
	// 未实现语义（reducer_test 契约：ErrNotImplemented），不修改既有行为；
	// 女巫 handler 层直接校验关闭守卫返回 ErrWitchActionClosed。
	if _, _, err := NewReducer().Reduce(st, TimeoutCommand{Meta: witchMeta("cl4", 0)}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("窗口关闭时 Reduce(Timeout) error = %v, want ErrNotImplemented（既有未实现契约）", err)
	}
	if _, _, err := NewReducer().(reducer).witchTimeout(st, TimeoutCommand{Meta: witchMeta("cl5", 0)}); !errors.Is(err, ErrWitchActionClosed) {
		t.Errorf("witchTimeout 窗口关闭守卫 error = %v, want ErrWitchActionClosed", err)
	}
}

// TestWitchSensitiveMessagesNeverPublic 验证 witch.* 私密前缀已加入
// 敏感白名单：任何 witch.* 消息都不能以 AudiencePublic 受众构造。
func TestWitchSensitiveMessagesNeverPublic(t *testing.T) {
	for _, key := range []string{
		WitchKillRevealMessageKey,
		WitchSavePromptMessageKey,
		WitchPoisonPromptMessageKey,
		WitchSaveLockedMessageKey,
		WitchPoisonLockedMessageKey,
		WitchNoneMessageKey,
	} {
		if _, err := NewMessageEffect(AudiencePublic, key, nil); err == nil {
			t.Errorf("NewMessageEffect(Public, %q) error = nil, want 拒绝", key)
		}
		if _, err := NewMessageEffect(AudienceActor, key, nil); err != nil {
			t.Errorf("NewMessageEffect(Actor, %q) error = %v, want nil", key, err)
		}
	}
}
