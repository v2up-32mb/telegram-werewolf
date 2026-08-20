package app

// 生产接线回归测试（Task 46 缺陷修复配套）：
// 通过 Build + WithWiring 装配真实命令面/服务适配器/Outbox 链，用
// fakeSource 注入文本 Update，断言真实出站消息（recordingSender）与
// SQLite 最终状态。与 testharness 的区别：这里是生产 Build 路径而非
// 测试内参考接线。

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// wiredHarness 组装生产 Build + 注入源/记录发送器。
type wiredHarness struct {
	app *App
	src *fakeSource
	rec *recordingSender
	ctx context.Context
}

func newWiredHarness(t *testing.T, recv int) *wiredHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var logBuf bytes.Buffer
	db := openTestDB(t)
	src := newFakeSource()
	rec := newRecordingSender(recv)

	wiring, err := NewWiring(ctx, testConfig(), mustLogger(t, &logBuf))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	a, err := Build(ctx, testConfig(),
		WithDB(db),
		WithMigrate(func(_ context.Context, d *sql.DB) error { return storage.Migrate(d) }),
		WithLogger(mustLogger(t, &logBuf)),
		WithSource(src),
		WithOutboxSender(rec.Send),
		WithWiring(wiring),
	)
	if err != nil {
		cancel()
		t.Fatalf("Build: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	<-src.started
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &wiredHarness{app: a, src: src, rec: rec, ctx: ctx}
}

func (h *wiredHarness) text(user int64, text string, updateID int64) {
	h.src.updates <- telegram.Update{
		UpdateID:   updateID,
		ReceivedAt: time.Now(),
		Message: &telegram.IncomingMessage{
			MessageID: int(updateID),
			ChatID:    user,
			UserID:    user,
			Text:      text,
		},
	}
}

func (h *wiredHarness) db() *sql.DB {
	if h.app == nil || h.app.db == nil {
		panic("app db 未装配")
	}
	return h.app.db
}

func recvAny(t *testing.T, rec *recordingSender, within time.Duration) outbox.Message {
	t.Helper()
	select {
	case m := <-rec.ch:
		return m
	case <-time.After(within):
		t.Fatalf("timeout waiting for outbox message")
		return outbox.Message{}
	}
}

func recvUntil(t *testing.T, rec *recordingSender, n int, within time.Duration) []outbox.Message {
	t.Helper()
	var out []outbox.Message
	deadline := time.Now().Add(within)
	for len(out) < n {
		select {
		case m := <-rec.ch:
			out = append(out, m)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timeout: want %d messages, got %d", n, len(out))
		}
	}
	return out
}

func textOf(m outbox.Message) string {
	p, ok := m.Payload.(telegram.Params)
	if !ok {
		return ""
	}
	return p.Text
}

func TestWiringStartShowsMenu(t *testing.T) {
	h := newWiredHarness(t, 8)
	h.text(100, "/start", 1001)
	m := recvAny(t, h.rec, 3*time.Second)
	if m.Operation != telegram.OpSendText {
		t.Fatalf("Operation = %q, want send_text", m.Operation)
	}
	if m.ChatID != 100 {
		t.Errorf("ChatID = %d, want 100", m.ChatID)
	}
	if got := textOf(m); !strings.Contains(got, "创建房间") {
		t.Errorf("主菜单应含「创建房间」，got %q", got)
	}
}

func TestWiringNewGamePersistsRoomAndSendsPanel(t *testing.T) {
	h := newWiredHarness(t, 16)
	h.text(100, "/newgame ABC123", 1001)
	msgs := recvUntil(t, h.rec, 1, 3*time.Second)
	var panelText string
	for _, m := range msgs {
		if m.ChatID != 100 {
			t.Errorf("消息投递 ChatID = %d, want 100", m.ChatID)
		}
		txt := textOf(m)
		if strings.Contains(txt, "房间面板") {
			panelText = txt
		}
	}
	if !strings.Contains(panelText, "房间码：ABC123") || !strings.Contains(panelText, "人数：1/6") || !strings.Contains(panelText, "（房主）") {
		t.Errorf("面板元素缺失，got %q", panelText)
	}

	repo := storage.NewRoomRepository(h.db())
	exists, err := repo.RoomExists(h.ctx, "ABC123")
	if err != nil || !exists {
		t.Fatalf("房间未持久化 exists=%v err=%v", exists, err)
	}
	hostIn, err := repo.PlayerInRoom(h.ctx, "ABC123", 100)
	if err != nil || !hostIn {
		t.Fatalf("房主未入房 hostIn=%v err=%v", hostIn, err)
	}
}

func TestWiringJoinUpdatesPanelAndConfirmsJoiner(t *testing.T) {
	h := newWiredHarness(t, 32)
	h.text(100, "/newgame ABC234", 1001)
	recvUntil(t, h.rec, 1, 3*time.Second)

	h.text(200, "/join ABC234", 1002)
	// 加入后出站：join.confirmed(加入者)、panel(房主)。命令面不再发
	// join_done（#27 同语义双发，Task 46 冒烟缺陷修复）。
	msgs := recvUntil(t, h.rec, 2, 3*time.Second)
	var joined, panel string
	for _, m := range msgs {
		txt := textOf(m)
		switch {
		case m.ChatID == 200:
			joined = txt
		case m.ChatID == 100 && strings.Contains(txt, "房间面板"):
			panel = txt
		}
	}
	if !strings.Contains(joined, "已加入房间") {
		t.Errorf("加入确认缺失，got %q", joined)
	}
	if !strings.Contains(panel, "人数：2/6") || !strings.Contains(panel, "2号") {
		t.Errorf("面板应含新成员，got %q", panel)
	}
}

func TestWiringScoreAndHelp(t *testing.T) {
	h := newWiredHarness(t, 16)
	if _, err := h.db().ExecContext(h.ctx, `INSERT INTO users(telegram_id, nickname, points) VALUES(100, '测试玩家', 5)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h.text(100, "/score", 1001)
	m := recvAny(t, h.rec, 3*time.Second)
	if !strings.Contains(textOf(m), "5") {
		t.Errorf("/score 应回积分，got %q", textOf(m))
	}
	h.text(100, "/help", 1002)
	m = recvAny(t, h.rec, 3*time.Second)
	if !strings.Contains(textOf(m), "/newgame") {
		t.Errorf("/help 应含命令清单，got %q", textOf(m))
	}
}

func TestWiringLeaveDissolvesEmptyRoom(t *testing.T) {
	h := newWiredHarness(t, 16)
	h.text(100, "/newgame ABC345", 1001)
	recvUntil(t, h.rec, 1, 3*time.Second)

	h.text(100, "/leave", 1002)
	msgs := recvUntil(t, h.rec, 1, 3*time.Second)
	if !strings.Contains(textOf(msgs[0]), "已退出房间") {
		t.Errorf("/leave 应回退出确认，got %q", textOf(msgs[0]))
	}
	repo := storage.NewRoomRepository(h.db())
	exists, err := repo.RoomExists(h.ctx, "ABC345")
	if err != nil {
		t.Fatalf("room exists err: %v", err)
	}
	if exists {
		t.Errorf("房主退出后空房应被解散并从 rooms 删除，房间仍存在")
	}
}
