package app

// Task 42 完整 6 人局端到端模拟（实施计划 Task 42）：
// 不连接真实 Telegram，在测试中从建房一直跑到结算和再来一局。
//
// 四种场景（testdata/scenarios/*.yaml，场景数据层见 scenarios_test.go）：
//   A good_win      好人胜：女巫救/毒、预言家查验、白天投狼、结算与再来一局
//   B wolf_win      狼人胜：夜间/投票超时、恶意退出判负
//   C tie_vote      复杂平票（加时发言/缩圈/无发言轮）与最终二人对决（RNG 兜底）
//   D restart_abort 进程重启中止：不结算积分但保留记录并通知房主
//
// 执行器复用 Task 27/33 既有测试基建（p0World/p1Clock/p1Wiring 纯函数），
// 组合真实生产组件：临时 SQLite + 迁移、storage、game reducer（确定性
// seqRNG）、outbox（Scheduler+Coalescer+recordingSender）、i18n 转义。
// 发牌经真实 room.Actor（StartGame/ConfirmRole/10s 超时自动确认）；
// 夜间/白天窗口沿用 p1 参考接线契约（begin*/BeginVote/resolveNight），
// 结算把 PersistSettlementEffect 映射为 storage.SettlementRepository.SettleGame
// （单事务写战报/积分/统计并清 active 标记），Abort 走 RecoveryRepository。
//
// 断言：无 goroutine 泄漏（outbox 全部送达、Actor 已停止）、Outbox per-Chat
// FIFO、SQLite 数据库最终状态、所有视图隐私（wolf./seer./witch./role. 禁
// Public；settlement.report 全员翻牌为规则必需例外）。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// scenarioPaths 是四个内置场景文件（相对 internal/app 包目录 cwd）。
var scenarioPaths = []string{
	"../../testdata/scenarios/good_win.yaml",
	"../../testdata/scenarios/wolf_win.yaml",
	"../../testdata/scenarios/tie_vote.yaml",
	"../../testdata/scenarios/restart_abort.yaml",
}

// TestMVPEndToEnd 加载并执行四个完整六人局场景。
func TestMVPEndToEnd(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ctx := context.Background()
	for _, path := range scenarioPaths {
		path := path
		t.Run(path, func(t *testing.T) {
			sc, err := loadScenario(path)
			if err != nil {
				t.Fatalf("load scenario %s: %v", path, err)
			}
			if err := sc.validate(); err != nil {
				t.Fatalf("validate scenario %s: %v", path, err)
			}
			if err := executeScenario(ctx, t, sc); err != nil {
				t.Fatalf("execute scenario %s: %v", sc.Name, err)
			}
		})
	}
	// 无 goroutine 泄漏：全部场景收尾后回落到基线（容差 8，防运行时抖动）。
	waitFor(t, 10*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+8
	}, "mvp e2e goroutine settle")
	if n := runtime.NumGoroutine(); n > baseline+8 {
		t.Errorf("goroutine 泄漏：场景后 %d > 基线 %d + 8", n, baseline)
	}
}

// ---------------------------------------------------------------------------
// mvpDriver：Task 42 场景执行器（自包含参考接线层）
// ---------------------------------------------------------------------------

// mvpDriver 组合 p0World 与确定性 reducer，按场景脚本驱动完整对局。
// 与 p1Driver 同模式：真实 reducer/真实 SQLite/真实 outbox，测试内
// 以接线契约开启夜间窗口（p1Open*）与白天投票（BeginVote），
// 结算持久化走真实 SettlementRepository。
type mvpDriver struct {
	t   *testing.T
	ctx context.Context
	w   *p0World
	rd  game.Reducer
	clk *p1Clock
	st  game.State

	sched  []p1TimerRec
	timers []p1TimerRec
	audit  []p1Deliver

	sinkMu sync.Mutex
	sink   []game.Effect

	outboxBaseline int

	userBySeat map[game.Seat]game.UserID
	wolfSeats  []game.Seat
	seerSeat   game.Seat
	witchSeat  game.Seat

	nightNum             int
	corr                 int
	timeoutSeq           int                // 超时命令唯一 ID 计数（跨窗口/跨夜不复用）
	lastTimeoutUnconfirm []game.Seat        // 收票超时时未确认者（leave.timeout_warning 路由依据）
	settlement           *game.Settlement   // 收到的 PersistSettlementEffect
	pending              []game.TimerEffect // submit 捕获、待 absorb 登记的计时
}

// newMVPDriver 创建执行器；st 为发牌前的等待大厅状态（PhaseLobby v1）。
func newMVPDriver(t *testing.T, ctx context.Context, w *p0World, rd game.Reducer, clk *p1Clock, st game.State) *mvpDriver {
	d := &mvpDriver{
		t:              t,
		ctx:            ctx,
		w:              w,
		rd:             rd,
		clk:            clk,
		st:             st,
		outboxBaseline: len(w.outbox.auditSnapshot()),
	}
	d.recomputeRoles()
	return d
}

// recomputeRoles 按当前状态重算角色座位映射。
func (d *mvpDriver) recomputeRoles() {
	d.userBySeat = p1UserBySeat(d.st.Players)
	d.wolfSeats = nil
	d.seerSeat = 0
	d.witchSeat = 0
	for _, p := range d.st.Players {
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

func (d *mvpDriver) meta(id string, actor game.UserID, phase game.Phase, version uint64) game.CommandMeta {
	return game.CommandMeta{ID: id, Actor: actor, ExpectedPhase: phase, PhaseVersion: version, ReceivedAt: d.clk.Now()}
}

// apply 经 reducer 执行命令并提交效果（夜间/投票路径）。
func (d *mvpDriver) apply(cmd game.Command) error {
	// 收票窗口超时的 leave.timeout_warning（AudienceActor，无 seat 参数）
	// 由接线层按超时命令上下文路由：预先记录未确认者供 submit 扇出。
	if _, ok := cmd.(game.TimeoutCommand); ok &&
		d.st.Phase == game.PhaseDayVote && d.st.Vote.Stage == game.VoteStageOpen &&
		d.st.Vote.Tie == game.TieNone {
		d.lastTimeoutUnconfirm = nil
		for _, s := range p1AliveSeats(d.st.Players) {
			if !d.st.Vote.Locked[s] {
				d.lastTimeoutUnconfirm = append(d.lastTimeoutUnconfirm, s)
			}
		}
	}
	st, effects, err := d.rd.Reduce(d.st, cmd)
	d.st = st
	if err != nil {
		return err
	}
	return d.submit(effects)
}

// submit 分发领域效果：MessageEffect → 渲染扇出；PersistSettlementEffect
// → 记录待持久化；TimerEffect/其他由调用方处理或忽略。
func (d *mvpDriver) submit(effects []game.Effect) error {
	for _, e := range effects {
		switch te := e.(type) {
		case game.MessageEffect:
			text, err := d.render(te.Key, te.Params)
			if err != nil {
				return err
			}
			chats, err := d.chatIDs(te.Audience, te.Key, te.Params)
			if err != nil {
				return err
			}
			for _, chat := range chats {
				d.corr++
				msg := outbox.Message{
					CorrelationID: fmt.Sprintf("mvp-%s-%d", string(d.st.RoomID), d.corr),
					RoomID:        d.st.RoomID,
					ChatID:        chat,
					Operation:     telegram.OpSendText,
					Priority:      outbox.PriorityNormal,
				}
				d.w.outbox.submit(msg, string(te.Key), text)
				d.audit = append(d.audit, p1Deliver{aud: te.Audience, key: te.Key, params: te.Params, chat: chat, text: text})
			}
		case game.PersistSettlementEffect:
			s := te.Result
			d.settlement = &s
		case game.TimerEffect:
			// reducer 发出的窗口计时（投票/平票/遗言）由 absorbTimers 登记；
			// 由 openWindow 传入的窗口效果在 openWindow 内吸收（避免双登记）。
			d.pending = append(d.pending, te)
		}
	}
	return nil
}

// absorbTimers 把 submit 捕获的 TimerEffect 登记进计时簿：Cancel 效果
// 取消当前阶段版本的全部存活窗口计时（reducer 语义：窗口提前结束/阶段
// 切换时作废旧计时，docs/技术选型.md §6.2）；Duration>0 登记新计时。
func (d *mvpDriver) absorbTimers(key string) {
	for _, te := range d.pending {
		if te.Cancel {
			d.cancelPhaseTimers(te.Phase, d.st.PhaseVersion)
			continue
		}
		d.scheduleTimer(key, te, d.st.PhaseVersion)
	}
	d.pending = nil
}

// cancelPhaseTimers 移除指定阶段与版本的全部存活计时器（窗口取消语义）。
func (d *mvpDriver) cancelPhaseTimers(phase game.Phase, version uint64) {
	kept := d.timers[:0]
	for _, r := range d.timers {
		if r.phase == phase && r.version == version {
			continue
		}
		kept = append(kept, r)
	}
	d.timers = kept
}

// scheduleTimer / cancelTimer / fireDueTimers：窗口计时簿记（与 p1 同契约）。
func (d *mvpDriver) scheduleTimer(key string, te game.TimerEffect, version uint64) {
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

func (d *mvpDriver) cancelTimer(key string, phase game.Phase, version uint64) {
	kept := d.timers[:0]
	for _, r := range d.timers {
		if r.key == key && r.phase == phase && r.version == version {
			continue
		}
		kept = append(kept, r)
	}
	d.timers = kept
	d.absorbTimers("round")
}

func (d *mvpDriver) fireDueTimers() {
	now := d.clk.Now()
	kept := d.timers[:0]
	for _, r := range d.timers {
		if !r.deadline.After(now) {
			if r.version == d.st.PhaseVersion && r.phase == d.st.Phase {
				d.timeoutSeq++
				cmd := game.TimeoutCommand{Meta: d.meta(fmt.Sprintf("mvp-timeout-%s-%d", r.key, d.timeoutSeq), 0, r.phase, r.version)}
				if err := d.apply(cmd); err != nil {
					d.t.Fatalf("fire %s timeout: %v", r.key, err)
				}
			}
			continue
		}
		kept = append(kept, r)
	}
	d.timers = kept
	d.absorbTimers("round")
}

// openWindow 执行参考接线钩子（st 切换 + 效果提交 + 计时登记）。
func (d *mvpDriver) openWindow(key string, fn func(game.State) (game.State, []game.Effect, error)) {
	next, effects, err := fn(d.st)
	if err != nil {
		d.t.Fatalf("open %s: %v", key, err)
	}
	d.st = next
	if err := d.submit(effects); err != nil {
		d.t.Fatalf("submit %s effects: %v", key, err)
	}
	d.absorbTimers(key)
}

// actorSink / drainActorSink：发牌阶段 room.Actor 的效果出口。
func (d *mvpDriver) actorSink(effects []game.Effect) {
	d.sinkMu.Lock()
	defer d.sinkMu.Unlock()
	d.sink = append(d.sink, effects...)
}

func (d *mvpDriver) drainActorSink() error {
	d.sinkMu.Lock()
	pending := d.sink
	d.sink = nil
	d.sinkMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	return d.submit(pending)
}

func (d *mvpDriver) flush() int {
	d.w.outbox.flush()
	return len(d.audit)
}

func (d *mvpDriver) sentSince(chat outbox.ChatID, from int) []p1Deliver {
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

// ---------------------------------------------------------------------------
// 渲染与受众（测试内最小 MarkdownV2 模板；参数统一 i18n.EscapeMarkdownV2）
// ---------------------------------------------------------------------------

func (d *mvpDriver) render(key string, params map[string]any) (string, error) {
	esc := i18n.EscapeMarkdownV2
	seatStr := func(k string) string { return p1SeatParam(params, k) }
	seatsStr := func(k string) string { return p1SeatsParam(params, k) }
	text := func(k, fallback string) string { return p1ParamString(params, k, fallback) }
	switch key {
	case game.DealRoleCardMessageKey:
		base := fmt.Sprintf("身份卡\n房间码：%s\n座位：%s\n角色：%s\n阵营：%s",
			esc(string(d.st.RoomID)), esc(text("seat", "")), esc(text("role", "")), esc(text("camp", "")))
		if mates, ok := params["wolf_mates"].([]game.Seat); ok && len(mates) > 0 {
			base += "\n狼队友：" + p1SeatsText(mates)
		}
		return base, nil
	case game.DealConfirmPromptMessageKey:
		return fmt.Sprintf("请确认已查看身份\n座位：%s", esc(text("seat", ""))), nil
	case game.DealConfirmDoneMessageKey:
		return "已确认身份", nil
	case game.DealConfirmDeleteMessageKey:
		return "确认消息已删除", nil
	case game.PhaseNightStartMessageKey:
		return fmt.Sprintf("🌙 第 %s 夜开始", esc(text("phase_number", "1"))), nil
	case game.WolfDiscussMessageKey:
		return fmt.Sprintf("狼人讨论\nround：%s", esc(text("round", ""))), nil
	case game.WolfVoteMessageKey:
		return fmt.Sprintf("狼人投票\nround：%s\n可选目标：%s", esc(text("round", "")), seatsStr("targets")), nil
	case game.WolfVoteLockedMessageKey:
		return "狼人投票已锁定", nil
	case game.WolfDiscussDeleteMessageKey:
		return "狼人讨论已删除", nil
	case game.WolfVoteDeleteMessageKey:
		return "狼人投票已删除", nil
	case game.WitchKillRevealMessageKey:
		return fmt.Sprintf("今晚狼人目标：%s", seatStr("kill_target")), nil
	case game.WitchSavePromptMessageKey:
		return fmt.Sprintf("是否使用解药？\n解药剩余：%s\n毒药剩余：%s", esc(text("save_used", "")), esc(text("poison_used", ""))), nil
	case game.WitchSaveLockedMessageKey:
		return fmt.Sprintf("解药选择已确认：%s", esc(text("used", ""))), nil
	case game.WitchPoisonPromptMessageKey:
		return "请选择毒药目标或不用毒药", nil
	case game.WitchPoisonLockedMessageKey:
		return fmt.Sprintf("毒药选择已确认：%s", seatStr("target")), nil
	case game.WitchNoneMessageKey:
		return "本夜女巫未使用任何药水", nil
	case game.SeerPromptMessageKey:
		return fmt.Sprintf("请选择查验目标：%s", seatsStr("targets")), nil
	case game.SeerResultMessageKey:
		return fmt.Sprintf("查验结果：%s", esc(text("camp", ""))), nil
	case game.SeerNoneMessageKey:
		return "本夜预言家未查验", nil
	case game.NightDeathMessageKey:
		return fmt.Sprintf("昨夜死亡：%s", seatsStr("victims")), nil
	case game.NightPeaceMessageKey:
		return "昨夜平安夜", nil
	case game.SettlementVictoryMessageKey:
		return fmt.Sprintf("游戏结束，胜方：%s", esc(text("winner", ""))), nil
	case game.VotePromptMessageKey:
		return fmt.Sprintf("请投票\n候选：%s\n截止：%s", seatsStr("candidates"), esc(fmt.Sprint(params["deadline"]))), nil
	case game.VoteLockedMessageKey:
		return fmt.Sprintf("投票已锁定：%s", seatStr("target")), nil
	case game.VoteDetailMessageKey:
		return "投票明细", nil
	case game.VoteTallyMessageKey:
		return "票数统计", nil
	case game.VoteResultMessageKey:
		return fmt.Sprintf("放逐结果：%s", seatStr("exiled")), nil
	case game.VoteDeleteMessageKey:
		return "投票消息已删除", nil
	case game.TieSpeechMessageKey:
		return fmt.Sprintf("平票加时发言\n候选：%s", seatsStr("candidates")), nil
	case game.TieSpeechTurnMessageKey:
		return fmt.Sprintf("请发言\n座位：%s", esc(text("seat", ""))), nil
	case game.TieRunoffMessageKey:
		return fmt.Sprintf("第 2 次（缩圈）投票\n候选：%s", seatsStr("candidates")), nil
	case game.TieRunoffPromptMessageKey:
		return fmt.Sprintf("缩圈投票\n候选：%s", seatsStr("candidates")), nil
	case game.TieNoSpeechMessageKey:
		return fmt.Sprintf("无发言投票第 %s 轮\n候选：%s", esc(text("round", "")), seatsStr("candidates")), nil
	case game.TieFinalMessageKey:
		return fmt.Sprintf("最终对决\n候选：%s", seatsStr("candidates")), nil
	case game.TieDuelPromptMessageKey:
		return fmt.Sprintf("最终对决投票\n候选：%s", seatsStr("candidates")), nil
	case game.TieDuelExcludedMessageKey:
		return fmt.Sprintf("本轮被排除投票权：%s", esc(text("seat", ""))), nil
	case game.LastWordsPromptMessageKey:
		return fmt.Sprintf("请发表遗言\n座位：%s", esc(text("seat", ""))), nil
	case game.LastWordsPublishedMessageKey:
		return fmt.Sprintf("遗言（%s 号）：%s", esc(text("seat", "")), esc(text("text", ""))), nil
	case game.LeaveMaliciousMessageKey:
		return fmt.Sprintf("玩家 %s 号恶意退出", esc(text("seat", ""))), nil
	case game.LeaveTimeoutWarningMessageKey:
		return fmt.Sprintf("连续超时预警：%s/3", esc(text("streak", ""))), nil
	case game.LeaveRemovedMessageKey:
		return fmt.Sprintf("连续超时已被强制移除：%s 号", esc(text("seat", ""))), nil
	case game.SettlementReportMessageKey:
		return fmt.Sprintf("战报\n房间：%s\n胜方：%s", esc(text("room_code", "")), esc(text("winner", ""))), nil
	case game.RematchMessageKey:
		return fmt.Sprintf("再来一局\n退出窗口：%s 秒", esc(text("window_seconds", ""))), nil
	default:
		return "", fmt.Errorf("mvp render: unknown key %q", key)
	}
}

// chatIDs 把受众映射为 ChatID 列表（与 p1 同契约；vote/tie/last_words/
// settlement/report 为 Task 42 新增映射）。
func (d *mvpDriver) chatIDs(a game.Audience, key string, params map[string]any) ([]outbox.ChatID, error) {
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
			return nil, fmt.Errorf("mvp: %s 需要 params.%s Seat 字段", key, seatKey)
		}
		u, ok := d.userBySeat[s]
		if !ok {
			return nil, fmt.Errorf("mvp: %s 座位 %d 无用户", key, s)
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
			game.DealConfirmDeleteMessageKey, game.VotePromptMessageKey, game.VoteLockedMessageKey,
			game.VoteDeleteMessageKey, game.LastWordsPromptMessageKey, game.TieSpeechTurnMessageKey,
			game.TieRunoffPromptMessageKey, game.TieDuelPromptMessageKey, game.TieDuelExcludedMessageKey:
			return seatChat("seat")
		case game.LeaveTimeoutWarningMessageKey:
			var chats []outbox.ChatID
			for _, s := range d.lastTimeoutUnconfirm {
				chats = append(chats, outbox.ChatID(d.userBySeat[s]))
			}
			sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
			return chats, nil
		case game.WitchKillRevealMessageKey, game.WitchSavePromptMessageKey, game.WitchSaveLockedMessageKey,
			game.WitchPoisonPromptMessageKey, game.WitchPoisonLockedMessageKey, game.WitchNoneMessageKey:
			return []outbox.ChatID{outbox.ChatID(d.userBySeat[d.witchSeat])}, nil
		case game.SeerPromptMessageKey, game.SeerResultMessageKey, game.SeerNoneMessageKey:
			return []outbox.ChatID{outbox.ChatID(d.userBySeat[d.seerSeat])}, nil
		default:
			return nil, fmt.Errorf("mvp: AudienceActor 未映射 key %q", key)
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
		return nil, fmt.Errorf("mvp: unsupported audience %v", a)
	}
}

// ---------------------------------------------------------------------------
// 角色选择器解析
// ---------------------------------------------------------------------------

// alivePlayers 返回按座位升序的存活玩家。
func (d *mvpDriver) alivePlayers() []game.Player {
	var out []game.Player
	for _, p := range d.st.Players {
		if !p.Dead && p.Seat.Valid() {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seat < out[j].Seat })
	return out
}

// aliveByCamp 返回按座位升序的存活指定阵营玩家。
func (d *mvpDriver) aliveByCamp(camp game.Camp) []game.Player {
	var out []game.Player
	for _, p := range d.alivePlayers() {
		if p.Role.Camp() == camp {
			out = append(out, p)
		}
	}
	return out
}

// aliveByRole 返回按座位升序的存活指定角色玩家。
func (d *mvpDriver) aliveByRole(role game.Role) []game.Player {
	var out []game.Player
	for _, p := range d.alivePlayers() {
		if p.Role == role {
			out = append(out, p)
		}
	}
	return out
}

// resolveSelector 把角色选择器解析为座位（存活玩家按座位升序取第 N 个）。
// 特殊值 none/abstain 返回 (0, nil)；解析失败返回明确错误。
func (d *mvpDriver) resolveSelector(sel string) (game.Seat, error) {
	if sel == "" || sel == "none" || sel == "abstain" {
		return 0, nil
	}
	role := func(r game.Role, n int) (game.Seat, error) {
		ps := d.aliveByRole(r)
		if n <= 0 || n > len(ps) {
			return 0, fmt.Errorf("selector %q: 存活%s不足 %d（实际 %d）", sel, r, n, len(ps))
		}
		return ps[n-1].Seat, nil
	}
	switch {
	case strings.HasPrefix(sel, "good_"):
		var n int
		if _, err := fmt.Sscanf(sel, "good_%d", &n); err != nil {
			return 0, fmt.Errorf("非法选择器 %q", sel)
		}
		ps := d.aliveByCamp(game.CampGood)
		if n <= 0 || n > len(ps) {
			return 0, fmt.Errorf("selector %q: 存活好人不足 %d（实际 %d）", sel, n, len(ps))
		}
		return ps[n-1].Seat, nil
	case strings.HasPrefix(sel, "wolf_"):
		var n int
		if _, err := fmt.Sscanf(sel, "wolf_%d", &n); err != nil {
			return 0, fmt.Errorf("非法选择器 %q", sel)
		}
		return role(game.RoleWolf, n)
	case strings.HasPrefix(sel, "villager_"):
		var n int
		if _, err := fmt.Sscanf(sel, "villager_%d", &n); err != nil {
			return 0, fmt.Errorf("非法选择器 %q", sel)
		}
		return role(game.RoleVillager, n)
	case sel == "seer":
		return role(game.RoleSeer, 1)
	case sel == "witch":
		return role(game.RoleWitch, 1)
	default:
		return 0, fmt.Errorf("非法选择器 %q", sel)
	}
}

// ---------------------------------------------------------------------------
// 场景驱动
// ---------------------------------------------------------------------------

// executeScenario 执行一个完整场景：建房→满员→发牌→脚本步骤→断言。
func executeScenario(ctx context.Context, t *testing.T, sc *scenario) error {
	w := newP0World(t)
	defer w.close()

	const host = game.UserID(101)
	roomID := game.RoomID(sc.RoomCode)
	if _, err := w.createRoom(host, "mvp-c0", sc.RoomCode); err != nil {
		return fmt.Errorf("create room: %w", err)
	}
	link := "https://t.me/werewolf_bot?start=" + sc.RoomCode
	for i := 0; i < 5; i++ {
		actor := game.UserID(102 + i)
		if _, err := w.join(actor, fmt.Sprintf("mvp-j%d", i), link, nil); err != nil {
			return fmt.Errorf("join %d: %w", actor, err)
		}
	}
	if n := w.roomPlayers(roomID); n != 6 {
		return fmt.Errorf("room_players = %d, want 6", n)
	}

	lobby := w.states[roomID].Copy()
	lobby.Lobby.Config = game.GameConfig{
		PlayerCount: game.MVPPlayerCount,
		Roles:       game.StandardDeck(),
		UseAI:       false,
		Victory:     game.VictorySlaughter,
	}
	lobby.Settings = game.DefaultRoomSettings()
	lobby.Settings.RevealRoleOnDeath = sc.Config.RevealRoleOnDeath
	lobby.Phase = game.PhaseLobby
	lobby.PhaseVersion = 1

	clk := newP1Clock(w.clock.Now())
	rd := game.NewReducerWithRNG(newSeqRNG(sc.Seed))
	d := newMVPDriver(t, ctx, w, rd, clk, lobby)

	// 发牌（真实 room.Actor）：StartGame → 5 人确认 → 10s 超时自动确认。
	if err := d.dealPhase(ctx, host, lobby, clk); err != nil {
		return err
	}

	for i, step := range sc.Script {
		switch step.Step {
		case "night":
			if err := d.driveNight(step); err != nil {
				return fmt.Errorf("第 %d 步 night %d: %w", i+1, step.Night, err)
			}
		case "day":
			if err := d.driveDay(step); err != nil {
				return fmt.Errorf("第 %d 步 day %d: %w", i+1, step.Day, err)
			}
		case "settle":
			if err := d.driveSettle(step); err != nil {
				return fmt.Errorf("第 %d 步 settle: %w", i+1, err)
			}
		case "abort":
			if err := d.driveAbort(step); err != nil {
				return fmt.Errorf("第 %d 步 abort: %w", i+1, err)
			}
		default:
			return fmt.Errorf("未知步骤 %q", step.Step)
		}
	}

	// 端到端断言：隐私、Outbox per-Chat FIFO、完整送达、goroutine 收尾。
	d.assertPrivacy()
	d.assertPerChatFIFO()
	d.assertAllDelivered()
	return nil
}

// dealPhase 经真实 room.Actor 完成发牌与全员确认（含 10s 超时自动确认）。
func (d *mvpDriver) dealPhase(ctx context.Context, host game.UserID, lobby game.State, clk *p1Clock) error {
	actor := room.NewActor(lobby, d.rd, clk, room.Options{Sink: d.actorSink})
	defer actor.Stop()

	probeID := 0
	readState := func() game.State {
		probeID++
		return probeActor(d.t, ctx, actor, clk, fmt.Sprintf("mvp-probe-%d", probeID))
	}

	res, err := actor.Dispatch(ctx, game.StartGameCommand{Meta: d.meta("mvp-sg", host, game.PhaseLobby, 1)})
	if err != nil || res.Err != nil {
		return fmt.Errorf("start game: dispatch=%v res=%v", err, res.Err)
	}
	if err := d.drainActorSink(); err != nil {
		return fmt.Errorf("submit deal effects: %w", err)
	}
	st := res.State
	if st.Phase != game.PhaseDeal || st.PhaseVersion != 2 {
		return fmt.Errorf("start game = %s v%d, want deal v2", st.Phase, st.PhaseVersion)
	}
	d.flush()
	d.recomputeRoles()
	userBySeat := p1UserBySeat(st.Players)
	seats := p1AliveSeats(st.Players)
	for i := 0; i < 5; i++ {
		u := userBySeat[seats[i]]
		res, err = actor.Dispatch(ctx, game.ConfirmRoleCommand{Meta: d.meta(fmt.Sprintf("mvp-c%d", i+1), u, game.PhaseDeal, 2)})
		if err != nil || res.Err != nil {
			return fmt.Errorf("confirm %d: dispatch=%v res=%v", i+1, err, res.Err)
		}
		if err := d.drainActorSink(); err != nil {
			return fmt.Errorf("submit confirm %d effects: %w", i+1, err)
		}
	}
	st = readState()
	if st.Phase != game.PhaseDeal || len(st.Deal.Confirmed) != 5 {
		return fmt.Errorf("5 人确认后 = %s confirmed=%d, want deal/5", st.Phase, len(st.Deal.Confirmed))
	}
	clk.Advance(10 * time.Second)
	waitDeadline := time.Now().Add(3 * time.Second)
	for {
		st = readState()
		if st.Phase == game.PhaseNight && len(st.Deal.Confirmed) == 6 {
			break
		}
		if time.Now().After(waitDeadline) {
			return fmt.Errorf("10s 超时未进入 night: %s confirmed=%d", st.Phase, len(st.Deal.Confirmed))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.PhaseVersion != 3 {
		return fmt.Errorf("night version = %d, want 3", st.PhaseVersion)
	}
	d.st = st
	d.recomputeRoles()
	d.nightNum = 1
	if err := d.drainActorSink(); err != nil {
		return fmt.Errorf("submit night-start effects: %w", err)
	}
	d.flush()
	return nil
}

// driveNight 执行一个夜间步骤：狼刀（或超时）→ 恶意退出 → 女巫 → 预言家
// → 夜结算（p1ResolveNight，含即时胜负结算）。
func (d *mvpDriver) driveNight(step scenarioStep) error {
	ver := d.st.PhaseVersion
	if d.st.Phase != game.PhaseNight {
		return fmt.Errorf("阶段 %s，期望 night", d.st.Phase)
	}
	d.nightNum = step.Night

	// 第 1 夜 start 由发牌流程产出；后续夜由 driveDay 的 finishDayVote 后补发。
	if step.Night > 1 {
		if err := d.emitNightStart(step.Night); err != nil {
			return err
		}
	}

	// ---- 狼人窗口 ----
	d.openWindow("wolf", p1OpenWolf)
	if step.WolfTimeout {
		clk := d.clk
		clk.Advance(p1WolfDuration(d.st.Settings))
		d.fireDueTimers()
	} else {
		target, err := d.resolveSelector(step.WolfKill)
		if err != nil {
			return err
		}
		for _, ws := range p1AliveWolfSeats(d.st.Players) {
			u := d.userBySeat[ws]
			if err := d.apply(game.WolfVoteCommand{Meta: d.meta(fmt.Sprintf("wv-%d-%d", step.Night, ws), u, game.PhaseNight, ver), Target: &target}); err != nil {
				return fmt.Errorf("wolf vote %d: %w", ws, err)
			}
			if err := d.apply(game.WolfConfirmCommand{Meta: d.meta(fmt.Sprintf("wk-%d-%d", step.Night, ws), u, game.PhaseNight, ver)}); err != nil {
				return fmt.Errorf("wolf confirm %d: %w", ws, err)
			}
		}
		d.cancelTimer("wolf", game.PhaseNight, ver)
	}
	d.flush()

	// ---- 恶意退出（游戏内存活主动退出，夜间判恶意死亡） ----
	if step.MaliciousExit != "" && step.MaliciousExit != "none" {
		seat, err := d.resolveSelector(step.MaliciousExit)
		if err != nil {
			return err
		}
		u := d.userBySeat[seat]
		if err := d.apply(game.LeaveGameCommand{Meta: d.meta(fmt.Sprintf("lv-%d-%d", step.Night, seat), u, game.PhaseNight, ver)}); err != nil {
			return fmt.Errorf("malicious exit %d: %w", seat, err)
		}
		d.flush()
	}

	// ---- 女巫窗口 ----
	if step.WitchTimeout {
		d.openWindow("witch", func(s game.State) (game.State, []game.Effect, error) { return p1OpenWitch(s, step.Night == 1) })
		clk := d.clk
		clk.Advance(p1OtherDuration(d.st.Settings))
		d.fireDueTimers()
	} else {
		d.openWindow("witch", func(s game.State) (game.State, []game.Effect, error) { return p1OpenWitch(s, step.Night == 1) })
		u := d.userBySeat[d.witchSeat]
		save := step.WitchSave != nil && *step.WitchSave
		if err := d.apply(game.WitchSaveCommand{Meta: d.meta(fmt.Sprintf("ws-%d", step.Night), u, game.PhaseNight, ver), Use: save}); err != nil {
			return fmt.Errorf("witch save: %w", err)
		}
		if err := d.apply(game.WitchConfirmCommand{Meta: d.meta(fmt.Sprintf("wsc-%d", step.Night), u, game.PhaseNight, ver)}); err != nil {
			return fmt.Errorf("witch save confirm: %w", err)
		}
		// 用解药后本夜不能再用毒（docs §夜间 3：每夜一瓶），毒药窗口已关闭；
		// 未用解药时按脚本选择毒药目标或显式关闭毒药窗口。
		if !save {
			poison, err := d.resolveSelector(step.WitchPoison)
			if err != nil {
				return err
			}
			var target *game.Seat
			if poison != 0 {
				target = &poison
			}
			if err := d.apply(game.WitchPoisonCommand{Meta: d.meta(fmt.Sprintf("wp-%d", step.Night), u, game.PhaseNight, ver), Target: target}); err != nil {
				return fmt.Errorf("witch poison: %w", err)
			}
			if err := d.apply(game.WitchConfirmCommand{Meta: d.meta(fmt.Sprintf("wpc-%d", step.Night), u, game.PhaseNight, ver)}); err != nil {
				return fmt.Errorf("witch poison confirm: %w", err)
			}
		}
		d.cancelTimer("witch", game.PhaseNight, ver)
	}
	d.flush()

	// ---- 预言家窗口 ----
	if step.SeerTimeout {
		d.openWindow("seer", p1OpenSeer)
		clk := d.clk
		clk.Advance(p1OtherDuration(d.st.Settings))
		d.fireDueTimers()
	} else {
		d.openWindow("seer", p1OpenSeer)
		target, err := d.resolveSelector(step.SeerCheck)
		if err != nil {
			return err
		}
		u := d.userBySeat[d.seerSeat]
		if err := d.apply(game.SeerCheckCommand{Meta: d.meta(fmt.Sprintf("sc-%d", step.Night), u, game.PhaseNight, ver), Target: target}); err != nil {
			return fmt.Errorf("seer check: %w", err)
		}
		if err := d.apply(game.SeerConfirmCommand{Meta: d.meta(fmt.Sprintf("scc-%d", step.Night), u, game.PhaseNight, ver)}); err != nil {
			return fmt.Errorf("seer confirm: %w", err)
		}
		if got := d.st.Night.SeerResults[target]; step.SeerExpect != "" && got.String() != step.SeerExpect {
			return fmt.Errorf("seer 查验 %d = %s，期望 %s", target, got, step.SeerExpect)
		}
		d.cancelTimer("seer", game.PhaseNight, ver)
	}
	d.flush()

	// ---- 夜结算 ----
	d.openWindow("resolve", p1ResolveNight)
	if d.st.Phase != game.PhaseDaySpeech && d.st.Phase != game.PhaseSettlement {
		return fmt.Errorf("N%d resolve = %s, want day_speech 或 settlement", step.Night, d.st.Phase)
	}
	d.flush()
	return nil
}

// emitNightStart 在进入新一夜时补发 phase.night.start（白天→夜间由接线层负责）。
func (d *mvpDriver) emitNightStart(night int) error {
	msg, err := game.NewMessageEffect(game.AudiencePublic, game.PhaseNightStartMessageKey, map[string]any{
		"phase_number": night,
	})
	if err != nil {
		return fmt.Errorf("phase.night.start: %w", err)
	}
	return d.submit([]game.Effect{msg})
}

// driveDay 执行一个白天步骤：BeginVote → 投票/超时 → 平票轮（可选）→
// 遗言 → 进入黑夜（或即时结算）。
func (d *mvpDriver) driveDay(step scenarioStep) error {
	if d.st.Phase != game.PhaseDaySpeech {
		return fmt.Errorf("阶段 %s，期望 day_speech", d.st.Phase)
	}
	d.openWindow("vote", func(s game.State) (game.State, []game.Effect, error) {
		return game.BeginVote(s, d.clk.Now())
	})
	ver := d.st.PhaseVersion
	d.flush()

	if step.Tie {
		if err := d.driveTieRounds(step, ver); err != nil {
			return err
		}
	} else if step.VoteTimeout {
		d.clk.Advance(time.Duration(game.VoteConfirmSeconds) * time.Second)
		d.fireDueTimers()
		d.flush()
	} else {
		if err := d.castVotes(step.Votes, ver); err != nil {
			return err
		}
	}

	if d.st.Phase == game.PhaseSettlement {
		d.flush()
		return nil // 白天放逐即时胜负 → 结算
	}
	if err := d.driveLastWords(step.LastWords, ver); err != nil {
		return err
	}
	if d.st.Phase == game.PhaseSettlement {
		d.flush()
		return nil
	}
	if d.st.Phase != game.PhaseNight {
		return fmt.Errorf("白天结束 = %s, want night", d.st.Phase)
	}
	d.flush()
	return nil
}

// castVotes 按意图投票并确认（收票窗口；含平票轮共用路径）。
func (d *mvpDriver) castVotes(votes []scenarioVote, ver uint64) error {
	for _, v := range votes {
		voter, err := d.resolveSelector(v.Voter)
		if err != nil {
			return err
		}
		target, err := d.resolveSelector(v.Target)
		if err != nil {
			return err
		}
		var tp *game.Seat
		if target != 0 {
			tp = &target
		}
		u := d.userBySeat[voter]
		if err := d.apply(game.VoteCommand{Meta: d.meta(fmt.Sprintf("v-%d-%d", voter, d.corr), u, game.PhaseDayVote, ver), Target: tp}); err != nil {
			return fmt.Errorf("vote %d: %w", voter, err)
		}
		if err := d.apply(game.VoteConfirmCommand{Meta: d.meta(fmt.Sprintf("vc-%d-%d", voter, d.corr), u, game.PhaseDayVote, ver)}); err != nil {
			return fmt.Errorf("vote confirm %d: %w", voter, err)
		}
	}
	d.absorbTimers("round")
	d.flush()
	return nil
}

// driveTieRounds 按 tie_rounds 驱动平票流程：首次投票（castVotes 后进入
// 平票）→ 加时发言超时 → 缩圈/无发言轮投票 → 最终对决投票或超时。
func (d *mvpDriver) driveTieRounds(step scenarioStep, ver uint64) error {
	for i, r := range step.TieRounds {
		switch {
		case r.Votes != nil:
			if err := d.castVotes(r.Votes, ver); err != nil {
				return fmt.Errorf("tie_rounds[%d] 投票: %w", i, err)
			}
		case r.SpeechTimeout:
			d.clk.Advance(time.Duration(game.TieSpeechSeconds) * time.Second)
			d.fireDueTimers()
			d.flush()
		case r.RoundTimeout:
			d.clk.Advance(time.Duration(game.VoteConfirmSeconds) * time.Second)
			d.fireDueTimers()
			d.flush()
		case r.FinalTimeout:
			d.clk.Advance(time.Duration(game.VoteConfirmSeconds) * time.Second)
			d.fireDueTimers()
			d.flush()
		}
		if d.st.Phase != game.PhaseDayVote {
			return nil // 平票已落定（放逐/胜利/结束白天）
		}
	}
	return nil
}

// driveLastWords 处理被票死者遗言（默认不报身份模式；none=超时无正文）。
func (d *mvpDriver) driveLastWords(words string, ver uint64) error {
	if d.st.Phase != game.PhaseDayVote || d.st.Vote.Stage != game.VoteStageLastWords {
		return nil // 报身份模式或已直接进入黑夜
	}
	if words == "" || words == "none" {
		d.clk.Advance(time.Duration(game.LastWordsSeconds) * time.Second)
		d.fireDueTimers()
	} else {
		exiled := *d.st.Vote.Exiled
		u := d.userBySeat[exiled]
		if err := d.apply(game.LastWordsCommand{Meta: d.meta(fmt.Sprintf("lw-%d", exiled), u, game.PhaseDayVote, ver), Text: words}); err != nil {
			return fmt.Errorf("last words: %w", err)
		}
	}
	d.absorbTimers("round")
	d.flush()
	return nil
}

// driveSettle 断言胜方并持久化结算（PersistSettlementEffect → storage），
// 可选再来一局（RematchCommand → 回大厅）。
func (d *mvpDriver) driveSettle(step scenarioStep) error {
	if d.st.Phase != game.PhaseSettlement {
		return fmt.Errorf("阶段 %s，期望 settlement", d.st.Phase)
	}
	// d.settlement 可能尚未填充：白天放逐即时胜负由真实 settle() 产出；
	// 夜间结算胜利由下方补全分支填充（见 driveSettle 注释）。
	if d.settlement != nil && d.settlement.Winner != d.st.Settled.Winner {
		return fmt.Errorf("结算胜方不一致：%s vs %s", d.settlement.Winner, d.st.Settled.Winner)
	}
	if step.ExpectWinner != "any" && d.st.Settled.Winner.String() != step.ExpectWinner {
		return fmt.Errorf("胜方 = %s，期望 %s", d.st.Settled.Winner, step.ExpectWinner)
	}

	// 白天放逐即时胜负已走真实 settle()（resolveExile → settle，产出
	// PersistSettlementEffect + settlement.report）；夜间结算胜利走 p1
	// 参考接线（p1ResolveNight/p1Settle 只写 Settled.Winner，不产
	// PersistSettlementEffect），此时才由测试接线补全领域结算契约：
	// 按 docs §积分系统 1 计算全员翻牌/积分/关键事件并投递 settlement.report。
	if d.settlement == nil {
		players, events := mvpComputeSettlement(d.st)
		d.settlement = &game.Settlement{
			RoomID:    d.st.RoomID,
			Phase:     d.st.Phase,
			Winner:    d.st.Settled.Winner,
			Players:   players,
			KeyEvents: events,
		}
		reportMsg, err := game.NewMessageEffect(game.AudiencePublic, game.SettlementReportMessageKey, map[string]any{
			"room_code":  string(d.st.RoomID),
			"winner":     d.st.Settled.Winner,
			"players":    players,
			"key_events": events,
		})
		if err != nil {
			return fmt.Errorf("settlement report message: %w", err)
		}
		if err := d.submit([]game.Effect{reportMsg}); err != nil {
			return fmt.Errorf("submit settlement report: %w", err)
		}
	}
	result := storage.GameResult{
		RoomCode:   d.st.RoomID,
		Phase:      d.st.Phase,
		WinnerCamp: d.settlement.Winner,
		Players:    toStoragePlayers(d.settlement.Players),
		Report:     fmt.Sprintf("房间 %s 胜方 %s：%d 名玩家，%d 个关键事件", d.st.RoomID, d.settlement.Winner, len(d.settlement.Players), len(d.settlement.KeyEvents)),
	}
	if err := storage.NewSettlementRepository(d.w.db).SettleGame(d.ctx, result); err != nil {
		return fmt.Errorf("settle game: %w", err)
	}
	if err := d.assertSettledDB(result); err != nil {
		return err
	}

	if step.Rematch {
		host := d.st.Lobby.Owner
		ver := d.st.PhaseVersion
		if err := d.apply(game.RematchCommand{Meta: d.meta("mvp-rematch", host, game.PhaseSettlement, ver)}); err != nil {
			return fmt.Errorf("rematch: %w", err)
		}
		if d.st.Phase != game.PhaseLobby {
			return fmt.Errorf("rematch 后阶段 = %s, want lobby", d.st.Phase)
		}
		if d.st.Lobby.RematchReadyAt.Before(d.clk.Now()) {
			return fmt.Errorf("rematch 退出窗口未设置")
		}
		if len(d.st.Players) != 6 {
			return fmt.Errorf("rematch 保留玩家 = %d, want 6", len(d.st.Players))
		}
		d.flush()
	}
	return nil
}

// assertSettledDB 断言结算后 SQLite 最终状态：games=1（胜方已写）、
// game_players=6、battle_reports=1、role_stats=6、rooms/room_players 清空、
// 积分与 game.PlayerResult.Score 一致。
func (d *mvpDriver) assertSettledDB(result storage.GameResult) error {
	if n := countRows(d.t, d.ctx, d.w, "games"); n != 1 {
		return fmt.Errorf("games = %d, want 1", n)
	}
	if n := countRows(d.t, d.ctx, d.w, "game_players"); n != 6 {
		return fmt.Errorf("game_players = %d, want 6", n)
	}
	if n := countRows(d.t, d.ctx, d.w, "battle_reports"); n != 1 {
		return fmt.Errorf("battle_reports = %d, want 1", n)
	}
	if n := countRows(d.t, d.ctx, d.w, "role_stats"); n != 6 {
		return fmt.Errorf("role_stats = %d, want 6", n)
	}
	if n := countRows(d.t, d.ctx, d.w, "rooms"); n != 0 {
		return fmt.Errorf("rooms = %d, want 0（结算清 active）", n)
	}
	if n := countRows(d.t, d.ctx, d.w, "room_players"); n != 0 {
		return fmt.Errorf("room_players = %d, want 0", n)
	}
	var winner string
	if err := d.w.db.QueryRowContext(d.ctx, `SELECT winner_camp FROM games LIMIT 1`).Scan(&winner); err != nil {
		return fmt.Errorf("read games.winner_camp: %w", err)
	}
	if winner != result.WinnerCamp.String() {
		return fmt.Errorf("games.winner_camp = %s, want %s", winner, result.WinnerCamp)
	}
	want := make(map[game.UserID]int)
	for _, p := range d.settlement.Players {
		want[p.UserID] = p.Score
	}
	for uid, score := range want {
		var got int
		if err := d.w.db.QueryRowContext(d.ctx, `SELECT points FROM users WHERE telegram_id = ?`, int64(uid)).Scan(&got); err != nil {
			return fmt.Errorf("read points of %d: %w", uid, err)
		}
		if got != score {
			return fmt.Errorf("玩家 %d 积分 = %d, want %d", uid, got, score)
		}
	}
	return nil
}

// driveAbort 模拟进程重启中止：扫描遗留房 → 标记中止（不清胜负/不动积分）
// → 向房主通知；断言记录保留（games.aborted=1）与积分不变。
func (d *mvpDriver) driveAbort(step scenarioStep) error {
	if !step.ExpectAborted {
		return fmt.Errorf("abort 步骤未声明 expect_aborted")
	}
	if _, err := d.w.db.ExecContext(d.ctx, `UPDATE rooms SET phase = ? WHERE room_code = ?`, d.st.Phase.String(), string(d.st.RoomID)); err != nil {
		return fmt.Errorf("persist final phase: %w", err)
	}
	recovery := storage.NewRecoveryRepository(d.w.db)
	interrupted, err := recovery.ListInterruptedRoomsOnStartup(d.ctx)
	if err != nil {
		return fmt.Errorf("list interrupted: %w", err)
	}
	if len(interrupted) != 1 || interrupted[0].Room.RoomCode != string(d.st.RoomID) {
		return fmt.Errorf("interrupted rooms = %+v, want [%s]", interrupted, d.st.RoomID)
	}

	// 通知房主（真实 AbortNotifier 入队 outbox；须在清场前扫描遗留房）。
	scanner := defaultAbortScanner{repo: d.w.repo}
	leftover, err := scanner.ListLeftover(d.ctx)
	if err != nil {
		return fmt.Errorf("list leftover: %w", err)
	}
	if len(leftover) != 1 {
		return fmt.Errorf("leftover rooms = %d, want 1", len(leftover))
	}
	ab := leftover[0]
	if ab.Code != d.st.RoomID || ab.HostUserID != d.st.Lobby.Owner || ab.Phase != d.st.Phase.String() {
		return fmt.Errorf("AbortedRoom = %+v, want Code=%s Host=%d Phase=%s", ab, d.st.RoomID, d.st.Lobby.Owner, d.st.Phase)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := defaultAbortNotifier{log: log, outbox: d.w.outbox.sched}
	before := d.countAbortMessages(ab.HostUserID)
	if err := notifier.NotifyAbort(d.ctx, ab); err != nil {
		return fmt.Errorf("notify abort: %w", err)
	}
	waitFor(d.t, 3*time.Second, func() bool { return d.countAbortMessages(ab.HostUserID) >= before+1 }, "abort notification")
	if got := d.countAbortMessages(ab.HostUserID) - before; got != 1 {
		return fmt.Errorf("房主中止通知 = %d, want 1", got)
	}

	if err := recovery.MarkInterrupted(d.ctx, d.st.RoomID); err != nil {
		return fmt.Errorf("mark interrupted: %w", err)
	}

	// 记录保留：games 一行 aborted=1；rooms/room_players 清空；无战报/统计。
	if n := countRows(d.t, d.ctx, d.w, "games"); n != 1 {
		return fmt.Errorf("中止后 games = %d, want 1（保留记录）", n)
	}
	var aborted int
	if err := d.w.db.QueryRowContext(d.ctx, `SELECT aborted FROM games LIMIT 1`).Scan(&aborted); err != nil {
		return fmt.Errorf("read games.aborted: %w", err)
	}
	if aborted != 1 {
		return fmt.Errorf("games.aborted = %d, want 1", aborted)
	}
	for _, table := range []string{"rooms", "room_players", "battle_reports", "role_stats", "game_players"} {
		if n := countRows(d.t, d.ctx, d.w, table); n != 0 {
			return fmt.Errorf("中止后 %s = %d, want 0", table, n)
		}
	}
	if step.ExpectScoreUnchanged {
		var n int64
		if err := d.w.db.QueryRowContext(d.ctx, `SELECT COUNT(*) FROM users WHERE points != 0`).Scan(&n); err != nil {
			return fmt.Errorf("count non-zero points: %w", err)
		}
		if n != 0 {
			return fmt.Errorf("中止后 %d 名玩家积分变化，want 0（不结算积分）", n)
		}
	}
	return nil
}

// countAbortMessages 统计房主 Chat 中 abort: 前缀的已送达消息数。
func (d *mvpDriver) countAbortMessages(host game.UserID) int {
	d.w.outbox.drainPending()
	n := 0
	for _, dd := range d.w.outbox.snapshot() {
		if dd.msg.ChatID == outbox.ChatID(host) && strings.HasPrefix(dd.msg.CorrelationID, "abort:") {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 端到端断言
// ---------------------------------------------------------------------------

// assertPrivacy 断言隐私不变量：敏感 key（wolf./seer./witch./role.）绝不
// AudiencePublic；公共消息 params 不含敏感字段；settlement.report 全员
// 翻牌为规则必需例外；驱动审计与 outbox 审计条数一致。
func (d *mvpDriver) assertPrivacy() {
	for _, r := range d.audit {
		if prefix := p1SensitivePrefix(r.key); prefix != "" && r.aud == game.AudiencePublic {
			d.t.Errorf("敏感消息 %s 以 AudiencePublic 发出（aud: %v chat:%d）", r.key, r.aud, r.chat)
		}
		if r.aud == game.AudiencePublic && r.key != game.SettlementReportMessageKey {
			for _, sensitive := range []string{"wolf_mates", "kill_target", "result", "save_used", "poison_used", "role", "camp", "cause"} {
				if _, ok := r.params[sensitive]; ok {
					d.t.Errorf("公共消息 %s 的 params 泄漏敏感字段 %q", r.key, sensitive)
				}
			}
		}
	}
	outboxAudited := len(d.w.outbox.auditSnapshot()) - d.outboxBaseline
	if len(d.audit) != outboxAudited {
		d.t.Errorf("驱动审计 %d 条与 outbox 审计(接管后) %d 条不一致", len(d.audit), outboxAudited)
	}
}

// assertAllDelivered 断言 Outbox 无积压：flush 后全部 enqueue 均已送达
// （含调度器 goroutine 收尾，杜绝静默丢弃/泄漏）。
func (d *mvpDriver) assertAllDelivered() {
	d.flush()
	d.w.outbox.drainPending()
	// abort: 消息由 AbortNotifier 直接入队 Scheduler（不经 Coalescer 计数），
	// 属预期额外送达；其余全部经 p0Outbox.enqueued 计数。
	extra := 0
	for _, dd := range d.w.outbox.snapshot() {
		if strings.HasPrefix(dd.msg.CorrelationID, "abort:") {
			extra++
		}
	}
	if len(d.w.outbox.snapshot()) != d.w.outbox.enqueued+extra {
		d.t.Errorf("outbox 送达 %d != 入队 %d + 中止直发 %d（存在积压/泄漏）",
			len(d.w.outbox.snapshot()), d.w.outbox.enqueued, extra)
	}
}

// mvpComputeSettlement 按 docs §积分系统 1 与 §结算 7 由最终状态推导
// 全员翻牌（含积分变化）与关键事件（测试接线复刻 game 未导出的
// computeSettlement；积分口径与 storage.pointsFor 一致保证 DB 断言）。
func mvpComputeSettlement(st game.State) ([]game.PlayerResult, []game.KeyEvent) {
	players := make([]game.PlayerResult, 0, len(st.Players))
	for _, p := range st.Players {
		if !p.Seat.Valid() {
			continue
		}
		camp := p.Role.Camp()
		isWinner := camp == st.Settled.Winner
		var score int
		switch {
		case p.MaliciousExit && !isWinner:
			score = -5
		case p.MaliciousExit:
			score = 0
		case p.Dead && isWinner:
			score = 2
		case isWinner:
			score = 5
		default:
			score = 0
		}
		players = append(players, game.PlayerResult{
			UserID:        p.UserID,
			Seat:          p.Seat,
			Role:          p.Role,
			Camp:          camp,
			Died:          p.Dead,
			MaliciousExit: p.MaliciousExit,
			Score:         score,
		})
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Seat < players[j].Seat })

	dead := append([]game.Player(nil), st.Players...)
	sort.Slice(dead, func(i, j int) bool { return dead[i].Seat < dead[j].Seat })
	var events []game.KeyEvent
	for _, p := range dead {
		if !p.Dead || !p.Seat.Valid() {
			continue
		}
		switch {
		case st.Vote.Exiled != nil && *st.Vote.Exiled == p.Seat:
			events = append(events, game.KeyEvent{Phase: game.PhaseDayVote, Text: fmt.Sprintf("白天投票放逐了 %d 号", p.Seat)})
		case st.Night.WitchPoisonTarget != nil && *st.Night.WitchPoisonTarget == p.Seat:
			events = append(events, game.KeyEvent{Phase: game.PhaseNight, Text: fmt.Sprintf("女巫毒杀了 %d 号", p.Seat)})
		case st.Night.WolfKillTarget != nil && *st.Night.WolfKillTarget == p.Seat:
			events = append(events, game.KeyEvent{Phase: game.PhaseNight, Text: fmt.Sprintf("狼人袭击了 %d 号", p.Seat)})
		default:
			events = append(events, game.KeyEvent{Phase: game.PhaseNight, Text: fmt.Sprintf("%d 号出局", p.Seat)})
		}
	}
	return players, events
}

// toStoragePlayers 把 game.PlayerResult 映射为 storage.PlayerResult。
func toStoragePlayers(ps []game.PlayerResult) []storage.PlayerResult {
	out := make([]storage.PlayerResult, 0, len(ps))
	for _, p := range ps {
		out = append(out, storage.PlayerResult{
			UserID:        p.UserID,
			Seat:          p.Seat,
			Role:          p.Role,
			Camp:          p.Camp,
			Died:          p.Died,
			MaliciousExit: p.MaliciousExit,
		})
	}
	return out
}

// assertPerChatFIFO 断言 Outbox per-Chat FIFO：对每个 Chat，游戏消息
// （mvp- 前缀，无 Coalescer 合并键）的送达序列与驱动审计的提交序列一致。
func (d *mvpDriver) assertPerChatFIFO() {
	d.w.outbox.drainPending()
	submitted := map[outbox.ChatID][]string{}
	for _, r := range d.audit {
		submitted[r.chat] = append(submitted[r.chat], r.key)
	}
	delivered := map[outbox.ChatID][]string{}
	for _, dd := range d.w.outbox.snapshot() {
		if strings.HasPrefix(dd.msg.CorrelationID, "mvp-") {
			delivered[dd.msg.ChatID] = append(delivered[dd.msg.ChatID], dd.key)
		}
	}
	for chat, want := range submitted {
		got := delivered[chat]
		if !reflect.DeepEqual(got, want) {
			d.t.Errorf("Chat %d 送达 key 序列 = %v, want %v（FIFO 破坏）", chat, got, want)
		}
	}
}
