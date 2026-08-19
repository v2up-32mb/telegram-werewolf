package storage_test

import (
	"context"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func TestApplyScorePenaltyIsAtomicAndIdempotent(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users (telegram_id, nickname, points) VALUES (7001, 'host', 12)`); err != nil {
		t.Fatal(err)
	}
	repo := storage.NewUserRepository(db)

	if err := repo.ApplyScorePenalty(ctx, "ROOM-B2", game.UserID(7001), 10); err != nil {
		t.Fatalf("first penalty: %v", err)
	}
	if got := pointsOf(t, db, 7001); got != 2 {
		t.Fatalf("points after first penalty = %d, want 2", got)
	}
	if err := repo.ApplyScorePenalty(ctx, "ROOM-B2", game.UserID(7001), 10); err != nil {
		t.Fatalf("retry penalty: %v", err)
	}
	if got := pointsOf(t, db, 7001); got != 2 {
		t.Fatalf("points after idempotent retry = %d, want 2", got)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM score_penalties WHERE room_code = 'ROOM-B2' AND user_id = 7001`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("ledger rows = %d, want 1", applied)
	}
}

func TestApplyScorePenaltyRollsBackLedgerWhenUpdateFails(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO users (telegram_id, nickname, points) VALUES (7002, 'host', 12)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_penalty BEFORE UPDATE OF points ON users
		WHEN NEW.telegram_id = 7002 BEGIN SELECT RAISE(ABORT, 'forced penalty failure'); END`); err != nil {
		t.Fatal(err)
	}
	repo := storage.NewUserRepository(db)
	if err := repo.ApplyScorePenalty(ctx, "ROOM-ROLLBACK", game.UserID(7002), 10); err == nil {
		t.Fatal("expected penalty error")
	}
	if got := pointsOf(t, db, 7002); got != 12 {
		t.Fatalf("points after rollback = %d, want 12", got)
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM score_penalties WHERE room_code = 'ROOM-ROLLBACK'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("rolled-back ledger rows = %d, want 0", applied)
	}
}
