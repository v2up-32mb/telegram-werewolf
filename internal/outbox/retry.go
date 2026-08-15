package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// Clock 抽象时间来源，供注入 fake clock 测试。
//
// 重试等待一律经 Clock.After 取得，不直接依赖 time.Sleep/time.After
// （docs/技术选型.md §7.1 429 RetryAfter 按服务端建议延迟重试）。
type Clock interface {
	// Now 返回当前时间。
	Now() time.Time
	// After 在至少 d 之后返回可接收的时间信号。
	After(d time.Duration) <-chan time.Time
}

// realClock 是生产环境的真实时钟。
type realClock struct{}

// Now 返回真实当前时间。
func (realClock) Now() time.Time { return time.Now() }

// After 等待真实 d 时长后触发。
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RetryPolicy 控制重试次数与临时错误退避间隔。
type RetryPolicy struct {
	// MaxRetries 是除首次尝试外的最大重试次数；达到后返回最后一次错误。
	MaxRetries int
	// RetryInterval 是临时错误（非 429）每次重试前的固定退避时长。
	RetryInterval time.Duration
}

// RateLimitedError 表示 Telegram 429 响应，RetryAfter 为服务端建议延迟。
type RateLimitedError struct {
	// RetryAfter 是服务端建议的等待时长。
	RetryAfter time.Duration
}

// Error 实现 error 接口。
func (e *RateLimitedError) Error() string {
	return "outbox: rate limited, retry after " + e.RetryAfter.String()
}

// PermanentError 表示永久错误（如 403 用户屏蔽、400 不可编辑），不重试。
type PermanentError struct {
	// Err 是底层原因，可为 nil。
	Err error
}

// Error 实现 error 接口。
func (e *PermanentError) Error() string {
	if e.Err == nil {
		return "outbox: permanent error"
	}
	return "outbox: permanent error: " + e.Err.Error()
}

// Unwrap 返回底层错误，支持 errors.Is/As 链。
func (e *PermanentError) Unwrap() error { return e.Err }

// RetryClass 是错误的重试类别。
type RetryClass int

const (
	// RetryClassPermanent 表示永久错误，不重试。
	RetryClassPermanent RetryClass = iota
	// RetryClassTemporary 表示超时/临时网络错误，按 RetryInterval 重试。
	RetryClassTemporary
	// RetryClassRateLimited 表示 429，按 RetryAfter 重试。
	RetryClassRateLimited
)

// Classify 把错误归类为永久 / 临时 / 429 三类。
//
// 收到 429（携带 RetryAfter）归为 RateLimited；显式 PermanentError
// （或 wrapped 成它的错误）归为 Permanent；其余（超时、临时网络错误）归为 Temporary。
func Classify(err error) RetryClass {
	if err == nil {
		return RetryClassTemporary
	}
	var rle *RateLimitedError
	if errors.As(err, &rle) {
		return RetryClassRateLimited
	}
	var pe *PermanentError
	if errors.As(err, &pe) {
		return RetryClassPermanent
	}
	return RetryClassTemporary
}

// RetryingSender 包装 SendFunc，在错误分类基础上按策略重试。
//
// 重试期间阻塞当前 goroutine：与 Task 17 Scheduler 每 Chat worker 串行
// 组合后，队头消息等待重试期间同一 Chat 后续消息不会越过它，其他 Chat
// 的 worker 不受影响。
type RetryingSender struct {
	send    SendFunc
	policy  RetryPolicy
	clock   Clock
	rate429 atomic.Int64
	retried atomic.Int64
}

// NewRetryingSender 创建重试发送器；clock 为 nil 时使用真实时钟。
func NewRetryingSender(send SendFunc, policy RetryPolicy, clock Clock) *RetryingSender {
	if clock == nil {
		clock = realClock{}
	}
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	return &RetryingSender{send: send, policy: policy, clock: clock}
}

// Send 发送 msg，按策略处理失败：成功即返回；永久错误不重试；
// 429 按 RetryAfter、临时错误按 RetryInterval 等待后重试，
// 超过 MaxRetries 返回最后一次错误。
func (r *RetryingSender) Send(ctx context.Context, msg Message) error {
	attempt := 0
	for {
		err := r.send(ctx, msg)
		if err == nil {
			return nil
		}
		attempt++
		class := Classify(err)
		if class == RetryClassRateLimited {
			r.rate429.Add(1)
		}
		if attempt > r.policy.MaxRetries {
			return err
		}
		switch class {
		case RetryClassPermanent:
			return err
		case RetryClassRateLimited:
			r.retried.Add(1)
			var rle *RateLimitedError
			errors.As(err, &rle)
			delay := r.policy.RetryInterval
			if rle != nil && rle.RetryAfter > 0 {
				delay = rle.RetryAfter
			}
			if err := r.wait(ctx, delay); err != nil {
				return err
			}
		default:
			r.retried.Add(1)
			if err := r.wait(ctx, r.policy.RetryInterval); err != nil {
				return err
			}
		}
	}
}

// wait 经注入时钟等待 d，ctx 取消时中断。
func (r *RetryingSender) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-r.clock.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RateLimitedCount 返回累计收到的 429 次数（docs/技术选型.md §11.3）。
func (r *RetryingSender) RateLimitedCount() int64 {
	return r.rate429.Load()
}

// RetriedCount 返回累计执行的重试次数（docs/技术选型.md §11.3）。
func (r *RetryingSender) RetriedCount() int64 {
	return r.retried.Load()
}
