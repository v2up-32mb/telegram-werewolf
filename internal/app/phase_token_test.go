package app

// S3 红测：阶段切换时旧阶段回调 token 整体失效（docs/技术选型.md §7.3），
// 限制令牌表容量占用并杜绝旧阶段按钮生效。

import (
	"context"
	"errors"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

func TestDirectorInvalidatesPhaseTokens(t *testing.T) {
	w, _, sched := newWiringSched(t, 8)
	defer func() { _ = sched.Close(context.Background()) }()
	d := newDirector(w)

	// 第 1 夜：prevPhase 置为 Night，下发该阶段投票 token。
	stNight := game.State{RoomID: "TK01", Phase: game.PhaseNight, PhaseVersion: 3}
	d.syncPhaseTokens("TK01", stNight)
	tok, err := w.IssueButton(100, "vote", "3", game.PhaseNight, 3)
	if err != nil {
		t.Fatalf("IssueButton: %v", err)
	}
	if _, err := w.tokens.Validate(tok, 100); err != nil {
		t.Fatalf("切换前 token 应有效: %v", err)
	}

	// 切到白天：旧阶段（Night）token 整体失效。
	stDay := game.State{RoomID: "TK01", Phase: game.PhaseDaySpeech, PhaseVersion: 4}
	d.syncPhaseTokens("TK01", stDay)
	if _, err := w.tokens.Validate(tok, 100); !errors.Is(err, telegram.ErrTokenNotFound) {
		t.Fatalf("旧阶段 token 未失效: err = %v, want ErrTokenNotFound", err)
	}

	// 同阶段内的 token 保持有效（不误杀）。
	tokDay, err := w.IssueButton(100, "vote", "2", game.PhaseDaySpeech, 4)
	if err != nil {
		t.Fatalf("IssueButton(day): %v", err)
	}
	d.syncPhaseTokens("TK01", stDay) // 阶段未变 → 不失效
	if _, err := w.tokens.Validate(tokDay, 100); err != nil {
		t.Fatalf("同阶段 token 被误杀: %v", err)
	}
}
