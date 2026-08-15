package outbox

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// Limiter 提供全局与单 Chat 双层的 token bucket 限速
// （docs/技术选型.md §7.1「全局限速与单聊天限速」）。
//
// 限速参数以构造参数传入，不读取 internal/config（Task 18 边界）。
// 实现基于 golang.org/x/time/rate（go.mod 已于 Task 2 锁定 v0.15.0）。
type Limiter struct {
	global *rate.Limiter
	tmpl   *rate.Limiter // 每 Chat limiter 的速率模板
	burst  int
	mu     sync.Mutex
	byChat map[ChatID]*rate.Limiter
}

// NewLimiter 创建双层限速器。
//
// globalPerSec 与 perChatPerSec 分别为全局、单 Chat 每秒令牌数；
// burst 为桶容量（<=0 时按 1 处理）。全局与各 Chat 限速互不影响。
func NewLimiter(globalPerSec, perChatPerSec float64, burst int) *Limiter {
	if burst <= 0 {
		burst = 1
	}
	return &Limiter{
		global: rate.NewLimiter(rate.Limit(globalPerSec), burst),
		tmpl:   rate.NewLimiter(rate.Limit(perChatPerSec), burst),
		burst:  burst,
		byChat: make(map[ChatID]*rate.Limiter),
	}
}

// Wait 阻塞直到全局与目标 Chat 的 token bucket 都允许一次发送。
//
// 先消耗全局令牌，再消耗该 Chat 令牌；不同 Chat 互不影响。
func (l *Limiter) Wait(ctx context.Context, chat ChatID) error {
	if err := l.global.Wait(ctx); err != nil {
		return err
	}
	return l.chatLimiter(chat).Wait(ctx)
}

// chatLimiter 获取（必要时创建）某 Chat 的独立限速器。
func (l *Limiter) chatLimiter(chat ChatID) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	cl, ok := l.byChat[chat]
	if !ok {
		cl = rate.NewLimiter(l.tmpl.Limit(), l.burst)
		l.byChat[chat] = cl
	}
	return cl
}
