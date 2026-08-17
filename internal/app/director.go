package app

// roomDirector 驱动一局游戏的阶段推进与消息呈现（B1-d）。
//
// 挂在 room.Actor.OnApplied 上，于 Actor goroutine 内串行执行：
//  1) 本批 Effects 按受众扇出（Public/Wolf/Seer/GodView 多接收者、Actor/Host
//     单接收者），每接收者按查看者认知渲染私密标记 + 临时操作按钮；
//  2) 阶段推进 pump：夜间 狼人→女巫→预言家→ResolveNight；白天 死讯/麦序
//     发言→BeginVote；结算/再来一局由 reducer 自身处理；
//  3) 维持夜/白天计数、麦序与回合计数器，同步 liveRegistry 状态。
//
// 已知边界（本任务如实记录，不伪造）：
//  - 临时操作消息的删除/编辑（deal.confirm_delete 等）依赖消息 ID 注册表，
//    属 I1 接线；本阶段删除标记效果仅记日志；
//  - 主消息滚动编辑/分页（Viewer）与 Coalescer 合并属 I1；
//  - Cooldown/Dissolve/ScorePenalty 持久化属 I2/I3，本阶段仅契约日志。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// roomDirector 是房间导演：管理每房阶段推进状态与 Actor 绑定。
type roomDirector struct {
	w     *Wiring
	rooms map[game.RoomID]*dirRoom
}

// dirRoom 是单房导演状态。
type dirRoom struct {
	actor        *room.Actor
	night        int
	wolfStarted  bool
	witchStarted bool
	seerStarted  bool
	day          int
	dayStarted   bool
	victims      []game.Seat
	cause        map[game.Seat]game.DeathCause
	speech       *speechDir
}

// speechDir 是白天发言麦序状态。
type speechDir struct {
	order   []game.Seat
	idx     int
	counter *game.RoundCounter
	timer   *time.Timer
}

func newDirector(w *Wiring) *roomDirector {
	return &roomDirector{w: w, rooms: make(map[game.RoomID]*dirRoom)}
}

func (d *roomDirector) room(roomID game.RoomID) *dirRoom {
	dr, ok := d.rooms[roomID]
	if !ok {
		dr = &dirRoom{}
		d.rooms[roomID] = dr
	}
	return dr
}

// bind 注册房间 Actor 到导演（开局引导 StartGame 时调用）；pump 的
// Adopt 依赖该引用把阶段推进结果回写 Actor。
func (d *roomDirector) bind(roomID game.RoomID, actor *room.Actor) {
	dr := d.room(roomID)
	dr.actor = actor
}

// onApplied 是 Actor 的效果/状态钩子（B1-a）：同步状态 + 扇出效果 + 阶段推进。
func (d *roomDirector) onApplied(roomID game.RoomID, st game.State, fx []game.Effect) {
	// 同步当前状态（reducer 过渡后的权威快照，供 /role、token 版本与
	// 发言拦截读取；B1-d 修复：普通过渡也需同步，不能只靠 pump 采纳）。
	d.w.reg.updateState(roomID, st)
	if err := d.w.fanOut(roomID, st, fx); err != nil {
		d.w.log.Error("app: director fanout", "room", string(roomID), "error", err)
	}
	for {
		next, nfx, adv, err := d.pump(roomID, st)
		if err != nil {
			d.w.log.Error("app: director pump", "room", string(roomID), "error", err)
			return
		}
		if !adv {
			return
		}
		dr := d.room(roomID)
		if dr.actor == nil {
			return
		}
		dr.actor.Adopt(next, nfx)
		d.w.reg.updateState(roomID, next)
		if err := d.w.fanOut(roomID, next, nfx); err != nil {
			d.w.log.Error("app: director pump fanout", "room", string(roomID), "error", err)
			return
		}
		st = next
	}
}

// pump 推进一个阶段窗口；adv=true 表示状态已变化（调用方需 Adopt）。
func (d *roomDirector) pump(roomID game.RoomID, st game.State) (game.State, []game.Effect, bool, error) {
	dr := d.room(roomID)
	switch st.Phase {
	case game.PhaseNight:
		if !dr.wolfStarted {
			dr.wolfStarted = true
			next, fx, err := game.BeginWolfPhase(st)
			return next, fx, true, err
		}
		if dr.wolfStarted && !dr.witchStarted && st.Night.WolfRound == 0 &&
			st.Night.WitchStage == game.WitchStageClosed && !st.Night.SeerActive {
			dr.witchStarted = true
			next, fx, err := game.BeginWitchPhase(st, dr.night == 1)
			if witchRoleDead(next) {
				// I5：死亡神职阶段仍开启但按原时长 2/3 假等待（docs §夜间 6
				// 防泄密）；操作按钮由 buttonsFor 对死亡接收者禁用。
				fx = scaleDeadRoleTimers(fx)
			}
			return next, fx, true, err
		}
		if dr.witchStarted && !dr.seerStarted &&
			st.Night.WitchStage == game.WitchStageClosed && !st.Night.SeerActive {
			dr.seerStarted = true
			next, fx, err := game.BeginSeerPhase(st)
			if seerRoleDead(next) {
				fx = scaleDeadRoleTimers(fx)
			}
			return next, fx, true, err
		}
		if dr.seerStarted && !st.Night.SeerActive {
			before := aliveSeatSet(st)
			next, fx, err := game.ResolveNight(st)
			if err != nil {
				return st, nil, false, err
			}
			dr.victims, dr.cause = diffDeaths(before, next)
			dr.night++
			dr.wolfStarted, dr.witchStarted, dr.seerStarted = false, false, false
			dr.dayStarted = false
			dr.speech = nil
			return next, fx, true, nil
		}
	case game.PhaseDaySpeech:
		if !dr.dayStarted {
			dr.dayStarted = true
			return d.startDay(st)
		}
	case game.PhaseSettlement:
		// 结算后导演复位；Rematch 由 reducer 处理回 Lobby（回到等待大厅）。
		dr.wolfStarted, dr.witchStarted, dr.seerStarted = false, false, false
		dr.dayStarted = false
		dr.speech = nil
	}
	return st, nil, false, nil
}

// startDay 开启白天：死讯播报 + 构造麦序 + 首名发言者控制消息 + 发言计时。
func (d *roomDirector) startDay(st game.State) (game.State, []game.Effect, bool, error) {
	dr := d.room(st.RoomID)
	dr.day++
	out := game.DayOutcome{Victims: dr.victims, Cause: dr.cause}
	st2, fx1, err := game.DayStart(st, out)
	if err != nil {
		return st, nil, false, err
	}
	order := game.BuildSpeechOrder(dr.victims, st2.Players)
	if len(order) == 0 {
		return st, nil, false, fmt.Errorf("app: no alive players to speak in room %s", st.RoomID)
	}
	st3 := st2.Copy()
	st3.Day = game.DayState{Speaker: order[0], SpeechOrder: order}
	dr.speech = &speechDir{order: order, idx: 0, counter: game.NewRoundCounter(game.SpeechMaxPerRound)}
	control, err := game.SpeechControl(st3, order[0], 0, len(order), d.w.now())
	if err != nil {
		return st, nil, false, err
	}
	d.armSpeechTimer(st.RoomID, st3)
	return st3, append(fx1, control...), true, nil
}

// armSpeechTimer 为当前发言者安排固定限时超时（docs「固定限时」）。
func (d *roomDirector) armSpeechTimer(roomID game.RoomID, st game.State) {
	dr := d.room(roomID)
	if dr.speech == nil || dr.speech.timer != nil {
		return
	}
	speechSec, _, _ := st.Settings.EffectiveDurations()
	if speechSec <= 0 {
		speechSec = game.DefaultSpeechSeconds
	}
	dr.speech.timer = time.AfterFunc(time.Duration(speechSec)*time.Second, func() {
		d.speechTimeout(roomID)
	})
}

// speechTimeout 是发言超时（真实 timer 或测试直接调用）：移交下一位/结束白天。
func (d *roomDirector) speechTimeout(roomID game.RoomID) {
	dr := d.room(roomID)
	if dr.actor == nil {
		return
	}
	if _, err := dr.actor.DispatchLocal(context.Background(), func(st game.State) (game.State, []game.Effect, error) {
		return d.advanceSpeech(roomID, st)
	}); err != nil {
		d.w.log.Warn("app: speech timeout dispatch", "room", string(roomID), "error", err)
	}
}

// advanceSpeech 移交麦位：下一位发言，或全部完成后进入白天投票。
func (d *roomDirector) advanceSpeech(roomID game.RoomID, st game.State) (game.State, []game.Effect, error) {
	if st.Phase != game.PhaseDaySpeech {
		return st, nil, nil
	}
	dr := d.room(roomID)
	if dr.speech == nil {
		return st, nil, nil
	}
	dr.speech.idx++
	if dr.speech.idx >= len(dr.speech.order) {
		// 麦序完成 → BeginVote（白天投票；docs 游戏流程设计.md §投票）。
		d.stopSpeechTimer(roomID)
		dr.speech = nil
		next, fx, err := game.BeginVote(st, d.w.now())
		return next, fx, err
	}
	seat := dr.speech.order[dr.speech.idx]
	dr.speech.counter = game.NewRoundCounter(game.SpeechMaxPerRound)
	next := st.Copy()
	next.Day.Speaker = seat
	control, err := game.SpeechControl(next, seat, 0, len(dr.speech.order), d.w.now())
	if err != nil {
		return st, nil, err
	}
	d.stopSpeechTimer(roomID)
	dr.speech.timer = nil
	d.armSpeechTimer(roomID, next)
	return next, control, nil
}

// stopSpeechTimer 停止当前发言计时器。
func (d *roomDirector) stopSpeechTimer(roomID game.RoomID) {
	dr, ok := d.rooms[roomID]
	if !ok || dr.speech == nil || dr.speech.timer == nil {
		return
	}
	dr.speech.timer.Stop()
	dr.speech.timer = nil
}

// Speak 拦截发言文本（textHandler 判定当前发言者后调用）：校验 + 计数 +
// 转播 + 原消息 3 秒自毁（docs 游戏流程设计.md §发言限制）。
func (d *roomDirector) Speak(roomID game.RoomID, user game.UserID, chatID, messageID int64, text string) error {
	dr := d.room(roomID)
	if dr.actor == nil {
		return nil
	}
	_, err := dr.actor.DispatchLocal(context.Background(), func(st game.State) (game.State, []game.Effect, error) {
		if st.Phase != game.PhaseDaySpeech {
			return st, nil, nil
		}
		sp := dr.speech
		if sp == nil || sp.idx >= len(sp.order) {
			return st, nil, nil
		}
		seat, ok := seatByUserOf(st, user)
		if !ok || seat != st.Day.Speaker {
			return st, nil, nil
		}
		if _, ok := game.CheckSpeechAccept(text); !ok {
			rej, err := game.SpeechReject(seat, game.SpeechRejectTooLong)
			if err != nil {
				return st, nil, err
			}
			return st, []game.Effect{rej}, nil
		}
		if err := sp.counter.Count(); err != nil {
			rej, rerr := game.SpeechReject(seat, game.SpeechRejectRoundFull)
			if rerr != nil {
				return st, nil, rerr
			}
			return st, []game.Effect{rej}, nil
		}
		acc, err := game.SpeechAccept(seat, text)
		if err != nil {
			return st, nil, err
		}
		del, err := game.SpeechSelfDelete(chatID, int(messageID))
		if err != nil {
			return st, nil, err
		}
		return st, []game.Effect{acc, del}, nil
	})
	return err
}

// EndSpeech 处理「结束发言」回调：移交麦位/进入投票。
func (d *roomDirector) EndSpeech(roomID game.RoomID) error {
	dr := d.room(roomID)
	if dr.actor == nil {
		return nil
	}
	d.stopSpeechTimer(roomID)
	if _, err := dr.actor.DispatchLocal(context.Background(), func(st game.State) (game.State, []game.Effect, error) {
		return d.advanceSpeech(roomID, st)
	}); err != nil {
		return err
	}
	return nil
}

// trySpeak 判定是否应把文本当发言处理（sender 为当前发言者）。
func (d *roomDirector) trySpeak(roomID game.RoomID, user game.UserID, chatID, messageID int64, text string) bool {
	lr, ok := d.w.reg.get(roomID)
	if !ok || lr.actor == nil || lr.st.Phase != game.PhaseDaySpeech {
		return false
	}
	seat, ok := seatByUserOf(lr.st, user)
	if !ok || seat != lr.st.Day.Speaker {
		return false
	}
	return d.Speak(roomID, user, chatID, messageID, text) == nil
}

// tryLastWords 判定并处理遗言文本（不报身份模式被票死者的 30 秒遗言，
// docs 游戏流程设计.md §结算 4）：构造 LastWordsCommand 经 Actor 进 reducer。
func (d *roomDirector) tryLastWords(roomID game.RoomID, user game.UserID, commandID, text string) bool {
	lr, ok := d.w.reg.get(roomID)
	if !ok || lr.actor == nil {
		return false
	}
	if lr.st.Phase != game.PhaseDayVote || lr.st.Vote.Stage != game.VoteStageLastWords {
		return false
	}
	if lr.st.Vote.Exiled == nil {
		return false
	}
	seat, ok := seatByUserOf(lr.st, user)
	if !ok || seat != *lr.st.Vote.Exiled {
		return false
	}
	cmd := game.LastWordsCommand{Meta: game.CommandMeta{
		ID:            commandID,
		Actor:         user,
		ExpectedPhase: lr.st.Phase,
		PhaseVersion:  lr.st.PhaseVersion,
		ReceivedAt:    time.Now(),
	}, Text: text}
	if _, err := lr.actor.Dispatch(context.Background(), cmd); err != nil {
		d.w.log.Warn("app: last words dispatch", "room", string(roomID), "error", err)
		return false
	}
	return true
}

// handleAction 处理导演本地信号（end_speech 等，无对应游戏命令）。
func (d *roomDirector) handleAction(ctx context.Context, act telegram.CallbackAction) error {
	switch act.Action {
	case "end_speech":
		roomID, ok := d.w.reg.roomOf(act.Owner)
		if !ok {
			return nil
		}
		return d.EndSpeech(roomID)
	default:
		d.w.log.Debug("app: director action ignored", "action", act.Action)
		return nil
	}
}

// release 释放导演持有的房间状态（房间解散/回收时）。
func (d *roomDirector) release(roomID game.RoomID) {
	if dr, ok := d.rooms[roomID]; ok {
		if dr.speech != nil && dr.speech.timer != nil {
			dr.speech.timer.Stop()
		}
		delete(d.rooms, roomID)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// seatByUserOf 返回用户在状态中的座位。
func seatByUserOf(st game.State, user game.UserID) (game.Seat, bool) {
	for _, p := range st.Players {
		if p.UserID == user {
			return p.Seat, true
		}
	}
	return 0, false
}

// aliveSeatSet 返回存活座位集合。
func aliveSeatSet(st game.State) map[game.Seat]bool {
	out := make(map[game.Seat]bool)
	for _, p := range st.Players {
		if !p.Dead && p.Seat.Valid() {
			out[p.Seat] = true
		}
	}
	return out
}

// diffDeaths 对比结算前后存活集合，产出死者与死因。
func diffDeaths(before map[game.Seat]bool, after game.State) ([]game.Seat, map[game.Seat]game.DeathCause) {
	var victims []game.Seat
	cause := map[game.Seat]game.DeathCause{}
	for _, p := range after.Players {
		if p.Dead && p.Seat.Valid() && before[p.Seat] {
			victims = append(victims, p.Seat)
			cause[p.Seat] = causeOfDeath(after, p.Seat)
		}
	}
	return sortedSeats(victims), cause
}

// causeOfDeath 从最终状态推导死因（白天放逐 > 毒杀 > 狼袭）。
func causeOfDeath(st game.State, seat game.Seat) game.DeathCause {
	if st.Vote.Exiled != nil && *st.Vote.Exiled == seat {
		return game.CauseUnknown
	}
	if st.Night.WitchPoisonTarget != nil && *st.Night.WitchPoisonTarget == seat {
		return game.CauseWitchPoison
	}
	if st.Night.WolfKillTarget != nil && *st.Night.WolfKillTarget == seat {
		return game.CauseWolfKill
	}
	return game.CauseUnknown
}

// witchSeatOf / seerSeatOf 返回女巫/预言家座位。
func witchSeatOf(st game.State) (game.Seat, bool) {
	for _, p := range st.Players {
		if p.Role == game.RoleWitch && p.Seat.Valid() {
			return p.Seat, true
		}
	}
	return 0, false
}

func seerSeatOf(st game.State) (game.Seat, bool) {
	for _, p := range st.Players {
		if p.Role == game.RoleSeer && p.Seat.Valid() {
			return p.Seat, true
		}
	}
	return 0, false
}

// witchRoleDead 报告女巫是否在行动窗口开始前已死亡（docs §夜间 6：死亡神职
// 不跳过阶段但假等待 2/3）。无女巫按死亡处理（防御）。
func witchRoleDead(st game.State) bool {
	seat, ok := witchSeatOf(st)
	if !ok {
		return true
	}
	return playerOfSeat(st, seat).Dead || playerOfSeat(st, seat).Left
}

// seerRoleDead 报告预言家是否在行动窗口开始前已死亡。
func seerRoleDead(st game.State) bool {
	seat, ok := seerSeatOf(st)
	if !ok {
		return true
	}
	return playerOfSeat(st, seat).Dead || playerOfSeat(st, seat).Left
}

// scaleDeadRoleTimers 把死亡神职阶段的计时器缩放为原时长的 2/3（I5，
// docs §夜间 6 防泄密：存活玩家不能通过「阶段变快」推断该角色已死）。
func scaleDeadRoleTimers(fx []game.Effect) []game.Effect {
	out := make([]game.Effect, 0, len(fx))
	for _, e := range fx {
		if te, ok := e.(game.TimerEffect); ok {
			te.Duration = game.DeadRoleStageDuration(te.Duration)
			out = append(out, te)
			continue
		}
		out = append(out, e)
	}
	return out
}

// isSlashCommand 报告文本是否为 Bot 命令（发言拦截前置判断）。
func isSlashCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}
