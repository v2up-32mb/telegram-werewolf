package room

import "time"

// Clock 是可注入的时钟，用于驱动 Actor 的阶段计时
// （docs/技术选型.md §6.2：测试通过可注入 Clock 驱动）。
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer 是阶段计时器的可注入抽象，语义对齐 time.Timer：
// Stop 返回 false 表示计时器已触发或已停止。
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// realClock 基于 time.Now/time.NewTimer 的生产时钟实现。
type realClock struct{}

// NewRealClock 返回生产时钟。
func NewRealClock() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{t: time.NewTimer(d)} }

// realTimer 适配 time.Timer（其 C 是字段而非方法）到 Timer 接口。
type realTimer struct {
	t *time.Timer
}

func (rt realTimer) C() <-chan time.Time { return rt.t.C }

func (rt realTimer) Stop() bool { return rt.t.Stop() }
