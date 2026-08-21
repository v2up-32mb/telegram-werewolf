package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// testConfig 返回通过 config.Validate 的最小配置（Build 内部会再次校验）。
func testConfig() *config.Config {
	return &config.Config{
		BotToken:      "test-token",
		DatabasePath:  "data/test.db",
		UpdateMode:    "polling",
		HealthAddress: "",
		LogFormat:     "text",
		DefaultLocale: "zh-CN",
		Outbox: config.OutboxConfig{
			GlobalRateLimitPerSecond:  1000,
			PerChatRateLimitPerSecond: 1000,
			SendTimeout:               config.Duration{Duration: 5 * time.Second},
			RetryInterval:             config.Duration{Duration: time.Millisecond},
			MaxRetries:                0,
		},
	}
}

// openTestDB 打开临时 SQLite（不预迁移；迁移由 Build 负责，以验证「migrations 完成」）。
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := storage.Open(path, storage.DefaultMaxOpenConns)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustLogger(t *testing.T, buf io.Writer) *slog.Logger {
	t.Helper()
	lg, err := observability.NewLogger("text", buf)
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	return lg
}

// syncBuffer 是并发安全的 bytes.Buffer 替身：App 后台 goroutine（消费
// update 的 dispatch goroutine）与测试主 goroutine 可能同时经不同
// Logger 实例写同一缓冲，race 检测下裸 bytes.Buffer 会报 DATA RACE。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// fakeSource 是 telegram.UpdateSource 的可控替身：Start 后关闭 started，
// ctx 取消后关闭 stopped。
type fakeSource struct {
	updates chan telegram.Update
	errs    chan error
	started chan struct{}
	stopped chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		updates: make(chan telegram.Update),
		errs:    make(chan error),
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (f *fakeSource) Start(ctx context.Context) {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
}

func (f *fakeSource) Updates() <-chan telegram.Update { return f.updates }
func (f *fakeSource) Errors() <-chan error            { return f.errs }

// recordingSender 是 outbox.SendFunc 替身：把每条消息送入有缓冲 channel。
type recordingSender struct {
	ch chan outbox.Message
}

func newRecordingSender(capacity int) *recordingSender {
	return &recordingSender{ch: make(chan outbox.Message, capacity)}
}

func (r *recordingSender) Send(_ context.Context, m outbox.Message) error {
	r.ch <- m
	return nil
}

// fakeReducer 是最小 game.Reducer：原样返回状态，不产生 Effects。
type fakeReducer struct{}

func (fakeReducer) Reduce(s game.State, _ game.Command) (game.State, []game.Effect, error) {
	return s, nil, nil
}

// fakeScanner 是 AbortScanner 替身。
type fakeScanner struct {
	rooms []AbortedRoom
	err   error
}

func (f *fakeScanner) ListLeftover(context.Context) ([]AbortedRoom, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rooms, nil
}

// fakeNotifier 是 AbortNotifier 替身。
type fakeNotifier struct {
	mu  sync.Mutex
	got []AbortedRoom
	err error
}

func (f *fakeNotifier) NotifyAbort(_ context.Context, r AbortedRoom) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r)
	return f.err
}

func (f *fakeNotifier) snapshot() []AbortedRoom {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AbortedRoom(nil), f.got...)
}

// waitFor 轮询真值条件直至超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func TestBuildWiresComponentsInOrder(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	src := newFakeSource()
	db := openTestDB(t)
	sender := newRecordingSender(16)

	a, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger), WithSource(src), WithOutboxSender(sender.Send))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer a.Stop(context.Background())

	if a.db != db {
		t.Fatal("app.db 未装配注入的数据库")
	}
	if a.source != src {
		t.Fatal("app.source 未装配注入的 UpdateSource")
	}
	if a.outbox == nil {
		t.Fatal("app.outbox 未装配（Outbox Scheduler 缺失）")
	}
	if a.rooms == nil {
		t.Fatal("app.rooms 未装配（Room Manager 缺失）")
	}
	if a.router == nil {
		t.Fatal("app.router 未装配（Telegram Router 缺失）")
	}

	out := logBuf.String()
	prev := -1
	for _, step := range []string{"step=db", "step=outbox", "step=manager", "step=telegram"} {
		idx := strings.Index(out, step)
		if idx < 0 {
			t.Fatalf("build 日志缺少 %s（装配顺序不可观测）", step)
		}
		if idx <= prev {
			t.Fatalf("build 顺序错误：%s 未按 db→outbox→manager→telegram 递增", step)
		}
		prev = idx
	}
}

func TestReadyTransitions(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	src := newFakeSource()
	db := openTestDB(t)

	a, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger), WithSource(src))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 未 Run：source 未启动，ready 必须失败。
	if err := a.Ready(context.Background()); err == nil {
		t.Fatal("Ready before Run = nil, want error（source 未启动）")
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	// Run 后：全部就绪条件满足 → ready 通过。
	waitFor(t, 3*time.Second, func() bool { return a.Ready(context.Background()) == nil }, "ready after Run")

	// 409 冲突：ready 必须转为失败。
	src.errs <- telegram.ErrConflict
	waitFor(t, 3*time.Second, func() bool {
		err := a.Ready(context.Background())
		return err != nil && strings.Contains(err.Error(), "conflict")
	}, "ready failure after 409 conflict")

	// 优雅停止：Run 返回 nil，停止后 ready 再次失败。
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run = %v, want nil（优雅退出）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内优雅返回")
	}
	if err := a.Ready(context.Background()); err == nil {
		t.Fatal("Ready after Stop = nil, want error（组件已停止）")
	}
}

func TestBuildRollbackClosesDBOnError(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	db := openTestDB(t)
	boom := errors.New("migrate boom")

	_, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger),
		WithMigrate(func(context.Context, *sql.DB) error { return boom }))
	if !errors.Is(err, boom) {
		t.Fatalf("Build error = %v, want wrap %v", err, boom)
	}
	// 错误回滚：已打开的 db 必须被关闭。
	if err := db.PingContext(context.Background()); err == nil {
		t.Fatal("db 在 Build 失败后仍可 Ping，want 已关闭（错误回滚）")
	}
}

func TestStopOrderAndDrain(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	src := newFakeSource()
	db := openTestDB(t)
	sender := newRecordingSender(64)

	a, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger), WithSource(src), WithOutboxSender(sender.Send))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// 让 Room Manager 持有一个真实 Actor，验证「停止 Room Actors」步真实生效。
	if _, err := a.rooms.CreateRoom(context.Background(), game.UserID(100), room.NewRealClock(), fakeReducer{}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}

	// 运行中入队两条消息：worker 必须全部送达后再停止（Scheduler 的
	// Close-drain 内部保证由 outbox 包自身测试覆盖；App 层断言关闭完成）。
	if err := a.outbox.Enqueue(outbox.Message{ChatID: outbox.ChatID(5), Operation: "send_text", CorrelationID: "c1"}); err != nil {
		t.Fatalf("Enqueue c1: %v", err)
	}
	if err := a.outbox.Enqueue(outbox.Message{ChatID: outbox.ChatID(5), Operation: "send_text", CorrelationID: "c2"}); err != nil {
		t.Fatalf("Enqueue c2: %v", err)
	}
	got := map[string]bool{}
	waitFor(t, 3*time.Second, func() bool {
		for {
			select {
			case m := <-sender.ch:
				got[m.CorrelationID] = true
			default:
				return got["c1"] && got["c2"]
			}
		}
	}, "运行中两条消息送达")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := a.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// 停止顺序：source→commands→rooms→outbox→db。
	out := logBuf.String()
	steps := []string{"step=source_stopped", "step=commands_closed", "step=rooms_stopped", "step=outbox_drained", "step=db_closed"}
	prev := -1
	for _, s := range steps {
		idx := strings.Index(out, s)
		if idx < 0 {
			t.Fatalf("stop 日志缺少 %s", s)
		}
		if idx <= prev {
			t.Fatalf("stop 顺序错误：%s 位置未严格递增", s)
		}
		prev = idx
	}

	// 停止后各组件拒绝工作。
	if err := a.outbox.Enqueue(outbox.Message{ChatID: 1}); !errors.Is(err, outbox.ErrClosed) {
		t.Fatalf("Enqueue after stop = %v, want outbox.ErrClosed", err)
	}
	if _, err := a.rooms.Get("ROOM-X"); !errors.Is(err, room.ErrClosed) {
		t.Fatalf("rooms.Get after stop = %v, want room.ErrClosed", err)
	}
	if err := a.db.PingContext(context.Background()); err == nil {
		t.Fatal("db 在 Stop 后仍可 Ping，want 已关闭")
	}
}

func TestLeftoverRoomsAbortNotified(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	src := newFakeSource()
	db := openTestDB(t)
	scanner := &fakeScanner{rooms: []AbortedRoom{
		{Code: "ROOM-A", HostUserID: game.UserID(111), Phase: "lobby"},
		{Code: "ROOM-B", HostUserID: game.UserID(222), Phase: "night"},
	}}
	notifier := &fakeNotifier{}

	a, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger), WithSource(src),
		WithAbortScanner(scanner), WithAbortNotifier(notifier))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	// 启动扫描：遗留房间必须逐一通知，且 ready 不被通知阻塞。
	waitFor(t, 3*time.Second, func() bool { return len(notifier.snapshot()) == 2 }, "abort notify 两个遗留房间")
	got := notifier.snapshot()
	if got[0].Code != "ROOM-A" || got[0].HostUserID != 111 || got[0].Phase != "lobby" {
		t.Fatalf("notify[0] = %+v, want ROOM-A/111/lobby", got[0])
	}
	if got[1].Code != "ROOM-B" || got[1].HostUserID != 222 || got[1].Phase != "night" {
		t.Fatalf("notify[1] = %+v, want ROOM-B/222/night", got[1])
	}
	if err := a.Ready(context.Background()); err != nil {
		t.Fatalf("Ready after abort notify = %v, want nil（通知不阻塞 ready）", err)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内返回")
	}
}

func TestLeftoverAbortNotifyErrorIsNonFatal(t *testing.T) {
	var logBuf bytes.Buffer
	logger := mustLogger(t, &logBuf)
	src := newFakeSource()
	db := openTestDB(t)
	scanner := &fakeScanner{rooms: []AbortedRoom{{Code: "ROOM-C", HostUserID: game.UserID(333), Phase: "lobby"}}}
	notifier := &fakeNotifier{err: errors.New("notify boom")}

	a, err := Build(context.Background(), testConfig(),
		WithDB(db), WithLogger(logger), WithSource(src),
		WithAbortScanner(scanner), WithAbortNotifier(notifier))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(runCtx) }()

	waitFor(t, 3*time.Second, func() bool {
		return len(notifier.snapshot()) == 1 && a.Ready(context.Background()) == nil
	}, "notify error 不阻塞 ready")

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run = %v, want nil（通知失败仍优雅退出）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 5s 内返回")
	}
}
