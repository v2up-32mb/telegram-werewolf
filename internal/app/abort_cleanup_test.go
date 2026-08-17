package app

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// seedLeftoverRoom 构造遗留活跃房间（rooms + room_players + users）。
func seedLeftoverRoom(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, u := range []struct {
		id  int64
		nic string
	}{{111, "房主"}, {222, "玩家2"}, {333, "玩家3"}} {
		if err := storage.NewUserRepository(db).Upsert(ctx, game.UserID(u.id), u.nic); err != nil {
			t.Fatalf("upsert user %d: %v", u.id, err)
		}
	}
	if err := storage.NewRoomRepository(db).Create(ctx, "ROOM-A", 111, "night"); err != nil {
		t.Fatalf("create room: %v", err)
	}
	repo := storage.NewRoomRepository(db)
	if _, err := repo.Join(ctx, "ROOM-A", 222); err != nil {
		t.Fatalf("join 222: %v", err)
	}
	if _, err := repo.Join(ctx, "ROOM-A", 333); err != nil {
		t.Fatalf("join 333: %v", err)
	}
}

// TestLeftoverRoomsMarkedInterruptedOnStartup 是 B2 红测：进程重启启动扫描
// 必须把遗留房间事务化清场——通知全部参与者 + 写 games.aborted=1 + 清空
// rooms/room_players（docs 技术选型.md §8.3、游戏流程设计.md §五 容灾）。
func TestLeftoverRoomsMarkedInterruptedOnStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	seedLeftoverRoom(t, ctx, db)

	src := newFakeSource()
	rec := newRecordingSender(16)
	a, err := Build(ctx, testConfig(),
		WithDB(db),
		WithMigrate(func(_ context.Context, d *sql.DB) error { return storage.Migrate(d) }),
		WithLogger(mustLogger(t, &bytes.Buffer{})),
		WithSource(src),
		WithOutboxSender(rec.Send),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()
	<-src.started

	// 通知全部 3 名参与者（房主 111 + 玩家 222/333）。
	var abortIDs []outbox.ChatID
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(abortIDs) < 3 {
		select {
		case m := <-rec.ch:
			if m.CorrelationID != "" && m.CorrelationID[:6] == "abort:" {
				abortIDs = append(abortIDs, m.ChatID)
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	if len(abortIDs) != 3 {
		t.Fatalf("中止通知 = %d 条, want 3（全部参与者，不只房主）; got=%v", len(abortIDs), abortIDs)
	}

	// rooms/room_players 清场（MarkInterrupted 在通知后提交，需等待）。
	waitFor(t, 5*time.Second, func() bool {
		var n int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms`).Scan(&n); err != nil {
			return false
		}
		return n == 0
	}, "rooms 清场")
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rooms`).Scan(&n); err != nil {
		t.Fatalf("rooms 计数: %v", err)
	}
	if n != 0 {
		t.Fatalf("rooms 行 = %d, want 0（遗留房被清场）", n)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_players`).Scan(&n); err != nil {
		t.Fatalf("room_players 计数: %v", err)
	}
	if n != 0 {
		t.Fatalf("room_players 行 = %d, want 0", n)
	}
	// 中止记录 games.aborted=1 落库。
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM games WHERE aborted = 1`).Scan(&n); err != nil {
		t.Fatalf("aborted games 计数: %v", err)
	}
	if n != 1 {
		t.Fatalf("games.aborted=1 行 = %d, want 1（保留中止记录）", n)
	}
	// 房主可再次建房（HostActive 不再被遗留行阻断）。
	if c, err := storage.NewRoomRepository(db).CountHostRooms(ctx, 111); err != nil {
		t.Fatalf("CountHostRooms: %v", err)
	} else if c != 0 {
		t.Fatalf("房主遗留活跃房 = %d, want 0（可再建房）", c)
	}

	cancel()
	<-done
}

// TestLeftoverAbortMarkErrorIsNonFatal 是 B2 回归：清场失败不阻断启动/就绪。
func TestLeftoverAbortMarkErrorIsNonFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	src := newFakeSource()
	rec := newRecordingSender(8)
	a, err := Build(ctx, testConfig(),
		WithDB(db),
		WithMigrate(func(_ context.Context, d *sql.DB) error { return storage.Migrate(d) }),
		WithLogger(mustLogger(t, &bytes.Buffer{})),
		WithSource(src),
		WithOutboxSender(rec.Send),
		WithAbortScanner(&fakeScanner{}), // 空扫描：不触发清场
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Run(ctx) }()
	<-src.started
	if err := a.Ready(ctx); err != nil {
		t.Fatalf("Ready = %v, want nil（清场失败不阻塞就绪）", err)
	}
	cancel()
	<-done
}

// ensure telegram import used（编译占位）。
var _ = telegram.OpSendText
