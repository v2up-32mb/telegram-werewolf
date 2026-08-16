package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// voteDeadline 是测试用固定截止时刻（UTC+8）。
func voteDeadline() time.Time {
	return time.Date(2026, 8, 16, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
}

// TestVotePromptViewRendersTargetsAndDeadline 验证投票提示视图：复用
// action.vote.prompt、列出候选与弃权选项、显示 UTC+8 截止时刻
// （docs 阶段消息设计.md §8.4、§3.3 时间文案）。
func TestVotePromptViewRendersTargetsAndDeadline(t *testing.T) {
	r := newRoleTestRenderer(t)
	got, err := NewVotePromptView(r, []game.Seat{1, 2, 3, 4, 5, 6}, voteDeadline())
	if err != nil {
		t.Fatalf("NewVotePromptView error = %v", err)
	}
	if !strings.Contains(got, "请投票给要放逐的玩家") {
		t.Errorf("投票提示缺少选区提示：%q", got)
	}
	if !strings.Contains(got, "1号") || !strings.Contains(got, "6号") {
		t.Errorf("投票提示缺少候选座位：%q", got)
	}
	if !strings.Contains(got, "弃权") {
		t.Errorf("投票提示缺少弃权选项：%q", got)
	}
	if !strings.Contains(got, "确认投票") {
		t.Errorf("投票提示缺少确认按钮文案：%q", got)
	}
	if !strings.Contains(got, "2026-08-16 12:00:00") {
		t.Errorf("投票提示缺少 UTC+8 截止时刻：%q", got)
	}
}

// TestVoteLockedViewRendersTargetAndAbstain 验证已锁定视图：有目标显示
// 目标座位，弃权（Target=nil）显示弃权。
func TestVoteLockedViewRendersTargetAndAbstain(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewVoteLockedView(r, seatPtr(3))
	if err != nil {
		t.Fatalf("NewVoteLockedView(3号) error = %v", err)
	}
	if !strings.Contains(got, "已确认") || !strings.Contains(got, "3号") {
		t.Errorf("锁定文案 = %q, want 含「已确认」与「3号」", got)
	}

	got, err = NewVoteLockedView(r, nil)
	if err != nil {
		t.Fatalf("NewVoteLockedView(弃权) error = %v", err)
	}
	if !strings.Contains(got, "弃权") {
		t.Errorf("弃权锁定文案 = %q, want 含「弃权」", got)
	}
}

// TestVoteDetailViewSortsByVoterSeat 验证逐人明细：按投票人座位升序，
// 弃权（Ballots=0）显示弃权；「谁投了谁」公开公布（docs §投票 1）。
func TestVoteDetailViewSortsByVoterSeat(t *testing.T) {
	r := newRoleTestRenderer(t)
	ballots := map[game.Seat]game.Seat{3: 1, 1: 0, 2: 4, 4: 2}
	got, err := NewVoteDetailView(r, ballots)
	if err != nil {
		t.Fatalf("NewVoteDetailView error = %v", err)
	}
	lines := []string{"1号 → 弃权", "2号 → 4号", "3号 → 1号", "4号 → 2号"}
	prev := -1
	for _, line := range lines {
		idx := strings.Index(got, line)
		if idx < 0 {
			t.Errorf("明细缺少行 %q：%q", line, got)
			continue
		}
		if idx < prev {
			t.Errorf("明细行序错误：%q 出现在 %q 之前", line, got)
		}
		prev = idx
	}
}

// TestVoteTallyViewRendersCounts 验证票数统计：逐候选票数与弃权数。
func TestVoteTallyViewRendersCounts(t *testing.T) {
	r := newRoleTestRenderer(t)
	counts := map[game.Seat]int{1: 2, 4: 1}
	got, err := NewVoteTallyView(r, counts, 3)
	if err != nil {
		t.Fatalf("NewVoteTallyView error = %v", err)
	}
	if !strings.Contains(got, "1号：2 票") || !strings.Contains(got, "4号：1 票") {
		t.Errorf("票数统计缺少候选票数：%q", got)
	}
	if !strings.Contains(got, "弃权：3 票") {
		t.Errorf("票数统计缺少弃权数：%q", got)
	}
}

// TestVoteResultViewExiledAndTie 验证放逐结果：唯一最高票显示被放逐座位，
// 平票（Exiled=nil）显示平票文案。
func TestVoteResultViewExiledAndTie(t *testing.T) {
	r := newRoleTestRenderer(t)

	got, err := NewVoteResultView(r, seatPtr(2))
	if err != nil {
		t.Fatalf("NewVoteResultView(2号) error = %v", err)
	}
	if !strings.Contains(got, "2号") || !strings.Contains(got, "放逐") {
		t.Errorf("放逐结果 = %q, want 含「2号」与「放逐」", got)
	}

	got, err = NewVoteResultView(r, nil)
	if err != nil {
		t.Fatalf("NewVoteResultView(平票) error = %v", err)
	}
	if !strings.Contains(got, "平票") {
		t.Errorf("平票结果 = %q, want 含「平票」", got)
	}
}

// TestLastWordsViews 验证遗言提示（30 秒）与遗言转播（docs §结算 4、
// §死亡玩家 4：遗言正常转播）。
func TestLastWordsViews(t *testing.T) {
	r := newRoleTestRenderer(t)

	prompt, err := NewLastWordsPromptView(r, game.Seat(2), voteDeadline())
	if err != nil {
		t.Fatalf("NewLastWordsPromptView error = %v", err)
	}
	if !strings.Contains(prompt, "遗言") || !strings.Contains(prompt, "2号") ||
		!strings.Contains(prompt, "30 秒") || !strings.Contains(prompt, "2026-08-16 12:00:00") {
		t.Errorf("遗言提示 = %q, want 含遗言/座位/30 秒/截止时刻", prompt)
	}

	published, err := NewLastWordsView(r, game.Seat(2), "我怀疑 3 号")
	if err != nil {
		t.Fatalf("NewLastWordsView error = %v", err)
	}
	if !strings.Contains(published, "2号") || !strings.Contains(published, "我怀疑 3 号") {
		t.Errorf("遗言转播 = %q, want 含座位与正文", published)
	}
}

// TestVoteViewsRejectNilRenderer 验证 nil renderer 显式报错。
func TestVoteViewsRejectNilRenderer(t *testing.T) {
	if _, err := NewVotePromptView(nil, []game.Seat{1}, voteDeadline()); err == nil {
		t.Errorf("NewVotePromptView(nil) 应报错")
	}
	if _, err := NewVoteLockedView(nil, seatPtr(1)); err == nil {
		t.Errorf("NewVoteLockedView(nil) 应报错")
	}
	if _, err := NewVoteDetailView(nil, map[game.Seat]game.Seat{1: 2}); err == nil {
		t.Errorf("NewVoteDetailView(nil) 应报错")
	}
	if _, err := NewVoteTallyView(nil, map[game.Seat]int{1: 2}, 0); err == nil {
		t.Errorf("NewVoteTallyView(nil) 应报错")
	}
	if _, err := NewVoteResultView(nil, seatPtr(1)); err == nil {
		t.Errorf("NewVoteResultView(nil) 应报错")
	}
	if _, err := NewLastWordsPromptView(nil, 1, voteDeadline()); err == nil {
		t.Errorf("NewLastWordsPromptView(nil) 应报错")
	}
	if _, err := NewLastWordsView(nil, 1, "遗言"); err == nil {
		t.Errorf("NewLastWordsView(nil) 应报错")
	}
}
