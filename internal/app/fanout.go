package app

// 局内效果扇出（B1-d）：MessageEffect 按受众分派到目标私聊并渲染+按钮，
// 持久化/解散/冷却等效果在接线层落地（I2/I3 在此逐步补齐）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// fanOut 执行一批领域 Effects：消息扇出 + 持久化/解散等落地。
func (w *Wiring) fanOut(roomID game.RoomID, st game.State, fx []game.Effect) error {
	for _, e := range fx {
		switch te := e.(type) {
		case game.MessageEffect:
			if err := w.fanOutMessage(roomID, st, te); err != nil {
				w.log.Warn("app: fanout message", "room", string(roomID), "key", te.Key, "error", err)
			}
		case game.TimerEffect:
			// Actor 已调度，忽略。
		case game.PersistEffect:
			// 创建/加入/退出已由适配器显式持久化。
		case game.PersistSettlementEffect:
			w.persistSettlement(te.Result)
		case game.DissolveEffect:
			w.dissolveRoom(roomID, te.Reason)
		case game.CooldownEffect:
			// I2：恶意退出跨局冷却持久化（接线层落地）。
			w.applyCooldown(roomID, te)
		case game.ScorePenaltyEffect:
			// I2：房主强制解散扣分。
			if err := w.applyScorePenalty(roomID, te); err != nil {
				return fmt.Errorf("app: apply score penalty for room %s: %w", roomID, err)
			}
		case game.DelayEffect:
			// I1：延迟执行 Inner（发言原消息 3 秒自毁等，docs 阶段消息设计.md
			// §16）。定时回调仍经 fanOut，删除语义见 speech.self_delete 分支。
			inner := te.Inner
			roomID, st := roomID, st
			time.AfterFunc(te.After, func() {
				if err := w.fanOut(roomID, st, []game.Effect{inner}); err != nil {
					w.log.Warn("app: delay fanout", "room", string(roomID), "error", err)
				}
			})
		default:
			w.log.Debug("app: effect ignored", "room", string(roomID), "type", fmt.Sprintf("%T", e))
		}
	}
	return nil
}

// isMainMessageKey 报告效果是否写入「时间段主消息」（docs 阶段消息设计.md
// §3/§16：死讯、投票明细/统计/结果、平票过程、重大事件、发言记录等）。
func isMainMessageKey(key string) bool {
	switch key {
	case "phase.night.start", "night.death", "night.peace", "day.death", "day.peace",
		"vote.detail", "vote.tally", "vote.result",
		"tie.speech", "tie.runoff", "tie.no_speech", "tie.final",
		"wolves.explode", "leave.malicious", "leave.removed",
		"governance.dissolve.initiated", "governance.dissolve.passed",
		"governance.kick.initiated", "governance.kick.passed",
		"governance.host_dissolve.confirm", "governance.host_dissolve.passed",
		"settlement.victory", "speech.accepted":
		return true
	}
	return false
}

// appendMain 把一段进程文本追加到 (chat, period) 的时间段主消息：
// 首次发送（OpSendText + Period），后续编辑同一条消息（OpEditMessage，
// productionSend 复用消息 ID）；超 3000 字符经 Viewer 软分页创建续页
// （docs §4.1：超过阈值照常发送并标记满，下一次更新创建续页，不复制旧页）。
func (w *Wiring) appendMain(roomID game.RoomID, chat int64, period, text string) error {
	key := mainPeriodKey{chat, period}
	periodTok, ok := telegram.ParsePeriod(period)
	if !ok {
		return fmt.Errorf("app: invalid main period %q", period)
	}
	ref, created, err := w.viewer.Append(outbox.ChatID(chat), periodTok, text)
	if err != nil {
		// 时间段已定稿：回退为独立消息，保证内容不丢。
		w.log.Warn("app: main append finalized fallback", "chat", chat, "period", period, "error", err)
		return w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpSendText,
			telegram.Params{ChatID: chat, Text: text}, outbox.PriorityNormal, "")
	}
	if created {
		w.mainMu.Lock()
		w.mainBody[key] = text // 新页（首页或续页）从本条开始，不复制旧页
		w.mainMu.Unlock()
		return w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpSendText,
			telegram.Params{ChatID: chat, Text: text, Period: period}, outbox.PriorityNormal, "")
	}
	w.mainMu.Lock()
	w.mainBody[key] += "\n\n" + text
	body := w.mainBody[key]
	w.mainMu.Unlock()
	_ = ref
	return w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpEditMessage,
		telegram.Params{ChatID: chat, Text: body, Period: period}, outbox.PriorityNormal, "")
}

// currentPeriod 返回导演当前时间段（"night.N"/"day.D"）；大厅/无期间返回空。
func (d *roomDirector) currentPeriod(st game.State) string {
	dr := d.room(st.RoomID)
	return dr.lastPeriod
}
func (w *Wiring) fanOutMessage(roomID game.RoomID, st game.State, e game.MessageEffect) error {
	// 发言原消息 3 秒自毁（docs 游戏流程设计.md §发言限制 2）：删除操作，
	// 不经受众分派/渲染。
	if e.Key == game.SpeechSelfDeleteMessageKey {
		chat, ok1 := e.Params["chat_id"].(int64)
		msgID, ok2 := e.Params["message_id"].(int)
		if !ok1 || !ok2 {
			return fmt.Errorf("app: speech.self_delete 参数缺失 chat_id/message_id")
		}
		return w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpDeleteMessage,
			telegram.Params{ChatID: chat, MessageID: msgID}, outbox.PriorityHigh, "")
	}
	chats, err := w.audienceChats(e.Audience, st, e)
	if err != nil {
		return err
	}
	// 大厅面板由导演路径触发时也必须携带真实 inline keyboard；不能
	// 回退为正文中的“操作：开始/设置/解散”说明（docs §房间面板）。
	if e.Key == game.LobbyPanelMessageKey {
		text, markup, err := w.renderMessage(e, roomID)
		if err != nil {
			return err
		}
		coalesce := "panel:" + string(roomID)
		for _, chat := range chats {
			if err := w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpSendText,
				telegram.Params{ChatID: chat, Text: text, ReplyMarkup: markup}, outbox.PriorityNormal, coalesce); err != nil {
				return err
			}
		}
		return nil
	}
	// 身份卡（Item 2）：sendPhoto 图片 + Caption，每接收者一条 OpSendRoleCard。
	if e.Key == game.DealRoleCardMessageKey {
		role := fmt.Sprint(e.Params["role"])
		seat := 0
		if s, ok := e.Params["seat"].(game.Seat); ok {
			seat = int(s)
		}
		for _, chat := range chats {
			if err := w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpSendRoleCard,
				telegram.Params{ChatID: chat, RoleCardRole: role, RoleCardSeat: seat}, outbox.PriorityHigh, ""); err != nil {
				return err
			}
		}
		return nil
	}
	// 主消息（时间段滚动编辑，Item 1）：每条独立发送/追加到该时间段主消息页。
	if isMainMessageKey(e.Key) {
		period := w.director.currentPeriod(st)
		if period == "" {
			return fmt.Errorf("app: main message %q in phase %v without period", e.Key, st.Phase)
		}
		for _, chat := range chats {
			v := viewerContext(st, game.UserID(chat))
			text, err := w.renderGameEffect(e, st, v)
			if err != nil {
				return err
			}
			if err := w.appendMain(roomID, chat, period, text); err != nil {
				return err
			}
		}
		return nil
	}
	for _, chat := range chats {
		v := viewerContext(st, game.UserID(chat))
		text, err := w.renderGameEffect(e, st, v)
		if err != nil {
			return err
		}
		markup, err := w.buttonsFor(e, st, v)
		if err != nil {
			return err
		}
		prio := outbox.PriorityNormal
		if e.Audience == game.AudienceGodView || strings.HasPrefix(e.Key, "settlement.") {
			prio = outbox.PriorityHigh
		}
		params := telegram.Params{ChatID: chat, Text: text, ReplyMarkup: markup}
		if err := w.enqueue("fx:"+string(roomID), roomID, chat, telegram.OpSendText, params, prio, ""); err != nil {
			return err
		}
	}
	return nil
}

// audienceChats 把受众解析为目标私聊 ChatID 列表（MVP 私聊模型：UserID 即
// ChatID，docs/技术选型.md §10）。
func (w *Wiring) audienceChats(aud game.Audience, st game.State, e game.MessageEffect) ([]int64, error) {
	switch aud {
	case game.AudienceActor:
		u, ok := actorForEffect(e, st)
		if !ok {
			return nil, fmt.Errorf("app: cannot resolve AudienceActor for key %q", e.Key)
		}
		return []int64{int64(u)}, nil
	case game.AudienceHost:
		return []int64{int64(st.Lobby.Owner)}, nil
	case game.AudiencePublic:
		return allPlayerChats(st), nil
	case game.AudienceWolf:
		var out []int64
		for _, p := range st.Players {
			if p.Role == game.RoleWolf && p.Seat.Valid() {
				out = append(out, int64(p.UserID))
			}
		}
		return out, nil
	case game.AudienceSeer:
		if seat, ok := seerSeatOf(st); ok {
			return []int64{int64(playerOfSeat(st, seat).UserID)}, nil
		}
		return nil, fmt.Errorf("app: no seer for AudienceSeer key %q", e.Key)
	case game.AudienceGodView:
		var out []int64
		for _, p := range st.Players {
			if p.Dead && p.Seat.Valid() {
				out = append(out, int64(p.UserID))
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("app: unknown audience %v", aud)
	}
}

// actorForEffect 解析 AudienceActor 的目标玩家：优先 params 座位，兜底按
// 消息前缀推断角色（witch.*→女巫、seer.*→预言家）。
func actorForEffect(e game.MessageEffect, st game.State) (game.UserID, bool) {
	for _, k := range []string{"seat", "speaker"} {
		if s, ok := e.Params[k]; ok {
			if seat, ok := s.(game.Seat); ok {
				if p := playerOfSeat(st, seat); p.UserID != 0 {
					return p.UserID, true
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(e.Key, "witch."):
		if seat, ok := witchSeatOf(st); ok {
			return playerOfSeat(st, seat).UserID, true
		}
	case strings.HasPrefix(e.Key, "seer."):
		if seat, ok := seerSeatOf(st); ok {
			return playerOfSeat(st, seat).UserID, true
		}
	}
	return 0, false
}

// allPlayerChats 返回全部入座玩家的私聊 ChatID。
func allPlayerChats(st game.State) []int64 {
	var out []int64
	for _, p := range st.Players {
		if p.Seat.Valid() {
			out = append(out, int64(p.UserID))
		}
	}
	return out
}

// buttonsFor 为临时操作消息生成 inline keyboard（callback_data=不透明 token，
// docs 技术选型.md §7.3；按钮 label 只显示座位号/私密标记，docs §5.2）。
func (w *Wiring) buttonsFor(e game.MessageEffect, st game.State, v viewerCtx) (*telegram.ReplyMarkup, error) {
	// I5：死亡玩家（上帝视角）不发送操作按钮（docs §夜间 6、§13.3 只读记录）。
	if v.dead {
		return nil, nil
	}
	mk := func(action, target string) string {
		tok, err := w.IssueButton(v.user, action, target, st.Phase, st.PhaseVersion)
		if err != nil {
			return ""
		}
		return tok
	}
	seatRows := func(seats []game.Seat, action string) [][]telegram.InlineButton {
		var rows [][]telegram.InlineButton
		var cur []telegram.InlineButton
		for _, s := range sortedSeats(seats) {
			cur = append(cur, telegram.InlineButton{Text: v.seatBtnLabel(st, s), CallbackData: mk(action, fmt.Sprint(s))})
			if len(cur) == 3 {
				rows = append(rows, cur)
				cur = nil
			}
		}
		if len(cur) > 0 {
			rows = append(rows, cur)
		}
		return rows
	}
	targets := func() []game.Seat {
		if s, ok := e.Params["targets"]; ok {
			return s.([]game.Seat)
		}
		if s, ok := e.Params["candidates"]; ok {
			return s.([]game.Seat)
		}
		return nil
	}

	switch e.Key {
	case "deal.confirm_prompt":
		return &telegram.ReplyMarkup{Rows: [][]telegram.InlineButton{
			{{Text: "已查看身份", CallbackData: mk("confirm_role", "")}},
		}}, nil
	case "wolf.vote":
		rows := seatRows(targets(), "wolf_vote")
		rows = append(rows, []telegram.InlineButton{{Text: "确认选择", CallbackData: mk("wolf_confirm", "")}})
		return &telegram.ReplyMarkup{Rows: rows}, nil
	case "witch.save.prompt":
		return &telegram.ReplyMarkup{Rows: [][]telegram.InlineButton{
			{{Text: "使用解药", CallbackData: mk("witch_save", "yes")}, {Text: "不使用解药", CallbackData: mk("witch_save", "no")}},
			{{Text: "确认选择", CallbackData: mk("witch_confirm", "")}},
		}}, nil
	case "witch.poison.prompt":
		rows := seatRows(targets(), "witch_poison")
		rows = append(rows, []telegram.InlineButton{
			{Text: "不使用毒药", CallbackData: mk("witch_poison", "abstain")},
			{Text: "确认选择", CallbackData: mk("witch_confirm", "")},
		})
		return &telegram.ReplyMarkup{Rows: rows}, nil
	case "seer.prompt":
		rows := seatRows(targets(), "seer_check")
		rows = append(rows, []telegram.InlineButton{{Text: "确认查验", CallbackData: mk("seer_confirm", "")}})
		return &telegram.ReplyMarkup{Rows: rows}, nil
	case "vote.prompt", "tie.runoff_prompt":
		rows := seatRows(targets(), "vote")
		rows = append(rows, []telegram.InlineButton{
			{Text: "弃权", CallbackData: mk("vote", "abstain")},
			{Text: "确认投票", CallbackData: mk("vote_confirm", "")},
		})
		return &telegram.ReplyMarkup{Rows: rows}, nil
	case "tie.duel_prompt":
		rows := seatRows(targets(), "vote")
		rows = append(rows, []telegram.InlineButton{{Text: "确认投票", CallbackData: mk("vote_confirm", "")}})
		return &telegram.ReplyMarkup{Rows: rows}, nil
	case "speech.turn":
		return &telegram.ReplyMarkup{Rows: [][]telegram.InlineButton{
			{{Text: "结束发言", CallbackData: mk("end_speech", "")}},
		}}, nil
	default:
		return nil, nil
	}
}

// persistSettlement 把结算结果落库（I3：战报/积分/统计/清 active；docs
// 技术选型.md §8.3 正常结算单事务）。
func (w *Wiring) persistSettlement(s game.Settlement) {
	result := storage.GameResult{
		RoomCode:   s.RoomID,
		Phase:      s.Phase,
		WinnerCamp: s.Winner,
		Report:     settlementReportText(s),
	}
	for _, p := range s.Players {
		result.Players = append(result.Players, storage.PlayerResult{
			UserID: p.UserID, Seat: p.Seat, Role: p.Role, Camp: p.Camp,
			Died: p.Died, MaliciousExit: p.MaliciousExit,
		})
	}
	if err := storage.NewSettlementRepository(w.db).SettleGame(context.Background(), result); err != nil {
		w.log.Error("app: persist settlement", "room", string(s.RoomID), "error", err)
		return
	}
	// 房间保留在内存注册表供「再来一局」（Rematch → PhaseLobby，docs 游戏
	// 流程设计.md §结算 5/6）；rooms 活跃行已由 SettleGame 单事务清除。
}

// settlementReportText 生成战报文本（胜方 + 全员身份翻牌 + 关键事件）。
func settlementReportText(s game.Settlement) string {
	var b strings.Builder
	winner := "好人"
	if s.Winner == game.CampWolf {
		winner = "狼人"
	}
	fmt.Fprintf(&b, "胜方：%s\n", winner)
	for _, p := range s.Players {
		status := "存活"
		if p.Died {
			status = "死亡"
		}
		fmt.Fprintf(&b, "%d号 %s（%s）%s 积分%+d\n", p.Seat, p.Role, p.Camp, status, p.Score)
	}
	for _, ev := range s.KeyEvents {
		fmt.Fprintf(&b, "· %s\n", ev.Text)
	}
	return b.String()
}

// dissolveRoom 处理房间解散（投票解散/房主强制解散/闲置过期）：清理 storage
// 活跃行 + 内存注册表 + 导演状态 + 停止房间 Actor（B3：DissolveEffect 从 Actor
// 自身 goroutine 的 OnApplied → fanOut 路径触达这里，故只能 Close 发信号，
// 不能同步 Stop 等待，否则死锁）。
func (w *Wiring) dissolveRoom(roomID game.RoomID, _ game.DissolveReason) {
	if err := w.repo.RemoveRoom(context.Background(), roomID); err != nil && !errors.Is(err, storage.ErrRoomNotFound) {
		w.log.Warn("app: dissolve remove room", "room", string(roomID), "error", err)
	}
	// 先取走 Actor 引用再清理注册表/导演（后续 Dispatch 拿不到已停止的 Actor）。
	if actor, ok := w.reg.takeActor(roomID); ok {
		actor.Close()
	}
	w.reg.removeRoom(roomID)
	w.director.release(roomID)
}

// stopActors 停止 Wiring 管理的全部房间 Actor 并等待退出（App 停机第③步；
// 幂等）。Director 持有的 speech timer 由 release 语义一并清理。
func (w *Wiring) stopActors() {
	for _, actor := range w.reg.actors() {
		actor.Close()
	}
	// 等待 goroutine 退出：Close 只发信号，此处统一等待，确保停机后无泄漏。
	for _, actor := range w.reg.actors() {
		actor.Stop()
	}
}

// SweepIdle 执行一轮闲置回收评估（I7）：对每个仍处等待大厅的房间调用
// EvaluateIdle（创建起 1 小时、到期前 10 分钟提醒一次）；到期发通知并解散，
// 游戏开始后（actor 非 nil / 非 lobby）不受影响（docs §闲置回收）。
func (w *Wiring) SweepIdle() {
	for _, code := range w.reg.roomCodes() {
		lr, ok := w.reg.get(code)
		if !ok || lr.actor != nil || lr.st.Phase != game.PhaseLobby {
			continue
		}
		newLt, fx, err := w.life.EvaluateIdle(context.Background(), lr.life, lr.st)
		if err != nil {
			w.log.Warn("app: idle evaluate", "room", string(code), "error", err)
			continue
		}
		w.reg.updateLifetime(code, newLt)
		if err := w.fanOut(code, lr.st, fx); err != nil {
			w.log.Warn("app: idle fanout", "room", string(code), "error", err)
		}
		for _, e := range fx {
			if me, ok := e.(game.MessageEffect); ok && me.Key == game.RoomExpiredMessageKey {
				w.dissolveRoom(code, game.DissolveReasonUnknown)
				break
			}
		}
	}
}

// applyCooldown 落地跨局加入冷却（I2：写 users.cooldown_until，docs
// 游戏流程设计.md §退出约束 10 分钟）。
func (w *Wiring) applyCooldown(_ game.RoomID, e game.CooldownEffect) {
	if e.User == 0 {
		return
	}
	if err := w.users.SetCooldown(context.Background(), e.User, w.now().Add(e.Duration)); err != nil {
		w.log.Warn("app: set cooldown", "user", int64(e.User), "reason", e.Reason, "error", err)
		return
	}
	w.log.Info("app: cooldown set", "user", int64(e.User), "reason", e.Reason, "duration", e.Duration)
}

// applyScorePenalty 落地积分扣减（I2/I3：房主强制解散 -10）。
// storage 在事务内写幂等账本并更新 users.points；失败必须向上返回，
// 不能只记日志后假报效果已应用。
func (w *Wiring) applyScorePenalty(roomID game.RoomID, e game.ScorePenaltyEffect) error {
	if w.users == nil {
		return errors.New("app: user repository is not attached")
	}
	if err := w.users.ApplyScorePenalty(context.Background(), roomID, e.User, e.Amount); err != nil {
		w.log.Warn("app: apply score penalty", "room", string(roomID), "user", int64(e.User), "amount", e.Amount, "error", err)
		return err
	}
	w.log.Info("app: score penalty applied", "room", string(roomID), "user", int64(e.User), "amount", e.Amount)
	return nil
}
