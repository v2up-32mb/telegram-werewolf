package app

// P0 端到端测试装配层（实施计划 Task 27：internal/app/testharness_test.go）。
//
// 边界说明：本文件只属于测试（package app 的 _test.go），不修改任何 production
// 文件。装配原则是「组合真实生产组件 + 测试内接缝适配器」：
//
//   - 真实组件：storage（临时 SQLite、迁移、RoomRepository、UserRepository）、
//     game（LobbyService/JoinService/LobbyLifecycleService、消息 key、命令/Effect）、
//     outbox（Scheduler/Coalescer/Message）、i18n（EscapeMarkdownV2）、
//     telegram（CreateRoomHandler/JoinHandler/LobbyHandler 与 From* 输入解析器）。
//   - 测试内适配器（因为 production 接线属后续任务，且现 schema 无
//     nickname/left/expires 持久化列）：
//       p0Registry   —— game.LobbyRoomRegistry（房间码/房主/过期内存态）；
//       p0JoinStore  —— game.JoinStore（持久化委托真实 RoomRepository；
//                        昵称唯一/退出不可重入以内存 roster 表达）；
//       fakeLifecycleClock —— game.LifecycleClock（可推进时间）；
//       p0Lifetimes  —— LobbyLifetime 注册表；
//       renderMarkdownV2 —— 把 MessageEffect 渲染为 MarkdownV2 文本；
//       p0Outbox     —— 真实 Scheduler+Coalescer+recordingSender 的测试记录层。
//
// 已知缺口（如实记录，本任务不覆盖）：App 传输管线
// （UpdateSource→Router→CommandHandler→Transport→Bot API）在 production 尚未接线；
// 本测试以适配层（telegram 输入解析 + 领域服务）为端到端入口，Fake Telegram
// 仅体现在发送替身（recordingSender）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// strPtr 返回字符串指针（测试辅助）。
func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// 确定性 RNG（game.RNG）与假时钟
// ---------------------------------------------------------------------------

// seqRNG 是确定性的 game.RNG 实现：固定种子，保证随机昵称/房间码可复现。
type seqRNG struct {
	r *rand.Rand
}

func newSeqRNG(seed int64) *seqRNG { return &seqRNG{r: rand.New(rand.NewSource(seed))} }

func (g *seqRNG) Intn(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("seqRNG: bound %d must be positive", n)
	}
	return g.r.Intn(n), nil
}

// fakeLifecycleClock 是 game.LifecycleClock 的可推进假时钟。
type fakeLifecycleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeLifecycleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeLifecycleClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------------------
// p0Registry：game.LobbyRoomRegistry 的测试内适配器（内存态）
// ---------------------------------------------------------------------------

// p0Registry 以内存态表达房间码唯一性、房主单活跃约束与过期标记；
// 与 storage 行为一致：已占用码不可再建房，过期码视为不存在可加入性。
type p0Registry struct {
	mu      sync.Mutex
	codes   map[game.RoomID]bool
	hosts   map[game.UserID]bool
	byUser  map[game.UserID]game.RoomID
	expired map[game.RoomID]bool
}

func newP0Registry() *p0Registry {
	return &p0Registry{
		codes:   make(map[game.RoomID]bool),
		hosts:   make(map[game.UserID]bool),
		byUser:  make(map[game.UserID]game.RoomID),
		expired: make(map[game.RoomID]bool),
	}
}

func (r *p0Registry) HostActive(host game.UserID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hosts[host]
}

func (r *p0Registry) ReserveCode(_ context.Context, code game.RoomID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.codes[code] || r.expired[code] {
		return false, nil
	}
	r.codes[code] = true
	return true, nil
}

func (r *p0Registry) setHost(host game.UserID, code game.RoomID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts[host] = true
	r.byUser[host] = code
}

func (r *p0Registry) setExpired(code game.RoomID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expired[code] = true
}

func (r *p0Registry) isExpired(code game.RoomID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expired[code]
}

func (r *p0Registry) has(code game.RoomID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.codes[code]
}

func (r *p0Registry) roomOf(user game.UserID) (game.RoomID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	code, ok := r.byUser[user]
	return code, ok
}

func (r *p0Registry) releaseUser(user game.UserID, code game.RoomID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byUser[user] == code {
		delete(r.byUser, user)
	}
	delete(r.hosts, user) // 房主退出后不再占用建房间席位（该房间仍存在）
}

// ---------------------------------------------------------------------------
// p0JoinStore：game.JoinStore 的测试内适配器
// ---------------------------------------------------------------------------

type p0Member struct {
	seat     game.Seat
	nickname string
}

// p0JoinStore 把持久化委托真实 storage.RoomRepository（真实 SQLite 座位分配、
// 存在性、密码哈希），以内存态表达 schema 暂不支持的昵称唯一与退出不可重入。
type p0JoinStore struct {
	mu     sync.Mutex
	repo   *storage.RoomRepository
	reg    *p0Registry
	roster map[game.RoomID]map[game.UserID]p0Member
	left   map[game.RoomID]map[game.UserID]bool
}

func newP0JoinStore(repo *storage.RoomRepository, reg *p0Registry) *p0JoinStore {
	return &p0JoinStore{
		repo:   repo,
		reg:    reg,
		roster: make(map[game.RoomID]map[game.UserID]p0Member),
		left:   make(map[game.RoomID]map[game.UserID]bool),
	}
}

func (s *p0JoinStore) LoadPasswordHash(ctx context.Context, roomID game.RoomID) (string, error) {
	return s.repo.RoomPasswordHash(ctx, roomID)
}

func (s *p0JoinStore) CheckRoom(ctx context.Context, roomID game.RoomID) error {
	if !s.reg.has(roomID) {
		return game.ErrRoomNotFound
	}
	if s.reg.isExpired(roomID) {
		return game.ErrRoomExpired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roster[roomID]) >= game.MVPPlayerCount {
		return game.ErrRoomFull
	}
	return nil
}

func (s *p0JoinStore) HasPlayer(ctx context.Context, roomID game.RoomID, user game.UserID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.roster[roomID][user]
	return ok, nil
}

func (s *p0JoinStore) HasLeft(ctx context.Context, roomID game.RoomID, user game.UserID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.left[roomID][user], nil
}

func (s *p0JoinStore) UserInRoom(ctx context.Context, user game.UserID) (bool, error) {
	_, ok := s.reg.roomOf(user)
	return ok, nil
}

func (s *p0JoinStore) ReservedNicknames(ctx context.Context, roomID game.RoomID) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reserved := make(map[string]bool)
	for _, m := range s.roster[roomID] {
		reserved[game.FoldNickname(m.nickname)] = true
	}
	return reserved, nil
}

func (s *p0JoinStore) Join(ctx context.Context, roomID game.RoomID, user game.UserID, nickname string) (game.Seat, error) {
	seat64, err := s.repo.Join(ctx, roomID, user)
	if err != nil {
		return 0, mapStorageJoinError(err)
	}
	seat := game.Seat(seat64)
	s.mu.Lock()
	if s.roster[roomID] == nil {
		s.roster[roomID] = make(map[game.UserID]p0Member)
	}
	s.roster[roomID][user] = p0Member{seat: seat, nickname: nickname}
	s.mu.Unlock()
	s.reg.setHost(user, roomID) // 复用 byUser 表达「单活跃房间」约束
	return seat, nil
}

// members 返回房间成员（按座位升序，房主 1 号在前）。
func (s *p0JoinStore) members(roomID game.RoomID) []p0Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := make([]p0Member, 0, len(s.roster[roomID]))
	for _, m := range s.roster[roomID] {
		ms = append(ms, m)
	}
	sortMembersBySeat(ms)
	return ms
}

func (s *p0JoinStore) memberNickname(roomID game.RoomID, user game.UserID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roster[roomID][user].nickname
}

// registerHost 在建房成功后登记房主席位（房主经 LobbyService.CreateRoom
// 进入房间，不走 JoinStore.Join；roster 需同步以便面板渲染与昵称保留）。
func (s *p0JoinStore) registerHost(roomID game.RoomID, user game.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roster[roomID] == nil {
		s.roster[roomID] = make(map[game.UserID]p0Member)
	}
	s.roster[roomID][user] = p0Member{seat: game.HostSeat, nickname: "房主"}
}

// recordLeave 在玩家退出后同步内存态：移除 roster、标记不可重入。
func (s *p0JoinStore) recordLeave(roomID game.RoomID, user game.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roster[roomID] != nil {
		delete(s.roster[roomID], user)
	}
	if s.left[roomID] == nil {
		s.left[roomID] = make(map[game.UserID]bool)
	}
	s.left[roomID][user] = true
}

func mapStorageJoinError(err error) error {
	switch {
	case errors.Is(err, storage.ErrRoomNotFound):
		return game.ErrRoomNotFound
	case errors.Is(err, storage.ErrRoomFull):
		return game.ErrRoomFull
	case errors.Is(err, storage.ErrUserAlreadyInRoom):
		return game.ErrAlreadyInRoom
	case errors.Is(err, storage.ErrSeatTaken):
		return game.ErrRoomFull // 座位唯一冲突的防御路径：满员语义
	default:
		return err
	}
}

func sortMembersBySeat(ms []p0Member) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].seat < ms[j-1].seat; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// ---------------------------------------------------------------------------
// p0Outbox：真实 Scheduler + Coalescer + recordingSender 的测试记录层
// ---------------------------------------------------------------------------

// p0Delivered 是一条消息的测试记录：路由元数据（outbox.Message）、
// 语义 key（game 消息 key）与渲染后的 MarkdownV2 文本。
type p0Delivered struct {
	msg  outbox.Message
	key  string
	text string
}

// p0Outbox 组合真实 outbox 组件：Coalescer（合并）→ Scheduler（per-Chat
// FIFO worker）→ recordingSender（Fake Telegram 发送替身）。audit 保留
// 全部提交记录（含被合并覆盖的面板版本），delivered 为真实送达序列。
type p0Outbox struct {
	mu        sync.Mutex
	t         *testing.T
	sched     *outbox.Scheduler
	sender    *recordingSender
	coalescer *outbox.Coalescer
	audit     []p0Delivered
	delivered []p0Delivered
	byCorr    map[string]p0Delivered
	enqueued  int
}

func newP0Outbox(t *testing.T) *p0Outbox {
	sender := newRecordingSender(128)
	sched := outbox.NewScheduler(sender.Send, 64)
	return &p0Outbox{
		t:         t,
		sched:     sched,
		sender:    sender,
		coalescer: outbox.NewCoalescer(),
		byCorr:    make(map[string]p0Delivered),
	}
}

// submit 记录审计并交给 Coalescer（尚未送入 Scheduler）。
func (o *p0Outbox) submit(msg outbox.Message, key, text string) {
	o.mu.Lock()
	rec := p0Delivered{msg: msg, key: key, text: text}
	o.audit = append(o.audit, rec)
	o.byCorr[msg.CorrelationID] = rec
	o.mu.Unlock()
	o.coalescer.Submit(msg)
}

// flush 把 Coalescer 中全部待发消息送入 Scheduler 并等待送达。
func (o *p0Outbox) flush() {
	for {
		m, ok := o.coalescer.Next()
		if !ok {
			break
		}
		if err := o.sched.Enqueue(m); err != nil {
			o.t.Fatalf("outbox enqueue: %v", err)
		}
		o.mu.Lock()
		o.enqueued++
		o.mu.Unlock()
	}
	waitFor(o.t, 3*time.Second, func() bool {
		o.drainPending()
		o.mu.Lock()
		defer o.mu.Unlock()
		return len(o.delivered) >= o.enqueued
	}, "outbox settle after flush")
}

// drainPending 非阻塞收集发送替身已送达的消息（补全渲染文本记录）。
func (o *p0Outbox) drainPending() {
	for {
		select {
		case m := <-o.sender.ch:
			o.mu.Lock()
			rec := o.byCorr[m.CorrelationID]
			o.delivered = append(o.delivered, p0Delivered{msg: m, key: rec.key, text: rec.text})
			o.mu.Unlock()
		default:
			return
		}
	}
}

func (o *p0Outbox) snapshot() []p0Delivered {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]p0Delivered(nil), o.delivered...)
}

func (o *p0Outbox) auditSnapshot() []p0Delivered {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]p0Delivered(nil), o.audit...)
}

func (o *p0Outbox) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.sched.Close(ctx); err != nil {
		o.t.Logf("outbox close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// p0World：组合根与场景驱动
// ---------------------------------------------------------------------------

// p0World 是 Task 27 测试的组合根：真实生产组件 + 测试内适配器。
type p0World struct {
	t       *testing.T
	ctx     context.Context
	db      *sql.DB
	repo    *storage.RoomRepository
	users   *storage.UserRepository
	reg     *p0Registry
	store   *p0JoinStore
	clock   *fakeLifecycleClock
	lives   map[game.RoomID]game.LobbyLifetime
	lobby   game.LobbyService
	joinSvc game.JoinService
	life    game.LobbyLifecycleService
	joinH   *telegram.JoinHandler
	leaveH  *telegram.LobbyHandler
	outbox  *p0Outbox

	states map[game.RoomID]game.State
	corr   int
}

// newP0World 组装临时 SQLite（真实迁移）+ 领域服务 + Outbox 测试记录层。
func newP0World(t *testing.T) *p0World {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("migrate temp db: %v", err)
	}
	repo := storage.NewRoomRepository(db)
	users := storage.NewUserRepository(db)
	reg := newP0Registry()
	store := newP0JoinStore(repo, reg)
	clock := &fakeLifecycleClock{now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}

	lobby, err := game.NewLobbyService(reg, newSeqRNG(1))
	if err != nil {
		t.Fatalf("new lobby service: %v", err)
	}
	join, err := game.NewJoinService(store, newSeqRNG(2))
	if err != nil {
		t.Fatalf("new join service: %v", err)
	}
	life, err := game.NewLobbyLifecycleService(clock)
	if err != nil {
		t.Fatalf("new lifecycle service: %v", err)
	}
	return &p0World{
		t:       t,
		ctx:     ctx,
		db:      db,
		repo:    repo,
		users:   users,
		reg:     reg,
		store:   store,
		clock:   clock,
		lives:   make(map[game.RoomID]game.LobbyLifetime),
		lobby:   lobby,
		joinSvc: join,
		life:    life,
		joinH:   telegram.NewJoinHandler(join),
		leaveH:  telegram.NewLobbyHandler(life),
		outbox:  newP0Outbox(t),
		states:  make(map[game.RoomID]game.State),
	}
}

// close 停止 Outbox 调度器（测试结束清理 goroutine）。
func (w *p0World) close() { w.outbox.close() }

// createRoom 建房并交付效果（/newgame 适配层入口；/start 属 App Router 缺口）。
func (w *p0World) createRoom(host game.UserID, commandID, code string) (game.State, error) {
	if err := w.users.Upsert(w.ctx, host, "房主"); err != nil {
		return game.State{}, fmt.Errorf("upsert host: %w", err)
	}
	in := telegram.CreateRoomInput{CommandID: commandID, Actor: host, CustomCode: code}
	h := telegram.NewCreateRoomHandler(w.lobby)
	st, effects, err := h.Create(w.ctx, in)
	if err != nil {
		return game.State{}, err
	}
	// PersistEffect 语义：rooms + 房主 1 号行真实落库。
	if err := w.repo.Create(w.ctx, st.RoomID, host, "lobby"); err != nil {
		return game.State{}, fmt.Errorf("persist create room: %w", err)
	}
	w.reg.setHost(host, st.RoomID)
	w.store.registerHost(st.RoomID, host)
	now := w.clock.Now()
	w.lives[st.RoomID] = game.LobbyLifetime{CreatedAt: now, ExpireAt: now.Add(game.IdleExpireAfter)}
	w.states[st.RoomID] = st
	// 建房面板：创建确认由命令面 commands.newgame_done 承担（Task 46
	// 冒烟修复：领域层 CreateRoom 不再产出 lobby.created），本 harness
	// 模拟生产适配器行为——建房后只投递房间面板一条。
	panel, err := game.NewMessageEffect(game.AudienceHost, game.LobbyPanelMessageKey, map[string]any{"room_code": string(st.RoomID)})
	if err != nil {
		return game.State{}, fmt.Errorf("panel effect: %w", err)
	}
	if err := w.applyEffects(st.RoomID, host, append(effects, panel)); err != nil {
		return game.State{}, err
	}
	return st, nil
}

// join 深链加入（FromInviteDeepLink → JoinHandler → game.JoinService）。
func (w *p0World) join(actor game.UserID, commandID, link string, nickname *string) (game.JoinResult, error) {
	in, ok := telegram.FromInviteDeepLink(link)
	if !ok {
		return game.JoinResult{}, fmt.Errorf("parse deep link %q", link)
	}
	in.CommandID = commandID
	in.Actor = actor
	in.Nickname = nickname
	// users 外键：入房前登记用户（users.nickname 为全局身份，房间昵称以 roster 为准）。
	globalNick := "玩家" + fmt.Sprint(actor)
	if nickname != nil {
		globalNick = *nickname
	}
	if err := w.users.Upsert(w.ctx, actor, globalNick); err != nil {
		return game.JoinResult{}, fmt.Errorf("upsert player: %w", err)
	}
	res, effects, err := w.joinH.Join(w.ctx, in)
	if err != nil {
		return game.JoinResult{}, err
	}
	roomID := game.RoomID(in.RawCode)
	// 加入不返回状态：按 JoinResult 追加成员到状态快照。
	st := w.states[roomID].Copy()
	st.Players = append(st.Players, game.Player{UserID: actor, Seat: res.Seat})
	w.states[roomID] = st
	if err := w.applyEffects(roomID, actor, effects); err != nil {
		return game.JoinResult{}, err
	}
	return res, nil
}

// leave 玩家退出（/leave → LobbyHandler.Leave → game.LobbyLifecycleService）。
func (w *p0World) leave(actor game.UserID, commandID string) (game.State, error) {
	roomID, ok := w.reg.roomOf(actor)
	if !ok {
		return game.State{}, fmt.Errorf("actor %d not in any room", actor)
	}
	in := telegram.LeaveInput{CommandID: commandID, Actor: actor}
	st := w.states[roomID]
	newSt, effects, err := w.leaveH.Leave(w.ctx, in, st)
	if err != nil {
		return game.State{}, err
	}
	// 持久化 + 内存同步（玩家进出不刷新 LobbyLifetime）。
	if err := w.repo.Leave(w.ctx, roomID, actor); err != nil {
		return game.State{}, fmt.Errorf("persist leave: %w", err)
	}
	w.store.recordLeave(roomID, actor)
	w.reg.releaseUser(actor, roomID)
	w.states[roomID] = newSt
	if err := w.applyEffects(roomID, actor, effects); err != nil {
		return game.State{}, err
	}
	return newSt, nil
}

// evaluateIdle 评估大厅闲置回收（FakeClock 已推进）；过期时登记房间过期。
func (w *p0World) evaluateIdle(ctx context.Context, roomID game.RoomID) []game.Effect {
	lt := w.lives[roomID]
	st := w.states[roomID]
	newLt, effects, err := w.life.EvaluateIdle(ctx, lt, st)
	if err != nil {
		w.t.Fatalf("evaluate idle: %v", err)
	}
	w.lives[roomID] = newLt
	for _, e := range effects {
		if me, ok := e.(game.MessageEffect); ok && me.Key == game.RoomExpiredMessageKey {
			w.reg.setExpired(roomID)
		}
	}
	if err := w.applyEffects(roomID, st.Lobby.Owner, effects); err != nil {
		w.t.Fatalf("apply idle effects: %v", err)
	}
	return effects
}

// applyEffects 执行领域 Effects：MessageEffect → 渲染 → Outbox；
// PersistEffect 由对应步骤显式持久化，此处跳过（领域标记语义）。
func (w *p0World) applyEffects(roomID game.RoomID, actor game.UserID, effects []game.Effect) error {
	st := w.states[roomID]
	for _, e := range effects {
		switch te := e.(type) {
		case game.MessageEffect:
			text, err := w.render(te, st)
			if err != nil {
				return err
			}
			chat, err := audienceChat(te.Audience, st, actor)
			if err != nil {
				return err
			}
			w.corr++
			msg := outbox.Message{
				CorrelationID: fmt.Sprintf("%s-%d", string(roomID), w.corr),
				RoomID:        roomID,
				ChatID:        chat,
				Operation:     telegram.OpSendText,
				Priority:      outbox.PriorityNormal,
				CoalesceKey:   panelCoalesceKey(te.Key, roomID),
			}
			w.outbox.submit(msg, string(te.Key), text)
		case game.PersistEffect:
			// 持久化由具体步骤显式执行（见 createRoom/join/leave 注释）。
		default:
			return fmt.Errorf("p0 world: unexpected effect type %T", e)
		}
	}
	return nil
}

// allAudited 返回全部提交审计记录（含被合并覆盖的中间面板版本）。
func (w *p0World) allAudited() []p0Delivered { return w.outbox.auditSnapshot() }

// flush 冲入 Outbox 并等待送达；返回「送达游标」供 sentSince 分步断言。
func (w *p0World) flush() int {
	w.outbox.flush()
	return len(w.outbox.snapshot())
}

// sentSince 返回 [cursor, 当前送达] 窗口内指定 Chat 的新增消息。
// delivered 按送达顺序追加；窗口用 cursor 单调推进保证分步断言互不重叠。
func (w *p0World) sentSince(chat outbox.ChatID, cursor int) []p0Delivered {
	all := w.outbox.snapshot()
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(all) {
		cursor = len(all)
	}
	var out []p0Delivered
	for _, d := range all[cursor:] {
		if d.msg.ChatID == chat {
			out = append(out, d)
		}
	}
	return out
}

// roomPlayers 返回 room_players 表中指定房间的行数（SQLite 真实状态）。
func (w *p0World) roomPlayers(room game.RoomID) int {
	var n int64
	err := w.db.QueryRowContext(w.ctx, `SELECT COUNT(*) FROM room_players WHERE room_code = ?`, string(room)).Scan(&n)
	if err != nil {
		w.t.Fatalf("count room_players: %v", err)
	}
	return int(n)
}

// activeCodes 返回 rooms 表全部活跃房间码（SQLite 真实状态）。
func (w *p0World) activeCodes() []string {
	rows, err := w.repo.ListActive(w.ctx)
	if err != nil {
		w.t.Fatalf("list active rooms: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.RoomCode)
	}
	return out
}

func (w *p0World) expired(room game.RoomID) bool { return w.reg.isExpired(room) }

func (w *p0World) advance(d time.Duration) { w.clock.advance(d) }

// ---------------------------------------------------------------------------
// MarkdownV2 渲染（测试内最小模板；参数统一经 i18n.EscapeMarkdownV2 转义）
// ---------------------------------------------------------------------------

// render 把 game.MessageEffect 渲染为 MarkdownV2 文本。
// 模板为测试内固定文案（代替暂缺的 i18n 面板类资源，属于已知缺口；
// 建房确认由命令面承担，领域层不再产出 lobby.created），
// 所有用户输入（昵称/房间码参数）经 EscapeMarkdownV2 转义；未知 key 报错。
func (w *p0World) render(e game.MessageEffect, st game.State) (string, error) {
	code := paramString(e.Params, "room_code", string(st.RoomID))
	esc := i18n.EscapeMarkdownV2
	switch e.Key {
	case game.JoinConfirmedMessageKey:
		nick := i18n.EscapeMarkdownV2(paramString(e.Params, "nickname", ""))
		seat := paramString(e.Params, "seat", "")
		return fmt.Sprintf("加入成功\n房间码：%s\n昵称：%s\n座位：%s", esc(code), nick, esc(seat)), nil
	case game.LobbyPanelMessageKey:
		return w.renderPanel(game.RoomID(code)), nil
	case game.LeaveConfirmedMessageKey:
		return fmt.Sprintf("已退出房间\n房间码：%s", esc(code)), nil
	case game.HostTransferredMessageKey:
		return fmt.Sprintf("你已成为新房主\n房间码：%s", esc(code)), nil
	case game.KickedMessageKey:
		return fmt.Sprintf("已被移出房间\n房间码：%s", esc(code)), nil
	case game.RenewedMessageKey:
		return fmt.Sprintf("房间已续期 1 小时\n房间码：%s", esc(code)), nil
	case game.IdleReminderMessageKey:
		return fmt.Sprintf("房间即将过期\n房间码：%s", esc(code)), nil
	case game.RoomExpiredMessageKey:
		return fmt.Sprintf("房间已过期\n房间码：%s", esc(code)), nil
	default:
		return "", fmt.Errorf("render: unknown message key %q", e.Key)
	}
}

// renderPanel 依据内存 roster 渲染房间面板（成员按座位升序）。
func (w *p0World) renderPanel(roomID game.RoomID) string {
	members := w.store.members(roomID)
	var b strings.Builder
	fmt.Fprintf(&b, "房间面板\n房间码：%s\n人数：%d/%d\n成员：\n", i18n.EscapeMarkdownV2(string(roomID)), len(members), game.MVPPlayerCount)
	for _, m := range members {
		line := fmt.Sprintf("%d号 %s", m.seat, i18n.EscapeMarkdownV2(m.nickname))
		if m.seat == game.HostSeat {
			line += "（房主）"
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func paramString(params map[string]any, key, fallback string) string {
	if v, ok := params[key]; ok {
		return fmt.Sprint(v)
	}
	return fallback
}

// audienceChat 把消息受众映射为 Telegram ChatID：
// AudienceActor → 操作者本人；AudienceHost → 房主；其余本场景不产生，报错。
func audienceChat(a game.Audience, st game.State, actor game.UserID) (outbox.ChatID, error) {
	switch a {
	case game.AudienceActor:
		return outbox.ChatID(actor), nil
	case game.AudienceHost:
		return outbox.ChatID(st.Lobby.Owner), nil
	case game.AudiencePublic:
		return 0, fmt.Errorf("audience public is not expected in lobby-only scenario (identity leak guard)")
	default:
		return 0, fmt.Errorf("audience %v not supported in p0 harness", a)
	}
}

// panelCoalesceKey 为面板消息生成稳定合并键（复用 outbox.Coalescer 语义）。
func panelCoalesceKey(key string, roomID game.RoomID) string {
	if key == game.LobbyPanelMessageKey {
		return "lobby.panel:" + string(roomID)
	}
	return ""
}
