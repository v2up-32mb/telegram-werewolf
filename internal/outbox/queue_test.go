package outbox

import (
	"errors"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

func TestMessageFields(t *testing.T) {
	msg := Message{
		CorrelationID: "corr-1",
		RoomID:        game.RoomID("ABC123"),
		ChatID:        ChatID(42),
		Operation:     "send_text",
		Priority:      PriorityHigh,
		CoalesceKey:   "phase-update",
	}
	if msg.CorrelationID != "corr-1" {
		t.Errorf("CorrelationID = %q, want corr-1", msg.CorrelationID)
	}
	if msg.RoomID != game.RoomID("ABC123") {
		t.Errorf("RoomID = %q, want ABC123", msg.RoomID)
	}
	if msg.ChatID != ChatID(42) {
		t.Errorf("ChatID = %d, want 42", msg.ChatID)
	}
	if msg.Operation != "send_text" {
		t.Errorf("Operation = %q, want send_text", msg.Operation)
	}
	if msg.Priority != PriorityHigh {
		t.Errorf("Priority = %d, want PriorityHigh", msg.Priority)
	}
	if msg.CoalesceKey != "phase-update" {
		t.Errorf("CoalesceKey = %q, want phase-update", msg.CoalesceKey)
	}
}

func TestQueueFIFO(t *testing.T) {
	q := NewQueue(3)
	if got := q.Len(); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
	for i := 0; i < 3; i++ {
		msg := Message{CorrelationID: string(rune('a' + i)), ChatID: ChatID(1)}
		if err := q.Enqueue(msg); err != nil {
			t.Fatalf("Enqueue #%d: %v", i, err)
		}
	}
	if got := q.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	for i := 0; i < 3; i++ {
		got, ok := q.TryDequeue()
		if !ok {
			t.Fatalf("TryDequeue #%d: empty, want %q", i, string(rune('a'+i)))
		}
		if want := string(rune('a' + i)); got.CorrelationID != want {
			t.Fatalf("TryDequeue #%d = %q, want %q", i, got.CorrelationID, want)
		}
	}
	if _, ok := q.TryDequeue(); ok {
		t.Fatal("TryDequeue on empty queue returned ok=true")
	}
}

func TestQueueFullReturnsErrQueueFullAndCounts(t *testing.T) {
	q := NewQueue(1)
	if err := q.Enqueue(Message{CorrelationID: "m1", ChatID: ChatID(1)}); err != nil {
		t.Fatalf("Enqueue m1: %v", err)
	}
	err := q.Enqueue(Message{CorrelationID: "m2", ChatID: ChatID(1)})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Enqueue m2 err = %v, want ErrQueueFull (errors.Is)", err)
	}
	if got := q.FullCount(); got != 1 {
		t.Fatalf("FullCount = %d, want 1", got)
	}
	// 消息未被静默丢弃：队列中仍只有 m1。
	got, ok := q.TryDequeue()
	if !ok || got.CorrelationID != "m1" {
		t.Fatalf("TryDequeue = %+v, %v; want m1", got, ok)
	}
}
