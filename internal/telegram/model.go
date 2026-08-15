package telegram

import "time"

// Update 是项目输入 DTO：把 Telegram 框架 Update 解码后立即转换为
// 领域可消费的输入，业务层不依赖 go-telegram/bot 类型
// （docs/技术选型.md §接入边界：业务模块接收统一的领域输入）。
type Update struct {
	// UpdateID 是 Telegram update_id，用于保序与后续去重（Task 20）。
	UpdateID int64
	// ReceivedAt 是服务端 Update 解码后立即记录的本机接收时间。
	ReceivedAt time.Time
	// Message 非 nil 表示消息类更新。
	Message *IncomingMessage
	// CallbackQuery 非 nil 表示回调查询类更新。
	CallbackQuery *IncomingCallbackQuery
}

// IncomingMessage 是消息类更新的领域 DTO。
type IncomingMessage struct {
	// MessageID 是 Telegram 消息 ID。
	MessageID int
	// ChatID 是来源聊天的 Telegram Chat ID。
	ChatID int64
	// UserID 是发送者 Telegram User ID。
	UserID int64
	// Text 是消息文本（命令或发言原文）。
	Text string
}

// IncomingCallbackQuery 是回调查询的领域 DTO。
type IncomingCallbackQuery struct {
	// ID 是回调唯一 ID，应答时必须回传。
	ID string
	// UserID 是点击按钮的用户。
	UserID int64
	// ChatID 是按钮所在消息的聊天。
	ChatID int64
	// MessageID 是按钮所在消息的 ID。
	MessageID int
	// Data 是按钮回调数据（不透明 Token 语义由 Task 20 承载）。
	Data string
}
