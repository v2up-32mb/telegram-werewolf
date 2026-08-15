package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock 是注入式时钟：After 记录请求的延迟时长并返回共享触发通道，
// 由测试通过 fire 推进；重试等待因此完全不依赖真实时间。
type fakeClock struct {
	mu     sync.Mutex
	afters []time.Duration
	ch     chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{ch: make(chan time.Time, 16)} }

func (f *fakeClock) Now() time.Time { return time.Unix(0, 0).UTC() }

func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.afters = append(f.afters, d)
	f.mu.Unlock()
	return f.ch
}

func (f *fakeClock) fire(n int) {
	for i := 0; i < n; i++ {
		f.ch <- f.Now()
	}
}

func (f *fakeClock) delays() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.afters))
	copy(out, f.afters)
	return out
}

func (f *fakeClock) waitForDelays(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.delays()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d recorded delays, have %v", n, f.delays())
}

func TestRetry429UsesRetryAfter(t *testing.T) {
	clock := newFakeClock()
	rlErr := &RateLimitedError{RetryAfter: 5 * time.Second}
	calls := 0
	send := func(ctx context.Context, msg Message) error {
		calls++
		if calls == 1 {
			return rlErr
		}
		return nil
	}
	rs := NewRetryingSender(send, RetryPolicy{MaxRetries: 2}, clock)

	result := make(chan error, 1)
	go func() { result <- rs.Send(context.Background(), Message{CorrelationID: "m"}) }()

	clock.waitForDelays(t, 1)
	if got := clock.delays()[0]; got != 5*time.Second {
		t.Fatalf("After(delay) = %v, want 5s（429 必须按服务端 RetryAfter 延迟）", got)
	}
	clock.fire(1)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Send = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send 未在 fake clock 推进后返回")
	}
	if got := rs.RetriedCount(); got != 1 {
		t.Fatalf("RetriedCount = %d, want 1", got)
	}
	if got := rs.RateLimitedCount(); got != 1 {
		t.Fatalf("RateLimitedCount = %d, want 1", got)
	}
}

func TestRetryMaxRetries(t *testing.T) {
	clock := newFakeClock()
	rlErr := &RateLimitedError{RetryAfter: 100 * time.Millisecond}
	rs := NewRetryingSender(func(ctx context.Context, msg Message) error {
		return rlErr
	}, RetryPolicy{MaxRetries: 2}, clock)

	result := make(chan error, 1)
	go func() { result <- rs.Send(context.Background(), Message{CorrelationID: "m"}) }()

	clock.waitForDelays(t, 1)
	clock.fire(1) // 推进第 1 次重试
	clock.waitForDelays(t, 2)
	clock.fire(1) // 推进第 2 次重试（第 3 次尝试后达到 MaxRetries=2）
	select {
	case err := <-result:
		if !errors.Is(err, rlErr) {
			t.Fatalf("Send err = %v, want 最后一次 429 错误", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send 未在最大重试后返回")
	}
	if got := rs.RetriedCount(); got != 2 {
		t.Fatalf("RetriedCount = %d, want 2", got)
	}
}

func TestRetryPermanentErrorNoRetry(t *testing.T) {
	clock := newFakeClock()
	permErr := &PermanentError{Err: errors.New("user blocked")}
	rs := NewRetryingSender(func(ctx context.Context, msg Message) error {
		return permErr
	}, RetryPolicy{MaxRetries: 3}, clock)

	if err := rs.Send(context.Background(), Message{CorrelationID: "m"}); !errors.Is(err, permErr) {
		t.Fatalf("Send err = %v, want permanent error", err)
	}
	if got := rs.RetriedCount(); got != 0 {
		t.Fatalf("RetriedCount = %d, want 0（永久错误不重试）", got)
	}
	if got := len(clock.delays()); got != 0 {
		t.Fatalf("After called %d times, want 0（永久错误不等待）", got)
	}
}

func TestRetrySucceedsImmediately(t *testing.T) {
	clock := newFakeClock()
	rs := NewRetryingSender(func(ctx context.Context, msg Message) error { return nil }, RetryPolicy{MaxRetries: 5}, clock)
	if err := rs.Send(context.Background(), Message{CorrelationID: "m"}); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	if got := len(clock.delays()); got != 0 {
		t.Fatalf("After called %d times, want 0", got)
	}
	if got := rs.RetriedCount(); got != 0 {
		t.Fatalf("RetriedCount = %d, want 0", got)
	}
}

// TestRetryHeadOfLineBlocksSameChatOnly 验证组合语义：队头消息等待 429 重试期间，
// 同一 Chat 后续消息不得越过它发送，其他 Chat 仍可推进。
func TestRetryHeadOfLineBlocksSameChatOnly(t *testing.T) {
	clock := newFakeClock()
	a1First := true
	var attempts = make(chan string, 16)
	var delivered = make(chan string, 16)

	transport := func(msg Message) error {
		if msg.CorrelationID == "a1" && a1First {
			a1First = false
			return &RateLimitedError{RetryAfter: 5 * time.Second}
		}
		return nil
	}
	sendFn := func(ctx context.Context, msg Message) error {
		attempts <- msg.CorrelationID
		err := transport(msg)
		if err == nil {
			delivered <- msg.CorrelationID
		}
		return err
	}
	rs := NewRetryingSender(sendFn, RetryPolicy{MaxRetries: 1}, clock)
	s := NewScheduler(rs.Send, 8)

	if err := s.Enqueue(Message{CorrelationID: "a1", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue a1: %v", err)
	}
	select {
	case id := <-attempts:
		if id != "a1" {
			t.Fatalf("first attempt = %q, want a1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a1 未被调度尝试")
	}
	// a1 已进入 429 等待（clock 未推进），此后同 Chat 的 a2 不得越序尝试。
	if err := s.Enqueue(Message{CorrelationID: "a2", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue a2: %v", err)
	}
	select {
	case id := <-attempts:
		t.Fatalf("a2 在 a1 等待重试期间被尝试（%q），违反同 Chat 队头不越序", id)
	case <-time.After(100 * time.Millisecond):
	}
	// 其他 Chat 仍可推进。
	if err := s.Enqueue(Message{CorrelationID: "b1", ChatID: ChatID(2)}); err != nil {
		t.Fatalf("Enqueue b1: %v", err)
	}
	select {
	case id := <-delivered:
		if id != "b1" {
			t.Fatalf("first delivered = %q, want b1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b1 未在其他 Chat 推进（并行性被破坏）")
	}

	// 推进 clock：a1 重试成功送达后，a2 才按 FIFO 处理。
	clock.fire(1)
	select {
	case id := <-delivered:
		if id != "a1" {
			t.Fatalf("delivered after fire = %q, want a1（队头重试成功后送达）", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a1 未在重试后送达")
	}
	select {
	case id := <-delivered:
		if id != "a2" {
			t.Fatalf("delivered #2 = %q, want a2（a1 完成后 a2 按 FIFO 发送）", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a2 未在 a1 完成后发送")
	}
}
