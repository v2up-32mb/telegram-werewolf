package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
)

// fakeClock 是可手动推进的可注入时钟（docs/技术选型.md §6.2）。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, ch: make(chan time.Time, 1), when: c.now.Add(d), active: true}
	c.timers = append(c.timers, t)
	return t
}

// Advance 推进当前时间并触发所有到期且仍 active 的 Timer（每个仅一次）。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	for _, t := range c.timers {
		if t.active && !t.when.After(now) {
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fire()
	}
}

// fakeTimer 模拟 time.Timer：Stop 返回是否成功取消；已触发后返回 false。
type fakeTimer struct {
	clock  *fakeClock
	ch     chan time.Time
	when   time.Time
	active bool
	fired  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.fired || !t.active {
		return false
	}
	t.active = false
	return true
}

func (t *fakeTimer) fire() {
	t.clock.mu.Lock()
	if !t.active {
		t.clock.mu.Unlock()
		return
	}
	t.active = false
	t.fired = true
	t.clock.mu.Unlock()
	select {
	case t.ch <- t.when:
	default:
	}
}

// fakeReducer 记录调用顺序与并发深度，并支持可注入 hook。
type fakeReducer struct {
	mu        sync.Mutex
	inFlight  int
	maxFlight int
	calls     []game.Command
	hook      func(cmd game.Command, st game.State) (game.State, []game.Effect, error)
}

func (f *fakeReducer) Reduce(st game.State, cmd game.Command) (game.State, []game.Effect, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxFlight {
		f.maxFlight = f.inFlight
	}
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.hook == nil {
		return st, nil, nil
	}
	return f.hook(cmd, st)
}

func (f *fakeReducer) snapshot() (calls []game.Command, maxFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]game.Command(nil), f.calls...), f.maxFlight
}

func (f *fakeReducer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// roomState 构造一个合法的初始房间状态。
func roomState() game.State {
	return game.State{
		RoomID:       "ABCDEF",
		GameID:       "g-1",
		Phase:        game.PhaseLobby,
		PhaseVersion: 1,
		Processed:    map[string]bool{},
	}
}

// meta 构造带唯一 ID 的命令元信息。
func meta(i int, at time.Time) game.CommandMeta {
	return game.CommandMeta{
		ID:            fmt.Sprintf("cmd-%d", i),
		Actor:         1,
		ExpectedPhase: game.PhaseLobby,
		PhaseVersion:  1,
		ReceivedAt:    at,
	}
}

// waitTrue 轮询等待条件成立，带超时。
func waitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

// plainCmd 构造普通（非 Timeout）命令，用于驱动 Actor 的计时路径。
func plainCmd(i int, at time.Time) game.Command {
	return game.WolfKillCommand{Meta: meta(i, at), Target: 1}
}

// TestActorSerial 验证同一房间命令严格串行（docs/技术选型.md §6.1）。
func TestActorSerial(t *testing.T) {
	red := &fakeReducer{}
	a := NewActor(roomState(), red, newFakeClock(), Options{})
	defer a.Stop()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Dispatch(context.Background(), plainCmd(i, time.Time{})); err != nil {
				t.Errorf("Dispatch#%d error = %v, want nil", i, err)
			}
		}(i)
	}
	wg.Wait()
	_, max := red.snapshot()
	if max > 1 {
		t.Errorf("reducer 最大并发深度 = %d, want 1（同房间必须串行）", max)
	}
	if got := red.count(); got != n {
		t.Errorf("reducer 调用次数 = %d, want %d", got, n)
	}
}

// TestActorTimerFire 验证 TimerEffect 驱动 Fake Clock 到期触发 Timeout，
// 且 Timeout 携带正确的阶段与版本（docs/技术选型.md §6.2）。
func TestActorTimerFire(t *testing.T) {
	fc := newFakeClock()
	m := observability.NewMetrics()
	timeoutCh := make(chan game.Command, 1)
	v1 := uint64(3)
	st := roomState()
	st.Phase = game.PhaseNight
	st.PhaseVersion = v1
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if tc, ok := cmd.(game.TimeoutCommand); ok {
			timeoutCh <- tc
			return st, nil, nil
		}
		return st, []game.Effect{game.TimerEffect{Phase: game.PhaseNight, Duration: 30 * time.Second}}, nil
	}}
	a := NewActor(st, red, fc, Options{Metrics: m})
	defer a.Stop()

	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("Dispatch error = %v, want nil", err)
	}
	fc.Advance(30 * time.Second)

	select {
	case tc := <-timeoutCh:
		tm := tc.(game.TimeoutCommand)
		if tm.Meta.ExpectedPhase != game.PhaseNight {
			t.Errorf("Timeout ExpectedPhase = %v, want night", tm.Meta.ExpectedPhase)
		}
		if tm.Meta.PhaseVersion != v1 {
			t.Errorf("Timeout PhaseVersion = %d, want %d", tm.Meta.PhaseVersion, v1)
		}
		if tm.Meta.ReceivedAt.IsZero() {
			t.Error("Timeout ReceivedAt 为空")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Timer 到期未触发 Timeout")
	}
	counters, _ := m.Snapshot()
	if counters[observability.MetricPhaseTimeout] != 1 {
		t.Errorf("MetricPhaseTimeout = %d, want 1", counters[observability.MetricPhaseTimeout])
	}
}

// TestActorTimerCancel 验证 TimerEffect(Cancel) 停止计时器，
// 推进时钟不再触发 Timeout。
func TestActorTimerCancel(t *testing.T) {
	fc := newFakeClock()
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if tc, ok := cmd.(game.TimeoutCommand); ok {
			t.Errorf("取消后仍触发 Timeout: %v", tc)
		}
		return st, []game.Effect{game.TimerEffect{Cancel: true}}, nil
	}}
	a := NewActor(roomState(), red, fc, Options{})
	defer a.Stop()

	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("Dispatch error = %v, want nil", err)
	}
	fc.Advance(time.Hour)
	time.Sleep(20 * time.Millisecond)
	if got := red.count(); got != 1 {
		t.Errorf("reducer 调用次数 = %d, want 1（Timer 已取消）", got)
	}
}

// TestActorStaleTimeoutRejected 验证旧 phaseVersion 的 Timeout 被拒绝
// （reducer 已切换版本但旧 Timer 仍竞争触发，docs/技术选型.md §6.2）。
func TestActorStaleTimeoutRejected(t *testing.T) {
	fc := newFakeClock()
	st := roomState()
	st.Phase = game.PhaseNight
	st.PhaseVersion = 1

	var gotVersion uint64
	var first bool
	m := observability.NewMetrics()
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if tc, ok := cmd.(game.TimeoutCommand); ok {
			gotVersion = tc.Meta.PhaseVersion
			return st, nil, game.ErrStalePhaseVersion
		}
		if !first {
			first = true
			next := st.Copy()
			next.PhaseVersion = 2 // 阶段切换：版本推进并设置新 Timer（timeoutVersion=1）
			return next, []game.Effect{game.TimerEffect{Phase: game.PhaseNight, Duration: 30 * time.Second}}, nil
		}
		next := st.Copy()
		next.PhaseVersion = 3 // 版本再次推进，但不更换 Timer（旧 Timer 竞争触发的模拟路径）
		return next, nil, nil
	}}
	a := NewActor(st, red, fc, Options{Metrics: m})
	defer a.Stop()

	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("Dispatch#0 error = %v, want nil", err)
	}
	if _, err := a.Dispatch(context.Background(), plainCmd(1, time.Time{})); err != nil {
		t.Fatalf("Dispatch#1 error = %v, want nil", err)
	}
	fc.Advance(30 * time.Second)

	waitTrue(t, "旧 Timeout 被处理", func() bool {
		calls, _ := red.snapshot()
		for _, c := range calls {
			if _, ok := c.(game.TimeoutCommand); ok {
				return true
			}
		}
		return false
	})
	if gotVersion != 2 {
		t.Errorf("Timeout PhaseVersion = %d, want 2（Timer 创建时的新阶段版本）", gotVersion)
	}
	// 旧版本 Timeout 被 reducer 拒绝：计入过期拒绝计数（docs/技术选型.md §11.3）。
	counters, _ := m.Snapshot()
	if counters[observability.MetricStaleRejected] != 1 {
		t.Errorf("MetricStaleRejected = %d, want 1", counters[observability.MetricStaleRejected])
	}
}

// TestActorPhaseTransitionReschedulesTimer 回归验证：Timeout 触发阶段切换时
// reducer 返回的新阶段 TimerEffect 必须被保留（不得被 onTimerFire 清理），
// 下一次到期仍能触发 Timeout 并携带新阶段/版本。
func TestActorPhaseTransitionReschedulesTimer(t *testing.T) {
	fc := newFakeClock()
	m := observability.NewMetrics()
	st := roomState()
	st.Phase = game.PhaseNight
	st.PhaseVersion = 1
	got := make(chan game.TimeoutCommand, 2)
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if tc, ok := cmd.(game.TimeoutCommand); ok {
			next := st.Copy()
			next.Phase = game.PhaseDaySpeech
			next.PhaseVersion++
			got <- tc
			// 阶段切换：安装下一阶段计时器。
			return next, []game.Effect{game.TimerEffect{Phase: next.Phase, Duration: 30 * time.Second}}, nil
		}
		return st, []game.Effect{game.TimerEffect{Phase: st.Phase, Duration: 30 * time.Second}}, nil
	}}
	a := NewActor(st, red, fc, Options{Metrics: m})
	defer a.Stop()

	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("Dispatch error = %v, want nil", err)
	}
	fc.Advance(30 * time.Second)

	first := waitTimeoutCommand(t, got)
	if first.Meta.ExpectedPhase != game.PhaseNight || first.Meta.PhaseVersion != 1 {
		t.Errorf("第 1 个 Timeout = %v/%d, want night/1", first.Meta.ExpectedPhase, first.Meta.PhaseVersion)
	}

	// 阶段切换后的新 Timer 必须继续生效。
	fc.Advance(30 * time.Second)
	second := waitTimeoutCommand(t, got)
	if second.Meta.ExpectedPhase != game.PhaseDaySpeech || second.Meta.PhaseVersion != 2 {
		t.Errorf("第 2 个 Timeout = %v/%d, want day-speech/2", second.Meta.ExpectedPhase, second.Meta.PhaseVersion)
	}
	counters, _ := m.Snapshot()
	if counters[observability.MetricPhaseTimeout] != 2 {
		t.Errorf("MetricPhaseTimeout = %d, want 2", counters[observability.MetricPhaseTimeout])
	}
}

// waitTimeoutCommand 等待一个 TimeoutCommand，超时则失败。
func waitTimeoutCommand(t *testing.T, ch <-chan game.TimeoutCommand) game.TimeoutCommand {
	t.Helper()
	select {
	case tc := <-ch:
		return tc
	case <-time.After(3 * time.Second):
		t.Fatal("等待 TimeoutCommand 超时")
		return game.TimeoutCommand{}
	}
}

// TestActorDeadlineBoundary 验证 deadline 语义：ReceivedAt <= phaseDeadline
// 生效；ReceivedAt > phaseDeadline 被拒绝并增加过期拒绝计数。
func TestActorDeadlineBoundary(t *testing.T) {
	fc := newFakeClock()
	m := observability.NewMetrics()
	setup := false
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if !setup {
			setup = true
			return st, []game.Effect{game.TimerEffect{Phase: game.PhaseLobby, Duration: 30 * time.Second}}, nil
		}
		return st, nil, nil
	}}
	a := NewActor(roomState(), red, fc, Options{Metrics: m})
	defer a.Stop()

	// 设置阶段 deadline（now + 30s）。
	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("setup Dispatch error = %v, want nil", err)
	}

	// deadline 前（ReceivedAt = now+25s <= deadline）命令生效。
	early := game.WolfKillCommand{Meta: game.CommandMeta{ID: "early", Actor: 1, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1, ReceivedAt: fc.Now().Add(25 * time.Second)}, Target: 1}
	if _, err := a.Dispatch(context.Background(), early); err != nil {
		t.Fatalf("deadline 前命令被拒绝: %v, want nil", err)
	}

	// 推进越过 deadline。
	fc.Advance(31 * time.Second)
	late := game.WolfKillCommand{Meta: game.CommandMeta{ID: "late", Actor: 1, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1, ReceivedAt: fc.Now()}, Target: 1}
	res, err := a.Dispatch(context.Background(), late)
	if err != nil {
		t.Fatalf("Dispatch 传输错误 = %v, want nil", err)
	}
	if !errors.Is(res.Err, ErrDeadlinePassed) {
		t.Fatalf("deadline 后命令 Result.Err = %v, want ErrDeadlinePassed", res.Err)
	}
	counters, _ := m.Snapshot()
	if counters[observability.MetricStaleRejected] != 1 {
		t.Errorf("MetricStaleRejected = %d, want 1", counters[observability.MetricStaleRejected])
	}
}

// TestActorDrainBeforeTimeout 验证应用 Timeout 前先按接收序排空
// 已缓冲 inbox：所有 deadline 前命令先于 Timeout 生效。
func TestActorDrainBeforeTimeout(t *testing.T) {
	fc := newFakeClock()
	st := roomState()
	st.Phase = game.PhaseNight
	entered := make(chan struct{})
	release := make(chan struct{})
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if _, ok := cmd.(game.TimeoutCommand); ok {
			return st, nil, nil
		}
		wc, isWolfKill := cmd.(game.WolfKillCommand)
		if isWolfKill && wc.Meta.ID == "cmd-0" {
			// 慢命令：阻塞 reducer，使后续命令先缓冲进 inbox。
			close(entered)
			<-release
			return st, []game.Effect{game.TimerEffect{Phase: game.PhaseNight, Duration: 30 * time.Second}}, nil
		}
		// 缓冲命令只读处理，不重新配置 Timer，避免干扰到期时序。
		return st, nil, nil
	}}
	a := NewActor(st, red, fc, Options{})
	defer a.Stop()

	slowDone := make(chan error, 1)
	go func() {
		_, err := a.Dispatch(context.Background(), plainCmd(0, fc.Now()))
		slowDone <- err
	}()
	<-entered

	// Actor 处理中，以下命令缓冲在 inbox。
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Dispatch(context.Background(), plainCmd(i, fc.Now())); err != nil {
				t.Errorf("Dispatch#%d error = %v, want nil", i, err)
			}
		}(i)
	}
	waitTrue(t, "inbox 已缓冲", func() bool { return len(a.inbox) == 2 })

	close(release) // 放行慢命令：此时创建 30s Timer
	if err := <-slowDone; err != nil {
		t.Fatalf("slow Dispatch error = %v, want nil", err)
	}
	fc.Advance(30 * time.Second) // Timer 到期，与 inbox 缓冲命令同时就绪
	waitTrue(t, "Timeout 被处理", func() bool {
		calls, _ := red.snapshot()
		for _, c := range calls {
			if _, ok := c.(game.TimeoutCommand); ok {
				return true
			}
		}
		return false
	})
	wg.Wait()

	calls, _ := red.snapshot()
	idxT := -1
	for i, c := range calls {
		if _, ok := c.(game.TimeoutCommand); ok {
			idxT = i
		}
	}
	// drain 语义：Timeout 必须是最后处理的命令，且两条缓冲命令先于它。
	if idxT != len(calls)-1 {
		t.Errorf("Timeout 不是最后处理的命令：idx=%d len=%d, calls=%v", idxT, len(calls), calls)
	}
	if len(calls) < 3 {
		t.Fatalf("calls 数量 = %d, want >= 3（慢命令+2 缓冲+Timeout）", len(calls))
	}
}

// TestActorInboxFull 验证 bounded inbox 满时 Dispatch 返回可观察错误
// 并增加计数器，禁止静默丢命令。
func TestActorInboxFull(t *testing.T) {
	fc := newFakeClock()
	m := observability.NewMetrics()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		once.Do(func() {
			close(entered)
			<-release
		})
		return st, nil, nil
	}}
	a := NewActor(roomState(), red, fc, Options{InboxSize: 2, Metrics: m})
	defer a.Stop()

	go func() {
		_, _ = a.Dispatch(context.Background(), plainCmd(0, fc.Now()))
	}()
	<-entered

	// Actor 处理中：两条命令缓冲进 inbox（容量 2）。
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Dispatch(context.Background(), plainCmd(i, fc.Now())); err != nil {
				t.Errorf("Dispatch#%d error = %v, want nil", i, err)
			}
		}(i)
	}
	waitTrue(t, "inbox 缓冲满", func() bool { return len(a.inbox) == 2 })

	if _, err := a.Dispatch(context.Background(), plainCmd(3, fc.Now())); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("Dispatch#3 err = %v, want ErrInboxFull", err)
	}
	// 计数器可观察：自定义名 room_inbox_full 应已递增。
	counters, _ := m.Snapshot()
	if counters["room_inbox_full"] != 1 {
		t.Errorf("room_inbox_full = %d, want 1", counters["room_inbox_full"])
	}
	close(release)
	wg.Wait()
}

// TestActorStop 验证 Stop 取消 Timer、关闭接收并等待 goroutine 退出，
// 停止后 Dispatch 返回明确错误。
func TestActorStop(t *testing.T) {
	fc := newFakeClock()
	timeoutCh := make(chan game.Command, 1)
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		if tc, ok := cmd.(game.TimeoutCommand); ok {
			timeoutCh <- tc
			return st, nil, nil
		}
		return st, []game.Effect{game.TimerEffect{Phase: game.PhaseLobby, Duration: 30 * time.Second}}, nil
	}}

	t.Run("停止后 Dispatch 返回 ErrClosed", func(t *testing.T) {
		a := NewActor(roomState(), red, fc, Options{})
		if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
			t.Fatalf("Dispatch error = %v, want nil", err)
		}
		a.Stop()
		a.Stop() // 幂等
		if _, err := a.Dispatch(context.Background(), plainCmd(1, time.Time{})); !errors.Is(err, ErrClosed) {
			t.Fatalf("停止后 Dispatch err = %v, want ErrClosed", err)
		}
	})

	t.Run("Stop 取消 Timer 不再触发", func(t *testing.T) {
		a := NewActor(roomState(), red, fc, Options{})
		if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
			t.Fatalf("Dispatch error = %v, want nil", err)
		}
		a.Stop()
		fc.Advance(time.Hour)
		select {
		case <-timeoutCh:
			t.Error("Stop 后 Timer 仍触发 Timeout")
		case <-time.After(50 * time.Millisecond):
		}
	})
}

// TestActorOnAppliedHook 验证 OnApplied 在每个命令/超时应用后于 Actor
// goroutine 内回调（携带新状态与该次 Effects）：涵盖 Dispatch 即时路径与
// Timer 触发路径（docs/技术选型.md §6.1/§6.2；生产导演依赖此钩子驱动
// 阶段推进与效果扇出，B1-a）。
func TestActorOnAppliedHook(t *testing.T) {
	fc := newFakeClock()
	eff, err := game.NewMessageEffect(game.AudienceHost, game.LobbyPanelMessageKey, map[string]any{})
	if err != nil {
		t.Fatalf("NewMessageEffect: %v", err)
	}
	var (
		mu     sync.Mutex
		states []game.State
		nfx    int
	)
	red := &fakeReducer{hook: func(cmd game.Command, st game.State) (game.State, []game.Effect, error) {
		return st, []game.Effect{eff, game.TimerEffect{Phase: game.PhaseNight, Duration: 30 * time.Second}}, nil
	}}
	a := NewActor(roomState(), red, fc, Options{
		OnApplied: func(s game.State, fx []game.Effect) {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, s)
			nfx += len(fx)
		},
	})
	defer a.Stop()

	// 即时路径：Dispatch 后 OnApplied 触发一次。
	if _, err := a.Dispatch(context.Background(), plainCmd(0, time.Time{})); err != nil {
		t.Fatalf("Dispatch error = %v, want nil", err)
	}
	// Timer 路径：到期触发 TimeoutCommand → OnApplied 第二次。
	fc.Advance(30 * time.Second)

	waitTrue(t, "OnApplied 触发次数", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(states) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if len(states) != 2 {
		t.Fatalf("OnApplied 触发次数 = %d, want 2（Dispatch + Timer 到期）", len(states))
	}
	if nfx == 0 {
		t.Error("OnApplied 收到 Effects 为空，want 非空")
	}
}
