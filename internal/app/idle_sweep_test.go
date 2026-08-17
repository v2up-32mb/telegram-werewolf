package app

// I7 红测：闲置回收接线——大厅房间超时（创建起 1 小时，前 10 分钟提醒一次，
// 续期 1 小时）经 SweepIdle 落地：到期发通知并解散房间（docs 游戏流程设计.md
// §闲置回收；游戏开始后不受影响）。

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestIdleSweepExpiresAndDissolves 验证：房间到期后 SweepIdle 发通知并解散。
func TestIdleSweepExpiresAndDissolves(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := base
	rec := newRecordingSender(16)
	sched := outbox.NewScheduler(rec.Send, 16)
	defer func() { _ = sched.Close(ctx) }()

	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	w.now = func() time.Time { return now }
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// 建房（现状 at base → ExpireAt = base + 1h）。
	if err := w.TextHandler().HandleText(ctx, cooldownUpdate(1, 9001, "/newgame IDLE01")); err != nil {
		t.Fatalf("newgame: %v", err)
	}
	if _, ok := w.reg.get("IDLE01"); !ok {
		t.Fatal("建房失败")
	}

	// 到期前 10 分钟：提醒一次（此时也先推进到提醒窗口）。
	now = base.Add(50 * time.Minute)
	w.SweepIdle()
	recvContaining(t, rec, 3*time.Second, "即将闲置过期")

	// 到期（base + 70 分钟）：发送过期通知并解散房间。
	now = base.Add(70 * time.Minute)
	w.SweepIdle()
	recvContaining(t, rec, 3*time.Second, "闲置过期")

	if _, ok := w.reg.get("IDLE01"); ok {
		t.Fatal("到期后房间未解散（仍留在 liveRegistry）")
	}
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms`).Scan(&n); err != nil {
		t.Fatalf("rooms 计数: %v", err)
	}
	if n != 0 {
		t.Fatalf("到期后 rooms 行 = %d, want 0（storage 同步清理）", n)
	}
}

// TestIdleSweepSkipsStartedGame 验证：游戏开始后房间不受闲置回收影响。
func TestIdleSweepSkipsStartedGame(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	now := base
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 8)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	w.now = func() time.Time { return now }
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := w.TextHandler().HandleText(ctx, cooldownUpdate(1, 9001, "/newgame IDLE02")); err != nil {
		t.Fatalf("newgame: %v", err)
	}
	// 模拟开局：手动把房间置为非 lobby（导演/actor 未创建时直接改状态）。
	if lr, ok := w.reg.get("IDLE02"); ok {
		lr.st.Phase = 3 // PhaseNight
	}
	now = base.Add(2 * time.Hour)
	w.SweepIdle()
	if _, ok := w.reg.get("IDLE02"); !ok {
		t.Fatal("游戏开始后的房间不应被闲置回收")
	}
}
