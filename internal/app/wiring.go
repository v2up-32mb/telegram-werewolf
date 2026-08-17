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
	reg       *liveRegistry
	repo      *storage.RoomRepository
	users     *storage.UserRepository
	now       func() time.Time

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
	w.startCoalescerFlusher()

	w.repo = storage.NewRoomRepository(db)
	w.users = storage.NewUserRepository(db)
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

// productionSend 是 Outbox 底层发送器：断言 Payload 为 telegram.Params
// 后按 Operation 经 Transport 分派（未知 op 返回明确错误，交由 Outbox
// 重试策略处理）。
func (w *Wiring) productionSend(ctx context.Context, msg outbox.Message) error {
	params, ok := msg.Payload.(telegram.Params)
	if !ok {
		return fmt.Errorf("app: outbox %q payload missing or not telegram.Params (%T)", msg.Operation, msg.Payload)
	}
	client, err := w.client()
	if err != nil {
		return err
	}
	tr := telegram.NewTransport(client)
	if err := tr.Send(ctx, msg.Operation, params); err != nil {
		return classifyTelegramError(msg, err)
	}
	w.log.Info("app: telegram sent", "op", msg.Operation, "chat", int64(msg.ChatID), "summary", summarize(params.Text))
	return nil
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

// startCoalescerFlusher 轮询把 Coalescer 待发消息送入 Scheduler（I1b）。
// Scheduler 关闭（应用停机）时退出。
func (w *Wiring) startCoalescerFlusher() {
	go func() {
		for {
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
			text, err := w.renderMessage(te, roomID)
			if err != nil {
				w.log.Error("app: render message effect", "room", string(roomID), "key", te.Key, "error", err)
				continue
			}
			if err := w.enqueue(corr, roomID, chat, telegram.OpSendText, telegram.Params{ChatID: chat, Text: text}, outbox.PriorityNormal, coalesce); err != nil {
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
func (w *Wiring) renderMessage(e game.MessageEffect, roomID game.RoomID) (string, error) {
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
		return w.renderer.Render(e.Key, data)
	default:
		return w.renderer.Render(e.Key, e.Params)
	}
}

// buildPanel 从注册表房间状态 + storage 昵称构建房主面板（S3/S4）。
func (w *Wiring) buildPanel(roomID game.RoomID) (string, error) {
	r, ok := w.reg.get(roomID)
	if !ok {
		return "", errors.New("app: panel room not found")
	}
	st := r.st

	code, err := w.renderer.Render("panel.room_code", map[string]any{"RoomCode": string(st.RoomID)})
	if err != nil {
		return "", err
	}
	count, err := w.renderer.Render("panel.count", map[string]any{"Count": len(st.Players), "Max": game.MVPPlayerCount})
	if err != nil {
		return "", err
	}
	title, err := w.renderer.Render("panel.title", nil)
	if err != nil {
		return "", err
	}
	phase, err := w.renderer.Render("panel.phase_lobby", nil)
	if err != nil {
		return "", err
	}
	header, err := w.renderer.Render("panel.members_header", nil)
	if err != nil {
		return "", err
	}
	invite, err := w.renderer.Render("panel.invite_line", map[string]any{"RoomCode": string(st.RoomID)})
	if err != nil {
		return "", err
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
			return "", err
		}
		lines = append(lines, line)
	}

	var buttons []string
	for _, k := range []string{"panel.button.start", "panel.button.settings", "panel.button.dismiss"} {
		label, err := w.renderer.Render(k, nil)
		if err != nil {
			return "", err
		}
		buttons = append(buttons, label)
	}
	btnLine, err := w.renderer.Render("panel.buttons_line", map[string]any{"Buttons": strings.Join(buttons, "  ")})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(title)
	b.WriteByte('\n')
	b.WriteString(code)
	b.WriteByte('\n')
	b.WriteString(count)
	b.WriteByte('\n')
	b.WriteString(phase)
	b.WriteByte('\n')
	b.WriteString(header)
	if len(lines) > 0 {
		b.WriteByte('\n')
		b.WriteString(strings.Join(lines, "\n"))
	}
	b.WriteByte('\n')
	b.WriteString(invite)
	b.WriteByte('\n')
	b.WriteString(btnLine)
	return b.String(), nil
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

// wiringAudienceChat 把受众映射为 Telegram ChatID（MVP 私聊模型：
// UserID 即 ChatID，docs/技术选型.md §10 私聊限定）。
func wiringAudienceChat(a game.Audience, roomID game.RoomID, actor game.UserID, reg *liveRegistry) (int64, error) {
	switch a {
	case game.AudienceActor:
		return int64(actor), nil
	case game.AudienceHost:
		r, ok := reg.get(roomID)
		if !ok {
			return 0, errors.New("app: host audience room not found")
		}
		return int64(r.host), nil
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

// createRoomAdapter 包装 game.LobbyService：成功后持久化（users upser +
// rooms 建行）并登记注册表，同时把服务 effects + 面板放进待发桥。
type createRoomAdapter struct {
	lobby game.LobbyService
	reg   *liveRegistry
	repo  *storage.RoomRepository
	users *storage.UserRepository
	now   func() time.Time
}

func (a createRoomAdapter) CreateRoom(ctx context.Context, req game.CreateRoomRequest) (game.State, []game.Effect, error) {
	// I2：冷却期间不能创建新房间（docs 游戏流程设计.md §退出约束）。
	if until, err := a.users.CooldownUntil(ctx, req.Host); err == nil && until.After(a.now()) {
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
	// 只推面板：创建确认文案由 CommandsHandler 的 commands.newgame_done
	// 承担（领域层 CreateRoom 已不再产出 lobby.created effect，Task 46
	// 冒烟修复），避免同一次 /newgame 连发三条消息（S3 预期：确认+面板）。
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
	users *storage.UserRepository
	now   func() time.Time
}

func (a joinRoomAdapter) Apply(ctx context.Context, req game.JoinRequest) (game.JoinResult, []game.Effect, error) {
	// I2：冷却期间不能加入其他房间（docs 游戏流程设计.md §退出约束）。
	if until, err := a.users.CooldownUntil(ctx, req.Actor); err == nil && until.After(a.now()) {
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
	lr, ok := a.reg.get(roomID)
	if !ok {
		return nil, game.ErrNotInRoom
	}
	newSt, fx, err := a.life.LeaveRoom(ctx, lr.st, game.LeaveCommand{Meta: game.CommandMeta{ID: commandID, Actor: actor}})
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
	lr, ok := a.reg.get(roomID)
	if !ok {
		return telegram.RoleReply{}, game.ErrNotInRoom
	}
	if lr.st.Phase == game.PhaseLobby || lr.st.Phase == game.PhaseDeal {
		return telegram.RoleReply{}, game.ErrWrongPhase
	}
	for _, p := range lr.st.Players {
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
		if err := h.w.handleCommand(ctx, cmd); err != nil {
			feedback = callbackFeedback(err)
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
	lr, ok := w.reg.get(roomID)
	if !ok {
		return nil
	}

	// 开局前（大厅）回调：建房后按钮（设置/解散/开始）。单账号冒烟范围
	// 内面板按钮尚未接线 inline keyboard，此路径主要保证 StartGame 可
	// 引导房间 Actor（B1-d 起按钮经 inline keyboard + CallbackAction 到达）。
	if lr.actor == nil {
		switch c := cmd.(type) {
		case game.SettingsCommand:
			_, fx, err := w.settings.Apply(ctx, c)
			if err != nil {
				w.log.Warn("app: settings rejected", "room", string(roomID), "error", err)
				return nil
			}
			return w.applyEffects(ctx, "cb:"+string(roomID), roomID, meta.Actor, fx)
		case game.StartGameCommand:
			lr.actor = w.newGameActor(lr.st)
			w.director.bind(roomID, lr.actor)
			res, err := lr.actor.Dispatch(ctx, c)
			if err != nil {
				w.log.Warn("app: start game dispatch", "room", string(roomID), "error", err)
				return nil
			}
			if res.Err != nil {
				w.log.Warn("app: start game rejected", "room", string(roomID), "error", res.Err)
				return res.Err // 领域拒绝（人数不足等）：供顶层回调反馈
			}
			// 发牌效果已由导演 OnApplied 扇出；导演同步 reg 状态（含 Adopt）。
			return nil
		case game.LeaveGameCommand:
			// 大厅退出走 /leave 文本路径；回调版兜底 ACK。
			return nil
		default:
			w.log.Debug("app: lobby callback ignored", "room", string(roomID), "command", fmt.Sprintf("%T", cmd))
			return nil
		}
	}

	res, err := lr.actor.Dispatch(ctx, cmd)
	if err != nil {
		w.log.Warn("app: room dispatch", "room", string(roomID), "error", err)
		return nil // 领域/基础设施拒绝：ACK
	}
	if res.Err != nil {
		w.log.Warn("app: command rejected", "room", string(roomID), "command", fmt.Sprintf("%T", cmd), "error", res.Err)
		return res.Err
	}
	// 效果已由导演 OnApplied 扇出；导演同步 reg 状态（含 Adopt 的阶段推进）。
	return nil
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
