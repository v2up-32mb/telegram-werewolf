package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// 自持错误分类：业务层通过 errors.Is/errors.As 识别 Telegram 错误，
// 不依赖 go-telegram/bot 的错误变量。
var (
	// ErrForbidden 表示 403（如用户屏蔽 Bot）。
	ErrForbidden = errors.New("telegram: forbidden")
	// ErrBadRequest 表示 400（如消息不可编辑）。
	ErrBadRequest = errors.New("telegram: bad request")
	// ErrConflict 表示 409（另一实例同时 getUpdates）。
	ErrConflict = errors.New("telegram: conflict")
)

// RateLimitError 表示 429，RetryAfter 为服务端建议重试延迟。
type RateLimitError struct {
	// RetryAfter 是服务端建议的等待时长。
	RetryAfter time.Duration
	// Err 是底层错误。
	Err error
}

// Error 实现 error 接口。
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("telegram: too many requests, retry after %s", e.RetryAfter)
}

// Unwrap 暴露底层错误链。
func (e *RateLimitError) Unwrap() error { return e.Err }

// Me 是 getMe 结果 DTO。
type Me struct {
	ID       int64
	Username string
}

// SentMessage 是发送/编辑成功后的消息 DTO。
type SentMessage struct {
	MessageID int
	// PhotoFileID 是上传图片后返回的新 file_id（仅 UploadPhoto 填充，
	// 供 media_cache 回写）。
	PhotoFileID string
}

// SendMessageParams 是 sendMessage 参数。
type SendMessageParams struct {
	ChatID    int64
	Text      string
	ParseMode string
	// ReplyMarkup 为 inline keyboard（B1-c：角色操作/确认按钮）。
	ReplyMarkup *ReplyMarkup
}

// EditMessageParams 是 editMessageText 参数。
type EditMessageParams struct {
	ChatID      int64
	MessageID   int
	Text        string
	ParseMode   string
	ReplyMarkup *ReplyMarkup
}

// DeleteMessageParams 是 deleteMessage 参数。
type DeleteMessageParams struct {
	ChatID    int64
	MessageID int
}

// SendPhotoParams 是 sendPhoto 参数（Caption 统一 MarkdownV2）。
type SendPhotoParams struct {
	ChatID    int64
	FileID    string
	Caption   string
	ParseMode string
}

// UploadPhotoParams 是「上传新图片并作为同一条 sendPhoto 发送」的参数。
type UploadPhotoParams struct {
	ChatID    int64
	Image     []byte
	MimeType  string
	Caption   string
	ParseMode string
}

// AnswerCallbackParams 是 answerCallbackQuery 参数（顶部通知 show_alert=false）。
type AnswerCallbackParams struct {
	CallbackQueryID string
	Text            string
	ShowAlert       bool
}

// BotCommand 是 setMyCommands 的单条命令描述（自持 DTO，不依赖框架类型）。
type BotCommand struct {
	Command     string
	Description string
}

// Client 是 Telegram Bot API 的领域边界接口。
//
// 业务层只依赖本接口与上述自持 DTO，不直接接触框架类型。
type Client interface {
	GetMe(ctx context.Context) (*Me, error)
	SendMessage(ctx context.Context, p SendMessageParams) (*SentMessage, error)
	EditMessageText(ctx context.Context, p EditMessageParams) (*SentMessage, error)
	DeleteMessage(ctx context.Context, p DeleteMessageParams) error
	SendPhoto(ctx context.Context, p SendPhotoParams) (*SentMessage, error)
	// UploadPhoto 上传新图片并作为同一条 sendPhoto 发送（身份卡首次发送，
	// 返回消息含新 file_id；Item 2 / docs 技术选型.md §10）。
	UploadPhoto(ctx context.Context, p UploadPhotoParams) (*SentMessage, error)
	AnswerCallbackQuery(ctx context.Context, p AnswerCallbackParams) error
	// SetMyCommands 注册斜杠命令菜单（用户输入 / 时自动提示）。
	SetMyCommands(ctx context.Context, commands []BotCommand) error
}

// clientOptions 是 NewClient 的自持选项。
type clientOptions struct {
	serverURL string
	skipGetMe bool
}

// ClientOption 配置 Client 构造。
type ClientOption func(*clientOptions)

// WithServerURL 重定向 Bot API 基址（测试用 Fake API）。
func WithServerURL(url string) ClientOption {
	return func(o *clientOptions) { o.serverURL = url }
}

// WithSkipGetMe 跳过构造时的 getMe 初始化检查。
func WithSkipGetMe() ClientOption {
	return func(o *clientOptions) { o.skipGetMe = true }
}

// Client 的 go-telegram/bot 封装实现。
type clientImpl struct {
	b *bot.Bot
}

// NewClient 创建 Client 封装。
func NewClient(token string, opts ...ClientOption) (Client, error) {
	var o clientOptions
	for _, opt := range opts {
		opt(&o)
	}
	botOpts := []bot.Option{}
	if o.serverURL != "" {
		botOpts = append(botOpts, bot.WithServerURL(o.serverURL))
	}
	if o.skipGetMe {
		botOpts = append(botOpts, bot.WithSkipGetMe())
	}
	b, err := bot.New(token, botOpts...)
	if err != nil {
		return nil, err
	}
	return &clientImpl{b: b}, nil
}

func (c *clientImpl) GetMe(ctx context.Context) (*Me, error) {
	u, err := c.b.GetMe(ctx)
	if err != nil {
		return nil, wrapTelegramError(err)
	}
	return &Me{ID: u.ID, Username: u.Username}, nil
}

func (c *clientImpl) SendMessage(ctx context.Context, p SendMessageParams) (*SentMessage, error) {
	botParams := &bot.SendMessageParams{
		ChatID: p.ChatID, Text: p.Text, ParseMode: models.ParseMode(p.ParseMode),
	}
	if p.ReplyMarkup != nil {
		botParams.ReplyMarkup = toInlineKeyboard(p.ReplyMarkup)
	}
	msg, err := c.b.SendMessage(ctx, botParams)
	if err != nil {
		return nil, wrapTelegramError(err)
	}
	return &SentMessage{MessageID: msg.ID}, nil
}

// toInlineKeyboard 把自持 ReplyMarkup 转为 go-telegram/bot 的
// InlineKeyboardMarkup（nil 时返回 nil，不携带 reply_markup）。
func toInlineKeyboard(rm *ReplyMarkup) *models.InlineKeyboardMarkup {
	if rm == nil {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(rm.Rows))
	for _, row := range rm.Rows {
		btns := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			btns = append(btns, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.CallbackData})
		}
		rows = append(rows, btns)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (c *clientImpl) EditMessageText(ctx context.Context, p EditMessageParams) (*SentMessage, error) {
	botParams := &bot.EditMessageTextParams{
		ChatID: p.ChatID, MessageID: p.MessageID, Text: p.Text, ParseMode: models.ParseMode(p.ParseMode),
	}
	if p.ReplyMarkup != nil {
		botParams.ReplyMarkup = toInlineKeyboard(p.ReplyMarkup)
	}
	msg, err := c.b.EditMessageText(ctx, botParams)
	if err != nil {
		return nil, wrapTelegramError(err)
	}
	return &SentMessage{MessageID: msg.ID}, nil
}

func (c *clientImpl) DeleteMessage(ctx context.Context, p DeleteMessageParams) error {
	_, err := c.b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: p.ChatID, MessageID: p.MessageID})
	if err != nil {
		return wrapTelegramError(err)
	}
	return nil
}

func (c *clientImpl) SendPhoto(ctx context.Context, p SendPhotoParams) (*SentMessage, error) {
	parseMode := p.ParseMode
	if parseMode == "" {
		// 身份卡图片 + Caption 统一 MarkdownV2（docs/阶段消息设计.md §6.1）。
		parseMode = markdownV2
	}
	msg, err := c.b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:    p.ChatID,
		Photo:     &models.InputFileString{Data: p.FileID},
		Caption:   p.Caption,
		ParseMode: models.ParseMode(parseMode),
	})
	if err != nil {
		return nil, wrapTelegramError(err)
	}
	return &SentMessage{MessageID: msg.ID}, nil
}

// UploadPhoto 上传新图片（multipart）并作为同一条 sendPhoto 发送，
// 返回的消息携带 photo 尺寸数组（新 file_id 供缓存回写，docs/技术选型.md
// §10 首次发送上传并缓存）。
func (c *clientImpl) UploadPhoto(ctx context.Context, p UploadPhotoParams) (*SentMessage, error) {
	parseMode := p.ParseMode
	if parseMode == "" {
		parseMode = markdownV2
	}
	msg, err := c.b.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID:    p.ChatID,
		Photo:     &models.InputFileUpload{Filename: "role-card.jpg", Data: bytes.NewReader(p.Image)},
		Caption:   p.Caption,
		ParseMode: models.ParseMode(parseMode),
	})
	if err != nil {
		return nil, wrapTelegramError(err)
	}
	// 提取最大尺寸 photo 的 file_id（缓存回写用）。
	out := &SentMessage{MessageID: msg.ID}
	if len(msg.Photo) > 0 {
		largest := msg.Photo[0]
		for _, ps := range msg.Photo[1:] {
			if ps.FileSize > largest.FileSize {
				largest = ps
			}
		}
		out.PhotoFileID = largest.FileID
	}
	return out, nil
}

func (c *clientImpl) AnswerCallbackQuery(ctx context.Context, p AnswerCallbackParams) error {
	_, err := c.b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: p.CallbackQueryID,
		Text:            p.Text,
		ShowAlert:       p.ShowAlert,
	})
	if err != nil {
		return wrapTelegramError(err)
	}
	return nil
}

// SetMyCommands 注册斜杠命令菜单（setMyCommands API），使用户在聊天
// 输入 / 时看到命令提示。启动时调用一次即可。
func (c *clientImpl) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	apiCmds := make([]models.BotCommand, 0, len(commands))
	for _, cmd := range commands {
		apiCmds = append(apiCmds, models.BotCommand{
			Command:     cmd.Command,
			Description: cmd.Description,
		})
	}
	_, err := c.b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: apiCmds})
	if err != nil {
		return wrapTelegramError(err)
	}
	return nil
}

// wrapTelegramError 把框架错误转成自持错误类型。
func wrapTelegramError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, bot.ErrorForbidden):
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	case errors.Is(err, bot.ErrorBadRequest):
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	case errors.Is(err, bot.ErrorConflict):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case bot.IsTooManyRequestsError(err):
		var tme *bot.TooManyRequestsError
		errors.As(err, &tme)
		retryAfter := time.Duration(0)
		if tme != nil {
			retryAfter = time.Duration(tme.RetryAfter) * time.Second
		}
		return &RateLimitError{RetryAfter: retryAfter, Err: err}
	default:
		return err
	}
}
