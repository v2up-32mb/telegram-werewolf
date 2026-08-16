package telegram

import (
	"fmt"
	"sort"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 白天主消息渲染（docs 游戏流程设计.md §白天/§死亡玩家权限/§主消息形态、
// 阶段消息设计.md §3.3 固定结构、§12 死讯与身份公开、§13 上帝视角）。
// 本层只产生渲染输入/文本，不执行 Telegram 绘制。

// DayView 是单个查看者的白天主消息渲染结果：
// 存活玩家使用【当前进度】+【本时间段累计进程】+【我的私密记录】（无
// 私密信息时仍显式「无」）；死亡玩家第三段改为【上帝视角记录】并携带
// 只读行动记录素材（docs §3.3/§13.3）。
type DayView struct {
	Title    string // 例如 ☀️ 第 2 天白天 · 记录 1
	Progress string // 【当前进度】段
	Timeline string // 【本时间段累计进程】段
	Private  string // 【我的私密记录】段（存活玩家；死亡玩家为空）
	GodView  string // 【上帝视角记录】段（死亡玩家；存活玩家为空）
	Actions  string // 只读行动记录素材（死亡玩家；存活玩家为空）
}

// RenderDayMain 渲染查看者 seat 的白天主消息（docs §12/§13）：
//
//   - 存活玩家：公共死讯（死亡名单；Settings.RevealRoleOnDeath=true 时
//     附死者身份；任何情况不含死因）+ 私密记录「无」；不泄漏其他玩家
//     身份、狼队友、刀口、毒口、查验结果；
//   - 死亡玩家：统一上帝视角第三段（全员身份 + 本夜行动/用药/查验/投票/
//     死讯素材，全部来自既有 State 导出字段）+ 本人真实身份/死因说明 +
//     进入上帝视角说明 + 只读行动记录素材（docs §12.2/§13）。
//
// dayNumber 是白天序号（自然时间线第 N 天）；nick 提供显示昵称（缺失时
// 仅输出座位号）。动态值统一经 i18n.EscapeMarkdownV2 转义。
func RenderDayMain(r *i18n.Renderer, st game.State, viewer game.Seat, dayNumber int, out game.DayOutcome, nick map[game.Seat]string) (DayView, error) {
	if r == nil {
		return DayView{}, fmt.Errorf("telegram: nil renderer")
	}
	bySeat := make(map[game.Seat]game.Player, len(st.Players))
	for _, p := range st.Players {
		if p.Seat.Valid() {
			bySeat[p.Seat] = p
		}
	}
	vp, ok := bySeat[viewer]
	if !ok {
		return DayView{}, fmt.Errorf("telegram: viewer seat %d not in players", viewer)
	}

	title := fmt.Sprintf("☀️ 第 %d 天白天 · 记录 1", dayNumber)
	dead := vp.Dead

	if len(out.Victims) == 0 {
		progress := "【当前进度】\n昨夜平安夜"
		timeline := "【本时间段累计进程】\n✓ 夜间结算完成\n● 白天进行中"
		if dead {
			god, err := dayGodView(r, st, viewer, out)
			if err != nil {
				return DayView{}, err
			}
			return DayView{
				Title: title, Progress: progress, Timeline: timeline,
				GodView: god, Actions: dayReadOnlyActions(),
			}, nil
		}
		return DayView{
			Title: title, Progress: progress, Timeline: timeline,
			Private: dayPrivateNone(),
		}, nil
	}

	progress := "【当前进度】\n" + dayDeathLines(r, st, out, nick, st.Settings.RevealRoleOnDeath)
	timeline := "【本时间段累计进程】\n✓ 夜间结算完成\n● 白天进行中"
	if dead {
		god, err := dayGodView(r, st, viewer, out)
		if err != nil {
			return DayView{}, err
		}
		return DayView{
			Title: title, Progress: progress, Timeline: timeline,
			GodView: god, Actions: dayReadOnlyActions(),
		}, nil
	}
	return DayView{
		Title: title, Progress: progress, Timeline: timeline,
		Private: dayPrivateNone(),
	}, nil
}

func dayPrivateNone() string {
	return "【我的私密记录】\n无"
}

// dayDeathLines 生成公共死讯正文：默认只公布谁死亡；报身份开启时才附
// 死者身份；绝不含死因（docs §12.1）。
func dayDeathLines(r *i18n.Renderer, st game.State, out game.DayOutcome, nick map[game.Seat]string, reveal bool) string {
	var b strings.Builder
	for _, s := range out.Victims {
		line := fmt.Sprintf("%d号", s)
		if name, ok := nick[s]; ok && name != "" {
			line += fmt.Sprintf("「%s」", i18n.EscapeMarkdownV2(name))
		}
		line += "死亡"
		if reveal {
			roleName, err := dayRoleName(r, playerBySeat(st.Players, s).Role)
			if err == nil {
				line += "\n身份：" + roleName
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func dayRoleName(r *i18n.Renderer, role game.Role) (string, error) {
	key, err := roleDisplayNameKey(role)
	if err != nil {
		return "", err
	}
	return r.Render(key, nil)
}

// playerBySeat 返回 st.Players 中座位 s 的玩家（缺失返回零值）。
func playerBySeat(players []game.Player, s game.Seat) game.Player {
	for _, p := range players {
		if p.Seat == s {
			return p
		}
	}
	return game.Player{}
}

// dayGodView 生成死亡玩家统一上帝视角段（docs §13.1）：全员身份、
// 本夜行动/用药/查验/投票素材、死讯、结算结果，以及本人真实身份/死因
// 说明与进入上帝视角说明（§12.2）。
func dayGodView(r *i18n.Renderer, st game.State, viewer game.Seat, out game.DayOutcome) (string, error) {
	var b strings.Builder
	b.WriteString("【上帝视角记录】\n")

	// 全员身份（上帝视角统一视图，不按死亡前角色区分）。
	seats := make([]game.Seat, 0, len(st.Players))
	for _, p := range st.Players {
		if p.Seat.Valid() {
			seats = append(seats, p.Seat)
		}
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	b.WriteString("全员身份：\n")
	for _, s := range seats {
		name, err := dayRoleName(r, playerBySeat(st.Players, s).Role)
		if err != nil {
			return "", err
		}
		mark := " "
		if s == viewer {
			mark = "（我）"
		}
		fmt.Fprintf(&b, "%d号：%s%s\n", s, name, mark)
	}

	// 本夜行动素材（只读上帝视角；来自既有 State 导出字段）。
	b.WriteString("本夜行动：\n")
	if st.Night.WitchUsedTonight && st.Night.WitchSaveUsed {
		b.WriteString("✓ 女巫解药：已使用\n")
	}
	if st.Night.WitchUsedTonight && st.Night.WitchPoisonUsed && st.Night.WitchPoisonTarget != nil {
		fmt.Fprintf(&b, "✓ 女巫毒药：%d号\n", *st.Night.WitchPoisonTarget)
	}
	checks := make([]game.Seat, 0, len(st.Night.SeerResults))
	for s := range st.Night.SeerResults {
		checks = append(checks, s)
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i] < checks[j] })
	for _, s := range checks {
		side := "好人"
		if st.Night.SeerResults[s] == game.CampWolf {
			side = "狼人"
		}
		fmt.Fprintf(&b, "✓ 查验：%d号 → %s\n", s, side)
	}
	wolfVotes := make([]game.Seat, 0, len(st.Night.WolfVotes))
	for s := range st.Night.WolfVotes {
		wolfVotes = append(wolfVotes, s)
	}
	sort.Slice(wolfVotes, func(i, j int) bool { return wolfVotes[i] < wolfVotes[j] })
	for _, s := range wolfVotes {
		target := st.Night.WolfVotes[s]
		if target != nil {
			fmt.Fprintf(&b, "✓ 狼人投票：%d号 → %d号\n", s, *target)
		}
	}

	// 死讯与结算结果。
	if len(out.Victims) > 0 {
		seatsText := make([]string, 0, len(out.Victims))
		for _, s := range out.Victims {
			seatsText = append(seatsText, fmt.Sprintf("%d号", s))
		}
		b.WriteString("死者：" + strings.Join(seatsText, "、") + "\n")
	}
	b.WriteString("结算：未分胜负，进入白天发言\n")

	// 本人真实身份/死因与进入上帝视角说明（§12.2；死因只出现于本人私聊）。
	self := playerBySeat(st.Players, viewer)
	if self.UserID != 0 {
		name, err := dayRoleName(r, self.Role)
		if err != nil {
			return "", err
		}
		cause := out.Cause[viewer]
		if !cause.Valid() {
			cause = game.CauseUnknown
		}
		fmt.Fprintf(&b, "我的身份：%s\n", name)
		fmt.Fprintf(&b, "我的死因：%s\n", i18n.EscapeMarkdownV2(cause.String()))
	}
	b.WriteString("你已进入上帝视角，可以看到全员身份与行动记录。")
	return b.String(), nil
}

// dayReadOnlyActions 生成死亡玩家的只读行动记录素材（docs §13.3：
// 无按钮、实时编辑、永久保留；按钮与编辑编排属接线层）。
func dayReadOnlyActions() string {
	return "【只读行动记录】\n后续角色行动将在此实时更新"
}
