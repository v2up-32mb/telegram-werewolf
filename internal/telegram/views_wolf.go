package telegram

import (
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 狼人夜间视图（docs §夜间 2、§狼人标识、§6/§7 临时操作消息）。
// 本层只描述渲染输入与产出 MarkdownV2 文案，不执行 Telegram 绘制；
// 讨论/投票消息的发送、删除与上帝视角副本策略由后续接线按
// game 包的 wolf.* 消息 key 执行。

// wolfSeatLabel 渲染单个座位按钮标记文案（button.seat 规约：
// 「N号{{.Mark}}」）；isMate 时附加狼人🧺标记语义由调用方传入 Mark。
func wolfSeatLabel(r *i18n.Renderer, seat game.Seat, mark string) (string, error) {
	return r.Render("button.seat", map[string]any{
		"Seat": int(seat),
		"Mark": i18n.SafeMarkdown(mark),
	})
}

// wolfTargetsText 渲染候选目标列表：每行「N号[🐺]」，狼队友座位附带
// 狼人私密标记（docs §狼人标识：🐺 标识只对有权限查看者可见，此处
// 只出现在发给狼人/上帝视角的内容中）。
func wolfTargetsText(r *i18n.Renderer, targets []game.Seat, mates map[game.Seat]bool) (string, error) {
	lines := make([]string, 0, len(targets))
	for _, seat := range targets {
		label, err := wolfSeatLabel(r, seat, wolfmateMark(mates, seat))
		if err != nil {
			return "", err
		}
		lines = append(lines, label)
	}
	return strings.Join(lines, "\n"), nil
}

// wolfmateMark 返回狼队友座位的私密标记；非队友返回空串。
func wolfmateMark(mates map[game.Seat]bool, seat game.Seat) string {
	if mates[seat] {
		return "🐺"
	}
	return ""
}

// matesSet 把狼队友座位切片转为集合。
func matesSet(wolfMates []game.Seat) map[game.Seat]bool {
	m := make(map[game.Seat]bool, len(wolfMates))
	for _, s := range wolfMates {
		m[s] = true
	}
	return m
}

// NewWolfVoteView 渲染狼人投票 UI：复用 action.wolf.kill.prompt 提示，
// 目标列表含狼队友标记（docs §6/§7 固定结构：标题+操作说明+候选）。
// 渲染失败显式报错。
func NewWolfVoteView(r *i18n.Renderer, round int, targets []game.Seat, wolfMates []game.Seat) (string, error) {
	prompt, err := r.Render("action.wolf.kill.prompt", nil)
	if err != nil {
		return "", fmt.Errorf("telegram: render wolf vote prompt: %w", err)
	}
	targetsText, err := wolfTargetsText(r, targets, matesSet(wolfMates))
	if err != nil {
		return "", err
	}
	return r.Render("wolf.vote", map[string]any{
		"Prompt":  i18n.SafeMarkdown(prompt),
		"Round":   round,
		"Targets": i18n.SafeMarkdown(targetsText),
	})
}

// NewWolfDiscussView 渲染狼人讨论消息标题：轮次与狼队友名单
// （docs §狼人标识 双保险：开局给狼人完整队友名单）。
func NewWolfDiscussView(r *i18n.Renderer, round int, wolfMates []game.Seat) (string, error) {
	labels := make([]string, 0, len(wolfMates))
	for _, seat := range wolfMates {
		label, err := wolfSeatLabel(r, seat, "")
		if err != nil {
			return "", err
		}
		labels = append(labels, label)
	}
	return r.Render("wolf.discuss", map[string]any{
		"Round":     round,
		"WolfMates": i18n.SafeMarkdown(strings.Join(labels, "、")),
	})
}

// NewWolfConfirmView 渲染狼人确认锁定文案：目标为击杀目标座位
// （button.seat 渲染）或空刀（Target=nil，SafeMarkdown「空刀」）。
func NewWolfConfirmView(r *i18n.Renderer, seat game.Seat, target *game.Seat) (string, error) {
	var targetText string
	if target == nil {
		targetText = "空刀"
	} else {
		label, err := wolfSeatLabel(r, *target, "")
		if err != nil {
			return "", err
		}
		targetText = label
	}
	seatLabel, err := wolfSeatLabel(r, seat, "")
	if err != nil {
		return "", err
	}
	return r.Render("wolf.vote.locked", map[string]any{
		"Seat":   i18n.SafeMarkdown(seatLabel),
		"Target": i18n.SafeMarkdown(targetText),
	})
}
