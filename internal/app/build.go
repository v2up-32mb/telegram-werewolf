package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// defaultOutboxQueueCapacity 是每 Chat 队列容量上限。
// config 尚无容量字段（Task 17 决策：容量作为构造参数传入），按固定
// 保守值装配，后续如需要再挪入配置。
const defaultOutboxQueueCapacity = 64

// defaultDeduperCapacity 是有界 update_id 去重窗口容量。
const defaultDeduperCapacity = 1024

// defaultCallbackTokenCapacity 是回调 Token 容量上限。
const defaultCallbackTokenCapacity = 1024

// defaultLimiterBurst 是双层 token bucket 的桶容量。config 尚无 burst 字段
// （Task 18 决策：burst 作为构造参数传入），取 4 使短突发可容忍，
// 全局/单 Chat 每秒速率仍由配置严格约束。
const defaultLimiterBurst = 4

// Options 是 Build 的可注入装配项（默认取真实组件；测试注入替身）。
type Options struct {
	Logger         *slog.Logger
	DB             *sql.DB
	Source         telegram.UpdateSource
	OutboxSender   outbox.SendFunc
	Migrate        func(context.Context, *sql.DB) error
	AbortScanner   AbortScanner
	AbortNotifier  AbortNotifier
	CommandHandler CommandHandler
	TextHandler    TextHandler
	Wiring         *Wiring
}

// Option 配置 Build。
type Option func(*Options)

// WithLogger 注入日志器（默认按 cfg.LogFormat 写入 io.Discard）。
func WithLogger(l *slog.Logger) Option {
	return func(o *Options) { o.Logger = l }
}

// WithDB 注入已打开的 *sql.DB；Build 对其取得所有权，装配失败或 Stop 时关闭。
func WithDB(db *sql.DB) Option {
	return func(o *Options) { o.DB = db }
}

// WithSource 注入 UpdateSource 替身（跳过真实 Telegram 客户端构造）。
func WithSource(s telegram.UpdateSource) Option {
	return func(o *Options) { o.Source = s }
}

// WithOutboxSender 注入 Outbox 底层发送器（默认 stub：渲染与 Transport
// 映射属后续任务，MVP 只验证有序发送与 drain）。
func WithOutboxSender(s outbox.SendFunc) Option {
	return func(o *Options) { o.OutboxSender = s }
}

// WithMigrate 注入迁移函数（默认 storage.Migrate；错误回滚测试用）。
func WithMigrate(f func(context.Context, *sql.DB) error) Option {
	return func(o *Options) { o.Migrate = f }
}

// WithAbortScanner 注入遗留房间扫描器（默认 RoomRepository.ListActive）。
func WithAbortScanner(s AbortScanner) Option {
	return func(o *Options) { o.AbortScanner = s }
}

// WithAbortNotifier 注入中止通知器（默认向房主私聊入队 Outbox）。
func WithAbortNotifier(n AbortNotifier) Option {
	return func(o *Options) { o.AbortNotifier = n }
}

// WithCommandHandler 注入命令消费 seam（默认记日志占位）。
func WithCommandHandler(h CommandHandler) Option {
	return func(o *Options) { o.CommandHandler = h }
}

// WithTextHandler 注入玩家文本命令处理器：非 nil 时 App 对消息类
// Update 先走 DispatchText（保持 update_id 去重与 ACK），把原始文本
// 交给接线层（Task 41 命令面）解析；nil 时维持原 Router 文本路由。
func WithTextHandler(h TextHandler) Option {
	return func(o *Options) { o.TextHandler = h }
}

// WithWiring 注入生产接线组件：装配时把真实发送器、命令处理与文本
// 处理接入 Outbox/Router 链路。显式 WithOutboxSender / WithCommandHandler
// 优先级更高；Wiring nil 时维持默认 stub 语义（既有测试不受影响）。
func WithWiring(w *Wiring) Option {
	return func(o *Options) { o.Wiring = w }
}

// sqliteCursorStore 把 Task 13 sqlc 游标查询适配为 telegram.CursorStore：
// bot_update_cursor 单行 id=1；无记录视为 0。
type sqliteCursorStore struct {
	db *sql.DB
}

func (s sqliteCursorStore) Load(ctx context.Context) (int64, error) {
	row, err := sqlc.New(s.db).GetUpdateCursor(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.UpdateID, nil
}

func (s sqliteCursorStore) Save(ctx context.Context, updateID int64) error {
	return sqlc.New(s.db).UpsertUpdateCursor(ctx, updateID)
}

// defaultCommandHandler 是命令消费占位 seam；玩法执行属阶段 P0，后续任务接线。
type defaultCommandHandler struct {
	log *slog.Logger
}

func (h defaultCommandHandler) Handle(_ context.Context, cmd game.Command) error {
	h.log.Warn("app: command not yet routed (P0 wiring pending)", "command", fmt.Sprintf("%T", cmd))
	return nil
}

// defaultAbortScanner 用 RecoveryRepository 扫描遗留房间（含全部参与者，
// B2；docs/技术选型.md §8.2 room_players 保留通知所需信息）。
type defaultAbortScanner struct {
	db *sql.DB
}

func (s defaultAbortScanner) ListLeftover(ctx context.Context) ([]AbortedRoom, error) {
	rooms, err := storage.NewRecoveryRepository(s.db).ListInterruptedRoomsOnStartup(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AbortedRoom, 0, len(rooms))
	for _, ir := range rooms {
		players := make([]game.UserID, 0, len(ir.Players))
		for _, p := range ir.Players {
			players = append(players, game.UserID(p.UserID))
		}
		out = append(out, AbortedRoom{
			Code:       game.RoomID(ir.Room.RoomCode),
			HostUserID: game.UserID(ir.Room.HostUserID),
			Players:    players,
			Phase:      ir.Room.Phase,
		})
	}
	return out, nil
}

// defaultAbortNotifier 向房间全部参与者私聊入队 Outbox「游戏结束」通知
// （B2：不只房主，docs 游戏流程设计.md §五 容灾）。
type defaultAbortNotifier struct {
	log    *slog.Logger
	outbox *outbox.Scheduler
}

func (n defaultAbortNotifier) NotifyAbort(_ context.Context, r AbortedRoom) error {
	players := r.Players
	if len(players) == 0 {
		players = []game.UserID{r.HostUserID}
	}
	for _, user := range players {
		msg := outbox.Message{
			CorrelationID: "abort:" + string(r.Code),
			RoomID:        r.Code,
			ChatID:        outbox.ChatID(user),
			Operation:     telegram.OpSendText,
			Priority:      outbox.PriorityHigh,
		}
		if err := n.outbox.Enqueue(msg); err != nil {
			return fmt.Errorf("app: enqueue abort notification to %d: %w", user, err)
		}
	}
	n.log.Info("app: abort notifications enqueued", "room", string(r.Code), "players", len(players))
	return nil
}

// outboxSendStub 是 Outbox 底层发送占位 seam：消息渲染与 Telegram
// Transport 映射（outbox.Message → telegram.Params）属后续任务；MVP 只
// 保证 Outbox 组件可接收、有序、关闭时 drain 不丢消息。
func outboxSendStub(log *slog.Logger) outbox.SendFunc {
	return func(_ context.Context, msg outbox.Message) error {
		log.Debug("app: outbox send (stub)", "operation", msg.Operation, "chat", int64(msg.ChatID), "correlation", msg.CorrelationID)
		return nil
	}
}

// Build 手动依赖注入装配全部组件（不使用 DI 框架）。
//
// 顺序：配置校验 → 打开 DB → 迁移 → Ping → 仓储 → Outbox 链
// （Limiter → RetryingSender → Scheduler + Coalescer）→ Room Manager →
// Telegram（cursor/deduper/token/router/source）→ 健康检查。
// 装配中途任一步失败：已打开资源全部关闭并返回带上下文的错误（错误回滚）。
func Build(ctx context.Context, cfg *config.Config, opts ...Option) (*App, error) {
	if cfg == nil {
		return nil, errors.New("app: nil config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("app: invalid config: %w", err)
	}
	var o Options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	log := o.Logger
	if log == nil {
		var err error
		log, err = observability.NewLogger(cfg.LogFormat, io.Discard)
		if err != nil {
			return nil, fmt.Errorf("app: new logger: %w", err)
		}
	}

	var db *sql.DB
	if o.DB != nil {
		db = o.DB
	} else {
		var err error
		db, err = storage.Open(cfg.DatabasePath, storage.DefaultMaxOpenConns)
		if err != nil {
			return nil, fmt.Errorf("app: open db: %w", err)
		}
	}
	owned := true
	defer func() {
		if owned {
			_ = db.Close()
		}
	}()

	migrate := o.Migrate
	if migrate == nil {
		migrate = func(_ context.Context, d *sql.DB) error { return storage.Migrate(d) }
	}
	if err := migrate(ctx, db); err != nil {
		return nil, fmt.Errorf("app: migrate: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("app: ping db: %w", err)
	}
	log.Info("app: build", "step", "db")

	// Outbox 链：底层发送 → 限速 → 重试 → Scheduler；Coalescer 供后续渲染任务使用。
	baseSend := o.OutboxSender
	if baseSend == nil {
		baseSend = outboxSendStub(log)
	}
	limiter := outbox.NewLimiter(
		float64(cfg.Outbox.GlobalRateLimitPerSecond),
		float64(cfg.Outbox.PerChatRateLimitPerSecond),
		defaultLimiterBurst,
	)
	limitedSend := func(ctx context.Context, msg outbox.Message) error {
		if err := limiter.Wait(ctx, msg.ChatID); err != nil {
			return err
		}
		return baseSend(ctx, msg)
	}
	retrying := outbox.NewRetryingSender(limitedSend, outbox.RetryPolicy{
		MaxRetries:    cfg.Outbox.MaxRetries,
		RetryInterval: cfg.Outbox.RetryInterval.Duration,
	}, nil)
	scheduler := outbox.NewScheduler(retrying.Send, defaultOutboxQueueCapacity,
		// 发送失败不再静默丢弃（Task 46 冒烟缺陷）：记录房间/会话/操作与
		// 错误类别，不打印 Payload 全文（日志脱敏，docs/测试验收清单.md S16）。
		outbox.WithSendErrorHandler(func(msg outbox.Message, err error) {
			log.Error("app: outbox send failed", "room", string(msg.RoomID), "chat", int64(msg.ChatID), "op", msg.Operation, "error", err)
		}),
	)
	coalescer := outbox.NewCoalescer()
	log.Info("app: build", "step", "outbox")

	// 生产接线（Task 46 缺陷修复）：Attach 注入真实发送器与命令/文本
	// 处理器；显式 Option 优先于 Wiring 默认。回调动作处理器由 Wiring
	// 提供（B1-b：Router.DispatchAction 分流 reducer 动作与导演信号）。
	var actionHandler CallbackActionHandler
	if o.Wiring != nil {
		if err := o.Wiring.Attach(db, scheduler); err != nil {
			return nil, fmt.Errorf("app: attach wiring: %w", err)
		}
		if o.OutboxSender == nil {
			baseSend = o.Wiring.OutboxSender()
		}
		if o.CommandHandler == nil {
			o.CommandHandler = o.Wiring.CommandHandler()
		}
		if o.TextHandler == nil {
			o.TextHandler = o.Wiring.TextHandler()
		}
		actionHandler = o.Wiring.ActionHandler()
	}

	// Room Manager：MVP 先用内存注册表（storage 唯一约束 Registry 适配属后续任务）。
	// clock/reducer 是 CreateRoom 时的房间创建期参数，由后续 P0 命令 handler 注入。
	rooms := room.NewManager(room.ManagerOptions{})
	log.Info("app: build", "step", "manager")

	// Telegram：cursor store（sqlc）→ Deduper → CallbackManager → Router → Source。
	// B1-c：Router 与 Wiring 共用同一 CallbackManager，导演下发的按钮 token
	// 才能被 Router 校验接收。
	cursor := sqliteCursorStore{db: db}
	deduper := telegram.NewDeduper(defaultDeduperCapacity)
	tokens := telegram.NewCallbackManager(defaultCallbackTokenCapacity)
	if o.Wiring != nil && o.Wiring.Tokens() != nil {
		tokens = o.Wiring.Tokens()
	}
	router := telegram.NewRouter(deduper, cursor, tokens)
	initialOffset := router.InitialOffset(ctx)
	source := o.Source
	if source == nil {
		var err error
		source, err = telegram.NewLongPollingSource(cfg.BotToken, initialOffset, telegram.WithSourceServerURL(cfg.BotAPIBaseURL))
		if err != nil {
			return nil, fmt.Errorf("app: telegram source: %w", err)
		}
	}
	log.Info("app: build", "step", "telegram")

	handler := o.CommandHandler
	if handler == nil {
		handler = defaultCommandHandler{log: log}
	}

	scanner := o.AbortScanner
	if scanner == nil {
		scanner = defaultAbortScanner{db: db}
	}
	notifier := o.AbortNotifier
	if notifier == nil {
		notifier = defaultAbortNotifier{log: log, outbox: scheduler}
	}
	abortRepo := storage.NewRecoveryRepository(db)

	a := &App{
		cfg:       cfg,
		log:       log,
		db:        db,
		outbox:    scheduler,
		coalescer: coalescer,
		rooms:     rooms,
		source:    source,
		router:    router,
		metrics:   observability.NewMetrics(),
		scanner:   scanner,
		notifier:  notifier,
		abortRepo: abortRepo,
		wiring:    o.Wiring,
		handler:   handler,
		action:    actionHandler,
		text:      o.TextHandler,
	}
	a.migrated.Store(true)

	if cfg.HealthAddress != "" {
		// config.Validate 已保证 health_address 为合法 host:port。
		a.health = &http.Server{Addr: cfg.HealthAddress, Handler: observability.NewHealthHandler(a.readyChecks())}
		log.Info("app: build", "step", "health", "addr", cfg.HealthAddress)
	}

	owned = false
	return a, nil
}
