package room

import (
	"testing"
	"time"
)

// TestFakeClockAdvance 验证 Fake Clock 推进触发到期 Timer。
func TestFakeClockAdvance(t *testing.T) {
	fc := newFakeClock()
	tm := fc.NewTimer(10 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("未到期的 Timer 提前触发")
	default:
	}
	fc.Advance(5 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("Advance(5s) 后 10s Timer 不应触发")
	default:
	}
	fc.Advance(6 * time.Second)
	select {
	case <-tm.C():
	default:
		t.Error("Advance 跨过到期点后 Timer 未触发")
	}
	tm2 := fc.NewTimer(time.Hour)
	if !tm2.Stop() {
		t.Error("active Timer Stop() = false, want true（成功取消）")
	}
}

// TestFakeTimerStopAfterFire 验证已触发的 Timer Stop() 返回 false。
func TestFakeTimerStopAfterFire(t *testing.T) {
	fc := newFakeClock()
	tm := fc.NewTimer(5 * time.Second)
	fc.Advance(6 * time.Second)
	select {
	case <-tm.C():
	default:
		t.Fatal("Timer 应已触发")
	}
	if tm.Stop() {
		t.Error("已触发 Timer Stop() = true, want false")
	}
}

// TestFakeTimerStopPreventsFire 验证 Stop 后推进不再触发。
func TestFakeTimerStopPreventsFire(t *testing.T) {
	fc := newFakeClock()
	tm := fc.NewTimer(5 * time.Second)
	if !tm.Stop() {
		t.Fatal("active Timer Stop() = false, want true")
	}
	fc.Advance(time.Hour)
	select {
	case <-tm.C():
		t.Error("Stop 后 Timer 仍触发")
	default:
	}
}
