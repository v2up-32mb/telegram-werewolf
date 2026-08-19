package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func TestDissolveRoomStopsBoundActor(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
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

	// 1. 创建真实房间（含持久化行），并手动绑定一个真实 Actor
	st := game.State{RoomID: "B3-ROOM", Phase: game.PhaseLobby, PhaseVersion: 1, Processed: map[string]bool{}}
	st.Lobby.Owner = 1
	st.Players = []game.Player{{UserID: 1, Seat: 1}}
	w.reg.create(st, 1, time.Now())
	lr, ok := w.reg.get(st.RoomID)
	if !ok {
		t.Fatal("room missing")
	}
	actor := w.newGameActor(lr.st)
	lr.actor = actor
	w.director.bind(st.RoomID, actor)

	// 2. 解散：必须同时停 Actor（原实现只清 storage/registry/director）
	w.dissolveRoom(st.RoomID, game.DissolveVoted)

	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := actor.Dispatch(ctx2, game.StartGameCommand{Meta: game.CommandMeta{Actor: 1}}); !errors.Is(err, room.ErrClosed) {
		t.Fatalf("Dispatch after room dissolve error = %v, want %v", err, room.ErrClosed)
	}
	// 3. 注册表里房间也应已被移除
	if _, ok := w.reg.get(st.RoomID); ok {
		t.Fatal("room still present in registry after dissolve")
	}
}

func TestWiringStopStopsAllBoundActors(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
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

	st := game.State{RoomID: "B3-STOP", Phase: game.PhaseLobby, PhaseVersion: 1, Processed: map[string]bool{}}
	st.Lobby.Owner = 1
	st.Players = []game.Player{{UserID: 1, Seat: 1}}
	w.reg.create(st, 1, time.Now())
	lr, ok := w.reg.get(st.RoomID)
	if !ok {
		t.Fatal("room missing")
	}
	actor := w.newGameActor(lr.st)
	lr.actor = actor
	w.director.bind(st.RoomID, actor)

	w.stopActors()

	ctx2, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := actor.Dispatch(ctx2, game.StartGameCommand{Meta: game.CommandMeta{Actor: 1}}); !errors.Is(err, room.ErrClosed) {
		t.Fatalf("Dispatch after Wiring stop error = %v, want %v", err, room.ErrClosed)
	}
}

// TestDissolveFromActorGoroutineDoesNotDeadlock 兜底验证：解散效果从 Actor
// 自身 goroutine（OnApplied → fanOut → DissolveEffect）触发时不得死锁。
func TestDissolveFromActorGoroutineDoesNotDeadlock(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
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

	st := game.State{RoomID: "B3-DL", Phase: game.PhaseLobby, PhaseVersion: 1, Processed: map[string]bool{}}
	st.Lobby.Owner = 1
	st.Players = []game.Player{{UserID: 1, Seat: 1}}
	w.reg.create(st, 1, time.Now())
	lr, ok := w.reg.get(st.RoomID)
	if !ok {
		t.Fatal("room missing")
	}
	actor := w.newGameActor(lr.st)
	lr.actor = actor
	w.director.bind(st.RoomID, actor)

	// 在 Actor goroutine 内直接触发解散效果（模拟 OnApplied 路径）
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.dissolveRoom(st.RoomID, game.DissolveVoted)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("dissolveRoom deadlocked when called from actor goroutine context")
	}
}
