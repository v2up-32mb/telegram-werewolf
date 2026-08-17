package app

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// cooldownUpdate 构造一条文本消息 update。
func cooldownUpdate(id int64, user int64, text string) telegram.Update {
	return telegram.Update{
		UpdateID: id, ReceivedAt: time.Now(),
		Message: &telegram.IncomingMessage{MessageID: int(id), ChatID: user, UserID: user, Text: text},
	}
}

// recvContaining 轮询接收器直到出现包含子串的消息（或超时）。
func recvContaining(t *testing.T, rec *recordingSender, within time.Duration, contains string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		select {
		case m := <-rec.ch:
			if p, ok := m.Payload.(telegram.Params); ok && bytes.Contains([]byte(p.Text), []byte(contains)) {
				return
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("未收到包含 %q 的消息", contains)
}

// TestCooldownBlocksCreateAndJoin 是 I2 红测：冷却期间不能创建/加入房间，
// 到期后放行（docs 游戏流程设计.md §退出约束）。
func TestCooldownBlocksCreateAndJoin(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rec := newRecordingSender(32)
	sched := outbox.NewScheduler(rec.Send, 16)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	text := func(id int64, user int64, content string) {
		if err := w.TextHandler().HandleText(ctx, cooldownUpdate(id, user, content)); err != nil {
			t.Fatalf("text(%s): %v", content, err)
		}
	}

	// 正常建房/加入。
	text(1, 7001, "/newgame GAME77")
	text(2, 7002, "/join GAME77")

	// 7003 进入 10 分钟冷却（先登记用户行）。
	if err := w.users.Upsert(ctx, 7003, "玩家"); err != nil {
		t.Fatalf("upsert 7003: %v", err)
	}
	if err := w.users.SetCooldown(ctx, 7003, w.now().Add(10*time.Minute)); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}
	// 冷却中建房被拒（cooldown 文案回执）。
	text(3, 7003, "/newgame GAME78")
	recvContaining(t, rec, 3*time.Second, "冷却")
	// 冷却中加入被拒。
	text(4, 7003, "/join GAME77")
	recvContaining(t, rec, 3*time.Second, "冷却")

	// 冷却到期后可建。
	if err := w.users.SetCooldown(ctx, 7003, w.now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetCooldown(expire): %v", err)
	}
	text(5, 7003, "/newgame GAME79")
	if _, ok := w.reg.get("GAME79"); !ok {
		t.Fatal("冷却到期后 /newgame GAME79 未创建成功")
	}
}

// TestCooldownPersistedViaEffect 是 I2 红测：CooldownEffect 经导演 fanOut
// 持久化 users.cooldown_until。
func TestCooldownPersistedViaEffect(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := storage.NewUserRepository(db).Upsert(ctx, 8001, "玩家"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 8)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	now := w.now()
	if err := w.fanOut("", game.State{}, []game.Effect{
		game.CooldownEffect{User: 8001, Duration: 10 * time.Minute, Reason: game.LeaveReasonMaliciousActive},
	}); err != nil {
		t.Fatalf("fanOut cooldown: %v", err)
	}
	until, err := storage.NewUserRepository(db).CooldownUntil(ctx, 8001)
	if err != nil {
		t.Fatalf("CooldownUntil: %v", err)
	}
	if !until.After(now.Add(9*time.Minute)) || until.After(now.Add(11*time.Minute)) {
		t.Fatalf("cooldown_until = %v, want ≈ now+10min（现在 %v）", until, now)
	}
}