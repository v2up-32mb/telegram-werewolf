package telegram

import (
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 女巫夜间视图（docs §夜间 3、§8.2 女巫、阶段消息设计 §女巫临时操作
// 消息）。本层只描述渲染输入与产出 MarkdownV2 文案，不执行 Telegram
// 绘制；刀口告知、解药/毒药提示消息的发送、删除与上帝视角副本策略
// 由后续接线按 game 包的 witch.* 消息 key 执行。

// witchSeatLabel 渲染单个座位按钮标记文案（button.seat 规约「N号」）。
func witchSeatLabel(r *i18n.Renderer, seat game.Seat) (string, error) {
	return r.Render("button.seat", map[string]any{
		"Seat": int(seat),
		"Mark": i18n.SafeMarkdown(""),
	})
}

// potionStatus 返回药品可用状态文案（可用/已用，docs §8.2 女巫私密记录）。
func potionStatus(r *i18n.Renderer, used bool) (string, error) {
	key := "witch.potion.available"
	if used {
		key = "witch.potion.used"
	}
	return r.Render(key, nil)
}

// NewWitchKillRevealView 渲染今晚刀人目标告知：有目标显示「N号」，
// target=0（哨兵，代表平安夜 WolfKillTarget=nil）显示平安夜文案。
// 只发给女巫本人与上帝视角（AudienceActor/AudienceGodView）。
func NewWitchKillRevealView(r *i18n.Renderer, target game.Seat) (string, error) {
	var targetText string
	if target == 0 {
		peace, err := r.Render("witch.peace_night", nil)
		if err != nil {
			return "", fmt.Errorf("telegram: render witch peace night: %w", err)
		}
		targetText = peace
	} else {
		label, err := witchSeatLabel(r, target)
		if err != nil {
			return "", fmt.Errorf("telegram: render witch kill target: %w", err)
		}
		targetText = label
	}
	return r.Render("witch.kill_reveal", map[string]any{
		"Target": i18n.SafeMarkdown(targetText),
	})
}

// NewWitchSaveView 渲染解药窗口 UI：使用解药提示 + 解药/毒药可用状态
// （docs §6/§7 固定结构：标题+操作说明+当前选择）。
func NewWitchSaveView(r *i18n.Renderer, saveUsed, poisonUsed bool) (string, error) {
	prompt, err := r.Render("action.witch.save.prompt", nil)
	if err != nil {
		return "", fmt.Errorf("telegram: render witch save prompt: %w", err)
	}
	saveStatus, err := potionStatus(r, saveUsed)
	if err != nil {
		return "", err
	}
	poisonStatus, err := potionStatus(r, poisonUsed)
	if err != nil {
		return "", err
	}
	return r.Render("witch.save.prompt", map[string]any{
		"Prompt":       i18n.SafeMarkdown(prompt),
		"SaveStatus":   i18n.SafeMarkdown(saveStatus),
		"PoisonStatus": i18n.SafeMarkdown(poisonStatus),
	})
}

// NewWitchPoisonView 渲染毒药窗口 UI：毒药提示 + 候选存活目标列表 +
// 「不使用毒药」选项。目标列表由调用方传入（存活玩家，不含死亡玩家）。
func NewWitchPoisonView(r *i18n.Renderer, targets []game.Seat, saveUsed, poisonUsed bool) (string, error) {
	prompt, err := r.Render("action.witch.poison.prompt", nil)
	if err != nil {
		return "", fmt.Errorf("telegram: render witch poison prompt: %w", err)
	}
	lines := make([]string, 0, len(targets))
	for _, seat := range targets {
		label, err := witchSeatLabel(r, seat)
		if err != nil {
			return "", err
		}
		lines = append(lines, label)
	}
	saveStatus, err := potionStatus(r, saveUsed)
	if err != nil {
		return "", err
	}
	poisonStatus, err := potionStatus(r, poisonUsed)
	if err != nil {
		return "", err
	}
	return r.Render("witch.poison.prompt", map[string]any{
		"Prompt":       i18n.SafeMarkdown(prompt),
		"Targets":      i18n.SafeMarkdown(strings.Join(lines, "\n")),
		"SaveStatus":   i18n.SafeMarkdown(saveStatus),
		"PoisonStatus": i18n.SafeMarkdown(poisonStatus),
	})
}

// NewWitchSaveConfirmView 渲染解药确认锁定文案（使用的药/不使用解药）。
func NewWitchSaveConfirmView(r *i18n.Renderer, used bool) (string, error) {
	key := "witch.choice.save_not_used"
	if used {
		key = "witch.choice.save_used"
	}
	choice, err := r.Render(key, nil)
	if err != nil {
		return "", err
	}
	return r.Render("witch.save.locked", map[string]any{
		"Choice": i18n.SafeMarkdown(choice),
	})
}

// NewWitchPoisonConfirmView 渲染毒药确认锁定文案（毒杀目标/不使用毒药）。
func NewWitchPoisonConfirmView(r *i18n.Renderer, target game.Seat) (string, error) {
	if target == 0 {
		choice, err := r.Render("witch.choice.poison_not_used", nil)
		if err != nil {
			return "", err
		}
		return r.Render("witch.poison.locked", map[string]any{
			"Choice": i18n.SafeMarkdown(choice),
		})
	}
	label, err := witchSeatLabel(r, target)
	if err != nil {
		return "", err
	}
	choice, err := r.Render("witch.choice.poison_used", map[string]any{
		"Seat": i18n.SafeMarkdown(label),
	})
	if err != nil {
		return "", err
	}
	return r.Render("witch.poison.locked", map[string]any{
		"Choice": i18n.SafeMarkdown(choice),
	})
}
