package app

// P1 红测：SendTemporary 必须经过 Outbox 限流器（与常规消息共享同一
// 令牌桶），不得绕过限流直发——否则错误风暴时绕过 Telegram flood 保护。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// countingClient 统计 SendMessage 直调次数。
type countingClient struct {
	sent int
}

func (c *countingClient) GetMe(context.Context) (*telegram.Me, error) {
	return &telegram.Me{ID: 1}, nil
}
func (c *countingClient) SendMessage(context.Context, telegram.SendMessageParams) (*telegram.SentMessage, error) {
	c.sent++
	return &telegram.SentMessage{MessageID: 100 + c.sent}, nil
}
func (c *countingClient) EditMessageText(context.Context, telegram.EditMessageParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{}, nil
}
func (c *countingClient) DeleteMessage(context.Context, telegram.DeleteMessageParams) error {
	return nil
}
func (c *countingClient) SendPhoto(context.Context, telegram.SendPhotoParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{}, nil
}
func (c *countingClient) UploadPhoto(context.Context, telegram.UploadPhotoParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{}, nil
}
func (c *countingClient) AnswerCallbackQuery(context.Context, telegram.AnswerCallbackParams) error {
	return nil
}
func (c *countingClient) SetMyCommands(context.Context, []telegram.BotCommand) error { return nil }

// TestSendTemporaryRespectsLimiter：限流器 per-chat 速率极低时，
// 连续 SendTemporary 应被限速拖慢（耗时 >= 令牌等待），证明走了限流器。
func TestSendTemporaryRespectsLimiter(t *testing.T) {
	w, _, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	// 注入极低速率限流器（每 chat 1 token/s、burst 1）+ 计数 client。
	w.limiter = outbox.NewLimiter(1000, 1, 1)
	cc := &countingClient{}
	w.opts.client = cc

	start := time.Now()
	const n = 3
	for i := 0; i < n; i++ {
		if err := w.replySenderForTest().SendTemporary(context.Background(), 7001, "boom", time.Second); err != nil {
			t.Fatalf("SendTemporary %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)

	// burst=1 + 1/s：第 2、3 条各需等约 1s → 总耗时应明显 > 1.5s。
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("SendTemporary x%d took %v, want >=1.5s (limiter not applied?)", n, elapsed)
	}
	if cc.sent != n {
		t.Fatalf("client sent = %d, want %d", cc.sent, n)
	}
}

// TestSendTemporaryFailsWhenLimiterCancelled：限流器 Wait 被取消时应返回
// 错误而不是绕过限流直发。
func TestSendTemporaryFailsWhenLimiterCancelled(t *testing.T) {
	w, _, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	w.limiter = outbox.NewLimiter(0.0001, 0.0001, 1) // 几乎无令牌
	cc := &countingClient{}
	w.opts.client = cc

	// 先耗掉 burst=1 的初始令牌，再以短超时等待第二个令牌（约 10000s）。
	if err := w.limiter.Wait(context.Background(), 7002); err != nil {
		t.Fatalf("prime limiter: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := w.replySenderForTest().SendTemporary(ctx, 7002, "boom", time.Second)
	if err == nil {
		t.Fatal("SendTemporary with cancelled limiter wait: want error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && cc.sent > 0 {
		t.Fatalf("message sent despite limiter wait failure")
	}
}
