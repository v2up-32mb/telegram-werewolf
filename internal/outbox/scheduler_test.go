package outbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newSinkScheduler 构造一个把已发送消息的 CorrelationID 推入 got 通道的调度器。
func newSinkScheduler(t *testing.T, capacity int, n int) (*Scheduler, <-chan string) {
	t.Helper()
	got := make(chan string, n)
	s := NewScheduler(func(ctx context.Context, msg Message) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		got <- msg.CorrelationID
		return nil
	}, capacity)
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, got
}

func TestSchedulerSameChatFIFO(t *testing.T) {
	s, got := newSinkScheduler(t, 8, 3)

	msgs := []Message{
		{CorrelationID: "m1", ChatID: ChatID(1), Operation: "a"},
		{CorrelationID: "m2", ChatID: ChatID(1), Operation: "b"},
		{CorrelationID: "m3", ChatID: ChatID(1), Operation: "c"},
	}
	for _, m := range msgs {
		if err := s.Enqueue(m); err != nil {
			t.Fatalf("Enqueue %s: %v", m.CorrelationID, err)
		}
	}
	for i, want := range []string{"m1", "m2", "m3"} {
		select {
		case gotID := <-got:
			if gotID != want {
				t.Fatalf("result #%d = %q, want %q", i, gotID, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for result #%d (%q)", i, want)
		}
	}
}

func TestSchedulerDifferentChatsParallel(t *testing.T) {
	var blockA = make(chan struct{})
	startedA := make(chan struct{})
	var markStarted sync.Once
	got := make(chan string, 2)
	s := NewScheduler(func(ctx context.Context, msg Message) error {
		if msg.ChatID == ChatID(1) {
			markStarted.Do(func() { close(startedA) })
			select {
			case <-blockA:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		got <- msg.CorrelationID
		return nil
	}, 8)
	var releaseA sync.Once
	release := func() { close(blockA) }
	t.Cleanup(func() {
		releaseA.Do(release) // 释放可能仍阻塞的 worker（幂等）
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})

	if err := s.Enqueue(Message{CorrelationID: "a1", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue a1: %v", err)
	}
	<-startedA // Chat A worker 已取出 a1 并进入阻塞
	if err := s.Enqueue(Message{CorrelationID: "b1", ChatID: ChatID(2)}); err != nil {
		t.Fatalf("Enqueue b1: %v", err)
	}

	select {
	case gotID := <-got:
		if gotID != "b1" {
			t.Fatalf("first parallel result = %q, want b1（Chat B 必须在 Chat A handler 阻塞时仍被推进）", gotID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Chat B 未被并行处理：Chat A 阻塞后 5s 内 Chat B 消息未到达")
	}
	releaseA.Do(release)
	select {
	case gotID := <-got:
		if gotID != "a1" {
			t.Fatalf("second result = %q, want a1", gotID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a1")
	}
}

func TestSchedulerPriorityDoesNotReorderWithinChat(t *testing.T) {
	s, got := newSinkScheduler(t, 8, 2)

	// 同 Chat 内：先入队普通，后入队关键；FIFO 因果顺序必须保持。
	if err := s.Enqueue(Message{CorrelationID: "normal-first", ChatID: ChatID(1), Priority: PriorityNormal}); err != nil {
		t.Fatalf("Enqueue normal-first: %v", err)
	}
	if err := s.Enqueue(Message{CorrelationID: "critical-second", ChatID: ChatID(1), Priority: PriorityCritical}); err != nil {
		t.Fatalf("Enqueue critical-second: %v", err)
	}
	for i, want := range []string{"normal-first", "critical-second"} {
		select {
		case gotID := <-got:
			if gotID != want {
				t.Fatalf("result #%d = %q, want %q（priority 不得破坏同 Chat 因果顺序）", i, gotID, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for result #%d (%q)", i, want)
		}
	}
}

func TestSchedulerQueueFull(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	s := NewScheduler(func(ctx context.Context, msg Message) error {
		if msg.CorrelationID == "m1" {
			once.Do(func() { close(started) })
			<-block
		}
		return nil
	}, 1)
	t.Cleanup(func() {
		close(block) // 释放可能仍阻塞的 worker
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})

	if err := s.Enqueue(Message{CorrelationID: "m1", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue m1: %v", err)
	}
	<-started // m1 已被 worker 取出并阻塞在 send，队列缓冲为空
	if err := s.Enqueue(Message{CorrelationID: "m2", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue m2: %v", err)
	}
	// 缓冲被 m2 占用且 m1 的 send 仍在阻塞，m3 必然满。
	err := s.Enqueue(Message{CorrelationID: "m3", ChatID: ChatID(1)})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Enqueue m3 err = %v, want ErrQueueFull", err)
	}
}

func TestSchedulerGracefulClose(t *testing.T) {
	before := runtime.NumGoroutine()
	time.Sleep(50 * time.Millisecond)

	s := NewScheduler(func(ctx context.Context, msg Message) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		return nil
	}, 4)
	// 不 Cleanup：本测试显式 Close。

	// 制造两个 Chat 的排队消息，保证有 worker goroutine。
	for i := 0; i < 4; i++ {
		if err := s.Enqueue(Message{CorrelationID: string(rune('a' + i)), ChatID: ChatID(1)}); err != nil {
			t.Fatalf("Enqueue a%d: %v", i, err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := s.Enqueue(Message{CorrelationID: string(rune('A' + i)), ChatID: ChatID(2)}); err != nil {
			t.Fatalf("Enqueue b%d: %v", i, err)
		}
	}

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close 后 Enqueue 必须返回确定错误。
	if err := s.Enqueue(Message{CorrelationID: "after-close", ChatID: ChatID(1)}); err == nil {
		t.Fatal("Enqueue after Close returned nil, want error")
	} else if !errors.Is(err, ErrClosed) {
		t.Fatalf("Enqueue after Close err = %v, want ErrClosed (errors.Is)", err)
	}
	// Close 幂等。
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// 等待所有调度器 goroutine 退出。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestSchedulerConcurrentEnqueueCloseNoLoss(t *testing.T) {
	var sent atomic.Int64
	s := NewScheduler(func(ctx context.Context, msg Message) error {
		sent.Add(1)
		return nil
	}, 64)

	var enqueued atomic.Int64
	var wg sync.WaitGroup
	const producers = 8
	const perProducer = 50
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				msg := Message{CorrelationID: fmt.Sprintf("p%d-%d", p, i), ChatID: ChatID(p % 2)}
				if err := s.Enqueue(msg); err == nil {
					enqueued.Add(1)
				}
			}
		}(p)
	}
	// 与 Enqueue 并发触发关闭：验证成功入队的消息在 Close 后全部被发送，无滞留。
	time.Sleep(2 * time.Millisecond)
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	if got, want := sent.Load(), enqueued.Load(); got != want {
		t.Fatalf("sent=%d enqueued=%d：成功入队的消息必须全部被发送（无滞留/静默丢失）", got, want)
	}
}

// TestSchedulerReportsSendErrors 是缺陷回归（红测）：worker 不得静默丢弃
// 发送错误——装配 WithSendErrorHandler 后，失败消息必须上报给回调
// （Task 46 冒烟：newgame 创建确认被 400 静默重试丢弃时没有任何日志）。
func TestSchedulerReportsSendErrors(t *testing.T) {
	boom := errors.New("boom")
	got := make(chan struct {
		msg Message
		err error
	}, 4)
	s := NewScheduler(func(ctx context.Context, msg Message) error { return boom }, 4,
		WithSendErrorHandler(func(m Message, err error) {
			got <- struct {
				msg Message
				err error
			}{m, err}
		}),
	)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	if err := s.Enqueue(Message{CorrelationID: "m1", ChatID: ChatID(1), Operation: "a"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case r := <-got:
		if r.msg.CorrelationID != "m1" || !errors.Is(r.err, boom) {
			t.Fatalf("reported = (%v, %v), want (m1, boom)", r.msg.CorrelationID, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("发送错误未上报给回调（worker 静默丢弃）")
	}
}
