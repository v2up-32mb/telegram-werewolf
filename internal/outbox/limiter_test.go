package outbox

import (
	"context"
	"testing"
	"time"
)

func TestLimiterGlobalRateLimit(t *testing.T) {
	l := NewLimiter(100, 1e9, 1) // 全局 100/s（burst 1 间隔 10ms），单 Chat 近乎不限
	start := time.Now()
	if err := l.Wait(context.Background(), ChatID(1)); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	first := time.Since(start)

	start = time.Now()
	if err := l.Wait(context.Background(), ChatID(1)); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	second := time.Since(start)

	if second < 5*time.Millisecond {
		t.Fatalf("second Wait elapsed = %v, want >= 5ms（全局 token bucket 必须限速）", second)
	}
	if first > 50*time.Millisecond {
		t.Fatalf("first Wait elapsed = %v, want <= 50ms（初始 burst 应立即放行）", first)
	}
}

func TestLimiterPerChatIsolation(t *testing.T) {
	l := NewLimiter(1e9, 100, 1) // 全局不限，单 Chat 100/s
	if err := l.Wait(context.Background(), ChatID(1)); err != nil {
		t.Fatalf("Chat A first Wait: %v", err)
	}
	start := time.Now()
	if err := l.Wait(context.Background(), ChatID(1)); err != nil {
		t.Fatalf("Chat A second Wait: %v", err)
	}
	if got := time.Since(start); got < 5*time.Millisecond {
		t.Fatalf("Chat A second Wait = %v, want >= 5ms（单 Chat 限速必须生效）", got)
	}

	// 其他 Chat 不受 Chat A 的填充影响。
	start = time.Now()
	if err := l.Wait(context.Background(), ChatID(2)); err != nil {
		t.Fatalf("Chat B Wait: %v", err)
	}
	if got := time.Since(start); got > 50*time.Millisecond {
		t.Fatalf("Chat B Wait = %v, want <= 50ms（各 Chat 独立限速，互不影响）", got)
	}
}

func TestLimiterGlobalAndPerChatBothApply(t *testing.T) {
	l := NewLimiter(100, 100, 1) // 全局与单 Chat 都是 100/s
	// 交替两个 Chat，验证全局层在第二个 Chat 上仍然限速（双层限制同时生效）。
	if err := l.Wait(context.Background(), ChatID(1)); err != nil {
		t.Fatalf("Chat A first Wait: %v", err)
	}
	start := time.Now()
	if err := l.Wait(context.Background(), ChatID(2)); err != nil {
		t.Fatalf("Chat B Wait: %v", err)
	}
	if got := time.Since(start); got < 5*time.Millisecond {
		t.Fatalf("Chat B Wait = %v, want >= 5ms（全局限速跨 Chat 生效，双层限制同时作用）", got)
	}
}
