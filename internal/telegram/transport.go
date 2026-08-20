package telegram

import (
	"context"
	"fmt"
)

// Outbox operation 常量：Transport 把 outbox.Message.Operation
// 映射为具体 Bot API 调用（本包定义，不修改 internal/outbox）。
const (
	// OpSendText 对应 sendMessage。
	OpSendText = "send_text"
	// OpEditMessage 对应 editMessageText。
	OpEditMessage = "edit_message"
	// OpDeleteMessage 对应 deleteMessage。
	OpDeleteMessage = "delete_message"
	// OpSendPhoto 对应 sendPhoto。
	OpSendPhoto = "send_photo"
	// OpSendRoleCard 对应身份卡发送（Item 2：sendPhoto 图片 + Caption，
	// 首次上传并缓存 file_id，后续复用；见 app.media 接线）。
	OpSendRoleCard = "send_role_card"
	// OpAnswerCallback 对应 answerCallbackQuery。
	OpAnswerCallback = "answer_callback_query"
)

// 富文本统一使用 Telegram MarkdownV2（docs/技术选型.md §7.2）。
const markdownV2 = "MarkdownV2"

// InlineButton 是 inline keyboard 单按钮：label 只显示座位号/私密标记与
// 操作名，callback_data 为不透明随机 token（docs/技术选型.md §7.3）。
type InlineButton struct {
	Text         string
	CallbackData string
}

// ReplyMarkup 是 sendMessage/editMessageText 的 inline keyboard 载荷
// （docs 阶段消息设计.md §5.2：目标按钮每行 3 个，不显示昵称/明文标记）。
type ReplyMarkup struct {
	Rows [][]InlineButton
}

// Params 是 operation 的执行参数（payload 由本包承载，不扩展 outbox.Message）。
type Params struct {
	ChatID          int64
	MessageID       int
	Text            string
	Caption         string
	FileID          string
	CallbackQueryID string
	ShowAlert       bool
	// ReplyMarkup 为 send_text/edit_message 的 inline keyboard（B1-c）。
	ReplyMarkup *ReplyMarkup
	// Period 标识主消息时间段（如 "night.1"；非空=主消息滚动编辑，接线层
	// productionSend 负责同一 (chat, period) 的 send→edit 消息 ID 复用与分页）。
	Period string
	// RoleCardRole / RoleCardSeat 供 OpSendRoleCard（Item 2：身份卡图片发送，
	// 角色名主干如 "werewolf"，座位号）。
	RoleCardRole string
	RoleCardSeat int
	// ParseMode 为空时文本类消息默认 MarkdownV2。
	ParseMode string
}

// Transport 把 Outbox operation 转成 Bot API 调用。
type Transport struct {
	client Client
}

// NewTransport 创建 operation 分派器。
func NewTransport(client Client) *Transport {
	return &Transport{client: client}
}

// Send 按 op 分派到对应的 Client 方法；未知 operation 返回明确错误。
//
// 组合语义：Transport 可作为 outbox.SendFunc 由上层在边界内使用
// （func(ctx, msg outbox.Message) error 需由上层把 op/payload 组装后调用本方法）。
func (t *Transport) Send(ctx context.Context, op string, p Params) error {
	parseMode := p.ParseMode
	if parseMode == "" {
		parseMode = markdownV2
	}
	switch op {
	case OpSendText:
		_, err := t.client.SendMessage(ctx, SendMessageParams{ChatID: p.ChatID, Text: p.Text, ParseMode: parseMode, ReplyMarkup: p.ReplyMarkup})
		return err
	case OpEditMessage:
		_, err := t.client.EditMessageText(ctx, EditMessageParams{ChatID: p.ChatID, MessageID: p.MessageID, Text: p.Text, ParseMode: parseMode, ReplyMarkup: p.ReplyMarkup})
		return err
	case OpDeleteMessage:
		return t.client.DeleteMessage(ctx, DeleteMessageParams{ChatID: p.ChatID, MessageID: p.MessageID})
	case OpSendPhoto:
		_, err := t.client.SendPhoto(ctx, SendPhotoParams{ChatID: p.ChatID, FileID: p.FileID, Caption: p.Caption, ParseMode: parseMode})
		return err
	case OpAnswerCallback:
		return t.client.AnswerCallbackQuery(ctx, AnswerCallbackParams{CallbackQueryID: p.CallbackQueryID, Text: p.Text, ShowAlert: p.ShowAlert})
	default:
		return fmt.Errorf("telegram: unknown outbox operation %q", op)
	}
}
