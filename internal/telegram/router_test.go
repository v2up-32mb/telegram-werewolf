package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// memCursorStore 是测试用内存 CursorStore。
type memCursorStore struct {
	mu   sync.Mutex
	ids  []int64
	load int64
}

func (s *memCursorStore) Load(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load, nil
}

func (s *memCursorStore) Save(ctx context.Context, updateID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load = updateID
	s.ids = append(s.ids, updateID)
	return nil
}

func (s *memCursorStore) saved() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.ids))
	copy(out, s.ids)
	return out
}

func newTestRouter(capacity int) (*Router, *memCursorStore, *CallbackManager) {
	store := &memCursorStore{}
	tokens := NewCallbackManager(64)
	r := NewRouter(NewDeduper(capacity), store, tokens)
	return r, store, tokens
}

func msgUpdate(id int64, text string) Update {
	return Update{UpdateID: id, ReceivedAt: time.Unix(1700000000, 0), Message: &IncomingMessage{ChatID: 1001, UserID: 2001, Text: text}}
}

func cbUpdate(id int64, userID int64, data string) Update {
	return Update{UpdateID: id, ReceivedAt: time.Unix(1700000000, 0), CallbackQuery: &IncomingCallbackQuery{ID: "cq", UserID: userID, ChatID: 1001, MessageID: 5, Data: data}}
}

func TestRouterTextCommands(t *testing.T) {
	r, _, tokens := newTestRouter(16)
	_ = tokens

	for _, tc := range []struct {
		text string
		want *game.ConfirmRoleCommand // 哨兵类型断言用
		kind string
	}{
		{"/start", nil, "CreateRoom"},
		{"/newgame", nil, "CreateRoom"},
		{"/join", nil, "JoinRoom"},
	} {
		u := msgUpdate(101, tc.text)
		cmd, ok := r.routeOne(u)
		if !ok {
			t.Fatalf("%s 未产生命令", tc.text)
		}
		switch tc.kind {
		case "CreateRoom":
			if _, ok := cmd.(game.CreateRoomCommand); !ok {
				t.Fatalf("%s 命令类型 = %T, want CreateRoomCommand", tc.text, cmd)
			}
		case "JoinRoom":
			if _, ok := cmd.(game.JoinRoomCommand); !ok {
				t.Fatalf("%s 命令类型 = %T, want JoinRoomCommand", tc.text, cmd)
			}
		}
		meta := commandMetaOf(cmd)
		if meta.Actor != 2001 || meta.ReceivedAt != u.ReceivedAt {
			t.Fatalf("%s Meta = %+v, want Actor 2001 ReceivedAt 非零", tc.text, meta)
		}
		if meta.ID == "" {
			t.Fatalf("%s Meta.ID 为空", tc.text)
		}
	}

	if cmd, ok := r.routeOne(msgUpdate(102, "gibberish")); ok {
		t.Fatalf("不可识别文本产生了命令 %T", cmd)
	}
}

func TestRouterCallbackCommand(t *testing.T) {
	r, _, tokens := newTestRouter(16)
	tok, err := tokens.Issue(TokenPayload{Owner: 2001, Action: "vote", Target: "3", ExpectedPhase: game.Phase(1), PhaseVersion: 7})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	u := cbUpdate(201, 2001, tok)
	cmd, ok := r.routeOne(u)
	if !ok {
		t.Fatal("有效 callback token 未产生命令")
	}
	vote, ok := cmd.(game.VoteCommand)
	if !ok {
		t.Fatalf("命令类型 = %T, want VoteCommand", cmd)
	}
	if vote.Target == nil || *vote.Target != game.Seat(3) {
		t.Fatalf("Target = %v, want seat 3", vote.Target)
	}
	if vote.Meta.Actor != 2001 || vote.Meta.ExpectedPhase != game.Phase(1) || vote.Meta.PhaseVersion != 7 {
		t.Fatalf("Meta = %+v, want actor 2001 phase 1 version 7", vote.Meta)
	}
	if vote.Meta.ReceivedAt != u.ReceivedAt {
		t.Fatalf("ReceivedAt 未取自 update")
	}
}

func TestRouterDuplicateUpdateRejected(t *testing.T) {
	r, _, _ := newTestRouter(16)
	var applied int
	apply := func(ctx context.Context, cmd game.Command) error { applied++; return nil }
	u := msgUpdate(301, "/start")
	if err := r.Dispatch(context.Background(), u, apply); err != nil {
		t.Fatalf("first Dispatch: %v", err)
	}
	if err := r.Dispatch(context.Background(), u, apply); err != nil {
		t.Fatalf("second Dispatch: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1（重复 update ID 不得重放）", applied)
	}
}

func TestRouterAckSavesCursor(t *testing.T) {
	r, store, _ := newTestRouter(16)
	u := msgUpdate(401, "/start")
	if err := r.Dispatch(context.Background(), u, func(ctx context.Context, cmd game.Command) error {
		return nil
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := store.saved(); len(got) != 1 || got[0] != 401 {
		t.Fatalf("saved = %v, want [401]（ACK 后提交 cursor）", got)
	}
}

func TestRouterApplyErrorDoesNotAdvanceCursor(t *testing.T) {
	r, store, _ := newTestRouter(16)
	u := msgUpdate(501, "/start")
	err := r.Dispatch(context.Background(), u, func(ctx context.Context, cmd game.Command) error {
		return errTestFailure
	})
	if err == nil {
		t.Fatal("Dispatch 应返回 apply 错误")
	}
	if got := store.saved(); len(got) != 0 {
		t.Fatalf("saved = %v, want 空（未 ACK 不得提前推进 cursor）", got)
	}
}

func TestRouterRejectedCallbackStillAcked(t *testing.T) {
	r, store, _ := newTestRouter(16)
	// 无效 token：不产生命令但属于明确拒绝 → ACK（Mark+Save），不重放。
	u := cbUpdate(601, 2001, "INVALID-TOKEN-XXXXXXXXXXXXXXXX")
	applied := 0
	if err := r.Dispatch(context.Background(), u, func(ctx context.Context, cmd game.Command) error {
		applied++
		return nil
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0（无效 token 不产生命令）", applied)
	}
	if got := store.saved(); len(got) != 1 || got[0] != 601 {
		t.Fatalf("saved = %v, want [601]（明确拒绝也必须 ACK 提交 cursor）", got)
	}
}

func TestRouterInitialOffset(t *testing.T) {
	r, store, _ := newTestRouter(16)
	store.load = 42
	if got := r.InitialOffset(context.Background()); got != 43 {
		t.Fatalf("InitialOffset = %d, want 43（Load+1）", got)
	}
}

func TestRouterDispatchSavesMonotonically(t *testing.T) {
	r, store, _ := newTestRouter(32)
	ctx := context.Background()
	for _, id := range []int64{701, 702, 703} {
		u := msgUpdate(id, "/join")
		if err := r.Dispatch(ctx, u, func(ctx context.Context, cmd game.Command) error { return nil }); err != nil {
			t.Fatalf("Dispatch(%d): %v", id, err)
		}
	}
	got := store.saved()
	if len(got) != 3 || got[0] != 701 || got[1] != 702 || got[2] != 703 {
		t.Fatalf("saved = %v, want [701 702 703]（单 dispatcher 单调推进 cursor）", got)
	}
}

// commandMetaOf 抽取命令的 Meta 字段用于断言。
func commandMetaOf(cmd game.Command) game.CommandMeta {
	switch c := cmd.(type) {
	case game.CreateRoomCommand:
		return c.Meta
	case game.JoinRoomCommand:
		return c.Meta
	case game.StartGameCommand:
		return c.Meta
	case game.ConfirmRoleCommand:
		return c.Meta
	case game.WolfKillCommand:
		return c.Meta
	case game.WitchUseCommand:
		return c.Meta
	case game.SeerCheckCommand:
		return c.Meta
	case game.SpeakCommand:
		return c.Meta
	case game.VoteCommand:
		return c.Meta
	default:
		return game.CommandMeta{}
	}
}

var errTestFailure = context.DeadlineExceeded
