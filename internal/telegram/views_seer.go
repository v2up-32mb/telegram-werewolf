package telegram

import (
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 预言家夜间视图（docs §夜间 4、§8.3 预言家、§5 玩家引用与私密标记）。
// 本层只描述渲染输入与产出 MarkdownV2 文案，不执行 Telegram 绘制；
// 查验提示/结果消息的发送、删除与上帝视角副本策略由后续接线按
// game 包的 seer.* 消息 key 执行。

// seerSeatLabel 渲染单个座位按钮标记文案（button.seat 规约「N号」）。
func seerSeatLabel(r *i18n.Renderer, seat game.Seat) (string, error) {
	return r.Render("button.seat", map[string]any{
		"Seat": int(seat),
		"Mark": i18n.SafeMarkdown(""),
	})
}

// seerMark 返回查验结果的私密标记文案（docs §5 预言家标记：
// 🟢 好人 / 🐺 狼人，仅预言家本人可见内容中使用）。
func seerMark(r *i18n.Renderer, camp game.Camp) (string, error) {
	switch camp {
	case game.CampWolf:
		return r.Render("mark.wolf_seer", nil)
	case game.CampGood:
		return r.Render("mark.good_seer", nil)
	default:
		return "", fmt.Errorf("telegram: unknown seer camp %v", camp)
	}
}

// NewSeerPromptView 渲染查验选择 UI：查验提示 + 候选存活目标列表
// （docs §6/§7 固定结构：标题+操作说明+候选）。
func NewSeerPromptView(r *i18n.Renderer, targets []game.Seat) (string, error) {
	prompt, err := r.Render("action.seer.check.prompt", nil)
	if err != nil {
		return "", fmt.Errorf("telegram: render seer check prompt: %w", err)
	}
	lines := make([]string, 0, len(targets))
	for _, seat := range targets {
		label, err := seerSeatLabel(r, seat)
		if err != nil {
			return "", err
		}
		lines = append(lines, label)
	}
	return r.Render("seer.prompt", map[string]any{
		"Prompt":  i18n.SafeMarkdown(prompt),
		"Targets": i18n.SafeMarkdown(strings.Join(lines, "\n")),
	})
}

// NewSeerResultView 渲染确认后的二分查验结果（docs §夜间 4：狼人/好人，
// 不区分具体神职）：目标座位 + 私密标记（🟢 好人 / 🐺 狼人）。
func NewSeerResultView(r *i18n.Renderer, target game.Seat, camp game.Camp) (string, error) {
	label, err := seerSeatLabel(r, target)
	if err != nil {
		return "", err
	}
	mark, err := seerMark(r, camp)
	if err != nil {
		return "", err
	}
	return r.Render("seer.result", map[string]any{
		"Target": i18n.SafeMarkdown(label),
		"Mark":   i18n.SafeMarkdown(mark),
	})
}

// NewSeerNoneView 渲染超时空验提示（docs「超时与默认选择」：本轮空验、
// 不随机查验、无结果；文案采用「已按默认选择处理」，不出现「已跳过」）。
func NewSeerNoneView(r *i18n.Renderer) (string, error) {
	return r.Render("seer.none", nil)
}
