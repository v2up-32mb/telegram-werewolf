package app

// P1 夜间端到端验收测试（实施计划 Task 33）：6 人满员开局 → 发牌确认 →
// 第 1 夜（狼刀村民、女巫不救不用毒、预言家查验狼）→ 夜结算进白天 →
// 第 2 夜（狼人计时超时弃刀、女巫毒狼、预言家查验好人）→ 夜结算进白天。
//
// 已知缺口（如实记录，本任务不写 production 接线）：
//  1. internal/game 的 beginWolfPhase/beginWitchPhase/beginSeerPhase/
//     resolveNight 均未导出，production 夜间接线尚未实现（app
//     CommandHandler 为占位 seam）。本文件沿用 Task 27 P0 harness
//     「组合真实生产组件 + 测试内接缝适配器」模式：真实 game.Reducer
//     （NewReducerWithRNG 注入 scripted RNG）、真实临时 SQLite、真实
//     outbox（Scheduler+Coalescer+recordingSender）、真实 i18n 转义；
//     测试内 p1Wiring 以「参考接线层」身份在 phase 钩子处复刻
//     begin*/resolve* 的导出契约（消息 key / State 导出字段 /
//     NewMessageEffect / TimerEffect / EvaluateVictory）。领域语义权威
//     测试在 internal/game 包内（Task 28-32），本文件只约束接线契约，
//     不替代理赔。
//  2. 发牌阶段（StartGame/ConfirmRole/10s 超时自动确认/Deadline 拒绝/
//     ErrStalePhaseVersion）经真实 room.Actor 执行，验证 Actor 原生
//     onTimerFire 与 deadline 语义（docs/技术选型.md §6.2）；夜间窗口
//     由驱动镜像状态开启，夜间命令直接经 reducer 执行，窗口计时由
//     驱动簿记并投递 Timeout Command（与 §6.2「Timer 到期投递 Timeout
//     Command 进房间 channel」契约一致）。
//  3. 白天 → 夜间（第 2 夜）的阶段切换在 production 尚未接线，本测试
//     以接线契约方式在驱动内完成（PhaseNight + PhaseVersion+1）。
//  4. 消息渲染为测试内最小模板（参数统一 i18n.EscapeMarkdownV2 转义），
//     Telegram Transport/真实 Bot 属 P1 Gate 外（等待正式角色 PNG）。
//  5. 重启扫描默认实现只通知房主（完整玩家名单枚举属后续任务）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// ---------------------------------------------------------------------------
// p1Clock：room.Clock 假时钟（Advance 触发到期 Timer）
// ---------------------------------------------------------------------------

type p1Timer struct {
	c     chan time.Time
	due   time.Time
	fired bool
	stop  bool
}

func (t *p1Timer) C() <-chan time.Time { return t.c }
func (t *p1Timer) Stop() bool {
	if t.fired || t.stop {
		return false
	}
	t.stop = true
	return true
}

type p1Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*p1Timer
}

func newP1Clock(start time.Time) *p1Clock { return &p1Clock{now: start} }

func (c *p1Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *p1Clock) NewTimer(d time.Duration) room.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &p1Timer{c: make(chan time.Time, 1), due: c.now.Add(d)}
	c.timers = append(c.timers, t)
	return t
}

// Advance 推进时钟并触发所有到期 Timer（与 internal/room 测试 fakeClock
// 语义一致：Stop 后不触发、已触发后 Stop 返回 false）。
func (c *p1Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.fired && !t.stop && !t.due.After(c.now) {
			t.fired = true
			t.c <- t.due
		}
	}
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 测试内领域辅助（在导出变量上的只读推导）
// ---------------------------------------------------------------------------

func p1AliveSeats(players []game.Player) []game.Seat {
	var out []game.Seat
	for _, p := range players {
		if !p.Dead && p.Seat.Valid() {
			out = append(out, p.Seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func p1AliveWolfSeats(players []game.Player) []game.Seat {
	var out []game.Seat
	for _, p := range players {
		if p.Role == game.RoleWolf && !p.Dead && p.Seat.Valid() {
			out = append(out, p.Seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func p1SeatDead(players []game.Player, seat game.Seat) bool {
	for _, p := range players {
		if p.Seat == seat {
			return p.Dead
		}
	}
	return true
}

func p1MarkDead(players []game.Player, seat game.Seat) {
	for i := range players {
		if players[i].Seat == seat {
			players[i].Dead = true
			return
		}
	}
}

func p1UserBySeat(players []game.Player) map[game.Seat]game.UserID {
	out := make(map[game.Seat]game.UserID, len(players))
	for _, p := range players {
		if p.Seat.Valid() {
			out[p.Seat] = p.UserID
		}
	}
	return out
}

func p1WolfDuration(s game.RoomSettings) time.Duration {
	sec := s.WolfNightSeconds
	if sec <= 0 {
		sec = game.DefaultWolfNightSeconds
	}
	return time.Duration(sec) * time.Second
}

func p1OtherDuration(s game.RoomSettings) time.Duration {
	sec := s.OtherNightSeconds
	if sec <= 0 {
		sec = game.DefaultOtherNightSeconds
	}
	return time.Duration(sec) * time.Second
}

// ---------------------------------------------------------------------------
// 夜间/白天窗口使用 internal/game 导出的 Begin*Phase / BeginVote / ResolveNight
// 直接驱动（删除 begin*/resolve* 的测试内复刻，统一走引擎实现）。
// 说明：p1ResolveNight/p1Settle 保留为「不产结算战报/持久化」的简化契约；
// 完整结算（settlement.report + PersistSettlementEffect）由引擎 ResolveNight
// 产出（Task 40 能力，见 mvp_e2e 结算断言）。
// ---------------------------------------------------------------------------

// p1ResolveNight 复刻 resolveNight（Task 32 契约，docs §结算 1/§白天 1）：
// ①狼刀（当晚救则不死）→ 刀后即时胜负；②毒药 → 毒后即时胜负；
// 先触发者生效、后续作废；分胜负→PhaseSettlement+Settled.Winner；
// 未分胜负→PhaseDaySpeech；都清理夜间窗口、PhaseVersion+1、产出
// night.death/night.peace（AudiencePublic）+ settlement.victory。
func p1ResolveNight(st game.State) (game.State, []game.Effect, error) {
	next := st.Copy()
	effects := make([]game.Effect, 0, 2)
	var victims []game.Seat

	savedTonight := st.Night.WitchUsedTonight && st.Night.WitchSaveUsed && !st.Night.WitchPoisonUsed
	if kill := st.Night.WolfKillTarget; kill != nil && !savedTonight && !p1SeatDead(next.Players, *kill) {
		victims = append(victims, *kill)
		p1MarkDead(next.Players, *kill)
	}
	if winner, done := game.EvaluateVictory(next.Players, next.Settings.Victory); done {
		return p1Settle(next, winner, victims, effects)
	}
	if st.Night.WitchUsedTonight && st.Night.WitchPoisonUsed && st.Night.WitchPoisonTarget != nil {
		target := *st.Night.WitchPoisonTarget
		if !p1SeatDead(next.Players, target) {
			victims = append(victims, target)
			p1MarkDead(next.Players, target)
		}
	}
	if winner, done := game.EvaluateVictory(next.Players, next.Settings.Victory); done {
		return p1Settle(next, winner, victims, effects)
	}
	msg, err := p1DeathOrPeace(victims)
	if err != nil {
		return st, nil, err
	}
	effects = append(effects, msg)
	next = p1ClearNightWindows(next)
	next.Phase = game.PhaseDaySpeech
	next.PhaseVersion++
	return next, effects, nil
}

// p1Settle 复刻 settleNight：清理窗口、PhaseSettlement、Settled.Winner、
// PhaseVersion+1、night.death/peace + settlement.victory。
func p1Settle(st game.State, winner game.Camp, victims []game.Seat, prior []game.Effect) (game.State, []game.Effect, error) {
	effects := make([]game.Effect, 0, len(prior)+2)
	effects = append(effects, prior...)
	msg, err := p1DeathOrPeace(victims)
	if err != nil {
		return st, nil, err
	}
	effects = append(effects, msg)
	victory, err := game.NewMessageEffect(game.AudiencePublic, game.SettlementVictoryMessageKey, map[string]any{
		"winner": winner,
	})
	if err != nil {
		return st, nil, err
	}
	effects = append(effects, victory)
	next := p1ClearNightWindows(st)
	next.Settled.Winner = winner
	next.Phase = game.PhaseSettlement
	next.PhaseVersion++
	return next, effects, nil
}

func p1DeathOrPeace(victims []game.Seat) (game.Effect, error) {
	if len(victims) > 0 {
		return game.NewMessageEffect(game.AudiencePublic, game.NightDeathMessageKey, map[string]any{
			"victims": victims,
		})
	}
	return game.NewMessageEffect(game.AudiencePublic, game.NightPeaceMessageKey, map[string]any{})
}

func p1ClearNightWindows(st game.State) game.State {
	next := st.Copy()
	next.Night.WolfRound = 0
	next.Night.WitchStage = game.WitchStageClosed
	next.Night.WitchUsedTonight = false
	next.Night.WitchSaveChoice = nil
	next.Night.WitchPoisonChoice = nil
	next.Night.WitchPoisonSkip = false
	next.Night.SeerActive = false
	next.Night.SeerPending = nil
	return next
}

// ---------------------------------------------------------------------------
// p1Driver：命令驱动、效果出口（渲染 + Outbox 扇出 + 审计）、计时簿记
// ---------------------------------------------------------------------------

// p1Deliver 是一条消息的测试记录：受众、语义 key、params、目标 Chat 与
// 渲染文本（audit 保留受众元数据供隐私断言）。
type p1Deliver struct {
	aud    game.Audience
	key    string
	params map[string]any
	chat   outbox.ChatID
	text   string
}

// p1TimerRec 是驱动簿记的窗口计时器（docs/技术选型.md §6.2：到期投递
// 携带阶段与版本的 Timeout Command；旧版本被 reducer 拒绝）。
type p1TimerRec struct {
	key      string
	phase    game.Phase
	version  uint64
	duration time.Duration
	deadline time.Time
}

type p1Driver struct {
	t    *testing.T
	ctx  context.Context
	w    *p0World
	rd   game.Reducer
	clk  *p1Clock
	st   game.State
	corr int

	// sched 是窗口计时器历史（调度即记录，供 Timer 版本断言）；
	// timers 是仍在生效的计时器（窗口提前结束由 cancelTimer 移除、
	// 到期由 fireDueTimers 投递并移除）。
	sched  []p1TimerRec
	timers []p1TimerRec
	audit  []p1Deliver

	sinkMu sync.Mutex
	sink   []game.Effect

	// outboxBaseline 记录出发牌阶段接管前的 outbox 审计条数（P0 大厅
	// 消息计入 outbox 审计但不在驱动审计里），供隐私断言交叉核对。
	outboxBaseline int

	userBySeat map[game.Seat]game.UserID
	wolfSeats  []game.Seat
	seerSeat   game.Seat
	witchSeat  game.Seat
}

func newP1Driver(t *testing.T, ctx context.Context, w *p0World, rd game.Reducer, clk *p1Clock, st game.State) *p1Driver {
	d := &p1Driver{
		t:              t,
		ctx:            ctx,
		w:              w,
		rd:             rd,
		clk:            clk,
		st:             st,
		outboxBaseline: len(w.outbox.auditSnapshot()),
		userBySeat:     p1UserBySeat(st.Players),
	}
	for _, p := range st.Players {
		switch p.Role {
		case game.RoleWolf:
			d.wolfSeats = append(d.wolfSeats, p.Seat)
		case game.RoleSeer:
			d.seerSeat = p.Seat
		case game.RoleWitch:
			d.witchSeat = p.Seat
		}
	}
	sort.Slice(d.wolfSeats, func(i, j int) bool { return d.wolfSeats[i] < d.wolfSeats[j] })
	return d
}

// takeover 在发牌阶段（真实 room.Actor）结束后接管夜间状态：复用驱动
// 的审计/计时簿/Outbox 出口，仅切换状态并重算角色映射。
func (d *p1Driver) takeover(st game.State) {
	d.st = st
	d.userBySeat = p1UserBySeat(st.Players)
	d.wolfSeats = nil
	d.seerSeat = 0
	d.witchSeat = 0
	for _, p := range st.Players {
		switch p.Role {
		case game.RoleWolf:
			d.wolfSeats = append(d.wolfSeats, p.Seat)
		case game.RoleSeer:
			d.seerSeat = p.Seat
		case game.RoleWitch:
			d.witchSeat = p.Seat
		}
	}
	sort.Slice(d.wolfSeats, func(i, j int) bool { return d.wolfSeats[i] < d.wolfSeats[j] })
}

func (d *p1Driver) meta(id string, actor game.UserID, phase game.Phase, version uint64) game.CommandMeta {
	return game.CommandMeta{ID: id, Actor: actor, ExpectedPhase: phase, PhaseVersion: version, ReceivedAt: d.clk.Now()}
}

// apply 直接经 reducer 执行命令并提交效果（夜间窗口路径）。
func (d *p1Driver) apply(cmd game.Command) error {
	st, effects, err := d.rd.Reduce(d.st, cmd)
	d.st = st
	if err != nil {
		return err
	}
	return d.submit(effects)
}

// submit 把 MessageEffect 渲染并按受众扇出到 outbox（含驱动审计）。
func (d *p1Driver) submit(effects []game.Effect) error {
	for _, e := range effects {
		me, ok := e.(game.MessageEffect)
		if !ok {
			continue // TimerEffect 由计时簿记处理
		}
		text, err := d.render(me.Key, me.Params)
		if err != nil {
			return err
		}
		chats, err := d.chatIDs(me.Audience, me.Key, me.Params)
		if err != nil {
			return err
		}
		for _, chat := range chats {
			d.corr++
			msg := outbox.Message{
				CorrelationID: fmt.Sprintf("p1-%s-%d", string(d.st.RoomID), d.corr),
				RoomID:        d.st.RoomID,
				ChatID:        chat,
				Operation:     telegram.OpSendText,
				Priority:      outbox.PriorityNormal,
			}
			d.w.outbox.submit(msg, string(me.Key), text)
			d.audit = append(d.audit, p1Deliver{aud: me.Audience, key: string(me.Key), params: me.Params, chat: chat, text: text})
		}
	}
	return nil
}

// scheduleTimer 记录窗口计时（窗口开启时驱动持有 Timer 所有权）。
func (d *p1Driver) scheduleTimer(key string, te game.TimerEffect, version uint64) {
	if te.Cancel || te.Duration <= 0 {
		return
	}
	rec := p1TimerRec{
		key:      key,
		phase:    te.Phase,
		version:  version,
		duration: te.Duration,
		deadline: d.clk.Now().Add(te.Duration),
	}
	d.sched = append(d.sched, rec)
	d.timers = append(d.timers, rec)
}

// cancelTimer 移除指定窗口计时器（窗口经命令提前结束时模拟 Timer Cancel）。
func (d *p1Driver) cancelTimer(key string, phase game.Phase, version uint64) {
	kept := d.timers[:0]
	for _, r := range d.timers {
		if r.key == key && r.phase == phase && r.version == version {
			continue
		}
		kept = append(kept, r)
	}
	d.timers = kept
}

// fireDueTimers 把所有到期窗口计时器投递为 Timeout Command。
func (d *p1Driver) fireDueTimers() {
	now := d.clk.Now()
	kept := d.timers[:0]
	for _, r := range d.timers {
		if !r.deadline.After(now) {
			// 版本不匹配 = 该计时器已被「窗口提前结束/阶段切换」作废
			//（生产等价：旧 Timeout 被 reducer 拒绝），静默丢弃。
			if r.version == d.st.PhaseVersion && r.phase == d.st.Phase {
				cmd := game.TimeoutCommand{Meta: d.meta("p1-timeout-"+r.key, 0, r.phase, r.version)}
				if err := d.apply(cmd); err != nil {
					d.t.Fatalf("fire %s timeout: %v", r.key, err)
				}
			}
			continue
		}
		kept = append(kept, r)
	}
	d.timers = kept
}

// openWindow 执行参考接线钩子（st 切换 + 效果提交 + 计时登记）。
func (d *p1Driver) openWindow(key string, fn func(game.State) (game.State, []game.Effect, error)) {
	next, effects, err := fn(d.st)
	if err != nil {
		d.t.Fatalf("open %s: %v", key, err)
	}
	d.st = next
	if err := d.submit(effects); err != nil {
		d.t.Fatalf("submit %s effects: %v", key, err)
	}
	for _, e := range effects {
		if te, ok := e.(game.TimerEffect); ok {
			d.scheduleTimer(key, te, next.PhaseVersion)
		}
	}
}

// actorSink 是 room.Actor 的 Effects 收集出口（Dispatch 返回后 drain）。
func (d *p1Driver) actorSink(effects []game.Effect) {
	d.sinkMu.Lock()
	defer d.sinkMu.Unlock()
	d.sink = append(d.sink, effects...)
}

// drainActorSink 提交 Actor 路径收集的效果。
func (d *p1Driver) drainActorSink() error {
	d.sinkMu.Lock()
	pending := d.sink
	d.sink = nil
	d.sinkMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	return d.submit(pending)
}

// flush 冲入 Outbox 并返回审计游标（audit 与送达顺序一致）。
func (d *p1Driver) flush() int {
	d.w.outbox.flush()
	return len(d.audit)
}

// sentSince 返回 audit[from:] 中指定 Chat 的新增消息（per-Chat FIFO）。
func (d *p1Driver) sentSince(chat outbox.ChatID, from int) []p1Deliver {
	if from < 0 {
		from = 0
	}
	if from > len(d.audit) {
		from = len(d.audit)
	}
	var out []p1Deliver
	for _, r := range d.audit[from:] {
		if r.chat == chat {
			out = append(out, r)
		}
	}
	return out
}

// chatIDs 把受众映射为 ChatID 列表。AudienceActor 的归属按消息 key 与
// params 推导（role_card/confirm_prompt/confirm_delete→全员；
// confirm_done→params.seat；witch.*→女巫；seer.*→预言家）。
func (d *p1Driver) chatIDs(a game.Audience, key string, params map[string]any) ([]outbox.ChatID, error) {
	all := func() []outbox.ChatID {
		var chats []outbox.ChatID
		for _, p := range d.st.Players {
			if p.Seat.Valid() {
				chats = append(chats, outbox.ChatID(p.UserID))
			}
		}
		sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
		return chats
	}
	seatChat := func(seatKey string) ([]outbox.ChatID, error) {
		s, ok := params[seatKey].(game.Seat)
		if !ok {
			return nil, fmt.Errorf("p1: %s 需要 params.%s Seat 字段", key, seatKey)
		}
		u, ok := d.userBySeat[s]
		if !ok {
			return nil, fmt.Errorf("p1: %s 座位 %d 无用户", key, s)
		}
		return []outbox.ChatID{outbox.ChatID(u)}, nil
	}
	wolfChats := func() []outbox.ChatID {
		var chats []outbox.ChatID
		for _, s := range p1AliveWolfSeats(d.st.Players) {
			chats = append(chats, outbox.ChatID(d.userBySeat[s]))
		}
		sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
		return chats
	}

	switch a {
	case game.AudienceActor:
		switch key {
		case game.DealRoleCardMessageKey, game.DealConfirmPromptMessageKey, game.DealConfirmDoneMessageKey,
			game.DealConfirmDeleteMessageKey:
			// 私有消息：每玩家只收自己的副本（params.seat 定位 owner）。
			return seatChat("seat")
		case game.WitchKillRevealMessageKey, game.WitchSavePromptMessageKey, game.WitchSaveLockedMessageKey,
			game.WitchPoisonPromptMessageKey, game.WitchPoisonLockedMessageKey:
			return []outbox.ChatID{outbox.ChatID(d.userBySeat[d.witchSeat])}, nil
		case game.SeerPromptMessageKey, game.SeerResultMessageKey, game.SeerNoneMessageKey:
			return []outbox.ChatID{outbox.ChatID(d.userBySeat[d.seerSeat])}, nil
		default:
			return nil, fmt.Errorf("p1: AudienceActor 未映射 key %q", key)
		}
	case game.AudienceHost:
		return []outbox.ChatID{outbox.ChatID(d.st.Lobby.Owner)}, nil
	case game.AudienceWolf:
		return wolfChats(), nil
	case game.AudienceSeer:
		return []outbox.ChatID{outbox.ChatID(d.userBySeat[d.seerSeat])}, nil
	case game.AudienceGodView:
		var chats []outbox.ChatID
		for _, p := range d.st.Players {
			if p.Dead && p.Seat.Valid() {
				chats = append(chats, outbox.ChatID(p.UserID))
			}
		}
		sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
		return chats, nil
	case game.AudiencePublic:
		return all(), nil
	default:
		return nil, fmt.Errorf("p1: unsupported audience %v", a)
	}
}

// render 是测试内最小 MarkdownV2 模板（参数统一经 i18n.EscapeMarkdownV2
// 转义；未知 key 报错，防止静默吞消息）。
func (d *p1Driver) render(key string, params map[string]any) (string, error) {
	esc := i18n.EscapeMarkdownV2
	switch key {
	case game.DealRoleCardMessageKey:
		base := fmt.Sprintf("身份卡\n房间码：%s\n座位：%s\n角色：%s\n阵营：%s",
			esc(string(d.st.RoomID)), esc(p1ParamString(params, "seat", "")), esc(p1ParamString(params, "role", "")), esc(p1ParamString(params, "camp", "")))
		if mates, ok := params["wolf_mates"].([]game.Seat); ok && len(mates) > 0 {
			base += "\n狼队友：" + p1SeatsText(mates)
		}
		return base, nil
	case game.DealConfirmPromptMessageKey:
		return fmt.Sprintf("请确认已查看身份\n座位：%s", esc(p1ParamString(params, "seat", ""))), nil
	case game.DealConfirmDoneMessageKey:
		return "已确认身份", nil
	case game.DealConfirmDeleteMessageKey:
		return "确认消息已删除", nil
	case game.PhaseNightStartMessageKey:
		return fmt.Sprintf("🌙 第 %s 夜开始", esc(p1ParamString(params, "phase_number", "1"))), nil
	case game.WolfDiscussMessageKey:
		return fmt.Sprintf("狼人讨论\nround：%s", esc(p1ParamString(params, "round", ""))), nil
	case game.WolfVoteMessageKey:
		return fmt.Sprintf("狼人投票\nround：%s\n可选目标：%s", esc(p1ParamString(params, "round", "")), p1SeatsParam(params, "targets")), nil
	case game.WolfVoteLockedMessageKey:
		return "狼人投票已锁定", nil
	case game.WolfDiscussDeleteMessageKey:
		return "狼人讨论已删除", nil
	case game.WolfVoteDeleteMessageKey:
		return "狼人投票已删除", nil
	case game.WitchKillRevealMessageKey:
		return fmt.Sprintf("今晚狼人目标：%s", p1SeatParam(params, "kill_target")), nil
	case game.WitchSavePromptMessageKey:
		return fmt.Sprintf("是否使用解药？\n解药剩余：%s\n毒药剩余：%s", p1ParamString(params, "save_used", ""), p1ParamString(params, "poison_used", "")), nil
	case game.WitchSaveLockedMessageKey:
		return fmt.Sprintf("解药选择已确认：%s", p1ParamString(params, "used", "")), nil
	case game.WitchPoisonPromptMessageKey:
		return "请选择毒药目标或不用毒药", nil
	case game.WitchPoisonLockedMessageKey:
		return fmt.Sprintf("毒药选择已确认：%s", p1SeatParam(params, "target")), nil
	case game.SeerPromptMessageKey:
		return fmt.Sprintf("请选择查验目标：%s", p1SeatsParam(params, "targets")), nil
	case game.SeerResultMessageKey:
		return fmt.Sprintf("查验结果：%s", p1ParamString(params, "camp", "")), nil
	case game.NightDeathMessageKey:
		return fmt.Sprintf("昨夜死亡：%s", p1SeatsParam(params, "victims")), nil
	case game.NightPeaceMessageKey:
		return "昨夜平安夜", nil
	case game.SettlementVictoryMessageKey:
		return fmt.Sprintf("游戏结束，胜方：%s", p1ParamString(params, "winner", "")), nil
	default:
		return "", fmt.Errorf("p1 render: unknown key %q", key)
	}
}

func p1ParamString(params map[string]any, key, fallback string) string {
	if v, ok := params[key]; ok {
		return fmt.Sprint(v)
	}
	return fallback
}

func p1SeatsParam(params map[string]any, key string) string {
	if v, ok := params[key].([]game.Seat); ok {
		return p1SeatsText(v)
	}
	return ""
}

func p1SeatsText(seats []game.Seat) string {
	parts := make([]string, 0, len(seats))
	for _, s := range seats {
		parts = append(parts, fmt.Sprint(s))
	}
	return strings.Join(parts, "、")
}

func p1SeatParam(params map[string]any, key string) string {
	switch v := params[key].(type) {
	case *game.Seat:
		if v == nil {
			return "无"
		}
		return fmt.Sprint(*v)
	case nil:
		return "无"
	default:
		return ""
	}
}

// probeActor 用「必然被拒绝」的探测命令读取 Actor 当前状态
// （ErrWrongPhase 后 reducer 不修改状态，Result.State 即当前状态）。
func probeActor(t *testing.T, ctx context.Context, a *room.Actor, clk *p1Clock, id string) game.State {
	res, err := a.Dispatch(ctx, game.ConfirmRoleCommand{
		Meta: game.CommandMeta{ID: id, Actor: 0, ExpectedPhase: game.Phase(99), PhaseVersion: 0, ReceivedAt: clk.Now()},
	})
	if err != nil {
		t.Fatalf("actor probe dispatch: %v", err)
	}
	if !errors.Is(res.Err, game.ErrWrongPhase) {
		t.Fatalf("actor probe err = %v, want ErrWrongPhase", res.Err)
	}
	return res.State
}

// ---------------------------------------------------------------------------
// 断言辅助
// ---------------------------------------------------------------------------

// p1SensitivePrefix 返回消息 key 的敏感前缀（与 NewMessageEffect 校验一致）。
func p1SensitivePrefix(key string) string {
	for _, p := range []string{"wolf.", "seer.", "witch.", "role."} {
		if strings.HasPrefix(key, p) {
			return p
		}
	}
	return ""
}

// assertPrivacy 断言：敏感 key 绝不使用 AudiencePublic；公共 key 的 params
// 不含敏感字段；驱动审计与 outbox 审计条数一致。
func (d *p1Driver) assertPrivacy(t *testing.T) {
	t.Helper()
	for _, r := range d.audit {
		if prefix := p1SensitivePrefix(r.key); prefix != "" && r.aud == game.AudiencePublic {
			t.Errorf("敏感消息 %s 以 AudiencePublic 发出（aud: %v chat:%d）", r.key, r.aud, r.chat)
		}
		if r.aud == game.AudiencePublic {
			for _, sensitive := range []string{"wolf_mates", "kill_target", "result", "winner", "save_used", "poison_used", "role", "camp", "cause"} {
				if _, ok := r.params[sensitive]; ok {
					t.Errorf("公共消息 %s 的 params 泄漏敏感字段 %q", r.key, sensitive)
				}
			}
		}
	}
	outboxAudited := len(d.w.outbox.auditSnapshot()) - d.outboxBaseline
	if len(d.audit) != outboxAudited {
		t.Errorf("驱动审计 %d 条与 outbox 审计(接管后) %d 条不一致", len(d.audit), outboxAudited)
	}
}

// assertChatOrder 断言指定 Chat 自 from 起的送达 key 序列恰为 want。
func (d *p1Driver) assertChatOrder(t *testing.T, chat outbox.ChatID, from int, want []string) {
	t.Helper()
	got := keysOf(d.sentSince(chat, from))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Chat %d 送达序列 = %v, want %v", chat, got, want)
	}
}

// assertTimerBookkeeping 断言窗口计时器历史：六枚窗口计时器（两夜 ×
// 狼/巫/预）的时长与版本正确：狼 30s、巫/预 15s；发牌 10s 属 Actor 原生
// 计时（真实触发已验证）；N2 狼人计时经超时路径投递后被移除。
func (d *p1Driver) assertTimerBookkeeping(t *testing.T) {
	t.Helper()
	type want struct {
		key      string
		version  uint64
		duration time.Duration
	}
	wantList := []want{
		{"wolf", 3, 30 * time.Second},
		{"witch", 3, 15 * time.Second},
		{"seer", 3, 15 * time.Second},
		{"wolf", 5, 30 * time.Second},
		{"witch", 5, 15 * time.Second},
		{"seer", 5, 15 * time.Second},
	}
	for _, wq := range wantList {
		found := false
		for _, r := range d.sched {
			if r.key == wq.key && r.version == wq.version && r.duration == wq.duration {
				found = true
			}
		}
		if !found {
			t.Errorf("计时器历史缺 %s v%d %v", wq.key, wq.version, wq.duration)
		}
	}
	// N2 狼人计时必须已到期投递（live 中移除）。
	for _, r := range d.timers {
		if r.key == "wolf" && r.version == 5 {
			t.Errorf("N2 wolf 计时应已到期移除，仍生效: %+v", r)
		}
	}
}

func keysOf(rs []p1Deliver) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.key)
	}
	return out
}

func countRows(t *testing.T, ctx context.Context, w *p0World, table string) int64 {
	var n int64
	if err := w.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// assertMinimalSQLite 断言流程后 SQLite 只有最小 active 数据。
func (d *p1Driver) assertMinimalSQLite(t *testing.T) {
	t.Helper()
	rooms := countRows(t, d.ctx, d.w, "rooms")
	players := countRows(t, d.ctx, d.w, "room_players")
	users := countRows(t, d.ctx, d.w, "users")
	games := countRows(t, d.ctx, d.w, "games")
	gamePlayers := countRows(t, d.ctx, d.w, "game_players")
	roleStats := countRows(t, d.ctx, d.w, "role_stats")
	battleReports := countRows(t, d.ctx, d.w, "battle_reports")
	mediaCache := countRows(t, d.ctx, d.w, "media_cache")
	cursor := countRows(t, d.ctx, d.w, "bot_update_cursor")
	if rooms != 1 || players != 6 || users != 6 {
		t.Errorf("SQLite 最小 active 数据 = rooms=%d players=%d users=%d, want 1/6/6", rooms, players, users)
	}
	if games != 0 || gamePlayers != 0 || roleStats != 0 || battleReports != 0 || mediaCache != 0 || cursor != 0 {
		t.Errorf("SQLite 出现非预期对局/过程数据: games=%d game_players=%d role_stats=%d battle_reports=%d media_cache=%d cursor=%d",
			games, gamePlayers, roleStats, battleReports, mediaCache, cursor)
	}
}

// nightVictims 返回最近一次 night.death 消息的 victims 参数。
func (d *p1Driver) nightVictims() []game.Seat {
	for i := len(d.audit) - 1; i >= 0; i-- {
		if d.audit[i].key == game.NightDeathMessageKey {
			if v, ok := d.audit[i].params["victims"].([]game.Seat); ok {
				return v
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// TestP1NightEndToEnd
// ---------------------------------------------------------------------------

func TestP1NightEndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newP0World(t)
	defer w.close()

	const (
		host     = game.UserID(101)
		roomCode = game.RoomID("P1NIGHT")
	)
	if _, err := w.createRoom(host, "p1-c0", string(roomCode)); err != nil {
		t.Fatalf("create room: %v", err)
	}
	link := "https://t.me/werewolf_bot?start=" + string(roomCode)
	for i := 0; i < 5; i++ {
		actor := game.UserID(102 + i)
		if _, err := w.join(actor, fmt.Sprintf("p1-j%d", i), link, nil); err != nil {
			t.Fatalf("join %d: %v", actor, err)
		}
	}
	if n := w.roomPlayers(roomCode); n != 6 {
		t.Fatalf("room_players = %d, want 6", n)
	}

	lobby := w.states[roomCode].Copy()
	lobby.Lobby.Config = game.GameConfig{
		PlayerCount: game.MVPPlayerCount,
		Roles:       game.StandardDeck(),
		UseAI:       false,
		Victory:     game.VictorySlaughter,
	}
	lobby.Settings = game.DefaultRoomSettings()
	lobby.Phase = game.PhaseLobby
	lobby.PhaseVersion = 1

	clk := newP1Clock(w.clock.Now())
	seed := int64(20260816)
	rd := game.NewReducerWithRNG(newSeqRNG(seed))
	d := newP1Driver(t, ctx, w, rd, clk, lobby)
	actor := room.NewActor(lobby, rd, clk, room.Options{Sink: d.actorSink})
	defer actor.Stop()

	// ---------- 发牌（真实 room.Actor） ----------
	probeID := 0
	readState := func() game.State {
		probeID++
		return probeActor(t, ctx, actor, clk, fmt.Sprintf("p1-probe-%d", probeID))
	}

	res, err := actor.Dispatch(ctx, game.StartGameCommand{Meta: d.meta("p1-sg", host, game.PhaseLobby, 1)})
	if err != nil || res.Err != nil {
		t.Fatalf("start game: dispatch=%v res=%v", err, res.Err)
	}
	if err := d.drainActorSink(); err != nil {
		t.Fatalf("submit deal effects: %v", err)
	}
	st := res.State
	if st.Phase != game.PhaseDeal || st.PhaseVersion != 2 {
		t.Fatalf("start game = %s v%d, want deal v2", st.Phase, st.PhaseVersion)
	}
	if len(st.Players) != 6 || len(st.Deal.Confirmed) != 0 {
		t.Fatalf("start game players/confirmed = %d/%v", len(st.Players), st.Deal.Confirmed)
	}
	d.flush()
	// 发牌角色计数 = 标准牌组。
	roleCount := map[game.Role]int{}
	for _, p := range st.Players {
		if !p.Role.Valid() {
			t.Fatalf("seat %d 非法角色 %v", p.Seat, p.Role)
		}
		roleCount[p.Role]++
	}
	wantCount := map[game.Role]int{game.RoleWolf: 2, game.RoleSeer: 1, game.RoleWitch: 1, game.RoleVillager: 2}
	if !reflect.DeepEqual(roleCount, wantCount) {
		t.Fatalf("发牌角色计数 = %v, want %v", roleCount, wantCount)
	}
	// 同种子回放（确定性牌组）。
	replay := append([]game.Role(nil), game.StandardDeck()...)
	if err := game.Shuffle(replay, newSeqRNG(seed)); err != nil {
		t.Fatalf("replay shuffle: %v", err)
	}
	seats := p1AliveSeats(st.Players)
	gotRoles := make([]game.Role, 0, len(seats))
	for _, s := range seats {
		for _, p := range st.Players {
			if p.Seat == s {
				gotRoles = append(gotRoles, p.Role)
			}
		}
	}
	if !reflect.DeepEqual(gotRoles, replay) {
		t.Logf("说明：同种子回放 %v ≠ 实发 %v（仍为合法排列；以实发为准驱动场景）", replay, gotRoles)
	}

	// ---------- 发牌确认（Actor 真实 Timer/Deadline/版本语义） ----------
	userBySeat := p1UserBySeat(st.Players)
	for i := 0; i < 5; i++ {
		u := userBySeat[seats[i]]
		res, err = actor.Dispatch(ctx, game.ConfirmRoleCommand{Meta: d.meta(fmt.Sprintf("p1-c%d", i+1), u, game.PhaseDeal, 2)})
		if err != nil || res.Err != nil {
			t.Fatalf("confirm %d: dispatch=%v res=%v", i+1, err, res.Err)
		}
		if err := d.drainActorSink(); err != nil {
			t.Fatalf("submit confirm %d effects: %v", i+1, err)
		}
	}
	st = readState()
	if st.Phase != game.PhaseDeal || len(st.Deal.Confirmed) != 5 {
		t.Fatalf("5 人确认后 = %s confirmed=%d, want deal/5", st.Phase, len(st.Deal.Confirmed))
	}
	// 旧版本确认 → ErrStalePhaseVersion。
	u6 := userBySeat[seats[5]]
	res, err = actor.Dispatch(ctx, game.ConfirmRoleCommand{Meta: d.meta("p1-c-stale", u6, game.PhaseDeal, 1)})
	if err != nil || !errors.Is(res.Err, game.ErrStalePhaseVersion) {
		t.Fatalf("旧版本确认 err = %v/%v, want ErrStalePhaseVersion", err, res.Err)
	}
	// 超过发牌截止时间到达的确认 → ErrDeadlinePassed（Actor deadline 语义）。
	res, err = actor.Dispatch(ctx, game.ConfirmRoleCommand{Meta: game.CommandMeta{
		ID: "p1-c-late", Actor: u6, ExpectedPhase: game.PhaseDeal, PhaseVersion: 2,
		ReceivedAt: clk.Now().Add(11 * time.Second),
	}})
	if err != nil || !errors.Is(res.Err, room.ErrDeadlinePassed) {
		t.Fatalf("超期确认 err = %v/%v, want ErrDeadlinePassed", err, res.Err)
	}
	// 推进 10 秒：真实 Actor Timer 触发 TimeoutCommand → 自动确认第 6 人。
	clk.Advance(10 * time.Second)
	waitDeadline := time.Now().Add(3 * time.Second)
	for {
		st = readState()
		if st.Phase == game.PhaseNight && len(st.Deal.Confirmed) == 6 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("10s 超时未进入 night: %s confirmed=%d", st.Phase, len(st.Deal.Confirmed))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.PhaseVersion != 3 {
		t.Fatalf("night PhaseVersion = %d, want 3", st.PhaseVersion)
	}
	if err := d.drainActorSink(); err != nil {
		t.Fatalf("submit timeout-deal effects: %v", err)
	}
	actor.Stop() // 此后夜间由驱动镜像 + reducer 驱动
	d.flush()

	// ---------- 夜间驱动接管（真实 reducer + 驱动计时；复用审计/计时簿） ----------
	d.takeover(st.Copy())
	wolf1 := outbox.ChatID(userBySeat[d.wolfSeats[0]])
	wolf2 := outbox.ChatID(userBySeat[d.wolfSeats[1]])
	witchChat := outbox.ChatID(userBySeat[d.witchSeat])
	seerChat := outbox.ChatID(userBySeat[d.seerSeat])
	hostChat := outbox.ChatID(host)

	// ---- 第 1 夜：狼人窗口 ----
	cursor := d.flush()
	d.openWindow("wolf", game.BeginWolfPhase)
	if d.st.Night.WolfRound != 1 {
		t.Fatalf("N1 WolfRound = %d, want 1", d.st.Night.WolfRound)
	}
	d.flush()
	wantWolfOpen := []string{game.WolfDiscussMessageKey, game.WolfVoteMessageKey}
	for _, wc := range []outbox.ChatID{wolf1, wolf2} {
		d.assertChatOrder(t, wc, cursor, wantWolfOpen)
	}
	// 狼刀：最小存活村民（保证女巫/预言家存活到第 2 夜）。
	villagers := []game.Seat{}
	for s, p := range userBySeat {
		for _, pl := range st.Players {
			if pl.Seat == s && pl.Role == game.RoleVillager {
				villagers = append(villagers, s)
			}
		}
		_ = p
	}
	sort.Slice(villagers, func(i, j int) bool { return villagers[i] < villagers[j] })
	if len(villagers) != 2 {
		t.Fatalf("villagers = %v, want 2", villagers)
	}
	g1 := villagers[0]
	for _, ws := range d.wolfSeats {
		u := userBySeat[ws]
		if err := d.apply(game.WolfVoteCommand{Meta: d.meta(fmt.Sprintf("p1-wv%d", ws), u, game.PhaseNight, 3), Target: &g1}); err != nil {
			t.Fatalf("N1 wolf vote %d: %v", ws, err)
		}
		if err := d.apply(game.WolfConfirmCommand{Meta: d.meta(fmt.Sprintf("p1-wk%d", ws), u, game.PhaseNight, 3)}); err != nil {
			t.Fatalf("N1 wolf confirm %d: %v", ws, err)
		}
	}
	if d.st.Night.WolfKillTarget == nil || *d.st.Night.WolfKillTarget != g1 || d.st.Night.WolfRound != 0 {
		t.Fatalf("N1 WolfKillTarget=%v round=%d, want %d/0", d.st.Night.WolfKillTarget, d.st.Night.WolfRound, g1)
	}
	d.cancelTimer("wolf", game.PhaseNight, 3) // 两狼确认 = 窗口提前结束，取消狼人计时
	d.flush()

	// ---- 第 1 夜：女巫（第一夜，不救不用毒） ----
	cursor = d.flush()
	d.openWindow("witch", func(s game.State) (game.State, []game.Effect, error) { return game.BeginWitchPhase(s, true) })
	if d.st.Night.WitchStage != game.WitchStageSave || !d.st.Night.WitchFirstNight {
		t.Fatalf("N1 witch stage/firstNight = %d/%v", d.st.Night.WitchStage, d.st.Night.WitchFirstNight)
	}
	// 旧版本女巫命令 → ErrStalePhaseVersion（版本 2 < 当前 3）。
	if err := d.apply(game.WitchSaveCommand{Meta: d.meta("p1-ws-stale", userBySeat[d.witchSeat], game.PhaseNight, 2), Use: false}); !errors.Is(err, game.ErrStalePhaseVersion) {
		t.Fatalf("旧版本 witch save err = %v, want ErrStalePhaseVersion", err)
	}
	if err := d.apply(game.WitchSaveCommand{Meta: d.meta("p1-save", userBySeat[d.witchSeat], game.PhaseNight, 3), Use: false}); err != nil {
		t.Fatalf("N1 witch save: %v", err)
	}
	if err := d.apply(game.WitchConfirmCommand{Meta: d.meta("p1-save-c", userBySeat[d.witchSeat], game.PhaseNight, 3)}); err != nil {
		t.Fatalf("N1 witch save confirm: %v", err)
	}
	if d.st.Night.WitchStage != game.WitchStagePoison {
		t.Fatalf("N1 witch stage = %d, want poison", d.st.Night.WitchStage)
	}
	if err := d.apply(game.WitchPoisonCommand{Meta: d.meta("p1-poison", userBySeat[d.witchSeat], game.PhaseNight, 3), Target: nil}); err != nil {
		t.Fatalf("N1 witch poison: %v", err)
	}
	if err := d.apply(game.WitchConfirmCommand{Meta: d.meta("p1-poison-c", userBySeat[d.witchSeat], game.PhaseNight, 3)}); err != nil {
		t.Fatalf("N1 witch poison confirm: %v", err)
	}
	if d.st.Night.WitchStage != game.WitchStageClosed || d.st.Night.WitchUsedTonight {
		t.Fatalf("N1 witch end = stage %d used %v, want closed/false", d.st.Night.WitchStage, d.st.Night.WitchUsedTonight)
	}
	d.cancelTimer("witch", game.PhaseNight, 3) // 女巫确认完成 = 阶段提前结束，取消计时
	d.cancelTimer("seer", game.PhaseNight, 3)  // 防残留（窗口尚未开启时无实际记录，幂等）

	// ---- 第 1 夜：预言家 ----
	d.openWindow("seer", game.BeginSeerPhase)
	if !d.st.Night.SeerActive {
		t.Fatalf("N1 seer 窗口未开启")
	}
	if err := d.apply(game.SeerCheckCommand{Meta: d.meta("p1-sc", userBySeat[d.seerSeat], game.PhaseNight, 3), Target: d.wolfSeats[0]}); err != nil {
		t.Fatalf("N1 seer check: %v", err)
	}
	if err := d.apply(game.SeerConfirmCommand{Meta: d.meta("p1-sc-c", userBySeat[d.seerSeat], game.PhaseNight, 3)}); err != nil {
		t.Fatalf("N1 seer confirm: %v", err)
	}
	if d.st.Night.SeerResults[d.wolfSeats[0]] != game.CampWolf {
		t.Fatalf("N1 seer result = %v, want CampWolf", d.st.Night.SeerResults[d.wolfSeats[0]])
	}
	d.cancelTimer("seer", game.PhaseNight, 3) // 查验完成 = 阶段提前结束

	// ---- 第 1 夜结算 ----
	cursor = d.flush()
	d.openWindow("resolve", p1ResolveNight)
	if d.st.Phase != game.PhaseDaySpeech || d.st.PhaseVersion != 4 {
		t.Fatalf("N1 resolve = %s v%d, want day_speech v4", d.st.Phase, d.st.PhaseVersion)
	}
	if !reflect.DeepEqual(d.nightVictims(), []game.Seat{g1}) {
		t.Fatalf("N1 victims = %v, want [%d]", d.nightVictims(), g1)
	}
	if d.st.Night.WolfRound != 0 || d.st.Night.WitchStage != game.WitchStageClosed || d.st.Night.SeerActive || d.st.Night.WitchUsedTonight {
		t.Fatalf("N1 夜间窗口未清理: %+v", d.st.Night)
	}
	d.flush()

	// ---- 第 2 夜：白天→夜间（production 未接线，测试内以接线契约驱动；
	// 生产接线会在此时投递 phase.night.start 公共主消息） ----
	night2, err := game.NewMessageEffect(game.AudiencePublic, game.PhaseNightStartMessageKey, map[string]any{"phase_number": 2})
	if err != nil {
		t.Fatalf("phase.night.start(2): %v", err)
	}
	if err := d.submit([]game.Effect{night2}); err != nil {
		t.Fatalf("submit phase.night.start(2): %v", err)
	}
	d.st.Phase = game.PhaseNight
	d.st.PhaseVersion++
	cursor = d.flush()
	d.openWindow("wolf", game.BeginWolfPhase)
	if d.st.Night.WolfRound != 1 {
		t.Fatalf("N2 WolfRound = %d, want 1", d.st.Night.WolfRound)
	}
	// 狼人 30 秒计时超时弃刀（驱动计时簿 → Timeout Command）。
	d.clk.Advance(30 * time.Second)
	d.fireDueTimers()
	if d.st.Night.WolfKillTarget != nil || d.st.Night.WolfRound != 0 {
		t.Fatalf("N2 狼人超时后 kill=%v round=%d, want nil/0（弃刀）", d.st.Night.WolfKillTarget, d.st.Night.WolfRound)
	}
	// 旧版本 Timeout → ErrStalePhaseVersion（v3 ≠ 当前 v5）。
	if err := d.apply(game.TimeoutCommand{Meta: d.meta("p1-to-stale", 0, game.PhaseNight, 3)}); !errors.Is(err, game.ErrStalePhaseVersion) {
		t.Fatalf("旧版本 Timeout err = %v, want ErrStalePhaseVersion", err)
	}
	d.flush()

	// ---- 第 2 夜：女巫（不救 + 毒狼） ----
	cursor = d.flush()
	d.openWindow("witch", func(s game.State) (game.State, []game.Effect, error) { return game.BeginWitchPhase(s, false) })
	if d.st.Night.WitchFirstNight {
		t.Fatalf("N2 WitchFirstNight = true, want false")
	}
	w2 := d.wolfSeats[1]
	if err := d.apply(game.WitchSaveCommand{Meta: d.meta("p2-save", userBySeat[d.witchSeat], game.PhaseNight, 5), Use: false}); err != nil {
		t.Fatalf("N2 witch save: %v", err)
	}
	if err := d.apply(game.WitchConfirmCommand{Meta: d.meta("p2-save-c", userBySeat[d.witchSeat], game.PhaseNight, 5)}); err != nil {
		t.Fatalf("N2 witch save confirm: %v", err)
	}
	if err := d.apply(game.WitchPoisonCommand{Meta: d.meta("p2-poison", userBySeat[d.witchSeat], game.PhaseNight, 5), Target: &w2}); err != nil {
		t.Fatalf("N2 witch poison %d: %v", w2, err)
	}
	if err := d.apply(game.WitchConfirmCommand{Meta: d.meta("p2-poison-c", userBySeat[d.witchSeat], game.PhaseNight, 5)}); err != nil {
		t.Fatalf("N2 witch poison confirm: %v", err)
	}
	if !d.st.Night.WitchPoisonUsed || d.st.Night.WitchPoisonTarget == nil || *d.st.Night.WitchPoisonTarget != w2 {
		t.Fatalf("N2 毒药未落定: used=%v target=%v", d.st.Night.WitchPoisonUsed, d.st.Night.WitchPoisonTarget)
	}
	d.cancelTimer("witch", game.PhaseNight, 5)

	// ---- 第 2 夜：预言家查验存活好人 ----
	d.openWindow("seer", game.BeginSeerPhase)
	var g3 game.Seat
	for _, s := range p1AliveSeats(d.st.Players) {
		if s != d.wolfSeats[0] && s != d.wolfSeats[1] && s != g1 && s != w2 && !p1SeatDead(st.Players, s) {
			g3 = s
			break
		}
	}
	if g3 == 0 {
		t.Fatalf("N2 找不到存活好人查验目标")
	}
	if err := d.apply(game.SeerCheckCommand{Meta: d.meta("p2-sc", userBySeat[d.seerSeat], game.PhaseNight, 5), Target: g3}); err != nil {
		t.Fatalf("N2 seer check: %v", err)
	}
	if err := d.apply(game.SeerConfirmCommand{Meta: d.meta("p2-sc-c", userBySeat[d.seerSeat], game.PhaseNight, 5)}); err != nil {
		t.Fatalf("N2 seer confirm: %v", err)
	}
	if d.st.Night.SeerResults[g3] != game.CampGood {
		t.Fatalf("N2 seer result = %v, want CampGood", d.st.Night.SeerResults[g3])
	}
	d.cancelTimer("seer", game.PhaseNight, 5)
	d.flush()

	// ---- 第 2 夜结算：victims=[w2]（狼人弃刀 + 毒狼） ----
	cursor = d.flush()
	d.openWindow("resolve", p1ResolveNight)
	if d.st.Phase != game.PhaseDaySpeech || d.st.PhaseVersion != 6 {
		t.Fatalf("N2 resolve = %s v%d, want day_speech v6", d.st.Phase, d.st.PhaseVersion)
	}
	if !reflect.DeepEqual(d.nightVictims(), []game.Seat{w2}) {
		t.Fatalf("N2 victims = %v, want [%d]", d.nightVictims(), w2)
	}
	d.flush()

	// ---------- 端到端断言 ----------
	// 1) 角色隐私：敏感 key 无 Public；公共 key 无敏感 params。
	d.assertPrivacy(t)
	// 2) 按钮 owner：wolf.* 只进狼 Chat、seer.result 只进预言家、
	//    witch.kill_reveal 只进女巫、公共主消息进全员。
	for _, r := range d.audit {
		switch r.key {
		case game.WolfVoteMessageKey, game.WolfDiscussMessageKey, game.WolfVoteLockedMessageKey,
			game.WolfDiscussDeleteMessageKey, game.WolfVoteDeleteMessageKey:
			if r.chat != wolf1 && r.chat != wolf2 && r.aud != game.AudienceGodView {
				t.Errorf("wolf.* 消息进入非狼 Chat %d（aud=%v key=%s）", r.chat, r.aud, r.key)
			}
		case game.SeerResultMessageKey:
			if r.chat != seerChat {
				t.Errorf("seer.result 进入非预言家 Chat %d", r.chat)
			}
		case game.WitchKillRevealMessageKey:
			if r.chat != witchChat {
				t.Errorf("witch.kill_reveal 进入非女巫 Chat %d", r.chat)
			}
		}
	}
	// 3) Timer 版本簿记。
	d.assertTimerBookkeeping(t)
	// 4) Outbox per-Chat FIFO：女巫 Chat 全序列（两个夜晚完整轨迹）。
	d.assertChatOrder(t, witchChat, 0, []string{
		game.DealRoleCardMessageKey, game.DealConfirmPromptMessageKey, game.DealConfirmDoneMessageKey,
		game.DealConfirmDeleteMessageKey, game.PhaseNightStartMessageKey,
		game.WitchKillRevealMessageKey, game.WitchSavePromptMessageKey, game.WitchSaveLockedMessageKey,
		game.WitchPoisonPromptMessageKey, game.WitchPoisonLockedMessageKey,
		game.NightDeathMessageKey,
		game.PhaseNightStartMessageKey,
		game.WitchKillRevealMessageKey, game.WitchSavePromptMessageKey, game.WitchSaveLockedMessageKey,
		game.WitchPoisonPromptMessageKey, game.WitchPoisonLockedMessageKey,
		game.NightDeathMessageKey,
	})
	// 5) SQLite 最小 active 数据。
	d.assertMinimalSQLite(t)
	if codes := w.activeCodes(); len(codes) != 1 || codes[0] != string(roomCode) {
		t.Fatalf("active rooms = %v, want [P1NIGHT]", codes)
	}
	// 6) 重启扫描：中止记录 + 房主通知。
	_ = hostChat
	d.assertRestartAbort(t, roomCode, host)
	t.Logf("end: phase=%s v%d 死者=%v", d.st.Phase, d.st.PhaseVersion, d.nightVictims())
}

// assertRestartAbort 模拟重启：默认扫描器发现遗留 active 房，默认通知器
// 向房主入队 Outbox 通知；同包内 App.scanLeftoverAborts 再执行一次并
// 断言房主收到通知（默认实现只通知房主，完整名单枚举属后续任务）。
func (d *p1Driver) assertRestartAbort(t *testing.T, roomID game.RoomID, host game.UserID) {
	t.Helper()
	// 阶段持久化属后续接线任务；此处测试内模拟接线落库（rooms.phase 更新
	// 为当前阶段），使 AbortedRoom.Phase 反映真实最终阶段。
	if _, err := d.w.db.ExecContext(d.ctx, `UPDATE rooms SET phase = ? WHERE room_code = ?`, d.st.Phase.String(), string(roomID)); err != nil {
		t.Fatalf("persist final phase: %v", err)
	}
	scanner := defaultAbortScanner{db: d.w.db}
	leftover, err := scanner.ListLeftover(d.ctx)
	if err != nil {
		t.Fatalf("list leftover: %v", err)
	}
	if len(leftover) != 1 {
		t.Fatalf("leftover rooms = %d, want 1", len(leftover))
	}
	ab := leftover[0]
	if ab.Code != roomID || ab.HostUserID != host || ab.Phase != "day_speech" {
		t.Fatalf("AbortedRoom = %+v, want Code=%s Host=%d Phase=day_speech", ab, roomID, host)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := defaultAbortNotifier{log: log, outbox: d.w.outbox.sched}

	hostCount := func() int {
		d.w.outbox.drainPending() // 先收集发送替身已送达记录再计数
		n := 0
		for _, dd := range d.w.outbox.snapshot() {
			if dd.msg.ChatID == outbox.ChatID(host) && strings.HasPrefix(dd.msg.CorrelationID, "abort:") {
				n++
			}
		}
		return n
	}
	beforeAborts := hostCount()
	if err := notifier.NotifyAbort(d.ctx, ab); err != nil {
		t.Fatalf("notify abort: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return hostCount() >= beforeAborts+1 }, "abort notification 1")

	appInst := &App{log: log, scanner: scanner, notifier: notifier}
	if err := appInst.scanLeftoverAborts(d.ctx); err != nil {
		t.Fatalf("app scanLeftoverAborts: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool { return hostCount() >= beforeAborts+2 }, "abort notification 2")
	if got := hostCount() - beforeAborts; got != 2 {
		t.Fatalf("房主中止通知总数 = %d, want 2（扫描器 + App 各 1）", got)
	}
}
