package app

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// memCursorStoreApp 是测试用内存 cursor（Router.DispatchAction ACK 记录）。
type memCursorStoreApp struct {
	mu  sync.Mutex
	hid int64
}

func (s *memCursorStoreApp) Load(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hid, nil
}

func (s *memCursorStoreApp) Save(_ context.Context, updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hid = updateID
	return nil
}

// TestWiringIssueButtonRoundTrip 验证 B1-c：Wiring 与 Router 共用同一
// CallbackManager——导演经 IssueButton 下发的按钮 token 能被 Router 的
// DispatchAction 校验并还原 owner/action/target/阶段/版本。
func TestWiringIssueButtonRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 8)
	defer func() { _ = sched.Close(ctx) }()

	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if w.Tokens() == nil {
		t.Fatal("Attach 后 Tokens() 为 nil，want 共享回调注册表")
	}

	tok, err := w.IssueButton(2001, "wolf_vote", "3", game.PhaseNight, 7)
	if err != nil {
		t.Fatalf("IssueButton: %v", err)
	}
	if tok == "" {
		t.Fatal("IssueButton 返回空 token")
	}

	r := telegram.NewRouter(telegram.NewDeduper(16), &memCursorStoreApp{}, w.Tokens())
	u := telegram.Update{
		UpdateID: 909, ReceivedAt: time.Unix(1700000000, 0),
		CallbackQuery: &telegram.IncomingCallbackQuery{ID: "cq", UserID: 2001, ChatID: 2001, MessageID: 3, Data: tok},
	}
	var got *telegram.CallbackAction
	if err := r.DispatchAction(ctx, u, func(ctx context.Context, act telegram.CallbackAction) error {
		got = &act
		return nil
	}); err != nil {
		t.Fatalf("DispatchAction: %v", err)
	}
	if got == nil {
		t.Fatal("Router 未收到导演下发的按钮动作")
	}
	if got.UpdateID != 909 || got.Owner != 2001 || got.Action != "wolf_vote" || got.Target != "3" ||
		got.ExpectedPhase != game.PhaseNight || got.PhaseVersion != 7 {
		t.Fatalf("CallbackAction = %+v, want wolf_vote/3/night/7", *got)
	}
	// 越权点击（owner 不符）被拒。
	r2 := telegram.NewRouter(telegram.NewDeduper(16), &memCursorStoreApp{}, w.Tokens())
	bad := telegram.Update{
		UpdateID: 910, ReceivedAt: time.Unix(1700000000, 0),
		CallbackQuery: &telegram.IncomingCallbackQuery{ID: "cq2", UserID: 9999, ChatID: 9999, MessageID: 3, Data: tok},
	}
	if err := r2.DispatchAction(ctx, bad, func(ctx context.Context, act telegram.CallbackAction) error {
		t.Fatal("越权点击不应被受理")
		return nil
	}); err != nil {
		t.Fatalf("越权 DispatchAction: %v", err)
	}
}

// TestCallbackActionAlwaysAnswered 是 B3 红测：每次回调动作都必须应答
// answerCallbackQuery（docs 阶段消息设计.md §9：每次 Inline Keyboard 点击
// 都必须回应 Callback Query；顶部通知 show_alert=false）。
func TestCallbackActionAlwaysAnswered(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	rec := newRecordingSender(16)
	sched := outbox.NewScheduler(rec.Send, 16)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// 无房玩家点击「投票」：命令被拒（无房）仍必须 answer 回调。
	act := telegram.CallbackAction{
		UpdateID: 7, Owner: 5001, Action: "vote", Target: "2",
		ExpectedPhase: game.PhaseDayVote, PhaseVersion: 3, ReceivedAt: time.Now(),
		CallbackQueryID: "cq-abc",
	}
	if err := w.ActionHandler().Handle(ctx, act); err != nil {
		t.Fatalf("ActionHandler.Handle: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var gotAnswer bool
	for time.Now().Before(deadline) {
		select {
		case m := <-rec.ch:
			if m.Operation == telegram.OpAnswerCallback {
				p := m.Payload.(telegram.Params)
				if p.CallbackQueryID == "cq-abc" {
					gotAnswer = true
				}
			}
		case <-time.After(5 * time.Millisecond):
		}
		if gotAnswer {
			break
		}
	}
	if !gotAnswer {
		t.Fatal("回调动作未被应答：缺少 answerCallbackQuery 出站消息")
	}
}
