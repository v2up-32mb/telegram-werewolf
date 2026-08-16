package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// UpdateSource 是 Telegram 更新输入源接口。
//
// MVP 仅实现 Long Polling；Webhook 保持未实现（docs/技术选型.md §接入边界）。
// 业务层依赖本接口与 Update DTO，不依赖框架类型。
type UpdateSource interface {
	// Start 启动源；ctx 取消后停止并关闭 Updates/Errors 通道。
	Start(ctx context.Context)
	// Updates 是保序的领域 Update 流（update_id 顺序）。
	Updates() <-chan Update
	// Errors 是源层错误（如 409 双实例冲突）。
	Errors() <-chan error
}

// sourceOptions 是 LongPollingSource 的自持选项。
type sourceOptions struct {
	serverURL string
}

// SourceOption 配置 UpdateSource 构造。
type SourceOption func(*sourceOptions)

// WithSourceServerURL 重定向 Bot API 基址（测试用 Fake API）。
func WithSourceServerURL(url string) SourceOption {
	return func(o *sourceOptions) { o.serverURL = url }
}

// LongPollingSource 是 go-telegram/bot 的 Long Polling 适配。
//
// 使用 WithNotAsyncHandlers()：单一顺序化 handler goroutine 处理
// 全部 Update，保持 update_id 接收顺序；房间间并发由上层 Room Actors
// 负责，而不是框架 handler workers（docs/技术选型.md §Update 接收）。
type LongPollingSource struct {
	b       *bot.Bot
	updates chan Update
	errors  chan error
	once    sync.Once
}

// NewLongPollingSource 创建 Long Polling 源。
//
// initialOffset 传给 getUpdates 的初始偏移；token 为空或 getMe 失败
// （未用 WithSkipGetMe 时）返回错误。
func NewLongPollingSource(token string, initialOffset int64, opts ...SourceOption) (*LongPollingSource, error) {
	var o sourceOptions
	for _, opt := range opts {
		opt(&o)
	}
	botOpts := []bot.Option{
		bot.WithNotAsyncHandlers(),
		bot.WithInitialOffset(initialOffset),
	}
	if o.serverURL != "" {
		botOpts = append(botOpts, bot.WithServerURL(o.serverURL))
	}
	s := &LongPollingSource{
		updates: make(chan Update, 64),
		errors:  make(chan error, 16),
	}
	// 源层错误（如 409 双实例冲突）经 WithErrorsHandler 进入 Errors 流。
	botOpts = append(botOpts, bot.WithErrorsHandler(func(err error) {
		select {
		case s.errors <- wrapTelegramError(err):
		default:
		}
	}))
	b, err := bot.New(token, botOpts...)
	if err != nil {
		return nil, err
	}
	s.b = b
	b.RegisterHandlerMatchFunc(func(*models.Update) bool { return true }, s.handle)
	return s, nil
}

// Start 启动后台 Long Polling 循环；ctx 取消后停止并关闭输出通道。
func (s *LongPollingSource) Start(ctx context.Context) {
	s.once.Do(func() {
		go func() {
			defer close(s.updates)
			defer close(s.errors)
			s.b.Start(ctx)
		}()
	})
}

// Updates 返回保序的领域 Update 流。
func (s *LongPollingSource) Updates() <-chan Update { return s.updates }

// Errors 返回源层错误流。
func (s *LongPollingSource) Errors() <-chan error { return s.errors }

// handle 是框架 handler：解码 Update 后立即记录 ReceivedAt，再转 DTO 投递。
func (s *LongPollingSource) handle(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil {
		return
	}
	u := Update{
		UpdateID:   update.ID,
		ReceivedAt: time.Now(),
	}
	if update.Message != nil {
		m := update.Message
		u.Message = &IncomingMessage{
			MessageID: m.ID,
			ChatID:    m.Chat.ID,
			UserID:    fromUserID(m.From),
			Text:      m.Text,
		}
	}
	if update.CallbackQuery != nil {
		cq := update.CallbackQuery
		u.CallbackQuery = &IncomingCallbackQuery{
			ID:     cq.ID,
			UserID: cq.From.ID,
			Data:   cq.Data,
		}
		// CallbackQuery.Message 是 MaybeInaccessibleMessage（值类型），
		// 仅当内部 *Message 非 nil 时才有可访问的 Chat/MessageID。
		if cq.Message.Message != nil {
			u.CallbackQuery.ChatID = cq.Message.Message.Chat.ID
			u.CallbackQuery.MessageID = cq.Message.Message.ID
		}
	}
	select {
	case s.updates <- u:
	case <-ctx.Done():
	}
}

// fromUserID 提取消息发送者 ID；nil From 时为 0。
func fromUserID(u *models.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}
