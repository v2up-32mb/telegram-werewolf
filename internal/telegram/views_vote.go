package telegram

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 白天投票与遗言视图（docs 游戏流程设计.md §投票、§结算 4 遗言、
// 阶段消息设计.md §8.4 白天投票、§3.3 时间文案、§4.3 长度不变量）。
// 本层只产生渲染输入/文本，不执行 Telegram 绘制；投票临时消息的发送/
// 删除与明细/统计/结果写入当天主消息由接线层按 game 包的
// vote.*/last_words.* 消息 key 执行。
//
// 长度不变量（docs §4.3）：单条渲染结果在追加到主消息页时不得超过
// Telegram 硬限制；本层文本均为短行，超长由接线层按 Viewer 分页处理。

// formatDeadline 返回 UTC+8 固定时区的截止时刻文本
// （docs §3.3：YYYY-MM-DD HH:mm:ss（UTC+8））。
func formatDeadline(t time.Time) string {
	return t.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05")
}

// NewVotePromptView 渲染投票 UI：复用 action.vote.prompt，候选座位列表
// 与弃权选项、确认提示及 UTC+8 截止时刻（docs §8.4、§3.3）。
func NewVotePromptView(r *i18n.Renderer, targets []game.Seat, deadline time.Time) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	prompt, err := r.Render("action.vote.prompt", nil)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(targets))
	for _, seat := range targets {
		label, err := wolfSeatLabel(r, seat, "")
		if err != nil {
			return "", err
		}
		lines = append(lines, label)
	}
	return r.Render("vote.prompt", map[string]any{
		"Prompt":   i18n.SafeMarkdown(prompt),
		"Targets":  i18n.SafeMarkdown(strings.Join(lines, "\n")),
		"Deadline": i18n.SafeMarkdown(formatDeadline(deadline)),
	})
}

// NewVoteLockedView 渲染确认锁定文案：target 非 nil 时显示目标座位，
// nil（弃权）显示「弃权」（docs §投票 1：确认后锁定）。
func NewVoteLockedView(r *i18n.Renderer, target *game.Seat) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	var targetText string
	if target == nil {
		targetText = "弃权"
	} else {
		label, err := wolfSeatLabel(r, *target, "")
		if err != nil {
			return "", err
		}
		targetText = label
	}
	return r.Render("vote.locked", map[string]any{
		"Target": i18n.SafeMarkdown(targetText),
	})
}

// NewVoteDetailView 渲染逐人明细（docs §投票 1：结束后统一公布「谁投了
// 谁」）：ballots 值 0=弃权，按投票人座位升序输出「N号 → M号/弃权」。
func NewVoteDetailView(r *i18n.Renderer, ballots map[game.Seat]game.Seat) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	voters := make([]game.Seat, 0, len(ballots))
	for from := range ballots {
		voters = append(voters, from)
	}
	sort.Slice(voters, func(i, j int) bool { return voters[i] < voters[j] })

	lines := make([]string, 0, len(voters))
	for _, from := range voters {
		fromLabel, err := wolfSeatLabel(r, from, "")
		if err != nil {
			return "", err
		}
		targetText := "弃权"
		if ballots[from] != 0 {
			label, err := wolfSeatLabel(r, ballots[from], "")
			if err != nil {
				return "", err
			}
			targetText = label
		}
		lines = append(lines, fromLabel+" → "+targetText)
	}
	return r.Render("vote.detail", map[string]any{
		"Lines": i18n.SafeMarkdown(strings.Join(lines, "\n")),
	})
}

// NewVoteTallyView 渲染票数统计：counts 为目标座位 → 票数，弃权单独一行
// （docs §投票 1：逐人明细 → 票数统计 → 放逐结果）。
func NewVoteTallyView(r *i18n.Renderer, counts map[game.Seat]int, abstain int) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	seats := make([]game.Seat, 0, len(counts))
	for seat := range counts {
		seats = append(seats, seat)
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })

	lines := make([]string, 0, len(seats)+1)
	for _, seat := range seats {
		label, err := wolfSeatLabel(r, seat, "")
		if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("%s：%d 票", label, counts[seat]))
	}
	lines = append(lines, fmt.Sprintf("弃权：%d 票", abstain))
	return r.Render("vote.tally", map[string]any{
		"Lines": i18n.SafeMarkdown(strings.Join(lines, "\n")),
	})
}

// NewVoteResultView 渲染放逐结果：exiled 非 nil 显示被放逐座位；
// nil（平票）显示平票文案（完整平票流程属 Task 37）。
func NewVoteResultView(r *i18n.Renderer, exiled *game.Seat) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	if exiled == nil {
		return r.Render("vote.result.tie", nil)
	}
	label, err := wolfSeatLabel(r, *exiled, "")
	if err != nil {
		return "", err
	}
	return r.Render("vote.result", map[string]any{
		"Target": i18n.SafeMarkdown(label),
	})
}

// NewLastWordsPromptView 渲染遗言提示（docs §结算 4：默认「不报身份」
// 时被票死者有 30 秒遗言）。
func NewLastWordsPromptView(r *i18n.Renderer, seat game.Seat, deadline time.Time) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	label, err := wolfSeatLabel(r, seat, "")
	if err != nil {
		return "", err
	}
	return r.Render("last_words.prompt", map[string]any{
		"Seat":     i18n.SafeMarkdown(label),
		"Deadline": i18n.SafeMarkdown(formatDeadline(deadline)),
	})
}

// NewLastWordsView 渲染遗言转播（docs §死亡玩家 4：遗言会正常转播）；
// text 为用户正文，经 i18n 默认转义防 MarkdownV2 注入。
func NewLastWordsView(r *i18n.Renderer, seat game.Seat, text string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("telegram: nil renderer")
	}
	label, err := wolfSeatLabel(r, seat, "")
	if err != nil {
		return "", err
	}
	return r.Render("last_words.published", map[string]any{
		"Seat": i18n.SafeMarkdown(label),
		"Text": text,
	})
}
