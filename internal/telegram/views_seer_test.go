package telegram

import (
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// TestSeerPromptViewRendersTargets 验证查验提示视图：含查验提示与候选
// 存活目标座位列表。
func TestSeerPromptViewRendersTargets(t *testing.T) {
	r := newRoleTestRenderer(t)
	targets := []game.Seat{1, 2, 4, 5, 6}

	got, err := NewSeerPromptView(r, targets)
	if err != nil {
		t.Fatalf("NewSeerPromptView error = %v", err)
	}
	if !strings.Contains(got, "请选择要查验的玩家") {
		t.Errorf("查验提示视图缺少查验提示：%q", got)
	}
	if !strings.Contains(got, "1号") || !strings.Contains(got, "6号") {
		t.Errorf("查验提示视图缺少候选目标座位列表：%q", got)
	}
	if strings.Contains(got, "3号") {
		t.Errorf("查验提示视图不应包含未传入的目标 3 号：%q", got)
	}
}

// TestSeerResultViewBinary 验证查验结果视图二分标记：狼人返回
// 「🐺 狼人」，好人返回「🟢 好人」，均含目标座位。
func TestSeerResultViewBinary(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewSeerResultView(r, game.Seat(2), game.CampWolf)
	if err != nil {
		t.Fatalf("NewSeerResultView(狼人) error = %v", err)
	}
	if !strings.Contains(got, "查验结果") || !strings.Contains(got, "2号") || !strings.Contains(got, "🐺 狼人") {
		t.Errorf("狼人结果视图 = %q, want 含「查验结果」「2号」「🐺 狼人」", got)
	}

	got, err = NewSeerResultView(r, game.Seat(5), game.CampGood)
	if err != nil {
		t.Fatalf("NewSeerResultView(好人) error = %v", err)
	}
	if !strings.Contains(got, "查验结果") || !strings.Contains(got, "5号") || !strings.Contains(got, "🟢 好人") {
		t.Errorf("好人结果视图 = %q, want 含「查验结果」「5号」「🟢 好人」", got)
	}
}

// TestSeerNoneViewRendersEmptyCheck 验证超时空验提示：不提示「已跳过」，
// 采用「已按默认选择处理」语义并说明本轮未查验。
func TestSeerNoneViewRendersEmptyCheck(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewSeerNoneView(r)
	if err != nil {
		t.Fatalf("NewSeerNoneView error = %v", err)
	}
	if !strings.Contains(got, "已按默认选择处理") || !strings.Contains(got, "未查验") {
		t.Errorf("超时空验提示 = %q, want 含「已按默认选择处理」与「未查验」", got)
	}
	if strings.Contains(got, "已跳过") {
		t.Errorf("超时空验提示不应出现「已跳过」：%q", got)
	}
}

// TestSeerViewsRejectMissingKey 验证渲染缺失 key 时显式报错不 panic。
func TestSeerViewsRejectMissingKey(t *testing.T) {
	r := newRoleTestRenderer(t)

	_, err := r.Render("seer.missing_key", nil)
	if err == nil {
		t.Fatal("Render(缺失 key) error = nil, want 显式错误")
	}

	views := []struct {
		name string
		fn   func() (string, error)
	}{
		{"prompt", func() (string, error) { return NewSeerPromptView(r, []game.Seat{1, 2, 4, 5, 6}) }},
		{"result", func() (string, error) { return NewSeerResultView(r, game.Seat(2), game.CampWolf) }},
		{"none", func() (string, error) { return NewSeerNoneView(r) }},
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
