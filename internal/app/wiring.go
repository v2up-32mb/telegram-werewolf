package app

// 生产接线层（Task 46 冒烟缺陷修复）：
// 把 internal/telegram 既有适配器（命令面/创建/加入/大厅）与 game 领域
// 服务、storage、Outbox、Telegram Transport/Client 连接成真实链路。
// 设计依据：docs/技术选型.md §6 房间 Actor、§7.1 进程内 Outbox；
// 参考 wiring 契约 internal/app/testharness_test.go（p0World/mvpDriver）。
//
// 本文件不修改既有默认装配（Build 无 Wiring 时仍维持 stub 语义，
// 既有单元测试不受影响）；cmd/werewolf serve 显式注入 Wiring。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// WiringOption 配置 Wiring 构造（测试注入替身）。
type WiringOption func(*wiringOptions)

type wiringOptions struct {
	client telegram.Client // nil → NewClient(cfg.BotToken)
}

// WithWiringClient 注入 Telegram Client（测试用替身；生产默认真实 Client）。
func WithWiringClient(c telegram.Client) WiringOption {
	return func(o *wiringOptions) { o.client = c }
}

// Wiring 是生产接线组件集合：命令面、效果管线、真实 Outbox 发送器。
// Attach 前不持有 DB/Outbox（由 Build 装配后注入）。
type Wiring struct {
	cfg      *config.Config
	log      *slog.Logger
	renderer *i18n.Renderer
	opts     wiringOptions

	db     *sql.DB
	outbox *outbox.Scheduler
	// coalescer 对尚未发送的低优先级阶段更新按 (ChatID, CoalesceKey) 合并
	//（I1b，docs 技术选型.md §7.1：面板刷新等滚动更新只保留最新版本）。
	coalescer *outbox.Coalescer
	// coalescerStop/coalescerDone 控制自动轮询 flusher 生命周期
	//（stop 请求 → 退出 → done 关闭；测试与停机用）。
	coalescerStop chan struct{}
	coalescerDone chan struct{}
	stopOnce      sync.Once
	reg           *liveRegistry
	repo          *storage.RoomRepository
	users         *storage.UserRepository
	// settleStore 是结算持久化 seam（I1 重试测试注入用）；Attach 默认创建
	// 真实 repo，测试可替换为"首次失败后续成功"的包装实现。
	settleStore settleStore
	// limiter 是 Outbox 链的限速器（P1：SendTemporary 直发路径也必须
	// 经过它，防止错误风暴绕过 Telegram flood 保护）。Build 装配时注入
	// 与 limitedSend 同一实例；nil 时 SendTemporary 降级不限流。
	limiter *outbox.Limiter
	now     func() time.Time

	commands *telegram.CommandsHandler
	handler  *callbackHandler
	text     *textHandler
	sendFn   outbox.SendFunc

	// settings 是房间设置服务（建房后大厅回调 SettingsCommand 使用）。
	settings game.SettingsService
	// tokens 是回调令牌注册表：与 Router 共用同一实例（B1-c），导演向
	// 该注册表下发按钮 token，Router 校验同名 token（docs/技术选型.md §7.3）。
	tokens *telegram.CallbackManager
	// director 是局内导演（B1-d）：挂在 Actor.OnApplied 上驱动阶段推进与扇出。
	director *roomDirector
	// life 是大厅生命周期服务（闲置回收评估，I7）。
	life game.LobbyLifecycleService

	// 主消息滚动编辑（Item 1：docs 阶段消息设计.md §3/§4）。
	// viewer 追踪每 Chat 每时间段主消息页（3000 字符软分页）；mainBody 保存
	// 当前页累计正文（供编辑）；mainMsgID 记录已发送的消息 ID（productionSend
	// 回填，供下一次编辑复用）。
	viewer    *telegram.Viewer
	mainBody  map[mainPeriodKey]string
	mainMsgID map[mainPeriodKey]int64
	// mainMu 保护 mainBody/mainMsgID：actor goroutine（appendMain 写正文）与
	// outbox worker（productionSendMain 读写消息 ID）并发访问，Go map 非安全。
	mainMu sync.Mutex

	// 身份卡媒体（Item 2）：botOnce/botID 惰性缓存 GetMe 结果（缓存键依据）。
	botOnce sync.Once
	botID   int64
	botErr  error
}

// mainPeriodKey 标识一个 Chat 的某个时间段主消息。
type mainPeriodKey struct {
	chat   int64
	period string
}

// NewWiring 创建生产接线（此时不触网；Client 创建延后到首次发送时按需）。
// renderer 构造失败（如 locale 资源缺失）返回错误。
func NewWiring(ctx context.Context, cfg *config.Config, log *slog.Logger, oo ...WiringOption) (*Wiring, error) {
	var o wiringOptions
	for _, opt := range oo {
		if opt != nil {
			opt(&o)
		}
	}
	renderer, err := i18n.NewRenderer(cfg.DefaultLocale)
	if err != nil {
		return nil, fmt.Errorf("app: wiring renderer: %w", err)
	}
	w := &Wiring{
		cfg:      cfg,
		log:      log,
		renderer: renderer,
		opts:     o,
		now:      time.Now,
	}
	return w, nil
}

// Attach 由 Build 在 DB 打开、Outbox 装配完成后注入依赖并构造全部组件。
// 幂等：重复调用返回错误。
func (w *Wiring) Attach(db *sql.DB, sched *outbox.Scheduler) error {
	if w.db != nil || w.outbox != nil {
		return errors.New("app: wiring already attached")
	}
	w.db = db
	w.outbox = sched
	w.tokens = telegram.NewCallbackManager(defaultCallbackTokenCapacity)
	w.director = newDirector(w)
	w.coalescer = outbox.NewCoalescer()
	w.coalescerDone = make(chan struct{})
	w.coalescerStop = make(chan struct{})
	w.startCoalescerFlusher()
	w.viewer = telegram.NewViewer()
	w.mainBody = make(map[mainPeriodKey]string)
	w.mainMsgID = make(map[mainPeriodKey]int64)

	w.repo = storage.NewRoomRepository(db)
	w.users = storage.NewUserRepository(db)
	w.settleStore = storage.NewSettlementRepository(db)
	w.reg = newLiveRegistry()

	registry := roomRegistryAdapter{repo: w.repo}
	lobby, err := game.NewLobbyService(registry, nil)
	if err != nil {
		return fmt.Errorf("app: wiring lobby service: %w", err)
	}
	join, err := game.NewJoinService(joinStoreAdapter{repo: w.repo, users: w.users}, nil)
	if err != nil {
		return fmt.Errorf("app: wiring join service: %w", err)
	}
	life, err := game.NewLobbyLifecycleService(lifecycleClock{now: w.now})
	if err != nil {
		return fmt.Errorf("app: wiring lifecycle service: %w", err)
	}
	w.life = life
	settingsRepo := roomSettingsAdapter{repo: w.repo}
	settingsSvc, err := game.NewSettingsService(settingsRepo)
	if err != nil {
		return fmt.Errorf("app: wiring settings service: %w", err)
	}

	createA := createRoomAdapter{lobby: lobby, reg: w.reg, repo: w.repo, users: w.users, now: w.now}
	joinA := joinRoomAdapter{join: join, reg: w.reg, users: w.users, now: w.now}
	leaveA := leaveServiceAdapter{life: life, reg: w.reg, repo: w.repo}
	rolesA := roleServiceAdapter{reg: w.reg}
	scoresA := scoreServiceAdapter{db: db}
	sender := replySender{w: w}

	cmdHandler, err := telegram.NewCommandsHandler(w.renderer, sender, createA, joinA, leaveA, rolesA, scoresA)
	if err != nil {
		return fmt.Errorf("app: wiring commands handler: %w", err)
	}
	w.commands = cmdHandler
	w.handler = &callbackHandler{w: w}
	w.settings = settingsSvc
	w.text = &textHandler{w: w, commands: cmdHandler, reg: w.reg}
	w.sendFn = w.productionSend
	return nil
}

// OutboxSender 返回真实 Outbox 底层发送器（outbox.Message →
// telegram.Params → Transport → Client）。
func (w *Wiring) OutboxSender() outbox.SendFunc { return w.sendFn }

// replySenderForTest 暴露 replySender 供测试直接驱动 SendTemporary 等
// 方法（replySender 为小写类型，外部测试文件无法构造）。
func (w *Wiring) replySenderForTest() replySender { return replySender{w: w} }

// AttachLimiter 注入与 Build 装配的 Outbox 链共享的限流器实例（P1）。
// 必须在 Attach 之后、App 启动前调用；nil 无效果。
func (w *Wiring) AttachLimiter(l *outbox.Limiter) {
	if l != nil {
		w.limiter = l
	}
}

// CommandHandler 返回回调/房间命令处理器（Router 派发的领域命令）。
func (w *Wiring) CommandHandler() CommandHandler { return w.handler }

// ActionHandler 返回回调动作处理器（B1-b）：校验后的 callback 动作 → 命令
// 或导演本地信号（end_speech 等）。App 对回调更新经 Router.DispatchAction
// 分派到此。
func (w *Wiring) ActionHandler() CallbackActionHandler { return &callbackActionHandler{w: w} }

// Tokens 返回与 Router 共享的回调令牌注册表（Attach 后非 nil；B1-c）。
func (w *Wiring) Tokens() *telegram.CallbackManager { return w.tokens }

// IssueButton 为一名玩家在指定阶段/版本下发一个回调 token（不透明随机值，
// docs/技术选型.md §7.3：payload 只存服务端；目标按钮由导演在渲染临时
// 操作消息时经此下发）。未 Attach 时返回错误。
func (w *Wiring) IssueButton(owner game.UserID, action, target string, phase game.Phase, version uint64) (string, error) {
	if w.tokens == nil {
		return "", errors.New("app: callback tokens not attached")
	}
	return w.tokens.Issue(telegram.TokenPayload{
		Owner: owner, Action: action, Target: target,
		ExpectedPhase: phase, PhaseVersion: version,
	})
}

// TextHandler 返回玩家文本命令处理器（/start /newgame /join /role /score /leave /help /rank）。
func (w *Wiring) TextHandler() TextHandler { return w.text }

// RegisterCommands 通过 setMyCommands API 向 Telegram 注册斜杠命令菜单，
// 使用户在聊天框输入 / 时自动弹出命令提示。启动时调用一次。
func (w *Wiring) RegisterCommands(ctx context.Context) error {
	client, err := w.client()
	if err != nil {
		return err
	}
	commands, err := telegram.BotCommands(w.renderer)
	if err != nil {
		return fmt.Errorf("app: build bot commands: %w", err)
	}
	if err := client.SetMyCommands(ctx, commands); err != nil {
		return fmt.Errorf("app: register bot commands: %w", err)
	}
	w.log.Info("app: bot commands registered", "count", len(commands))
	return nil
}

// productionSend 是 Outbox 底层发送器：断言 Payload 为 telegram.Params
// 后按 Operation 经 Transport 分派（未知 op 返回明确错误，交由 Outbox
// 重试策略处理）。带 Period 的主消息：send 后回填消息 ID，edit 复用该 ID
// 实现同一时间段主消息滚动编辑（Item 1；docs 阶段消息设计.md §3/§4）。
func (w *Wiring) productionSend(ctx context.Context, msg outbox.Message) error {
	params, ok := msg.Payload.(telegram.Params)
	if !ok {
		return fmt.Errorf("app: outbox %q payload missing or not telegram.Params (%T)", msg.Operation, msg.Payload)
	}
	client, err := w.client()
	if err != nil {
		return err
	}
	if params.Period != "" {
		return w.productionSendMain(ctx, client, msg, params)
	}
	if msg.Operation == telegram.OpSendRoleCard {
		return w.sendRoleCard(ctx, params.ChatID, params.RoleCardRole, params.RoleCardSeat)
	}
	tr := telegram.NewTransport(client)
	if err := tr.Send(ctx, msg.Operation, params); err != nil {
		return classifyTelegramError(msg, err)
	}
	w.log.Info("app: telegram sent", "op", msg.Operation, "chat", int64(msg.ChatID), "summary", summarize(params.Text))
	return nil
}

// productionSendMain 处理主消息 send->edit 滚动：send 回填 ID，edit 复用
// （同一 Chat 同一时间段 FIFO 保证 send 先于 edit 处理）。
func (w *Wiring) productionSendMain(ctx context.Context, client telegram.Client, msg outbox.Message, params telegram.Params) error {
	key := mainPeriodKey{params.ChatID, params.Period}
	switch msg.Operation {
	case telegram.OpSendText:
		sent, err := client.SendMessage(ctx, telegram.SendMessageParams{
			ChatID: params.ChatID, Text: params.Text, ParseMode: params.ParseMode,
		})
		if err != nil {
			return classifyTelegramError(msg, err)
		}
		w.mainMu.Lock()
		w.mainMsgID[key] = int64(sent.MessageID)
		w.mainMu.Unlock()
		w.log.Info("app: main msg created", "chat", params.ChatID, "period", params.Period, "id", sent.MessageID)
		return nil
	case telegram.OpEditMessage:
		w.mainMu.Lock()
		id := w.mainMsgID[key]
		w.mainMu.Unlock()
		if id == 0 {
			// 防御：主消息尚未创建（不应发生）：退回 send，保证内容不丢。
			sent, err := client.SendMessage(ctx, telegram.SendMessageParams{
				ChatID: params.ChatID, Text: params.Text, ParseMode: params.ParseMode, ReplyMarkup: params.ReplyMarkup,
			})
			if err != nil {
				return classifyTelegramError(msg, err)
			}
			w.mainMu.Lock()
			w.mainMsgID[key] = int64(sent.MessageID)
			w.mainMu.Unlock()
			return nil
		}
		if _, err := client.EditMessageText(ctx, telegram.EditMessageParams{
			ChatID: params.ChatID, MessageID: int(id), Text: params.Text, ParseMode: params.ParseMode, ReplyMarkup: params.ReplyMarkup,
		}); err != nil {
			return classifyTelegramError(msg, err)
		}
		return nil
	default:
		tr := telegram.NewTransport(client)
		if err := tr.Send(ctx, msg.Operation, params); err != nil {
			return classifyTelegramError(msg, err)
		}
		return nil
	}
}

// classifyTelegramError 把 Telegram 侧错误映射为 Outbox 重试语义：
//   - 400/403（ErrBadRequest/ErrForbidden）→ outbox.PermanentError，
//     永久失败不重试（Task 46 冒烟缺陷：newgame 创建确认被 400 当
//     临时错误重试约 9 秒后静默丢弃）；
//   - 429（*telegram.RateLimitError）→ outbox.RateLimitedError，
//     保留服务端 RetryAfter，令 RetryingSender 按建议延迟退避；
//   - 其余临时/网络错误原样透传，由 Outbox 按 RetryInterval 重试。
func classifyTelegramError(msg outbox.Message, err error) error {
	var rle *telegram.RateLimitError
	switch {
	case errors.Is(err, telegram.ErrBadRequest), errors.Is(err, telegram.ErrForbidden):
		return &outbox.PermanentError{Err: fmt.Errorf("app: telegram send %q %w", msg.Operation, err)}
	case errors.As(err, &rle):
		return &outbox.RateLimitedError{RetryAfter: rle.RetryAfter}
	default:
		return fmt.Errorf("app: telegram send %q: %w", msg.Operation, err)
	}
}

// summarize 返回文本前 300 个 rune 的单行摘要（冒烟证据/排障用；
// 覆盖房间面板全文，只截取文案，不涉及 token 等秘密）。
func summarize(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) > 300 {
		runes = runes[:300]
	}
	return strings.Join(strings.Fields(string(runes)), " ")
}

// client 惰性构造真实 Client（含 getMe 认证）；失败错误可重试。
func (w *Wiring) client() (telegram.Client, error) {
	if w.opts.client != nil {
		return w.opts.client, nil
	}
	c, err := telegram.NewClient(w.cfg.BotToken, telegram.WithServerURL(w.cfg.BotAPIBaseURL))
	if err != nil {
		return nil, fmt.Errorf("app: telegram client: %w", err)
	}
	w.opts.client = c
	return c, nil
}

// ---------------------------------------------------------------------------
// Outbox 消息构造与效果管线
// ---------------------------------------------------------------------------

// enqueue 把一条 operation 消息投递给 Outbox：可合并消息（CoalesceKey 非空）
// 先进 Coalescer 合并（同 key 只保留最新版本），其余直投 Scheduler
// （docs 技术选型.md §7.1）。
func (w *Wiring) enqueue(corr string, roomID game.RoomID, chat int64, op string, params telegram.Params, prio outbox.Priority, coalesce string) error {
	if w.outbox == nil {
		return fmt.Errorf("app: wiring outbox not attached")
	}
	msg := outbox.Message{
		CorrelationID: corr,
		RoomID:        roomID,
		ChatID:        outbox.ChatID(chat),
		Operation:     op,
		Priority:      prio,
		CoalesceKey:   coalesce,
		Payload:       params,
	}
	if coalesce != "" {
		w.coalescer.Submit(msg)
		return nil
	}
	return w.outbox.Enqueue(msg)
}

// startCoalescerFlusher 启动自动轮询把 Coalescer 待发消息送入 Scheduler
// （I1b，docs 技术选型.md §7.1）。生产默认使用；测试关闭它并走确定性
// drainCoalesced，避免 20ms 轮询与断言窗口的竞争（2026-08-18 CI race job：
// 5 条突发面板可能被轮询拆成两批 → 两次发送）。
func (w *Wiring) startCoalescerFlusher() {
	go func() {
		defer close(w.coalescerDone)
		for {
			select {
			case <-w.coalescerStop:
				return
			default:
			}
			m, ok := w.coalescer.Next()
			if !ok {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if err := w.outbox.Enqueue(m); err != nil {
				if errors.Is(err, outbox.ErrClosed) {
					return
				}
				w.log.Warn("app: coalescer flush enqueue", "error", err)
			}
		}
	}()
}

// stopCoalescerFlusher 停止自动轮询并等待退出（幂等；App 停机/测试清理用）。
func (w *Wiring) stopCoalescerFlusher() {
	w.stopOnce.Do(func() {
		close(w.coalescerStop)
		<-w.coalescerDone
	})
}

// drainCoalesced 确定性冲刷 Coalescer 全部待发消息到 Scheduler（测试/同步
// 路径；生产仍用自动轮询）。
func (w *Wiring) drainCoalesced() {
	for {
		m, ok := w.coalescer.Next()
		if !ok {
			return
		}
		if err := w.outbox.Enqueue(m); err != nil {
			if errors.Is(err, outbox.ErrClosed) {
				return
			}
			w.log.Warn("app: coalescer drain enqueue", "error", err)
		}
	}
}

// sendText 渲染 i18n 文案并作为 send_text 投递（默认 MarkdownV2）。
func (w *Wiring) sendText(ctx context.Context, corr string, roomID game.RoomID, chat int64, key string, data map[string]any) error {
	text, err := w.renderer.Render(key, data)
	if err != nil {
		return fmt.Errorf("app: render %q: %w", key, err)
	}
	return w.enqueue(corr, roomID, chat, telegram.OpSendText, telegram.Params{ChatID: chat, Text: text}, outbox.PriorityNormal, "")
}

// applyEffects 执行领域 Effects：MessageEffect → 渲染 → Outbox；
// PersistEffect 已由各适配器显式持久化（此处仅告警未识别类型）。
func (w *Wiring) applyEffects(ctx context.Context, corr string, roomID game.RoomID, actor game.UserID, effects []game.Effect) error {
	for _, e := range effects {
		switch te := e.(type) {
		case game.MessageEffect:
			chat, err := wiringAudienceChat(te.Audience, roomID, actor, w.reg)
			if err != nil {
				w.log.Warn("app: effects audience skip", "room", string(roomID), "key", te.Key, "error", err)
				continue
			}
			coalesce := ""
			if te.Key == game.LobbyPanelMessageKey {
				coalesce = "panel:" + string(roomID)
			}
			text, mk, err := w.renderMessage(te, roomID)
			if err != nil {
				w.log.Error("app: render message effect", "room", string(roomID), "key", te.Key, "error", err)
				continue
			}
			if err := w.enqueue(corr, roomID, chat, telegram.OpSendText, telegram.Params{ChatID: chat, Text: text, ReplyMarkup: mk}, outbox.PriorityNormal, coalesce); err != nil {
				return err
			}
		case game.PersistEffect:
			// 创建/加入/退出的持久化在服务适配器内显式完成；此处不做重复写。
			w.log.Debug("app: persist effect handled by adapter", "room", string(roomID), "kind", te.Kind)
		default:
			w.log.Debug("app: effect ignored", "room", string(roomID), "type", fmt.Sprintf("%T", e))
		}
	}
	return nil
}

// renderMessage 把 MessageEffect 渲染为 MarkdownV2 文本：面板走专用
// 构建器（从房间状态取成员），其余走 i18n 文案。
func (w *Wiring) renderMessage(e game.MessageEffect, roomID game.RoomID) (string, *telegram.ReplyMarkup, error) {
	switch e.Key {
	case game.LobbyPanelMessageKey:
		return w.buildPanel(roomID)
	case game.SettingsUpdatedMessageKey:
		data := map[string]any{"room_code": e.Params["room_code"]}
		if v, ok := e.Params["password_set"].(bool); ok && v {
			data["Password"] = "已设置"
		} else {
			data["Password"] = "未设置"
		}
		text, err := w.renderer.Render(e.Key, data)
		return text, nil, err
	default:
		text, err := w.renderer.Render(e.Key, e.Params)
		return text, nil, err
	}
}

// buildPanel 从注册表房间状态 + storage 昵称构建房主面板（S3/S4）。
func (w *Wiring) buildPanel(roomID game.RoomID) (string, *telegram.ReplyMarkup, error) {
	st, _, _, _, ok := w.reg.snapshot(roomID)
	if !ok {
		return "", nil, errors.New("app: panel room not found")
	}

	code, err := w.renderer.Render("panel.room_code", map[string]any{"RoomCode": string(st.RoomID)})
	if err != nil {
		return "", nil, err
	}
	count, err := w.renderer.Render("panel.count", map[string]any{"Count": len(st.Players), "Max": game.MVPPlayerCount})
	if err != nil {
		return "", nil, err
	}
	title, err := w.renderer.Render("panel.title", nil)
	if err != nil {
		return "", nil, err
	}
	phase, err := w.renderer.Render("panel.phase_lobby", nil)
	if err != nil {
		return "", nil, err
	}
	header, err := w.renderer.Render("panel.members_header", nil)
	if err != nil {
		return "", nil, err
	}
	invite, err := w.renderer.Render("panel.invite_line", map[string]any{"RoomCode": string(st.RoomID)})
	if err != nil {
		return "", nil, err
	}

	var lines []string
	for _, p := range sortedPlayers(st.Players) {
		nick := ""
		if u, err := w.users.Load(context.Background(), p.UserID); err == nil {
			nick = u.Nickname
		}
		mark := ""
		if p.UserID == st.Lobby.Owner {
			mark = "（房主）"
			if m, err := w.renderer.Render("panel.host_mark", nil); err == nil {
				mark = m
			}
		}
		line, err := w.renderer.Render("panel.member_line", map[string]any{"Seat": p.Seat, "Nickname": nick, "Mark": mark})
		if err != nil {
			return "", nil, err
		}
		lines = append(lines, line)
	}

	labels := make(map[string]string, 3)
	for _, k := range []string{"panel.button.start", "panel.button.settings", "panel.button.dismiss"} {
		label, err := w.renderer.Render(k, nil)
		if err != nil {
			return "", nil, err
		}
		labels[k] = label
	}

	actions := []struct {
		labelKey string
		action   string
	}{
		{"panel.button.start", "start_game"},
		{"panel.button.settings", "settings"},
		{"panel.button.dismiss", "host_dissolve"},
	}
	var rows [][]telegram.InlineButton
	var cur []telegram.InlineButton
	for _, it := range actions {
		tok, err := w.IssueButton(st.Lobby.Owner, it.action, "", game.PhaseLobby, st.PhaseVersion)
		if err != nil {
			return "", nil, fmt.Errorf("app: issue lobby button %q: %w", it.action, err)
		}
		cur = append(cur, telegram.InlineButton{Text: labels[it.labelKey], CallbackData: tok})
		if len(cur) == 3 {
			rows = append(rows, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		rows = append(rows, cur)
	}
	markup := &telegram.ReplyMarkup{Rows: rows}

	var b strings.Builder
	b.WriteString(title)
	b.WriteByte('\n')
	b.WriteString(code)
	b.WriteByte('\n')
	b.WriteString(count)
	b.WriteByte('\n')
	b.WriteString(phase)
	b.WriteByte('\n')
	// 设置摘要行
	settings := st.Settings
	if settings == (game.RoomSettings{}) {
		settings = game.DefaultRoomSettings()
	}
	summary, err := w.renderer.Render("panel.settings_summary", map[string]any{
		"Victory":   w.victoryLabel(settings.Victory),
		"Speech":    w.speechModeLabel(settings.SpeechMode),
		"FastMode":  w.boolLabel(settings.FastMode),
		"WolfKill":  w.wolfKillLabel(settings.WolfMustKill),
		"Reveal":    w.revealLabel(settings.RevealRoleOnDeath),
		"WitchSave": w.witchSaveLabel(settings.WitchSelfSaveFirstNight),
	})
	if err == nil {
		b.WriteString(summary)
		b.WriteByte('\n')
	}
	b.WriteString(header)
	if len(lines) > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Join(lines, "\n"))
	}
	b.WriteByte('\n')
	b.WriteString(invite)
	return b.String(), markup, nil
}

func sortedPlayers(ps []game.Player) []game.Player {
	out := append([]game.Player(nil), ps...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Seat < out[j-1].Seat; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// buildSettingsPanel 构建房间设置面板文本与 inline keyboard。
// 每个设置项一行按钮，最后一行为"返回大厅"按钮。
func (w *Wiring) buildSettingsPanel(roomID game.RoomID) (string, *telegram.ReplyMarkup, error) {
	st, _, host, _, ok := w.reg.snapshot(roomID)
	if !ok {
		return "", nil, errors.New("app: settings panel room not found")
	}
	settings := st.Settings
	if settings == (game.RoomSettings{}) {
		settings = game.DefaultRoomSettings()
	}

	title, err := w.renderer.Render("settings.panel_title", nil)
	if err != nil {
		return "", nil, err
	}

	// 设置项定义：label, target, 当前值文案, 描述文案
	type settingItem struct {
		label   string
		target  string
		current string
		desc    string
	}

	items := []settingItem{
		{w.mustRender("settings.speech_mode"), "speech_mode", w.speechModeLabel(settings.SpeechMode), w.mustRender("settings.speech_mode_desc")},
		{w.mustRender("settings.fast_mode"), "fast_mode", w.boolLabel(settings.FastMode), w.mustRender("settings.fast_mode_desc")},
		{w.mustRender("settings.wolf_must_kill"), "wolf_must_kill", w.wolfKillLabel(settings.WolfMustKill), w.mustRender("settings.wolf_must_kill_desc")},
		{w.mustRender("settings.reveal_role"), "reveal_role", w.revealLabel(settings.RevealRoleOnDeath), w.mustRender("settings.reveal_role_desc")},
		{w.mustRender("settings.witch_self_save"), "witch_self_save", w.witchSaveLabel(settings.WitchSelfSaveFirstNight), w.mustRender("settings.witch_self_save_desc")},
		{w.mustRender("settings.victory_mode"), "victory_mode", w.victoryLabel(settings.Victory), w.mustRender("settings.victory_mode_desc")},
	}

	var b strings.Builder
	b.WriteString(title)
	b.WriteByte('\n')
	for _, it := range items {
		b.WriteString(it.label)
		b.WriteString("（")
		b.WriteString(it.desc)
		b.WriteString("）：")
		b.WriteString(it.current)
		b.WriteByte('\n')
	}

	// 构建按钮
	var rows [][]telegram.InlineButton
	for _, it := range items {
		label := it.label + "：" + it.current
		tok, err := w.IssueButton(host, "settings_toggle", it.target, game.PhaseLobby, st.PhaseVersion)
		if err != nil {
			return "", nil, fmt.Errorf("app: issue settings toggle button %q: %w", it.target, err)
		}
		rows = append(rows, []telegram.InlineButton{{Text: label, CallbackData: tok}})
	}

	// 返回按钮
	backLabel, err := w.renderer.Render("settings.back", nil)
	if err != nil {
		return "", nil, err
	}
	backTok, err := w.IssueButton(host, "settings_back", "", game.PhaseLobby, st.PhaseVersion)
	if err != nil {
		return "", nil, fmt.Errorf("app: issue settings back button: %w", err)
	}
	rows = append(rows, []telegram.InlineButton{{Text: backLabel, CallbackData: backTok}})

	markup := &telegram.ReplyMarkup{Rows: rows}
	return b.String(), markup, nil
}

// buildDissolveConfirmPanel 构建解散确认面板（确认/取消按钮）。
func (w *Wiring) buildDissolveConfirmPanel(roomID game.RoomID) (string, *telegram.ReplyMarkup, error) {
	st, _, host, _, ok := w.reg.snapshot(roomID)
	if !ok {
		return "", nil, errors.New("app: dissolve confirm panel room not found")
	}

	text, err := w.renderer.Render("panel.dissolve_confirm", nil)
	if err != nil {
		text = "⚠️ 确认解散房间？"
	}

	confirmLabel, err := w.renderer.Render("panel.button.confirm_dissolve", nil)
	if err != nil {
		confirmLabel = "确认解散"
	}
	cancelLabel, err := w.renderer.Render("panel.button.cancel", nil)
	if err != nil {
		cancelLabel = "取消"
	}

	confirmTok, err := w.IssueButton(host, "host_dissolve", "confirm", game.PhaseLobby, st.PhaseVersion)
	if err != nil {
		return "", nil, fmt.Errorf("app: issue dissolve confirm button: %w", err)
	}
	cancelTok, err := w.IssueButton(host, "dissolve_cancel", "", game.PhaseLobby, st.PhaseVersion)
	if err != nil {
		return "", nil, fmt.Errorf("app: issue dissolve cancel button: %w", err)
	}

	markup := &telegram.ReplyMarkup{Rows: [][]telegram.InlineButton{
		{{Text: confirmLabel, CallbackData: confirmTok}, {Text: cancelLabel, CallbackData: cancelTok}},
	}}
	return text, markup, nil
}

// mustRender 渲染 i18n key，失败时返回 key 本身（不阻断面板构建）。
func (w *Wiring) mustRender(key string) string {
	s, err := w.renderer.Render(key, nil)
	if err != nil {
		return key
	}
	return s
}

func (w *Wiring) speechModeLabel(m game.SpeechMode) string {
	switch m {
	case game.SpeechFixed:
		return w.mustRender("settings.speech_fixed")
	case game.SpeechSoft:
		return w.mustRender("settings.speech_soft")
	default:
		return w.mustRender("settings.speech_fixed")
	}
}

func (w *Wiring) boolLabel(on bool) string {
	if on {
		return w.mustRender("settings.on")
	}
	return w.mustRender("settings.off")
}

func (w *Wiring) wolfKillLabel(mustKill bool) string {
	if mustKill {
		return w.mustRender("settings.wolf_must")
	}
	return w.mustRender("settings.wolf_allow_skip")
}

func (w *Wiring) revealLabel(reveal bool) string {
	if reveal {
		return w.mustRender("settings.reveal_yes")
	}
	return w.mustRender("settings.reveal_no")
}

func (w *Wiring) witchSaveLabel(canSave bool) string {
	if canSave {
		return w.mustRender("settings.witch_can_save")
	}
	return w.mustRender("settings.witch_cannot_save")
}

func (w *Wiring) victoryLabel(v game.VictoryMode) string {
	switch v {
	case game.VictorySide:
		return w.mustRender("settings.victory_side")
	default:
		return w.mustRender("settings.victory_slaughter")
	}
}

// wiringAudienceChat 把受众映射为 Telegram ChatID（MVP 私聊模型：
// UserID 即 ChatID，docs/技术选型.md §10 私聊限定）。
func wiringAudienceChat(a game.Audience, roomID game.RoomID, actor game.UserID, reg *liveRegistry) (int64, error) {
	switch a {
	case game.AudienceActor:
		return int64(actor), nil
	case game.AudienceHost:
		_, _, host, _, ok := reg.snapshot(roomID)
		if !ok {
			return 0, errors.New("app: host audience room not found")
		}
		return int64(host), nil
	default:
		return 0, fmt.Errorf("app: audience %d not supported in lobby wiring", a)
	}
}

// ---------------------------------------------------------------------------
// 房间注册表（生产：内存栈 + storage 持久化）
// ---------------------------------------------------------------------------

type liveRegistry struct {
	mu      sync.Mutex
	rooms   map[game.RoomID]*liveRoom
	byUser  map[game.UserID]game.RoomID
	pending map[game.RoomID][]pendingFX
}

type liveRoom struct {
	host  game.UserID
	st    game.State
	life  game.LobbyLifetime
	actor *room.Actor // nil 直到开局（StartGame 时引导）
}

// pendingFX 是命令面（CommandsHandler 丢弃 effects）与效果管线之间的
// 桥：适配器在服务成功后登记待发效果，文本处理器返回后统一冲刷。
type pendingFX struct {
	actor   game.UserID
	roomID  game.RoomID
	effects []game.Effect
}

func newLiveRegistry() *liveRegistry {
	return &liveRegistry{
		rooms:   make(map[game.RoomID]*liveRoom),
		byUser:  make(map[game.UserID]game.RoomID),
		pending: make(map[game.RoomID][]pendingFX),
	}
}

func (r *liveRegistry) create(st game.State, host game.UserID, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Settings 由接线层填充（docs「房间设置修改截止」：建房后配置快照；
	// 领域 State.Settings 零值表示未填充）。MVP 起点为默认配置；后续
	// SettingsService 修改经 room_settings 表持久化并按需刷新。
	if st.Settings == (game.RoomSettings{}) {
		st.Settings = game.DefaultRoomSettings()
	}
	r.rooms[st.RoomID] = &liveRoom{
		host: host,
		st:   st,
		life: game.LobbyLifetime{CreatedAt: now, ExpireAt: now.Add(game.IdleExpireAfter)},
	}
	r.byUser[host] = st.RoomID
}

func (r *liveRegistry) get(code game.RoomID) (*liveRoom, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	return lr, ok
}

// snapshot 返回房间状态的安全快照（锁内拷贝）：跨 goroutine 调用方
// （TrySpeak/SweepIdle/适配器）读取 lr.st/lr.actor/lr.host/lr.life 时，
// 避免与 Actor goroutine 的 updateState/takeActor 写发生数据竞争（I3）。
// 注意：st 是值拷贝，actor/host 是引用值；调用方不应依赖其后续变更。
func (r *liveRegistry) snapshot(code game.RoomID) (st game.State, actor *room.Actor, host game.UserID, life game.LobbyLifetime, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	if !ok {
		return game.State{}, nil, 0, game.LobbyLifetime{}, false
	}
	return lr.st, lr.actor, lr.host, lr.life, true
}

func (r *liveRegistry) roomOf(user game.UserID) (game.RoomID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	code, ok := r.byUser[user]
	return code, ok
}

func (r *liveRegistry) recordJoin(code game.RoomID, user game.UserID, seat game.Seat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	if !ok {
		return
	}
	lr.st.Players = append(append([]game.Player(nil), lr.st.Players...), game.Player{UserID: user, Seat: seat})
	lr.st.Processed["join:"+fmt.Sprint(user)] = true
	r.byUser[user] = code
}

func (r *liveRegistry) removePlayer(code game.RoomID, user game.UserID, newOwner game.UserID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	if !ok {
		return
	}
	var out []game.Player
	for _, p := range lr.st.Players {
		if p.UserID != user {
			out = append(out, p)
		}
	}
	lr.st.Players = out
	if newOwner != 0 {
		lr.st.Lobby.Owner = newOwner
		lr.host = newOwner
	}
	delete(r.byUser, user)
	if len(out) == 0 {
		delete(r.rooms, code)
	}
}

// removeRoom 移除房间（结算/解散/回收）：注销注册表与用户唯一约束。
func (r *liveRegistry) removeRoom(code game.RoomID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	if !ok {
		return
	}
	for _, p := range lr.st.Players {
		delete(r.byUser, p.UserID)
	}
	delete(r.rooms, code)
}

// roomCodes 返回全部注册房间码（I7 闲置回收迭代用）。
func (r *liveRegistry) roomCodes() []game.RoomID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]game.RoomID, 0, len(r.rooms))
	for code := range r.rooms {
		out = append(out, code)
	}
	return out
}

// takeActor 取出并清除房间的 Actor 引用（B3：解散/停机时停止 Actor 前
// 调用，防止后续 Dispatch 路径拿到已停止的 Actor 继续投递）。
func (r *liveRegistry) takeActor(code game.RoomID) (*room.Actor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lr, ok := r.rooms[code]
	if !ok || lr.actor == nil {
		return nil, false
	}
	actor := lr.actor
	lr.actor = nil
	return actor, true
}

// adoptActor 把新建的房间 Actor 写回注册表（I3：handleCommand 的
// start_game 引导 Actor 时使用；原代码直接写 lr.actor 字段，锁外
// 写入与 takeActor/更新读竞态，统一改经此持锁方法）。
func (r *liveRegistry) adoptActor(code game.RoomID, actor *room.Actor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lr, ok := r.rooms[code]; ok {
		lr.actor = actor
	}
}

// actors 返回全部房间 Actor 的快照（B3：App 停机统一停止 Wiring 创建的
// Actor——它们不归 room.Manager 管）。
func (r *liveRegistry) actors() []*room.Actor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*room.Actor, 0, len(r.rooms))
	for _, lr := range r.rooms {
		if lr.actor != nil {
			out = append(out, lr.actor)
		}
	}
	return out
}

// updateLifetime 更新房间闲置生命周期元数据（I7：续期/到期评估结果回写）。
func (r *liveRegistry) updateLifetime(code game.RoomID, lt game.LobbyLifetime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lr, ok := r.rooms[code]; ok {
		lr.life = lt
	}
}

func (r *liveRegistry) pushPending(roomID game.RoomID, actor game.UserID, fx []game.Effect) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[roomID] = append(r.pending[roomID], pendingFX{actor: actor, roomID: roomID, effects: fx})
}

// drainPending 冲刷全部待发效果并清空（文本命令处理成功后调用）。
func (r *liveRegistry) drainPending() []pendingFX {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []pendingFX
	for id, fx := range r.pending {
		out = append(out, fx...)
		delete(r.pending, id)
	}
	return out
}

// ---------------------------------------------------------------------------
// 文本命令面（Task 41 CommandsHandler + 待发效果桥）
// ---------------------------------------------------------------------------

type textHandler struct {
	w        *Wiring
	commands *telegram.CommandsHandler
	reg      *liveRegistry
}

func (h *textHandler) HandleText(ctx context.Context, u telegram.Update) error {
	if u.Message == nil {
		return nil
	}
	in := telegram.CommandInput{
		CommandID:  fmt.Sprintf("u%d", u.UpdateID),
		Actor:      game.UserID(u.Message.UserID),
		ChatID:     u.Message.ChatID,
		UserID:     u.Message.UserID,
		Text:       u.Message.Text,
		ReceivedAt: u.ReceivedAt,
		IsPrivate:  u.Message.ChatID == u.Message.UserID,
	}
	h.w.log.Info("app: text command", "update", u.UpdateID, "chat", u.Message.ChatID, "user", u.Message.UserID, "text", summarize(u.Message.Text))
	// 白天发言拦截（B1-d）：非斜杠命令且发送者为当前发言者时走导演发言，
	// 否则走命令面（/start /newgame /join /role /score /leave /help /rank）。
	if !isSlashCommand(in.Text) {
		if roomID, ok := h.reg.roomOf(in.Actor); ok {
			if h.w.director.trySpeak(roomID, in.Actor, in.ChatID, int64(u.Message.MessageID), in.Text) {
				return nil
			}
			if h.w.director.tryLastWords(roomID, in.Actor, in.CommandID, in.Text) {
				return nil
			}
		}
	}
	if err := h.commands.Handle(ctx, in); err != nil {
		// 命令面已尽量反馈；外部错误只记日志，仍返回 nil 以避免
		// update_id 卡死重投风暴（Router.DispatchText 以 nil 才提交 cursor）。
		h.w.log.Error("app: text command handling", "update", u.UpdateID, "error", err)
		return nil
	}
	for _, pf := range h.reg.drainPending() {
		if err := h.w.applyEffects(ctx, "cmd:"+string(pf.roomID)+"@"+fmt.Sprint(pf.actor), pf.roomID, pf.actor, pf.effects); err != nil {
			h.w.log.Error("app: flush pending effects", "room", string(pf.roomID), "error", err)
		}
	}
	// 斜杠命令处理后删除原始命令消息，保持聊天整洁。
	if isSlashCommand(in.Text) {
		_ = h.w.enqueue("del:"+fmt.Sprintf("u%d", u.UpdateID), "", u.Message.ChatID,
			telegram.OpDeleteMessage,
			telegram.Params{ChatID: u.Message.ChatID, MessageID: u.Message.MessageID},
			outbox.PriorityHigh, "")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 服务适配器（game 领域 seam 的生产实现）
// ---------------------------------------------------------------------------

// lifecycleClock 适配 time.Now 到 game.LifecycleClock。
type lifecycleClock struct{ now func() time.Time }

func (c lifecycleClock) Now() time.Time { return c.now() }

// roomRegistryAdapter 实现 game.LobbyRoomRegistry：storage rooms 表
// 是唯一性权威（HostActive=已建房；ReserveCode=房间码未占用）。
type roomRegistryAdapter struct{ repo *storage.RoomRepository }

func (a roomRegistryAdapter) HostActive(host game.UserID) bool {
	n, err := a.repo.CountHostRooms(context.Background(), host)
	return err == nil && n > 0
}

func (a roomRegistryAdapter) ReserveCode(ctx context.Context, code game.RoomID) (bool, error) {
	free, err := a.repo.CodeFree(ctx, code)
	return free, err
}

// joinStoreAdapter 实现 game.JoinStore：users/rooms/room_players 联合查询。
type joinStoreAdapter struct {
	repo  *storage.RoomRepository
	users *storage.UserRepository
}

func (a joinStoreAdapter) LoadPasswordHash(ctx context.Context, roomID game.RoomID) (string, error) {
	h, err := a.repo.RoomPasswordHash(ctx, roomID)
	if errors.Is(err, storage.ErrRoomNotFound) {
		return "", game.ErrRoomNotFound
	}
	return h, err
}

func (a joinStoreAdapter) CheckRoom(ctx context.Context, roomID game.RoomID) error {
	free, err := a.repo.CodeFree(ctx, roomID)
	if err != nil {
		return err
	}
	// 房间已存在（room 有玩家）或仅注册表内存存在：storage 里房间创建时
	// 即写入 rooms 行，因此 rooms 表存在即房间存在。
	if free {
		// rooms 行不存在不代表房间不存在（并发窗口）；以房间行优先判定。
		ok, err := a.repo.RoomExists(ctx, roomID)
		if err != nil {
			return err
		}
		if !ok {
			return game.ErrRoomNotFound
		}
	}
	return nil
}

func (a joinStoreAdapter) HasPlayer(ctx context.Context, roomID game.RoomID, user game.UserID) (bool, error) {
	return a.repo.PlayerInRoom(ctx, roomID, user)
}

func (a joinStoreAdapter) HasLeft(ctx context.Context, roomID game.RoomID, user game.UserID) (bool, error) {
	// MVP 单账号冒烟范围：大厅退出即删除 room_players 行，允许重新加入
	// （计划 S9「非进行中局不触发冷却」）。进行中局退出的重入封禁由领域
	// 状态（Player.Left）与既有 E2E 覆盖，属多账号追加项（M2）。
	return false, nil
}

func (a joinStoreAdapter) UserInRoom(ctx context.Context, user game.UserID) (bool, error) {
	return a.repo.UserInAnyRoom(ctx, user)
}

func (a joinStoreAdapter) ReservedNicknames(ctx context.Context, roomID game.RoomID) (map[string]bool, error) {
	names, err := a.repo.RoomNicknames(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[strings.ToLower(n)] = true
	}
	return out, nil
}

func (a joinStoreAdapter) Join(ctx context.Context, roomID game.RoomID, user game.UserID, nickname string) (game.Seat, error) {
	if err := a.users.Upsert(ctx, user, nickname); err != nil {
		return 0, err
	}
	seat, err := a.repo.Join(ctx, roomID, user)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrRoomNotFound):
			return 0, game.ErrRoomNotFound
		case errors.Is(err, storage.ErrRoomFull), errors.Is(err, storage.ErrSeatTaken):
			return 0, game.ErrRoomFull
		case errors.Is(err, storage.ErrUserAlreadyInRoom):
			return 0, game.ErrAlreadyInRoom
		case errors.Is(err, storage.ErrUserNotFound):
			return 0, game.ErrUserInRoom
		default:
			return 0, err
		}
	}
	return game.Seat(seat), nil
}

// userStore 是用户持久化最小 seam（I2 fail-closed 测试注入用）。
// UserRepository 满足该接口；测试可用 fake 注入验证 DB 故障分支。
type userStore interface {
	CooldownUntil(ctx context.Context, id game.UserID) (time.Time, error)
	Upsert(ctx context.Context, id game.UserID, nickname string) error
}

// settleStore 是 SettleGame 的最小 seam（I1 重试测试注入用）。
type settleStore interface {
	SettleGame(ctx context.Context, result storage.GameResult) error
}

// createRoomAdapter 包装 game.LobbyService：成功后持久化（users upser +
// rooms 建行）并登记注册表，同时把服务 effects + 面板放进待发桥。
type createRoomAdapter struct {
	lobby game.LobbyService
	reg   *liveRegistry
	repo  *storage.RoomRepository
	users userStore
	now   func() time.Time
}

func (a createRoomAdapter) CreateRoom(ctx context.Context, req game.CreateRoomRequest) (game.State, []game.Effect, error) {
	// I2：冷却期间不能创建新房间（docs 游戏流程设计.md §退出约束）。
	// fail-closed：查冷却出错（DB 故障等）时拒绝创建，宁可保守不放行，
	// 防止玩家绕过退出冷却刷局。
	until, err := a.users.CooldownUntil(ctx, req.Host)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		// fail-closed：DB 故障（非"用户不存在"的正常首次建房）时保守拒绝，
		// 防止绕过冷却刷局。
		return game.State{}, nil, fmt.Errorf("app: cooldown lookup (create): %w", err)
	}
	if until.After(a.now()) {
		return game.State{}, nil, game.ErrCooldownActive
	}
	st, fx, err := a.lobby.CreateRoom(ctx, req)
	if err != nil {
		return st, nil, err
	}
	if err := a.users.Upsert(ctx, req.Host, "房主"); err != nil {
		return st, nil, fmt.Errorf("app: upsert host: %w", err)
	}
	if err := a.repo.Create(ctx, st.RoomID, req.Host, "lobby"); err != nil {
		if errors.Is(err, storage.ErrRoomCodeTaken) {
			return st, nil, game.ErrRoomCodeTaken
		}
		return st, nil, fmt.Errorf("app: persist create room: %w", err)
	}
	a.reg.create(st, req.Host, a.now())
	// 只推面板：房间面板包含房间码、成员、邀请链接，承担全部创建反馈，
	// 不再单独发送 newgame_done 确认文案，避免双发消息。
	panel, err := game.NewMessageEffect(game.AudienceHost, game.LobbyPanelMessageKey, map[string]any{"room_code": string(st.RoomID)})
	if err != nil {
		return st, nil, err
	}
	a.reg.pushPending(st.RoomID, req.Host, []game.Effect{panel})
	return st, fx, nil
}

// joinRoomAdapter 包装 game.JoinService：成功后同步注册表并把服务
// effects（加入确认 + 房主面板）放进待发桥。
type joinRoomAdapter struct {
	join  game.JoinService
	reg   *liveRegistry
	users userStore
	now   func() time.Time
}

func (a joinRoomAdapter) Apply(ctx context.Context, req game.JoinRequest) (game.JoinResult, []game.Effect, error) {
	// I2：冷却期间不能加入其他房间（docs 游戏流程设计.md §退出约束）。
	// fail-closed：查冷却出错时拒绝加入，防止绕过冷却刷局。
	until, err := a.users.CooldownUntil(ctx, req.Actor)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		// fail-closed：DB 故障（非"用户不存在"的正常首次入房）时保守拒绝。
		return game.JoinResult{}, nil, fmt.Errorf("app: cooldown lookup (join): %w", err)
	}
	if until.After(a.now()) {
		return game.JoinResult{}, nil, game.ErrCooldownActive
	}
	res, fx, err := a.join.Apply(ctx, req)
	if err != nil {
		return res, nil, err
	}
	a.reg.recordJoin(req.RoomID, req.Actor, res.Seat)
	a.reg.pushPending(req.RoomID, req.Actor, fx)
	return res, fx, nil
}

// leaveServiceAdapter 实现 telegram.LeaveService：大厅退出（房主最后
// 一人退出 → 解散并清理 storage；其余 → 移交/面板）。
type leaveServiceAdapter struct {
	life game.LobbyLifecycleService
	reg  *liveRegistry
	repo *storage.RoomRepository
}

func (a leaveServiceAdapter) Leave(ctx context.Context, actor game.UserID, commandID string) ([]game.Effect, error) {
	roomID, ok := a.reg.roomOf(actor)
	if !ok {
		return nil, game.ErrNotInRoom
	}
	st, _, _, _, ok := a.reg.snapshot(roomID)
	if !ok {
		return nil, game.ErrNotInRoom
	}
	newSt, fx, err := a.life.LeaveRoom(ctx, st, game.LeaveCommand{Meta: game.CommandMeta{ID: commandID, Actor: actor}})
	if err != nil {
		return nil, err
	}
	if err := a.repo.Leave(ctx, roomID, actor); err != nil && !errors.Is(err, storage.ErrUserNotInRoom) {
		return nil, fmt.Errorf("app: persist leave: %w", err)
	}
	// 空房 / 房主退出：解散（单账号冒烟范围；多人房主移交由
	// lifecycle LeaveRoom 返回新 Owner，见下）。
	newOwner := newSt.Lobby.Owner
	a.reg.removePlayer(roomID, actor, newOwner)
	if len(newSt.Players) == 0 {
		if err := a.repo.RemoveRoom(ctx, roomID); err != nil {
			return nil, fmt.Errorf("app: dissolve room: %w", err)
		}
	}
	a.reg.pushPending(roomID, actor, fx)
	return fx, nil
}

// roleServiceAdapter 实现 telegram.RoleService：发牌前返回 ErrWrongPhase
// （映射为 commands.no_role_yet），发牌后返回身份与阵营中文名。
type roleServiceAdapter struct{ reg *liveRegistry }

func (a roleServiceAdapter) Role(ctx context.Context, actor game.UserID) (telegram.RoleReply, error) {
	roomID, ok := a.reg.roomOf(actor)
	if !ok {
		return telegram.RoleReply{}, game.ErrNotInRoom
	}
	st, _, _, _, ok := a.reg.snapshot(roomID)
	if !ok {
		return telegram.RoleReply{}, game.ErrNotInRoom
	}
	if st.Phase == game.PhaseLobby || st.Phase == game.PhaseDeal {
		return telegram.RoleReply{}, game.ErrWrongPhase
	}
	for _, p := range st.Players {
		if p.UserID == actor {
			return telegram.RoleReply{RoleName: roleNameCN(p.Role), CampName: campNameCN(p.Role)}, nil
		}
	}
	return telegram.RoleReply{}, game.ErrNotInRoom
}

func roleNameCN(r game.Role) string {
	switch r {
	case game.RoleWolf:
		return "狼人"
	case game.RoleSeer:
		return "预言家"
	case game.RoleWitch:
		return "女巫"
	default:
		return "平民"
	}
}

func campNameCN(r game.Role) string {
	switch r {
	case game.RoleWolf:
		return "狼人阵营"
	default:
		return "好人阵营"
	}
}

// scoreServiceAdapter 实现 telegram.ScoreService：读取 users.points。
type scoreServiceAdapter struct{ db *sql.DB }

func (a scoreServiceAdapter) Score(ctx context.Context, actor game.UserID) (int64, error) {
	var p int64
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(points, 0) FROM users WHERE telegram_id = ?`, int64(actor)).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, game.ErrNotInRoom
	}
	if err != nil {
		return 0, fmt.Errorf("app: load score: %w", err)
	}
	return p, nil
}

// replySender 把 CommandsHandler 回复投递到 Outbox（真实发送链路）。
type replySender struct{ w *Wiring }

func (s replySender) Send(ctx context.Context, chatID int64, text string) error {
	return s.w.enqueue("cmd", "", chatID, telegram.OpSendText, telegram.Params{ChatID: chatID, Text: text}, outbox.PriorityNormal, "")
}

// SendTemporary 直接通过 Telegram Client 发送消息并获取 message ID，
// 然后调度延迟自动删除。绕过 Outbox 以获取 message ID（Outbox 的
// SendFunc 不返回已发送消息 ID），但必须先经过与 Outbox 链共享的同一
// 限流器（P1）：错误风暴时临时消息同样受全局/单 Chat 速率约束，不得
// 绕过 Telegram flood 保护。限流等待失败（ctx 取消）时返回错误，不降级
// 直发；Client 不可用时才回退常规 Outbox 发送（不自动删除）。
func (s replySender) SendTemporary(ctx context.Context, chatID int64, text string, delay time.Duration) error {
	if s.w.limiter != nil {
		if err := s.w.limiter.Wait(ctx, outbox.ChatID(chatID)); err != nil {
			return fmt.Errorf("app: temporary send rate limited: %w", err)
		}
	}
	client, err := s.w.client()
	if err != nil {
		// 降级：Client 不可用时走常规发送（不自动删除）
		return s.w.enqueue("cmd", "", chatID, telegram.OpSendText, telegram.Params{ChatID: chatID, Text: text}, outbox.PriorityNormal, "")
	}
	sent, err := client.SendMessage(ctx, telegram.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "MarkdownV2",
	})
	if err != nil {
		// 发送失败时走 Outbox 重试链路作为兜底
		return s.w.enqueue("cmd", "", chatID, telegram.OpSendText, telegram.Params{ChatID: chatID, Text: text}, outbox.PriorityNormal, "")
	}
	// 调度延迟删除
	msgID := sent.MessageID
	time.AfterFunc(delay, func() {
		_ = s.w.enqueue("del:tmp", "", chatID, telegram.OpDeleteMessage,
			telegram.Params{ChatID: chatID, MessageID: msgID},
			outbox.PriorityLow, "")
	})
	return nil
}

// roomSettingsAdapter 实现 game.SettingsRepository。
type roomSettingsAdapter struct{ repo *storage.RoomRepository }

func (a roomSettingsAdapter) SaveSettings(ctx context.Context, roomID game.RoomID, settings game.RoomSettings, passwordHash string) error {
	raw, err := game.MarshalSettings(settings)
	if err != nil {
		return err
	}
	return a.repo.UpdateRoomSettings(ctx, roomID, raw, passwordHash)
}

func (a roomSettingsAdapter) LoadPasswordHash(ctx context.Context, roomID game.RoomID) (string, error) {
	h, err := a.repo.RoomPasswordHash(ctx, roomID)
	if errors.Is(err, storage.ErrRoomNotFound) {
		return "", game.ErrRoomNotFound
	}
	return h, err
}

// ---------------------------------------------------------------------------
// 回调/房间命令处理器（Router 派发的领域命令 / 回调动作）
// ---------------------------------------------------------------------------

type callbackHandler struct {
	w *Wiring
}

// Handle 是 CommandHandler 适配器：回调动作经 actionHandler 映射为命令后
// 复用同一处理路径。
func (h *callbackHandler) Handle(ctx context.Context, cmd game.Command) error {
	return h.w.handleCommand(ctx, cmd)
}

// callbackActionHandler 处理校验后的回调动作（B1-b）：reducer 动作 →
// 命令 → handleCommand；导演本地信号（end_speech 等）先 ACK 待 B1-d 接线。
type callbackActionHandler struct {
	w *Wiring
}

func (h *callbackActionHandler) Handle(ctx context.Context, act telegram.CallbackAction) error {
	payload := &telegram.TokenPayload{
		Owner:         act.Owner,
		Action:        act.Action,
		Target:        act.Target,
		ExpectedPhase: act.ExpectedPhase,
		PhaseVersion:  act.PhaseVersion,
	}
	meta := game.CommandMeta{
		ID:            fmt.Sprintf("u%d", act.UpdateID),
		Actor:         act.Owner,
		ExpectedPhase: act.ExpectedPhase,
		PhaseVersion:  act.PhaseVersion,
		ReceivedAt:    act.ReceivedAt,
	}
	// B3：无论成功/拒绝，每次 callback 都必须 answer（docs 阶段消息设计.md
	// §9：短按钮反馈经顶部通知，show_alert=false）。
	feedback := ""
	if cmd, ok := telegram.CallbackCommand(payload, meta); ok {
		if hostCmd, isHostDissolve := cmd.(game.HostDissolveCommand); isHostDissolve {
			// 大厅阶段（actor==nil）解散：在此拦截确认流程
			roomID, roomOk := h.w.reg.roomOf(act.Owner)
			if roomOk {
				_, actor, _, _, snapOk := h.w.reg.snapshot(roomID)
				if snapOk && actor == nil {
					if !hostCmd.Confirm {
						// 第一次点击：编辑原面板为确认面板
						text, markup, err := h.w.buildDissolveConfirmPanel(roomID)
						if err != nil {
							feedback = "面板加载失败"
						} else if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
							telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text, ReplyMarkup: markup},
							outbox.PriorityHigh, ""); err != nil {
							feedback = "面板更新失败"
						}
						if act.CallbackQueryID != "" {
							return h.w.answerCallback(act.CallbackQueryID, feedback)
						}
						return nil
					}
					// Confirm=true：真正解散，编辑原面板为已解散
					if err := h.w.repo.RemoveRoom(ctx, roomID); err != nil {
						h.w.log.Warn("app: lobby dissolve failed", "room", string(roomID), "error", err)
						feedback = "解散失败，请重试"
					} else {
						h.w.reg.removeRoom(roomID)
						h.w.log.Info("app: lobby dissolved by host", "room", string(roomID), "host", int64(meta.Actor))
						text, _ := h.w.renderer.Render("panel.dissolved", map[string]any{"room_code": string(roomID)})
						if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
							telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text},
							outbox.PriorityHigh, ""); err != nil {
							h.w.log.Warn("app: enqueue dissolved panel failed", "room", string(roomID), "error", err)
						}
					}
					if act.CallbackQueryID != "" {
						return h.w.answerCallback(act.CallbackQueryID, feedback)
					}
					return nil
				}
			}
			// 游戏中：走正常流程（带积分检查）
			score, err := (scoreServiceAdapter{db: h.w.db}).Score(ctx, hostCmd.Meta.Actor)
			if err != nil {
				feedback = callbackFeedback(err)
				cmd = nil
			} else {
				hostCmd.HostScore = int(score)
				cmd = hostCmd
			}
		}
		if cmd == nil {
			if act.CallbackQueryID != "" {
				return h.w.answerCallback(act.CallbackQueryID, feedback)
			}
			return nil
		}
		if err := h.w.handleCommand(ctx, cmd); err != nil {
			feedback = callbackFeedback(err)
		}
	} else if act.Action == "settings" {
		// 房间设置：编辑当前消息展示设置面板（inline keyboard 切换按钮）。
		roomID, ok := h.w.reg.roomOf(act.Owner)
		if !ok {
			feedback = "房间不存在"
		} else {
			text, markup, err := h.w.buildSettingsPanel(roomID)
			if err != nil {
				feedback = "设置面板加载失败"
			} else if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
				telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text, ReplyMarkup: markup},
				outbox.PriorityHigh, ""); err != nil {
				feedback = "设置面板更新失败"
			}
		}
	} else if act.Action == "settings_toggle" {
		// 切换某个设置项。
		roomID, ok := h.w.reg.roomOf(act.Owner)
		if !ok {
			feedback = "房间不存在"
		} else {
			newSettings, err := h.w.applySettingsToggle(ctx, roomID, act.Owner, act.Target, meta)
			if err != nil {
				feedback = callbackFeedback(err)
			} else {
				h.w.reg.updateSettings(roomID, newSettings)
				text, markup, err := h.w.buildSettingsPanel(roomID)
				if err != nil {
					feedback = "设置面板刷新失败"
				} else if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
					telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text, ReplyMarkup: markup},
					outbox.PriorityHigh, ""); err != nil {
					feedback = "设置面板刷新失败"
				} else {
					feedback = h.w.mustRender("settings.toggle_updated")
				}
			}
		}
	} else if act.Action == "settings_back" {
		// 返回大厅面板。
		roomID, ok := h.w.reg.roomOf(act.Owner)
		if !ok {
			feedback = "房间不存在"
		} else {
			text, markup, err := h.w.buildPanel(roomID)
			if err != nil {
				feedback = "面板加载失败"
			} else if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
				telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text, ReplyMarkup: markup},
				outbox.PriorityHigh, ""); err != nil {
				feedback = "面板更新失败"
			}
		}
	} else if act.Action == "dissolve_cancel" {
		// 取消解散，返回大厅面板。
		roomID, ok := h.w.reg.roomOf(act.Owner)
		if !ok {
			feedback = "房间不存在"
		} else {
			text, markup, err := h.w.buildPanel(roomID)
			if err != nil {
				feedback = "面板加载失败"
			} else if err := h.w.enqueue("cb:"+string(roomID), roomID, act.ChatID, telegram.OpEditMessage,
				telegram.Params{ChatID: act.ChatID, MessageID: act.MessageID, Text: text, ReplyMarkup: markup},
				outbox.PriorityHigh, ""); err != nil {
				feedback = "面板更新失败"
			}
		}
	} else if err := h.w.director.handleAction(ctx, act); err != nil {
		feedback = "操作失败，请重试"
	}
	if act.CallbackQueryID != "" {
		return h.w.answerCallback(act.CallbackQueryID, feedback)
	}
	return nil
}

// answerCallback 应答回调查询（OpAnswerCallback；空文本=纯 ACK，k 图书馆
// 顶部通知 show_alert=false）。
func (w *Wiring) answerCallback(callbackQueryID, text string) error {
	if w.outbox == nil {
		return errors.New("app: wiring outbox not attached")
	}
	return w.outbox.Enqueue(outbox.Message{
		CorrelationID: "cb:" + callbackQueryID,
		Operation:     telegram.OpAnswerCallback,
		Priority:      outbox.PriorityHigh,
		Payload:       telegram.Params{CallbackQueryID: callbackQueryID, Text: text},
	})
}

// applySettingsToggle 切换某个设置项并调用 SettingsService 持久化。
// 返回更新后的 RoomSettings。
func (w *Wiring) applySettingsToggle(ctx context.Context, roomID game.RoomID, actor game.UserID, target string, meta game.CommandMeta) (game.RoomSettings, error) {
	st, _, _, _, ok := w.reg.snapshot(roomID)
	if !ok {
		return game.RoomSettings{}, errors.New("app: room not found")
	}
	settings := st.Settings
	if settings == (game.RoomSettings{}) {
		settings = game.DefaultRoomSettings()
	}

	switch target {
	case "speech_mode":
		if settings.SpeechMode == game.SpeechFixed {
			settings.SpeechMode = game.SpeechSoft
		} else {
			settings.SpeechMode = game.SpeechFixed
		}
	case "fast_mode":
		settings.FastMode = !settings.FastMode
	case "wolf_must_kill":
		settings.WolfMustKill = !settings.WolfMustKill
	case "reveal_role":
		settings.RevealRoleOnDeath = !settings.RevealRoleOnDeath
	case "witch_self_save":
		settings.WitchSelfSaveFirstNight = !settings.WitchSelfSaveFirstNight
	case "victory_mode":
		if settings.Victory == game.VictorySlaughter {
			settings.Victory = game.VictorySide
		} else {
			settings.Victory = game.VictorySlaughter
		}
	default:
		return game.RoomSettings{}, fmt.Errorf("app: unknown settings toggle %q", target)
	}

	cmd := game.SettingsCommand{
		Meta:     meta,
		RoomID:   roomID,
		Settings: settings,
	}
	updated, _, err := w.settings.Apply(ctx, cmd)
	if err != nil {
		return game.RoomSettings{}, err
	}
	return updated, nil
}

// callbackFeedback 把领域拒绝映射为短顶部通知文案（docs §9 示例：
// 操作已经过期 / 该目标已经死亡 等）。
func callbackFeedback(err error) string {
	switch {
	case errors.Is(err, game.ErrStalePhaseVersion), errors.Is(err, game.ErrWrongPhase), errors.Is(err, room.ErrDeadlinePassed):
		return "操作已经过期"
	case errors.Is(err, game.ErrDeadPlayer):
		return "你已死亡，无法操作"
	case errors.Is(err, game.ErrInvalidTarget):
		return "目标无效或已死亡"
	case errors.Is(err, game.ErrVoteLocked), errors.Is(err, game.ErrWolfVoteLocked):
		return "该操作已确认锁定"
	case errors.Is(err, game.ErrNotHost):
		return "仅房主可操作"
	case errors.Is(err, game.ErrRoomNotFull):
		return "房间尚未满员，无法开始"
	case errors.Is(err, game.ErrRematchWindowOpen):
		return "退出窗口尚未结束，请稍后再开始"
	default:
		return "操作失败，请重试"
	}
}

// handleCommand 处理一条领域命令（建房后大厅回调 / 局内 Actor 分派）。
func (w *Wiring) handleCommand(ctx context.Context, cmd game.Command) error {
	meta, ok := commandMetaOf(cmd)
	if !ok {
		w.log.Warn("app: callback command without meta", "command", fmt.Sprintf("%T", cmd))
		return nil
	}
	roomID, ok := w.reg.roomOf(meta.Actor)
	if !ok {
		// 无房回调：回复无房提示后 ACK（不重投）。
		_ = w.sendText(ctx, "cb", "", int64(meta.Actor), "commands.no_room", nil)
		return nil
	}
	st, actor, _, _, ok := w.reg.snapshot(roomID)
	if !ok {
		return nil
	}

	// 开局前（大厅）回调：建房后按钮（设置/解散/开始）。单账号冒烟范围
	// 内面板按钮尚未接线 inline keyboard，此路径主要保证 StartGame 可
	// 引导房间 Actor（B1-d 起按钮经 inline keyboard + CallbackAction 到达）。
	if actor == nil {
		switch c := cmd.(type) {
		case game.SettingsCommand:
			_, fx, err := w.settings.Apply(ctx, c)
			if err != nil {
				w.log.Warn("app: settings rejected", "room", string(roomID), "error", err)
				return nil
			}
			return w.applyEffects(ctx, "cb:"+string(roomID), roomID, meta.Actor, fx)
		case game.StartGameCommand:
			newActor := w.newGameActor(st)
			// 写回注册表供后续命令/推进引用（原 lr.actor 字段）。
			w.reg.adoptActor(roomID, newActor)
			w.director.bind(roomID, newActor)
			res, err := newActor.Dispatch(ctx, c)
			if err != nil {
				w.log.Warn("app: start game dispatch", "room", string(roomID), "error", err)
				return nil
			}
			if res.Err != nil {
				w.log.Warn("app: start game rejected", "room", string(roomID), "error", res.Err, "error_type", fmt.Sprintf("%T", res.Err))
				// B3：开局被领域拒绝（人数不足/退出窗口未过等）时退役刚绑定
				// 的 Actor——否则大厅房间挂着 actor != nil，SweepIdle 永久跳过
				// 它，Actor goroutine 泄漏。
				w.retireActor(roomID)
				w.director.release(roomID)
				return res.Err // 领域拒绝（人数不足等）：供顶层回调反馈
			}
			// 发牌效果已由导演 OnApplied 扇出；导演同步 reg 状态（含 Adopt）。
			return nil
		case game.LeaveGameCommand:
			// 大厅退出走 /leave 文本路径；回调版兜底 ACK。
			return nil
		case game.HostDissolveCommand:
			// 大厅阶段房主解散已在 callbackActionHandler 中拦截处理，
			// 此路径不应到达（兜底 ACK）。
			w.log.Warn("app: lobby dissolve reached handleCommand (should be handled in callback)", "room", string(roomID))
			return nil
		default:
			w.log.Debug("app: lobby callback ignored", "room", string(roomID), "command", fmt.Sprintf("%T", cmd))
			return nil
		}
	}

	res, err := actor.Dispatch(ctx, cmd)
	if err != nil {
		w.log.Warn("app: room dispatch", "room", string(roomID), "command", fmt.Sprintf("%T", cmd), "error", err)
		return nil // 领域/基础设施拒绝：ACK
	}
	if res.Err != nil {
		w.log.Warn("app: command rejected", "room", string(roomID), "command", fmt.Sprintf("%T", cmd), "error", res.Err)
		return res.Err
	}
	// B3：Rematch 回大厅后退役本局 Actor——房间交回 /newgame 周期（再次
	// start_game 会引导新 Actor），否则 SweepIdle 因 actor != nil 永久跳过
	// 该房间，Actor goroutine/Timer 泄漏。
	if _, ok := cmd.(game.RematchCommand); ok {
		w.retireActor(roomID)
	}
	// 效果已由导演 OnApplied 扇出；导演同步 reg 状态（含 Adopt 的阶段推进）。
	return nil
}

// retireActor 退役房间的当前 Actor（B3）：取出引用并 Close 发信号退出。
// 房间留在注册表（Rematch 回大厅语义），下次 start_game 引导新 Actor。
func (w *Wiring) retireActor(roomID game.RoomID) {
	if actor, ok := w.reg.takeActor(roomID); ok {
		actor.Close()
		w.log.Info("app: room actor retired", "room", string(roomID))
	}
}

// newGameActor 创建开局 Actor：绑定导演 OnApplied（B1-a/B1-d）。
func (w *Wiring) newGameActor(st game.State) *room.Actor {
	roomID := st.RoomID
	return room.NewActor(st, game.NewReducer(), room.NewRealClock(), room.Options{
		OnApplied: func(ast game.State, fx []game.Effect) {
			w.director.onApplied(roomID, ast, fx)
		},
	})
}

// commandMetaOf 提取全部命令的 Meta（Router 产出的命令均携带 Meta）。
func commandMetaOf(cmd game.Command) (game.CommandMeta, bool) {
	switch c := cmd.(type) {
	case game.CreateRoomCommand:
		return c.Meta, true
	case game.JoinRoomCommand:
		return c.Meta, true
	case game.StartGameCommand:
		return c.Meta, true
	case game.ConfirmRoleCommand:
		return c.Meta, true
	case game.WolfKillCommand:
		return c.Meta, true
	case game.WolfVoteCommand:
		return c.Meta, true
	case game.WolfConfirmCommand:
		return c.Meta, true
	case game.WitchUseCommand:
		return c.Meta, true
	case game.WitchSaveCommand:
		return c.Meta, true
	case game.WitchPoisonCommand:
		return c.Meta, true
	case game.WitchConfirmCommand:
		return c.Meta, true
	case game.SeerCheckCommand:
		return c.Meta, true
	case game.SeerConfirmCommand:
		return c.Meta, true
	case game.SpeakCommand:
		return c.Meta, true
	case game.VoteCommand:
		return c.Meta, true
	case game.VoteConfirmCommand:
		return c.Meta, true
	case game.LastWordsCommand:
		return c.Meta, true
	case game.TimeoutCommand:
		return c.Meta, true
	case game.ExplodeCommand:
		return c.Meta, true
	case game.LeaveGameCommand:
		return c.Meta, true
	case game.GovernanceDissolveCommand:
		return c.Meta, true
	case game.GovernanceDissolveVoteCommand:
		return c.Meta, true
	case game.GovernanceKickCommand:
		return c.Meta, true
	case game.GovernanceKickVoteCommand:
		return c.Meta, true
	case game.HostDissolveCommand:
		return c.Meta, true
	case game.RematchCommand:
		return c.Meta, true
	default:
		return game.CommandMeta{}, false
	}
}

func (r *liveRegistry) updateState(code game.RoomID, st game.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lr, ok := r.rooms[code]; ok {
		lr.st = st
		lr.host = st.Lobby.Owner
	}
}

// updateSettings 更新注册表中房间的设置快照（设置面板切换后同步）。
func (r *liveRegistry) updateSettings(code game.RoomID, settings game.RoomSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if lr, ok := r.rooms[code]; ok {
		lr.st.Settings = settings
	}
}
