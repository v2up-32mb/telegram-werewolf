package app

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// TestProductionBuildAnswersNewGameCommand 是 Task 46 首个冒烟缺陷的失败
// 回归测试（红测）：生产装配（不注入 CommandHandler / OutboxSender 之外的
// 玩法替身）下，玩家私聊发送 /newgame 后，Bot 必须至少产生一条真实出站
// 消息（send_text → ChatID 等于操作者）作为建房反馈。
//
// 修复前行为（红测留档 /tmp/task46-red-production-wiring.txt）：生产装配
// 无 Wiring，defaultCommandHandler 只记录 "P0 wiring pending" 并返回 nil，
// Outbox 底层发送仍为 stub → 无任何出站消息 → 本测试 FAIL。
// 修复后行为：WithWiring 装配真实命令面与服务适配器，/newgame 经
// CommandsHandler 分派、LobbyService 建房、效果管线渲染并把 send_text
// 投递给 Outbox → 记录发送器收到 ChatID=100 的消息 → PASS。
func TestProductionBuildAnswersNewGameCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logBuf bytes.Buffer
	db := openTestDB(t)
	src := newFakeSource()
	rec := newRecordingSender(16)

	wiring, err := NewWiring(ctx, testConfig(), mustLogger(t, &logBuf))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	app, err := Build(ctx, testConfig(),
		WithDB(db),
		WithMigrate(func(_ context.Context, d *sql.DB) error { return storage.Migrate(d) }),
		WithLogger(mustLogger(t, &logBuf)),
		WithSource(src),
		WithOutboxSender(rec.Send),
		WithWiring(wiring),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.Run(ctx)
	}()
	<-src.started

	src.updates <- telegram.Update{
		UpdateID:   1001,
		ReceivedAt: time.Now(),
		Message: &telegram.IncomingMessage{
			MessageID: 7,
			ChatID:    100,
			UserID:    100,
			Text:      "/newgame",
		},
	}

	select {
	case m := <-rec.ch:
		if m.ChatID != 100 {
			t.Errorf("出站消息 ChatID = %d, want 100（私聊 ChatID == UserID）", m.ChatID)
		}
		if m.Operation != telegram.OpSendText {
			t.Errorf("出站消息 Operation = %q, want %q", m.Operation, telegram.OpSendText)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("生产装配下 /newgame 未产生任何出站消息：命令消费仍为 P0 stub（接线缺失）")
	}

	cancel()
	<-done
}

// TestAppIgnoresNilSourceErrors 验证源层空错误（长轮询抖动路径）不会
// 被记为 ERROR 刷屏，且后续更新仍正常处理（Task 46 冒烟中观察到
// 周期空错误噪声后的回归测试）。
func TestAppIgnoresNilSourceErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logBuf bytes.Buffer
	db := openTestDB(t)
	src := newFakeSource()
	rec := newRecordingSender(8)

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
		t.Fatalf("Build: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()
	<-src.started

	src.errs <- nil // 空错误
	time.Sleep(100 * time.Millisecond)
	src.updates <- telegram.Update{
		UpdateID:   9001,
		ReceivedAt: time.Now(),
		Message: &telegram.IncomingMessage{
			MessageID: 1,
			ChatID:    100,
			UserID:    100,
			Text:      "/help",
		},
	}
	recvAny(t, rec, 3*time.Second)

	cancel()
	<-done
	if got := logBuf.String(); strings.Contains(got, "app: update source error") {
		t.Errorf("nil 源错误不应记为 ERROR，日志包含：%q", got)
	}
}
