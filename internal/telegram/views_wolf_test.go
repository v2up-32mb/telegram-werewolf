package telegram

import (
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// TestWolfVoteViewRendersPromptAndTargets 验证狼人投票视图：复用
// action.wolf.kill.prompt、含轮次与目标座位列表（狼队友带标记）。
func TestWolfVoteViewRendersPromptAndTargets(t *testing.T) {
	r := newRoleTestRenderer(t)
	targets := []game.Seat{1, 2, 3, 4, 5, 6}
	got, err := NewWolfVoteView(r, 1, targets, []game.Seat{2})
	if err != nil {
		t.Fatalf("NewWolfVoteView error = %v, want nil", err)
	}
	if !strings.Contains(got, "请选择要击杀的玩家") {
		t.Errorf("投票视图缺少击杀提示：%q", got)
	}
	if !strings.Contains(got, "第 1 轮") {
		t.Errorf("投票视图缺少轮次：%q", got)
	}
	if !strings.Contains(got, "1号") || !strings.Contains(got, "6号") {
		t.Errorf("投票视图缺少目标座位列表：%q", got)
	}
	if !strings.Contains(got, "2号") {
		t.Errorf("投票视图缺少狼队友座位：%q", got)
	}
}

// TestWolfDiscussViewRendersTitle 验证狼人讨论视图标题含轮次与狼队友名单。
func TestWolfDiscussViewRendersTitle(t *testing.T) {
	r := newRoleTestRenderer(t)
	got, err := NewWolfDiscussView(r, 2, []game.Seat{2})
	if err != nil {
		t.Fatalf("NewWolfDiscussView error = %v, want nil", err)
	}
	if !strings.Contains(got, "狼人讨论") || !strings.Contains(got, "第 2 轮") {
		t.Errorf("讨论标题 = %q, want 含「狼人讨论」与轮次", got)
	}
	if !strings.Contains(got, "狼队友") || !strings.Contains(got, "2号") {
		t.Errorf("讨论标题缺少狼队友名单：%q", got)
	}
}

// TestWolfConfirmViewRendersLocked 验证确认锁定文案：有目标显示击杀目标，
// 空刀（Target=nil）显示空刀；参数经 MarkdownV2 转义。
func TestWolfConfirmViewRendersLocked(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewWolfConfirmView(r, game.Seat(1), seatPtr(3))
	if err != nil {
		t.Fatalf("NewWolfConfirmView(击杀) error = %v", err)
	}
	if !strings.Contains(got, "已确认") || !strings.Contains(got, "3号") {
		t.Errorf("确认锁定文案 = %q, want 含「已确认」与目标「3号」", got)
	}

	got, err = NewWolfConfirmView(r, game.Seat(1), nil)
	if err != nil {
		t.Fatalf("NewWolfConfirmView(空刀) error = %v", err)
	}
	if !strings.Contains(got, "空刀") {
		t.Errorf("空刀确认文案 = %q, want 含「空刀」", got)
	}
}

// seatPtr 返回指向目标座位的指针（辅助）。
func seatPtr(seat game.Seat) *game.Seat { return &seat }

// TestWolfViewsRejectMissingKey 验证渲染缺失 key 时显式报错不 panic。
func TestWolfViewsRejectMissingKey(t *testing.T) {
	r := newRoleTestRenderer(t)

	// 伪造一个不含 wolf.* 新 key 的渲染键：直接调用底层 Render 模拟
	// 缺失 key 场景（NewWolfVoteView 依赖既有 action/button 文案齐全，
	// 缺失 key 的显式报错由 i18n.Render 保证，此处验证不 panic）。
	_, err := r.Render("wolf.missing_key", nil)
	if err == nil {
		t.Fatal("Render(缺失 key) error = nil, want 显式错误")
	}

	got, err := NewWolfVoteView(r, 1, []game.Seat{1, 2}, nil)
	if err != nil {
		t.Fatalf("NewWolfVoteView error = %v", err)
	}
	if got == "" {
		t.Error("投票视图为空")
	}
}
