package app

// 缺陷回归（红测）：productionSend 必须把 Telegram 侧错误分类为
// outbox 的永久/限速错误，否则 400 会被当临时错误重试约 9 秒后
// 静默丢弃（Task 46 冒烟：newgame 创建确认缺失）、429 的 RetryAfter
// 也会丢失。

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

type classifyClient struct {
	sendErr error
}

func (c *classifyClient) GetMe(context.Context) (*telegram.Me, error) { return nil, nil }
func (c *classifyClient) SendMessage(ctx context.Context, p telegram.SendMessageParams) (*telegram.SentMessage, error) {
	return nil, c.sendErr
}
func (c *classifyClient) EditMessageText(context.Context, telegram.EditMessageParams) (*telegram.SentMessage, error) {
	return nil, nil
}
func (c *classifyClient) DeleteMessage(context.Context, telegram.DeleteMessageParams) error {
	return nil
}
func (c *classifyClient) SendPhoto(context.Context, telegram.SendPhotoParams) (*telegram.SentMessage, error) {
	return nil, nil
}
func (c *classifyClient) AnswerCallbackQuery(context.Context, telegram.AnswerCallbackParams) error {
	return nil
}

func newClassifyWiring(t *testing.T, cl telegram.Client) *Wiring {
	t.Helper()
	w := &Wiring{log: mustLogger(t, &bytes.Buffer{}), opts: wiringOptions{client: cl}}
	return w
}

func TestProductionSendClassifiesBadRequestAsPermanent(t *testing.T) {
	w := newClassifyWiring(t, &classifyClient{sendErr: telegram.ErrBadRequest})
	err := w.productionSend(context.Background(), outbox.Message{
		ChatID: 1, Operation: telegram.OpSendText, Payload: telegram.Params{ChatID: 1, Text: "x"},
	})
	var pe *outbox.PermanentError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *outbox.PermanentError（400 不可重试）", err)
	}
}

func TestProductionSendClassifiesRateLimit(t *testing.T) {
	w := newClassifyWiring(t, &classifyClient{sendErr: &telegram.RateLimitError{RetryAfter: 7 * time.Second, Err: errors.New("429")}})
	err := w.productionSend(context.Background(), outbox.Message{
		ChatID: 1, Operation: telegram.OpSendText, Payload: telegram.Params{ChatID: 1, Text: "x"},
	})
	var rle *outbox.RateLimitedError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *outbox.RateLimitedError（429 保留 RetryAfter）", err)
	}
	if rle.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", rle.RetryAfter)
	}
}
