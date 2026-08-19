package app

import (
	"bytes"
	"context"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func TestFanOutAppliesScorePenaltyThroughStorage(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (telegram_id, nickname, points) VALUES (7101, 'host', 10)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sched := outbox.NewScheduler(func(context.Context, outbox.Message) error { return nil }, 8)
	defer func() { _ = sched.Close(ctx) }()
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer w.stopCoalescerFlusher()

	fx := []game.Effect{game.ScorePenaltyEffect{User: 7101, Amount: 10}}
	if err := w.fanOut("ROOM-B2-APP", game.State{}, fx); err != nil {
		t.Fatalf("fanOut: %v", err)
	}
	if err := w.fanOut("ROOM-B2-APP", game.State{}, fx); err != nil {
		t.Fatalf("idempotent fanOut retry: %v", err)
	}
	var points int
	if err := db.QueryRow(`SELECT points FROM users WHERE telegram_id = 7101`).Scan(&points); err != nil {
		t.Fatal(err)
	}
	if points != 0 {
		t.Fatalf("points after fanOut = %d, want 0", points)
	}
}
