package app

// 局内消息渲染与扇出（B1-d）。
//
// 设计：每个 MessageEffect 按受众分派到目标私聊（Public/Wolf/Seer/GodView
// 会扇出到多名玩家），渲染时按查看者认知添加私密标记（docs 阶段消息设计.md
// §5：狼人 🐺 狼队友、预言家 🟢 好人/🐺 狼人、上帝视角完整角色标记）；
// 临时操作消息附带 inline keyboard 按钮（callback_data 为不透明 token，
// docs 技术选型.md §7.3）。文本统一经 i18n.Renderer（默认 MarkdownV2 转义）。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// viewerCtx 是渲染时查看者的认知上下文（私密标记依据）。
type viewerCtx struct {
	user     game.UserID
	seat     game.Seat
	role     game.Role
	dead     bool
	wolfMate map[game.Seat]bool      // 查看者是狼人：狼队友座位
	checked  map[game.Seat]game.Camp // 查看者是预言家：已验结果
}

// viewerContext 构造查看者上下文。
func viewerContext(st game.State, viewer game.UserID) viewerCtx {
	v := viewerCtx{user: viewer}
	for _, p := range st.Players {
		if p.UserID != viewer {
			continue
		}
		v.seat, v.role, v.dead = p.Seat, p.Role, p.Dead
		break
	}
	if v.role == game.RoleWolf {
		v.wolfMate = make(map[game.Seat]bool)
		for _, p := range st.Players {
			if p.Role == game.RoleWolf && p.Seat != v.seat {
				v.wolfMate[p.Seat] = true
			}
		}
	}
	if v.role == game.RoleSeer {
		v.checked = make(map[game.Seat]game.Camp, len(st.Night.SeerResults))
		for seat, camp := range st.Night.SeerResults {
			v.checked[seat] = camp
		}
	}
	return v
}

// playerOfSeat 返回座位玩家；不存在返回零值。
func playerOfSeat(st game.State, seat game.Seat) game.Player {
	for _, p := range st.Players {
		if p.Seat == seat {
			return p
		}
	}
	return game.Player{}
}

// seatNick 返回座位昵称（users 全局身份；缺省返回座位号）。
func (w *Wiring) seatNick(st game.State, seat game.Seat) string {
	p := playerOfSeat(st, seat)
	if p.UserID == 0 {
		return fmt.Sprintf("%d号", seat)
	}
	if u, err := w.users.Load(context.Background(), p.UserID); err == nil && u.Nickname != "" {
		return u.Nickname
	}
	return fmt.Sprintf("%d号", seat)
}

// seatMark 返回座位在查看者视角的私密标记（空串=无标记）。
func (v viewerCtx) seatMark(st game.State, seat game.Seat) string {
	if v.dead {
		switch playerOfSeat(st, seat).Role {
		case game.RoleWolf:
			return "🐺 狼人"
		case game.RoleSeer:
			return "🔮 预言家"
		case game.RoleWitch:
			return "💊 女巫"
		default:
			return "👤 平民"
		}
	}
	if v.role == game.RoleWolf && v.wolfMate[seat] {
		return "🐺 狼队友"
	}
	if v.role == game.RoleSeer {
		if camp, ok := v.checked[seat]; ok {
			if camp == game.CampWolf {
				return "🐺 狼人"
			}
			return "🟢 好人"
		}
	}
	return ""
}

// seatRef 渲染「N号「昵称」·标记」引用（含昵称与私密标记）。
func (w *Wiring) seatRef(v viewerCtx, st game.State, seat game.Seat) i18n.SafeMarkdown {
	label := w.seatNick(st, seat)
	mark := v.seatMark(st, seat)
	if mark != "" {
		label += " · " + mark
	}
	return i18n.SafeMarkdown(i18n.EscapeMarkdownV2(label))
}

// seatBtnLabel 渲染按钮标记：「N号[🐺]」（docs §5.2：Emoji 紧贴座位号）。
func (v viewerCtx) seatBtnLabel(st game.State, seat game.Seat) string {
	label := fmt.Sprintf("%d号", seat)
	if v.role == game.RoleWolf && v.wolfMate[seat] {
		label += "🐺"
	}
	return label
}

// seatListText 把座位列表渲染为「1号、2号」。
func (w *Wiring) seatListText(v viewerCtx, st game.State, seats []game.Seat) i18n.SafeMarkdown {
	labels := make([]string, 0, len(seats))
	for _, s := range seats {
		labels = append(labels, string(w.seatRef(v, st, s)))
	}
	return i18n.SafeMarkdown(strings.Join(labels, "、"))
}

// sortedSeats 返回升序座位副本。
func sortedSeats(seats []game.Seat) []game.Seat {
	out := append([]game.Seat(nil), seats...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// aliveSeatSlice 返回存活玩家座位（升序）。
func aliveSeatSlice(st game.State) []game.Seat {
	var out []game.Seat
	for _, p := range st.Players {
		if !p.Dead && p.Seat.Valid() {
			out = append(out, p.Seat)
		}
	}
	return sortedSeats(out)
}

// renderGameEffect 按 key 渲染局内效果为 MarkdownV2 文本（每接收者视角）。
func (w *Wiring) renderGameEffect(e game.MessageEffect, st game.State, v viewerCtx) (string, error) {
	r := w.renderer
	p := e.Params
	seatParam := func(k string) *game.Seat {
		if s, ok := p[k]; ok {
			if seat, ok := s.(game.Seat); ok {
				return &seat
			}
		}
		return nil
	}

	switch e.Key {
	case "phase.night.start":
		return r.Render("phase.night.start", map[string]any{"PhaseNumber": p["phase_number"]})
	case "night.death":
		victims := p["victims"].([]game.Seat)
		return r.Render("night.death", map[string]any{"Victims": w.seatListText(v, st, sortedSeats(victims))})
	case "night.peace":
		return r.Render("night.peace", nil)
	case "day.death":
		victims := p["victims"].([]game.Seat)
		roles := ""
		if rm, ok := p["roles"].(map[game.Seat]game.Role); ok {
			var parts []string
			for _, s := range sortedSeats(victims) {
				parts = append(parts, string(w.seatRef(v, st, s))+"（"+game.Role(rm[s]).String()+"）")
			}
			roles = "\n身份：" + strings.Join(parts, "、")
		}
		return r.Render("day.death", map[string]any{
			"Victims": w.seatListText(v, st, sortedSeats(victims)),
			"Roles":   i18n.SafeMarkdown(roles),
		})
	case "day.peace":
		return r.Render("day.peace", nil)
	case "day.death_private":
		role := p["role"].(game.Role)
		cause := p["cause"].(game.DeathCause)
		causeText := "未知"
		switch cause {
		case game.CauseWolfKill:
			causeText = "被狼人袭击"
		case game.CauseWitchPoison:
			causeText = "被女巫毒杀"
		}
		return r.Render("day.death_private", map[string]any{
			"Role":  game.Role(role).String(),
			"Cause": causeText,
		})
	case "deal.role_card":
		role := p["role"].(string)
		camp := p["camp"].(string)
		matesLine := ""
		if mates, ok := p["wolf_mates"].([]game.Seat); ok && len(mates) > 0 {
			m, err := r.Render("deal.wolf_mates_line", map[string]any{"Mates": w.seatListText(v, st, sortedSeats(mates))})
			if err != nil {
				return "", err
			}
			matesLine = m
		}
		return r.Render("deal.role_card", map[string]any{
			"Role":          role,
			"Camp":          camp,
			"WolfMatesLine": i18n.SafeMarkdown(matesLine),
		})
	case "deal.confirm_prompt":
		return r.Render("deal.confirm_prompt", nil)
	case "deal.confirm_done":
		return r.Render("deal.confirm_done", nil)
	case "wolf.discuss":
		round := p["round"].(int)
		mates := p["wolf_mates"].([]game.Seat)
		labels := make([]string, 0, len(mates))
		for _, s := range sortedSeats(mates) {
			labels = append(labels, string(w.seatRef(v, st, s)))
		}
		return r.Render("wolf.discuss", map[string]any{
			"Round":     round,
			"WolfMates": i18n.SafeMarkdown(strings.Join(labels, "、")),
		})
	case "wolf.vote":
		round := p["round"].(int)
		targets := p["targets"].([]game.Seat)
		prompt, err := r.Render("action.wolf.kill.prompt", nil)
		if err != nil {
			return "", err
		}
		var lines []string
		for _, s := range sortedSeats(targets) {
			lines = append(lines, v.seatBtnLabel(st, s))
		}
		return r.Render("wolf.vote", map[string]any{
			"Prompt":  i18n.SafeMarkdown(prompt),
			"Round":   round,
			"Targets": i18n.SafeMarkdown(strings.Join(lines, "\n")),
		})
	case "wolf.vote.locked":
		seat := p["seat"].(game.Seat)
		return r.Render("wolf.vote.locked", map[string]any{
			"Seat":   w.seatRef(v, st, seat),
			"Target": targetOrAbstain(w, v, st, seatParam("target")),
		})
	case "wolf.tie":
		return r.Render("wolf.tie", map[string]any{"Round": p["round"]})
	case "witch.kill_reveal":
		kill := p["kill_target"].(*game.Seat)
		if kill == nil {
			return r.Render("witch.kill_reveal", map[string]any{"Target": "平安夜"})
		}
		return r.Render("witch.kill_reveal", map[string]any{"Target": w.seatRef(v, st, *kill)})
	case "witch.save.prompt":
		prompt, err := r.Render("action.witch.save.prompt", nil)
		if err != nil {
			return "", err
		}
		return r.Render("witch.save.prompt", map[string]any{
			"Prompt":       i18n.SafeMarkdown(prompt),
			"SaveStatus":   potionStatus(p["save_used"].(bool)),
			"PoisonStatus": potionStatus(p["poison_used"].(bool)),
		})
	case "witch.poison.prompt":
		prompt, err := r.Render("action.witch.poison.prompt", nil)
		if err != nil {
			return "", err
		}
		targets := p["targets"].([]game.Seat)
		var lines []string
		for _, s := range sortedSeats(targets) {
			lines = append(lines, v.seatBtnLabel(st, s))
		}
		return r.Render("witch.poison.prompt", map[string]any{
			"Prompt":       i18n.SafeMarkdown(prompt),
			"Targets":      i18n.SafeMarkdown(strings.Join(lines, "\n")),
			"SaveStatus":   potionStatus(p["save_used"].(bool)),
			"PoisonStatus": potionStatus(p["poison_used"].(bool)),
		})
	case "witch.save.locked":
		choice := "不使用解药"
		if used, ok := p["used"].(bool); ok && used {
			choice = "使用解药"
		}
		return r.Render("witch.save.locked", map[string]any{"Choice": choice})
	case "witch.poison.locked":
		choice := "不使用毒药"
		if t := seatParam("target"); t != nil {
			choice = string(w.seatRef(v, st, *t))
		}
		return r.Render("witch.poison.locked", map[string]any{"Choice": choice})
	case "witch.none":
		return r.Render("witch.none", nil)
	case "seer.prompt":
		prompt, err := r.Render("action.seer.check.prompt", nil)
		if err != nil {
			return "", err
		}
		targets := p["targets"].([]game.Seat)
		var lines []string
		for _, s := range sortedSeats(targets) {
			lines = append(lines, v.seatBtnLabel(st, s))
		}
		return r.Render("seer.prompt", map[string]any{
			"Prompt":  i18n.SafeMarkdown(prompt),
			"Targets": i18n.SafeMarkdown(strings.Join(lines, "\n")),
		})
	case "seer.result":
		target := p["target"].(game.Seat)
		camp := p["camp"].(game.Camp)
		mark := "🟢 好人"
		if camp == game.CampWolf {
			mark = "🐺 狼人"
		}
		return r.Render("seer.result", map[string]any{
			"Target": w.seatRef(v, st, target),
			"Mark":   i18n.SafeMarkdown(mark),
		})
	case "seer.none":
		return r.Render("seer.none", nil)
	case "vote.prompt":
		prompt, err := r.Render("action.vote.prompt", nil)
		if err != nil {
			return "", err
		}
		targets := p["candidates"].([]game.Seat)
		var lines []string
		for _, s := range sortedSeats(targets) {
			lines = append(lines, v.seatBtnLabel(st, s))
		}
		lines = append(lines, "[弃权]")
		return r.Render("vote.prompt", map[string]any{
			"Prompt":   i18n.SafeMarkdown(prompt),
			"Targets":  i18n.SafeMarkdown(strings.Join(lines, "\n")),
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "vote.locked":
		seat := p["seat"].(game.Seat)
		return r.Render("vote.locked", map[string]any{
			"Seat":   w.seatRef(v, st, seat),
			"Target": targetOrAbstain(w, v, st, seatParam("target")),
		})
	case "vote.detail":
		ballots := p["ballots"].(map[game.Seat]game.Seat)
		var lines []string
		for _, from := range sortedSeatKeys(ballots) {
			to := ballots[from]
			lines = append(lines, string(w.seatRef(v, st, from))+" → "+string(targetOrAbstain(w, v, st, seatOf(to))))
		}
		return r.Render("vote.detail", map[string]any{"Lines": i18n.SafeMarkdown(strings.Join(lines, "\n"))})
	case "vote.tally":
		counts := p["counts"].(map[game.Seat]int)
		abstain := p["abstain"].(int)
		var lines []string
		for _, s := range sortedSeatIntKeys(counts) {
			lines = append(lines, fmt.Sprintf("%s：%d 票", w.seatRef(v, st, s), counts[s]))
		}
		lines = append(lines, fmt.Sprintf("弃权：%d 票", abstain))
		return r.Render("vote.tally", map[string]any{"Lines": i18n.SafeMarkdown(strings.Join(lines, "\n"))})
	case "vote.result":
		if exiled := seatParam("exiled"); exiled != nil {
			return r.Render("vote.result", map[string]any{"Target": w.seatRef(v, st, *exiled)})
		}
		return r.Render("vote.result.none", nil)
	case "last_words.prompt":
		seat := p["seat"].(game.Seat)
		return r.Render("last_words.prompt", map[string]any{
			"Seat":     w.seatRef(v, st, seat),
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "last_words.published":
		seat := p["seat"].(game.Seat)
		return r.Render("last_words.published", map[string]any{
			"Seat": w.seatRef(v, st, seat),
			"Text": p["text"],
		})
	case "tie.speech":
		return r.Render("tie.speech", map[string]any{"Candidates": w.seatListText(v, st, p["candidates"].([]game.Seat))})
	case "tie.speech_turn":
		seat := p["seat"].(game.Seat)
		return r.Render("tie.speech_turn", map[string]any{
			"Seat":     w.seatRef(v, st, seat),
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "tie.runoff":
		return r.Render("tie.runoff", map[string]any{"Candidates": w.seatListText(v, st, p["candidates"].([]game.Seat))})
	case "tie.no_speech":
		return r.Render("tie.no_speech", map[string]any{
			"Round":      p["round"],
			"Candidates": w.seatListText(v, st, p["candidates"].([]game.Seat)),
		})
	case "tie.final":
		return r.Render("tie.final", map[string]any{"Candidates": w.seatListText(v, st, p["candidates"].([]game.Seat))})
	case "tie.runoff_prompt":
		return r.Render("tie.runoff_prompt", map[string]any{
			"Seat":     w.seatRef(v, st, p["seat"].(game.Seat)),
			"Targets":  w.seatListText(v, st, p["candidates"].([]game.Seat)),
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "tie.duel_prompt":
		return r.Render("tie.duel_prompt", map[string]any{
			"Seat":     w.seatRef(v, st, p["seat"].(game.Seat)),
			"Targets":  w.seatListText(v, st, p["candidates"].([]game.Seat)),
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "tie.duel_excluded":
		return r.Render("tie.duel_excluded", map[string]any{"Seat": p["seat"]})
	case "speech.turn":
		seat := p["speaker"].(game.Seat)
		return r.Render("speech.turn", map[string]any{
			"Seat":     w.seatRef(v, st, seat),
			"Sent":     p["sent"],
			"Total":    p["total"],
			"Deadline": w.formatDeadline(p["deadline"]),
		})
	case "speech.accepted":
		seat := p["seat"].(game.Seat)
		return r.Render("speech.accepted", map[string]any{
			"Speaker": w.seatRef(v, st, seat),
			"Text":    p["text"],
		})
	case "speech.rejected":
		return r.Render("speech.rejected", map[string]any{"Reason": p["reason"]})
	case "speech.time_left":
		return r.Render("speech.time_left", nil)
	case "settlement.victory":
		return r.Render("settlement.victory", map[string]any{"Winner": p["winner"]})
	case "settlement.report":
		return r.Render("settlement.report", map[string]any{"Winner": p["winner"]})
	case "wolves.explode":
		return r.Render("wolves.explode", map[string]any{"Seat": p["seat"]})
	case "governance.dissolve.initiated":
		return r.Render("governance.dissolve.initiated", map[string]any{"Initiator": p["initiator"]})
	case "governance.dissolve.vote":
		return r.Render("governance.dissolve.vote", nil)
	case "governance.dissolve.passed":
		return r.Render("governance.dissolve.passed", nil)
	case "governance.kick.initiated":
		return r.Render("governance.kick.initiated", map[string]any{
			"Initiator": p["initiator"], "Target": p["target"],
		})
	case "governance.kick.vote":
		return r.Render("governance.kick.vote", nil)
	case "governance.kick.passed":
		return r.Render("governance.kick.passed", map[string]any{"Target": p["target"]})
	case "governance.host_dissolve.confirm":
		return r.Render("governance.host_dissolve.confirm", nil)
	case "governance.host_dissolve.passed":
		return r.Render("governance.host_dissolve.passed", nil)
	case "lobby.rematch":
		return r.Render("lobby.rematch", map[string]any{"WindowSeconds": p["window_seconds"]})
	default:
		// 回退：把 params 原样交给 i18n 模板（已有模板的 key 直接命中）。
		if text, err := r.Render(e.Key, e.Params); err == nil {
			return text, nil
		}
		return "", fmt.Errorf("app: no renderer for game effect key %q", e.Key)
	}
}

// potionStatus 渲染药品状态。
func potionStatus(used bool) string {
	if used {
		return "已用"
	}
	return "可用"
}

// targetOrAbstain 渲染目标座位或「弃权/空刀」。
func targetOrAbstain(w *Wiring, v viewerCtx, st game.State, seat *game.Seat) i18n.SafeMarkdown {
	if seat == nil {
		return i18n.SafeMarkdown("弃权")
	}
	return w.seatRef(v, st, *seat)
}

// seatOf 把可能为 0 的座位转为 *Seat。
func seatOf(s game.Seat) *game.Seat {
	if s == 0 {
		return nil
	}
	return &s
}

// sortedSeatKeys 返回 map 座位键升序。
func sortedSeatKeys(m map[game.Seat]game.Seat) []game.Seat {
	out := make([]game.Seat, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	return sortedSeats(out)
}

// sortedSeatIntKeys 返回 map 座位键升序。
func sortedSeatIntKeys(m map[game.Seat]int) []game.Seat {
	out := make([]game.Seat, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	return sortedSeats(out)
}

// formatDeadline 渲染 UTC+8 截止时刻（docs 阶段消息设计.md §3.5）。
func (w *Wiring) formatDeadline(v any) i18n.SafeMarkdown {
	if t, ok := v.(time.Time); ok {
		return i18n.SafeMarkdown(t.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05（UTC+8）"))
	}
	return i18n.SafeMarkdown(fmt.Sprint(v))
}
