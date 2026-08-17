package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
)

// Envelope 是进入房间 Actor 的命令信封（实施计划 Key actor sketch）。
// Fn 非 nil 时优先执行（B1-d 导演本地推进：发言麦序移交、阶段引导等非
// reducer 逻辑），否则走 Command 的 reducer 路径；两者都在 Actor goroutine
// 内串行处理。
type Envelope struct {
	Command game.Command
	Fn      LocalFunc
	Reply   chan<- Result
}

// LocalFunc 是导演本地推进函数：接收当前状态，返回新状态 + Effects
// （B1-d）。只在 Actor goroutine 内执行，保持同房间严格有序
// （docs/技术选型.md §6.1）。
type LocalFunc func(st game.State) (game.State, []game.Effect, error)

// EffectSink 是 Effects 的可注入出口（除 TimerEffect 外的副作用）。
type EffectSink func(effects []game.Effect)

// Options 是 Actor 的构造选项。
type Options struct {
	InboxSize int                    // bounded inbox 容量；<=0 时用默认值
	Metrics   *observability.Metrics // 轻量计数器（可为 nil）
	Sink      EffectSink             // Effects 出口（可为 nil）

	// OnApplied 在每个命令/超时成功应用后，于 Actor goroutine 内回调，
	// 携带该次应用后的新状态与本批 Effects（B1-a）。供生产导演驱动
	// 阶段推进（night/day pump）与效果扇出（含 Timer 触发路径产生的
	// 效果，修复 B3：onTimerFire 丢弃的 Result 经 Sink 不丢）。
	OnApplied func(st game.State, fx []game.Effect)
}

// defaultInboxSize 是默认 bounded inbox 容量。
const defaultInboxSize = 64

// inboxFullMetric 是 inbox 满拒绝的计数器名。
const inboxFullMetric = "room_inbox_full"

// Actor 是每个活跃房间独占状态的事件循环
// （docs/技术选型.md §6.1：同一房间内严格按接收顺序串行处理）。
type Actor struct {
	inbox   chan Envelope
	state   game.State
	reducer game.Reducer
	clock   Clock
	sink    EffectSink
	metrics *observability.Metrics
	onApply func(st game.State, fx []game.Effect)

	done     chan struct{}
	stopOnce sync.Once
	closed   atomic.Bool
	wg       sync.WaitGroup

	timer          Timer
	timerCh        <-chan time.Time
	deadline       time.Time
	timeoutPhase   game.Phase
	timeoutVersion uint64
}

// NewActor 创建并启动一个 Room Actor 的事件循环 goroutine。
func NewActor(initial game.State, reducer game.Reducer, clock Clock, opts Options) *Actor {
	size := opts.InboxSize
	if size <= 0 {
		size = defaultInboxSize
	}
	a := &Actor{
		inbox:   make(chan Envelope, size),
		state:   initial,
		reducer: reducer,
		clock:   clock,
		sink:    opts.Sink,
		metrics: opts.Metrics,
		onApply: opts.OnApplied,
		done:    make(chan struct{}),
	}
	a.wg.Add(1)
	go a.run()
	return a
}

// Dispatch 将命令投递到房间 inbox 并等待处理结果。
// inbox 满时返回 ErrInboxFull（可观察、不静默丢弃）；Actor 停止后返回 ErrClosed。
func (a *Actor) Dispatch(ctx context.Context, cmd game.Command) (Result, error) {
	if a.closed.Load() {
		return Result{}, ErrClosed
	}
	reply := make(chan Result, 1)
	env := Envelope{Command: cmd, Reply: reply}
	select {
	case a.inbox <- env:
	case <-a.done:
		return Result{}, ErrClosed
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
		a.inc(inboxFullMetric)
		return Result{}, ErrInboxFull
	}
	select {
	case res := <-reply:
		return res, nil
	case <-a.done:
		return Result{}, ErrClosed
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// DispatchLocal 把导演本地推进函数投递到房间 inbox 并等待结果
// （B1-d；与 Dispatch 同一信箱串行，见 Envelope.Fn）。
func (a *Actor) DispatchLocal(ctx context.Context, fn LocalFunc) (Result, error) {
	if fn == nil {
		return Result{}, errors.New("room: dispatch local requires non-nil fn")
	}
	if a.closed.Load() {
		return Result{}, ErrClosed
	}
	reply := make(chan Result, 1)
	env := Envelope{Fn: fn, Reply: reply}
	select {
	case a.inbox <- env:
	case <-a.done:
		return Result{}, ErrClosed
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
		a.inc(inboxFullMetric)
		return Result{}, ErrInboxFull
	}
	select {
	case res := <-reply:
		return res, nil
	case <-a.done:
		return Result{}, ErrClosed
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Adopt 以 st/fx 覆盖当前状态并应用效果（B1-d）。仅限在 Actor goroutine
// 内调用（OnApplied 回调中），不重复触发 OnApplied——调用方（导演 pump）
// 自行驱动后续推进。用于阶段引导（Begin*Phase / BeginVote / ResolveNight）
// 把结果回写 Actor。
func (a *Actor) Adopt(st game.State, fx []game.Effect) {
	a.state = st
	a.applyEffects(fx)
}

// Stop 幂等地停止 Actor：取消计时器、关闭接收并等待事件循环 goroutine 退出。
func (a *Actor) Stop() {
	a.stopOnce.Do(func() {
		a.closed.Store(true)
		close(a.done)
	})
	a.wg.Wait()
}

func (a *Actor) run() {
	defer a.wg.Done()
	for {
		select {
		case <-a.done:
			a.stopTimer()
			return
		case env := <-a.inbox:
			a.handleEnvelope(env)
		case <-a.timerCh:
			a.onTimerFire()
		}
	}
}

func (a *Actor) handleEnvelope(env Envelope) {
	var res Result
	if env.Fn != nil {
		st, fx, err := env.Fn(a.state)
		a.state = st
		a.applyEffects(fx)
		if a.onApply != nil {
			a.onApply(st, fx)
		}
		res = Result{State: st, Effects: fx, Err: err}
	} else {
		res = a.apply(env.Command)
	}
	select {
	case env.Reply <- res:
	default:
	}
}

func (a *Actor) apply(cmd game.Command) Result {
	// Deadline 语义：服务端标记的 ReceivedAt <= phaseDeadline 生效，> 拒绝并计数。
	// 未标记（零值）的命令视为未携带时间戳，跳过检查（保留既有调用方行为）。
	if !a.deadline.IsZero() && !isTimeoutCommand(cmd) {
		at := commandReceivedAt(cmd)
		if !at.IsZero() && at.After(a.deadline) {
			a.inc(observability.MetricStaleRejected)
			return Result{State: a.state, Err: ErrDeadlinePassed}
		}
	}
	st, effects, err := a.reducer.Reduce(a.state, cmd)
	a.state = st
	a.applyEffects(effects)
	// 旧阶段版本的 Timeout/命令被 reducer 拒绝（ErrStalePhaseVersion）时，
	// 计入过期拒绝计数（docs/技术选型.md §11.3）。
	if errors.Is(err, game.ErrStalePhaseVersion) {
		a.inc(observability.MetricStaleRejected)
	}
	// B1-a：无论成功/拒绝，都在 Actor goroutine 内回传新状态与本批 Effects，
	// 供导演驱动阶段推进与效果扇出（拒绝时 effects 为空）。
	if a.onApply != nil {
		a.onApply(st, effects)
	}
	return Result{State: st, Effects: effects, Err: err}
}

func (a *Actor) applyEffects(effects []game.Effect) {
	for _, e := range effects {
		if te, ok := e.(game.TimerEffect); ok {
			a.scheduleTimer(te)
			continue
		}
		if a.sink != nil {
			a.sink([]game.Effect{e})
		}
	}
}

// scheduleTimer 依据 TimerEffect 启动/替换或取消阶段计时器，并同时维护阶段
// 截止时间（phaseDeadline）。TimerEffect.Phase 仅起说明作用：Timeout 携带的
// 权威阶段与版本取自 reducer 返回的新 state（Phase/PhaseVersion）。
func (a *Actor) scheduleTimer(te game.TimerEffect) {
	if te.Cancel {
		a.stopTimer()
		a.deadline = time.Time{}
		a.timeoutPhase = 0
		a.timeoutVersion = 0
		return
	}
	if te.Duration <= 0 {
		return
	}
	a.stopTimer()
	a.timeoutPhase = a.state.Phase
	a.timeoutVersion = a.state.PhaseVersion
	a.deadline = a.clock.Now().Add(te.Duration)
	a.timer = a.clock.NewTimer(te.Duration)
	a.timerCh = a.timer.C()
}

func (a *Actor) stopTimer() {
	if a.timer != nil {
		// Stop 返回 false 表示已触发/已停止：竞争触发由版本校验兜底
		//（docs/技术选型.md §6.2）。
		_ = a.timer.Stop()
		a.timer = nil
	}
	a.timerCh = nil
}

// onTimerFire 在计时器到期时先排空已缓冲 inbox（按接收序处理 deadline 前
// 命令，保证其先于 Timeout 生效），再构造携带创建时阶段/版本的
// TimeoutCommand 交给 reducer；旧版本由 reducer 拒绝。
// Timeout 处理中新安装的 TimerEffect（阶段切换）必须保留：仅当 fired
// timer 未被 drain 命令或 Timeout 自身的 Effects 替换时才清理其引用。
func (a *Actor) onTimerFire() {
	fired := a.timer
	a.drainInbox()
	tc := game.TimeoutCommand{
		Meta: game.CommandMeta{
			ID:            fmt.Sprintf("timeout-%s-%d", a.timeoutPhase, a.timeoutVersion),
			Actor:         0,
			ExpectedPhase: a.timeoutPhase,
			PhaseVersion:  a.timeoutVersion,
			ReceivedAt:    a.clock.Now(),
		},
	}
	a.inc(observability.MetricPhaseTimeout)
	a.apply(tc)
	if a.timer == fired {
		// 无新阶段 Timer 被安装：清理已触发旧 timer 的引用，等待下一枚。
		a.timer = nil
		a.timerCh = nil
	}
}

// drainInbox 非阻塞地按接收序处理当前已缓冲的命令。
func (a *Actor) drainInbox() {
	for {
		select {
		case env := <-a.inbox:
			a.handleEnvelope(env)
		default:
			return
		}
	}
}

func (a *Actor) inc(name string) {
	if a.metrics != nil {
		a.metrics.IncCounter(name)
	}
}

// isTimeoutCommand 判断命令是否为系统 Timeout。
func isTimeoutCommand(cmd game.Command) bool {
	_, ok := cmd.(game.TimeoutCommand)
	return ok
}

// commandReceivedAt 提取命令的服务端接收时间；未标记返回零值。
func commandReceivedAt(cmd game.Command) time.Time {
	switch c := cmd.(type) {
	case game.CreateRoomCommand:
		return c.Meta.ReceivedAt
	case game.JoinRoomCommand:
		return c.Meta.ReceivedAt
	case game.StartGameCommand:
		return c.Meta.ReceivedAt
	case game.ConfirmRoleCommand:
		return c.Meta.ReceivedAt
	case game.WolfKillCommand:
		return c.Meta.ReceivedAt
	case game.WitchUseCommand:
		return c.Meta.ReceivedAt
	case game.SeerCheckCommand:
		return c.Meta.ReceivedAt
	case game.SpeakCommand:
		return c.Meta.ReceivedAt
	case game.VoteCommand:
		return c.Meta.ReceivedAt
	case game.TimeoutCommand:
		return c.Meta.ReceivedAt
	default:
		return time.Time{}
	}
}
