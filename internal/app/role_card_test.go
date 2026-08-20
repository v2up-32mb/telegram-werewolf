package app

// Item 2 红测：照片身份卡——首次发送上传并缓存 file_id，后续同一
// (Bot, 图片) 命中缓存用 file_id 直发（docs 技术选型.md §10）。

import (
	"bytes"
	"context"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// roleCardClient 记录上传/直发并返回固定 file_id 的假 Client。
type roleCardClient struct {
	uploads     int
	sends       int
	lastFileID  string
	lastCaption string
}

func (c *roleCardClient) GetMe(context.Context) (*telegram.Me, error) {
	return &telegram.Me{ID: 42, Username: "test_bot"}, nil
}
func (c *roleCardClient) SendMessage(context.Context, telegram.SendMessageParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{MessageID: 10}, nil
}
func (c *roleCardClient) EditMessageText(context.Context, telegram.EditMessageParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{MessageID: 11}, nil
}
func (c *roleCardClient) DeleteMessage(context.Context, telegram.DeleteMessageParams) error {
	return nil
}
func (c *roleCardClient) SendPhoto(_ context.Context, p telegram.SendPhotoParams) (*telegram.SentMessage, error) {
	c.sends++
	c.lastFileID = p.FileID
	c.lastCaption = p.Caption
	return &telegram.SentMessage{MessageID: 12}, nil
}
func (c *roleCardClient) UploadPhoto(_ context.Context, p telegram.UploadPhotoParams) (*telegram.SentMessage, error) {
	c.uploads++
	c.lastCaption = p.Caption
	return &telegram.SentMessage{MessageID: 13, PhotoFileID: "file-abc-upload"}, nil
}
func (c *roleCardClient) AnswerCallbackQuery(context.Context, telegram.AnswerCallbackParams) error {
	return nil
}
func (c *roleCardClient) SetMyCommands(context.Context, []telegram.BotCommand) error {
	return nil
}

// TestRoleCardUploadThenCacheHit 验证：首次 send_role_card 上传并缓存
// file_id，后续命中缓存直发（不重复上传）。
func TestRoleCardUploadThenCacheHit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	fc := &roleCardClient{}
	rec := newRecordingSender(8)
	sched := outbox.NewScheduler(rec.Send, 8)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}), WithWiringClient(fc))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	rc := func() outbox.Message {
		return outbox.Message{ChatID: 100, Operation: telegram.OpSendRoleCard,
			Payload: telegram.Params{ChatID: 100, RoleCardRole: "werewolf", RoleCardSeat: 1}}
	}
	// 首次：上传并发送，caption 由 RoleCardView 渲染（非空）。
	if err := w.productionSend(ctx, rc()); err != nil {
		t.Fatalf("first productionSend: %v", err)
	}
	if fc.uploads != 1 || fc.sends != 0 {
		t.Fatalf("首次 uploads=%d sends=%d, want 1/0（未命中：上传并同消息发送）", fc.uploads, fc.sends)
	}
	if fc.lastCaption == "" {
		t.Fatal("身份卡 Caption 为空（RoleCardView 应渲染角色文案）")
	}
	// media_cache 落库 1 行（Bot ID 42 + 图片内容哈希）。
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_cache`).Scan(&n); err != nil {
		t.Fatalf("media_cache 计数: %v", err)
	}
	if n != 1 {
		t.Fatalf("media_cache 行 = %d, want 1（file_id 已缓存）", n)
	}

	// 第二次（同 Bot/图片）：命中缓存 → file_id 直发，不再上传。
	if err := w.productionSend(ctx, rc()); err != nil {
		t.Fatalf("second productionSend: %v", err)
	}
	if fc.uploads != 1 || fc.sends != 1 {
		t.Fatalf("第二次 uploads=%d sends=%d, want 1/1（命中缓存：file_id 直发不重复上传）", fc.uploads, fc.sends)
	}
	if fc.lastFileID != "file-abc-upload" {
		t.Fatalf("直发 file_id = %q, want 首次上传返回的 file-abc-upload", fc.lastFileID)
	}
}
