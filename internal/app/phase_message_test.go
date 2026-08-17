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

// newWiringSched 构造已 Attach 的 Wiring + recording Scheduler。
func newWiringSched(t *testing.T, cap int) (*Wiring, *recordingSender, *outbox.Scheduler) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rec := newRecordingSender(cap)
	sched := outbox.NewScheduler(rec.Send, cap)
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return w, rec, sched
}

// TestSpeechSelfDeleteDelaysThenDeletes 是 I1 红测：发言原消息 3 秒自毁——
// DelayEffect 包裹 speech.self_delete，定时后产出 OpDeleteMessage（docs
// 游戏流程设计.md §发言限制 2）。
func TestSpeechSelfDeleteDelaysThenDeletes(t *testing.T) {
	w, rec, sched := newWiringSched(t, 8)
	defer func() { _ = sched.Close(context.Background()) }()

	inner := game.MessageEffect{
		Audience: game.AudienceActor, Key: "speech.self_delete",
		Params: map[string]any{"chat_id": int64(12345), "message_id": 99},
	}
	if err := w.fanOut("", game.State{}, []game.Effect{game.DelayEffect{After: 50 * time.Millisecond, Inner: inner}}); err != nil {
		t.Fatalf("fanOut delay: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var gotDelete bool
	for time.Now().Before(deadline) && !gotDelete {
		select {
		case m := <-rec.ch:
			if m.Operation == telegram.OpDeleteMessage {
				if p, ok := m.Payload.(telegram.Params); ok && p.ChatID == 12345 && p.MessageID == 99 {
					gotDelete = true
				}
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !gotDelete {
		t.Fatal("DelayEffect 后未产生 OpDeleteMessage（原消息 3 秒自毁）")
	}
}

// TestCoalescerMergesPanelUpdates 是 I1 红测：同一 Chat 同一 CoalesceKey 的
// 面板刷新只保留最新版本（docs 技术选型.md §7.1）。
func TestCoalescerMergesPanelUpdates(t *testing.T) {
	w, rec, sched := newWiringSched(t, 8)
	defer func() { _ = sched.Close(context.Background()) }()

	for i := 0; i < 5; i++ {
		if err := w.enqueue("corr", "ROOM", 100, telegram.OpSendText,
			telegram.Params{ChatID: 100, Text: "面板版本"}, outbox.PriorityNormal, "panel:ROOM"); err != nil {
			t.Fatalf("enqueue #%d: %v", i, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	panels := 0
	for time.Now().Before(deadline) && panels == 0 {
		select {
		case m := <-rec.ch:
			if m.CoalesceKey == "panel:ROOM" {
				panels++
			}
		case <-time.After(5 * time.Millisecond):
		}
	}
	// 稍等，确认没有重复面板送达。
	time.Sleep(150 * time.Millisecond)
	for {
		select {
		case m := <-rec.ch:
			if m.CoalesceKey == "panel:ROOM" {
				panels++
			}
		default:
			goto done
		}
	}
done:
	if panels != 1 {
		t.Fatalf("面板送达 = %d, want 1（同 key 合并只发最新一版）", panels)
	}
}
