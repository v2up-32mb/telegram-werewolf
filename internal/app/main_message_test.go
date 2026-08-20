package app

// Item 1 红测：主消息滚动编辑/分页（docs 阶段消息设计.md §3/§4）——
// 同一时间段主消息首条 send、后续 edit 复用同一消息 ID；超过 3000 字符
// 软分页创建续页；productionSend 回填消息 ID。

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// fakeIDClient 是返回递增消息 ID 的假 Client（主消息 send→edit 链用）。
type fakeIDClient struct {
	sends    int
	edits    int
	editIDs  []int
	lastText string
}

func (c *fakeIDClient) GetMe(context.Context) (*telegram.Me, error) {
	return &telegram.Me{ID: 1, Username: "b"}, nil
}
func (c *fakeIDClient) SendMessage(_ context.Context, p telegram.SendMessageParams) (*telegram.SentMessage, error) {
	c.sends++
	c.lastText = p.Text
	return &telegram.SentMessage{MessageID: c.sends}, nil
}
func (c *fakeIDClient) EditMessageText(_ context.Context, p telegram.EditMessageParams) (*telegram.SentMessage, error) {
	c.edits++
	c.editIDs = append(c.editIDs, p.MessageID)
	c.lastText = p.Text
	return &telegram.SentMessage{MessageID: 100 + c.edits}, nil
}
func (c *fakeIDClient) DeleteMessage(context.Context, telegram.DeleteMessageParams) error { return nil }
func (c *fakeIDClient) SendPhoto(context.Context, telegram.SendPhotoParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{MessageID: 1}, nil
}
func (c *fakeIDClient) UploadPhoto(context.Context, telegram.UploadPhotoParams) (*telegram.SentMessage, error) {
	return &telegram.SentMessage{MessageID: 1}, nil
}
func (c *fakeIDClient) AnswerCallbackQuery(context.Context, telegram.AnswerCallbackParams) error {
	return nil
}
func (c *fakeIDClient) SetMyCommands(context.Context, []telegram.BotCommand) error {
	return nil
}

// TestProductionSendMainEditChain 验证 productionSend 对 Period 消息：
// 首条 send 回填消息 ID，后续 edit 复用该 ID。
func TestProductionSendMainEditChain(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	fc := &fakeIDClient{}
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

	m1 := outbox.Message{ChatID: 100, Operation: telegram.OpSendText,
		Payload: telegram.Params{ChatID: 100, Text: "🌙 第 1 夜 · 死讯", Period: "night.1"}}
	if err := w.productionSend(ctx, m1); err != nil {
		t.Fatalf("productionSend send: %v", err)
	}
	m2 := outbox.Message{ChatID: 100, Operation: telegram.OpEditMessage,
		Payload: telegram.Params{ChatID: 100, Text: "🌙 第 1 夜 · 死讯\n\n投票明细…", Period: "night.1"}}
	if err := w.productionSend(ctx, m2); err != nil {
		t.Fatalf("productionSend edit: %v", err)
	}
	if fc.sends != 1 || fc.edits != 1 {
		t.Fatalf("send=%d edit=%d, want 1/1", fc.sends, fc.edits)
	}
	if len(fc.editIDs) != 1 || fc.editIDs[0] != 1 {
		t.Fatalf("edit 复用的消息 ID = %v, want [1]（首条 send 返回 ID 1）", fc.editIDs)
	}
	if !strings.Contains(fc.lastText, "投票明细") {
		t.Fatalf("edit 正文 = %q, want 含后续进程（滚动编辑）", fc.lastText)
	}
}

// TestAppendMainCreatesThenEdits 验证 appendMain：首条 send，后续 edit 同 ID。
func TestAppendMainCreatesThenEdits(t *testing.T) {
	ctx := context.Background()
	w, rec, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(ctx) }()

	if err := w.appendMain("R", 100, "night.1", "死讯：2号 死亡"); err != nil {
		t.Fatalf("append#1: %v", err)
	}
	if err := w.appendMain("R", 100, "night.1", "投票明细：1号→2号"); err != nil {
		t.Fatalf("append#2: %v", err)
	}

	var ops []string
	var editTexts []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case m := <-rec.ch:
			p := m.Payload.(telegram.Params)
			if p.Period != "night.1" {
				continue
			}
			ops = append(ops, m.Operation)
			if m.Operation == telegram.OpEditMessage {
				editTexts = append(editTexts, p.Text)
			}
			if len(ops) == 2 {
				goto done
			}
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
done:
	if len(ops) != 2 || ops[0] != telegram.OpSendText || ops[1] != telegram.OpEditMessage {
		t.Fatalf("主消息操作序列 = %v, want [send_text edit_message]（首条 send、后续 edit）", ops)
	}
	if len(editTexts) != 1 || !strings.Contains(editTexts[0], "投票明细") {
		t.Fatalf("edit 正文 = %q, want 含两条进程（滚动编辑）", editTexts)
	}
}

// TestAppendMainPaginates 验证超过 3000 字符后创建续页（新 send）。
func TestAppendMainPaginates(t *testing.T) {
	ctx := context.Background()
	w, rec, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(ctx) }()

	big := strings.Repeat("内容", 300) // 600 字 → 远小于 3000
	// 用 2900+ 字符逼近阈值：先用一长条填到接近 3000。
	long := strings.Repeat("长", 2900)
	// 第一次：创建
	if err := w.appendMain("R", 200, "day.1", long); err != nil {
		t.Fatalf("append long: %v", err)
	}
	// 第二次：超过 3000（2900 + 200 > 3000）→ 标记满，仍 edit。
	if err := w.appendMain("R", 200, "day.1", big); err != nil {
		t.Fatalf("append big: %v", err)
	}
	// 第三次：已满 → 创建续页（新 send）。
	if err := w.appendMain("R", 200, "day.1", "续页内容"); err != nil {
		t.Fatalf("append page2: %v", err)
	}

	var ops []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(ops) < 3 {
		select {
		case m := <-rec.ch:
			p := m.Payload.(telegram.Params)
			if p.Period != "day.1" {
				continue
			}
			ops = append(ops, m.Operation)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	want := []string{telegram.OpSendText, telegram.OpEditMessage, telegram.OpSendText}
	if len(ops) != 3 || ops[0] != want[0] || ops[1] != want[1] || ops[2] != want[2] {
		t.Fatalf("分页操作序列 = %v, want %v（超阈值标记满→续页新 send）", ops, want)
	}
}
