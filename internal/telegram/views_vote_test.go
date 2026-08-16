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

// TestVoteResultViewExiledAndNone 验证放逐结果：唯一最高票显示被放逐
// 座位；无人被放逐（全员弃权，Exiled=nil）显示「无人被放逐」——真实
// 平票已走平票流程，不再输出 nil 结果（docs 游戏流程设计.md §投票 4）。
func TestVoteResultViewExiledAndNone(t *testing.T) {
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
		t.Fatalf("NewVoteResultView(无人被放逐) error = %v", err)
	}
	if !strings.Contains(got, "无人被放逐") {
		t.Errorf("无放逐结果 = %q, want 含「无人被放逐」", got)
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

// TestTieViews 验证平票流程视图：加时发言公告/发言提示、缩圈公告与提示、
// 无发言轮公告、最终对决公告与对决提示、被排除投票人通知（docs §投票 4）。
func TestTieViews(t *testing.T) {
	r := newRoleTestRenderer(t)
	candidates := []game.Seat{1, 4}

	speech, err := NewTieSpeechView(r, candidates)
	if err != nil {
		t.Fatalf("NewTieSpeechView error = %v", err)
	}
	if !strings.Contains(speech, "平票") || !strings.Contains(speech, "1号") || !strings.Contains(speech, "4号") {
		t.Errorf("加时发言公告 = %q, want 含平票与候选", speech)
	}

	turn, err := NewTieSpeechTurnView(r, game.Seat(1), voteDeadline())
	if err != nil {
		t.Fatalf("NewTieSpeechTurnView error = %v", err)
	}
	if !strings.Contains(turn, "1号") || !strings.Contains(turn, "2026-08-16 12:00:00") {
		t.Errorf("加时发言提示 = %q, want 含座位与截止时刻", turn)
	}

	runoff, err := NewTieRunoffView(r, candidates)
	if err != nil {
		t.Fatalf("NewTieRunoffView error = %v", err)
	}
	if !strings.Contains(runoff, "第 2 次投票") || !strings.Contains(runoff, "1号") {
		t.Errorf("缩圈公告 = %q, want 含第 2 次投票与候选", runoff)
	}

	noSpeech, err := NewTieNoSpeechView(r, 2, candidates)
	if err != nil {
		t.Fatalf("NewTieNoSpeechView error = %v", err)
	}
	if !strings.Contains(noSpeech, "第 2 轮") || !strings.Contains(noSpeech, "无发言投票") {
		t.Errorf("无发言轮公告 = %q, want 含轮次与无发言投票", noSpeech)
	}

	final, err := NewTieFinalView(r, candidates)
	if err != nil {
		t.Fatalf("NewTieFinalView error = %v", err)
	}
	if !strings.Contains(final, "最终对决") || !strings.Contains(final, "禁止弃权") {
		t.Errorf("最终对决公告 = %q, want 含最终对决与禁止弃权", final)
	}

	runoffPrompt, err := NewTieRunoffPromptView(r, game.Seat(2), candidates, voteDeadline())
	if err != nil {
		t.Fatalf("NewTieRunoffPromptView error = %v", err)
	}
	if !strings.Contains(runoffPrompt, "2号") || !strings.Contains(runoffPrompt, "确认") {
		t.Errorf("缩圈提示 = %q, want 含座位与确认提示", runoffPrompt)
	}

	duelPrompt, err := NewTieDuelPromptView(r, game.Seat(2), candidates, voteDeadline())
	if err != nil {
		t.Fatalf("NewTieDuelPromptView error = %v", err)
	}
	if !strings.Contains(duelPrompt, "禁止弃权") || !strings.Contains(duelPrompt, "1号") {
		t.Errorf("对决提示 = %q, want 含禁止弃权与候选", duelPrompt)
	}

	excluded, err := NewTieDuelExcludedView(r, game.Seat(3))
	if err != nil {
		t.Fatalf("NewTieDuelExcludedView error = %v", err)
	}
	if !strings.Contains(excluded, "3号") || !strings.Contains(excluded, "失去投票权") {
		t.Errorf("被排除通知 = %q, want 含座位与失去投票权", excluded)
	}
}

// TestTieViewsRejectNilRenderer 验证平票视图 nil renderer 显式报错。
func TestTieViewsRejectNilRenderer(t *testing.T) {
	candidates := []game.Seat{1, 4}
	if _, err := NewTieSpeechView(nil, candidates); err == nil {
		t.Errorf("NewTieSpeechView(nil) 应报错")
	}
	if _, err := NewTieSpeechTurnView(nil, 1, voteDeadline()); err == nil {
		t.Errorf("NewTieSpeechTurnView(nil) 应报错")
	}
	if _, err := NewTieRunoffView(nil, candidates); err == nil {
		t.Errorf("NewTieRunoffView(nil) 应报错")
	}
	if _, err := NewTieNoSpeechView(nil, 1, candidates); err == nil {
		t.Errorf("NewTieNoSpeechView(nil) 应报错")
	}
	if _, err := NewTieFinalView(nil, candidates); err == nil {
		t.Errorf("NewTieFinalView(nil) 应报错")
	}
	if _, err := NewTieRunoffPromptView(nil, 1, candidates, voteDeadline()); err == nil {
		t.Errorf("NewTieRunoffPromptView(nil) 应报错")
	}
	if _, err := NewTieDuelPromptView(nil, 1, candidates, voteDeadline()); err == nil {
		t.Errorf("NewTieDuelPromptView(nil) 应报错")
	}
	if _, err := NewTieDuelExcludedView(nil, 1); err == nil {
		t.Errorf("NewTieDuelExcludedView(nil) 应报错")
	}
}
