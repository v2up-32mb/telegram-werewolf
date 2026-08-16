package telegram

import (
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// TestWitchKillRevealViewRendersTarget 验证刀口告知视图：有目标渲染目标
// 座位「N号」，无目标（平安夜）渲染平安夜文案。
func TestWitchKillRevealViewRendersTarget(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewWitchKillRevealView(r, game.Seat(5))
	if err != nil {
		t.Fatalf("NewWitchKillRevealView(有目标) error = %v", err)
	}
	if !strings.Contains(got, "今晚狼人选择的目标") || !strings.Contains(got, "5号") {
		t.Errorf("刀口告知视图 = %q, want 含目标与「5号」", got)
	}

	got, err = NewWitchKillRevealView(r, 0)
	if err != nil {
		t.Fatalf("NewWitchKillRevealView(平安夜) error = %v", err)
	}
	if !strings.Contains(got, "平安夜") {
		t.Errorf("平安夜告知视图 = %q, want 含「平安夜」", got)
	}
}

// TestWitchSaveViewRendersPromptAndPotions 验证解药窗口视图：含使用解药
// 提示与解药/毒药可用状态（可用/已用）。
func TestWitchSaveViewRendersPromptAndPotions(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewWitchSaveView(r, false, false)
	if err != nil {
		t.Fatalf("NewWitchSaveView error = %v", err)
	}
	if !strings.Contains(got, "是否使用解药") {
		t.Errorf("解药视图缺少使用解药提示：%q", got)
	}
	if !strings.Contains(got, "解药：可用") || !strings.Contains(got, "毒药：可用") {
		t.Errorf("解药视图缺少药品可用状态：%q", got)
	}

	got, err = NewWitchSaveView(r, true, true)
	if err != nil {
		t.Fatalf("NewWitchSaveView(已用) error = %v", err)
	}
	if !strings.Contains(got, "解药：已用") || !strings.Contains(got, "毒药：已用") {
		t.Errorf("解药视图已用状态 = %q, want 解药/毒药均为已用", got)
	}
}

// TestWitchPoisonViewRendersTargets 验证毒药窗口视图：含目标座位列表与
// 「不使用毒药」选项。
func TestWitchPoisonViewRendersTargets(t *testing.T) {
	r := newRoleTestRenderer(t)
	targets := []game.Seat{1, 2, 3, 5, 6}

	got, err := NewWitchPoisonView(r, targets, false, false)
	if err != nil {
		t.Fatalf("NewWitchPoisonView error = %v", err)
	}
	if !strings.Contains(got, "毒药") {
		t.Errorf("毒药视图缺少毒药提示：%q", got)
	}
	if !strings.Contains(got, "1号") || !strings.Contains(got, "6号") {
		t.Errorf("毒药视图缺少目标座位列表：%q", got)
	}
	if !strings.Contains(got, "不使用毒药") {
		t.Errorf("毒药视图缺少「不使用毒药」选项：%q", got)
	}
	// 毒药目标必须为存活玩家：4 号（女巫自己）未列入 targets 时不应出现。
	if strings.Contains(got, "4号") {
		t.Errorf("毒药视图不应包含未传入的目标 4 号：%q", got)
	}
}

// TestWitchSaveConfirmViewRendersLocked 验证解药确认文案：使用/不使用
// 解药两种确认反馈。
func TestWitchSaveConfirmViewRendersLocked(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewWitchSaveConfirmView(r, true)
	if err != nil {
		t.Fatalf("NewWitchSaveConfirmView(使用) error = %v", err)
	}
	if !strings.Contains(got, "已确认") || !strings.Contains(got, "使用解药") {
		t.Errorf("使用解药确认文案 = %q, want 含「已确认使用解药」", got)
	}

	got, err = NewWitchSaveConfirmView(r, false)
	if err != nil {
		t.Fatalf("NewWitchSaveConfirmView(不使用) error = %v", err)
	}
	if !strings.Contains(got, "不使用解药") {
		t.Errorf("不使用解药确认文案 = %q, want 含「不使用解药」", got)
	}
}

// TestWitchPoisonConfirmViewRendersLocked 验证毒药确认文案：有目标显示
// 毒杀目标，无目标显示「不使用毒药」。
func TestWitchPoisonConfirmViewRendersLocked(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewWitchPoisonConfirmView(r, game.Seat(3))
	if err != nil {
		t.Fatalf("NewWitchPoisonConfirmView(毒人) error = %v", err)
	}
	if !strings.Contains(got, "已确认") || !strings.Contains(got, "3号") {
		t.Errorf("毒人确认文案 = %q, want 含「已确认」与「3号」", got)
	}

	got, err = NewWitchPoisonConfirmView(r, 0)
	if err != nil {
		t.Fatalf("NewWitchPoisonConfirmView(不使用) error = %v", err)
	}
	if !strings.Contains(got, "不使用毒药") {
		t.Errorf("不使用毒药确认文案 = %q, want 含「不使用毒药」", got)
	}
}

// TestWitchViewsRejectMissingKey 验证渲染缺失 key 时显式报错不 panic。
func TestWitchViewsRejectMissingKey(t *testing.T) {
	r := newRoleTestRenderer(t)

	_, err := r.Render("witch.missing_key", nil)
	if err == nil {
		t.Fatal("Render(缺失 key) error = nil, want 显式错误")
	}

	views := []struct {
		name string
		fn   func() (string, error)
	}{
		{"kill_reveal", func() (string, error) { return NewWitchKillRevealView(r, game.Seat(5)) }},
		{"save", func() (string, error) { return NewWitchSaveView(r, false, false) }},
		{"poison", func() (string, error) { return NewWitchPoisonView(r, []game.Seat{1, 2, 3, 5, 6}, false, false) }},
		{"save_confirm", func() (string, error) { return NewWitchSaveConfirmView(r, true) }},
		{"poison_confirm", func() (string, error) { return NewWitchPoisonConfirmView(r, game.Seat(3)) }},
	}
	for _, v := range views {
		got, err := v.fn()
		if err != nil {
			t.Fatalf("%s 视图渲染 error = %v, want nil", v.name, err)
		}
		if got == "" {
			t.Errorf("%s 视图为空", v.name)
		}
	}
}
