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
	// go-telegram/bot 内部 getUpdates 使用 lastUpdateID+1 作为 offset，
	// 因此 InitialOffset 返回水位本身，由库内部 +1。
	if got := r.InitialOffset(context.Background()); got != 42 {
		t.Fatalf("InitialOffset = %d, want 42（Load，库内部 +1）", got)
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

// TestRouterNewCallbackCommandSet 验证 B1-b：回调 token 动作覆盖引擎新命令集
// （WolfVote/WitchSave/WitchPoison/确认命令/治理/再来一局等），旧的
// wolf_kill/witch_use/speak 命令退役。
func TestRouterNewCallbackCommandSet(t *testing.T) {
	r, _, tokens := newTestRouter(64)

	issue := func(action, target string) string {
		tok, err := tokens.Issue(TokenPayload{
			Owner: 2001, Action: action, Target: target,
			ExpectedPhase: game.PhaseNight, PhaseVersion: 3,
		})
		if err != nil {
			t.Fatalf("Issue(%s): %v", action, err)
		}
		return tok
	}

	t.Run("狼人投票与确认", func(t *testing.T) {
		cmd, ok := r.routeOne(cbUpdate(11, 2001, issue("wolf_vote", "3")))
		if !ok {
			t.Fatal("wolf_vote 未产生命令")
		}
		wv, ok := cmd.(game.WolfVoteCommand)
		if !ok {
			t.Fatalf("类型 = %T, want WolfVoteCommand", cmd)
		}
		if wv.Target == nil || *wv.Target != 3 {
			t.Fatalf("Target = %v, want 3", wv.Target)
		}
		if cmd2, ok := r.routeOne(cbUpdate(12, 2001, issue("wolf_vote", "abstain"))); !ok {
			t.Fatal("wolf_vote abstain 未产生命令")
		} else if w := cmd2.(game.WolfVoteCommand); w.Target != nil {
			t.Fatalf("abstain Target = %v, want nil（空刀）", w.Target)
		}
		if _, ok := r.routeOne(cbUpdate(13, 2001, issue("wolf_confirm", ""))); !ok {
			t.Fatal("wolf_confirm 未产生命令")
		}
	})

	t.Run("女巫救/毒与确认", func(t *testing.T) {
		cmd, ok := r.routeOne(cbUpdate(21, 2001, issue("witch_save", "yes")))
		if !ok {
			t.Fatal("witch_save 未产生命令")
		}
		ws, ok := cmd.(game.WitchSaveCommand)
		if !ok || !ws.Use {
			t.Fatalf("witch_save(yes) = %T %+v, want Use=true", cmd, ws)
		}
		cmd2, ok := r.routeOne(cbUpdate(22, 2001, issue("witch_poison", "5")))
		if !ok {
			t.Fatal("witch_poison 未产生命令")
		}
		wp, ok := cmd2.(game.WitchPoisonCommand)
		if !ok || wp.Target == nil || *wp.Target != 5 {
			t.Fatalf("witch_poison(5) = %T %+v, want Target=5", cmd2, wp)
		}
		cmd3, ok := r.routeOne(cbUpdate(23, 2001, issue("witch_poison", "abstain")))
		if !ok {
			t.Fatal("witch_poison abstain 未产生命令")
		}
		if w := cmd3.(game.WitchPoisonCommand); w.Target != nil {
			t.Fatalf("abstain Target = %v, want nil（不使用毒药）", w.Target)
		}
		if _, ok := r.routeOne(cbUpdate(24, 2001, issue("witch_confirm", ""))); !ok {
			t.Fatal("witch_confirm 未产生命令")
		}
	})

	t.Run("预言家查验与确认", func(t *testing.T) {
		cmd, ok := r.routeOne(cbUpdate(31, 2001, issue("seer_check", "4")))
		if !ok {
			t.Fatal("seer_check 未产生命令")
		}
		if sc, ok := cmd.(game.SeerCheckCommand); !ok || sc.Target != 4 {
			t.Fatalf("seer_check = %T %+v, want Target=4", cmd, sc)
		}
		if _, ok := r.routeOne(cbUpdate(32, 2001, issue("seer_confirm", ""))); !ok {
			t.Fatal("seer_confirm 未产生命令")
		}
	})

	t.Run("投票确认与自爆/退出/再来一局", func(t *testing.T) {
		if _, ok := r.routeOne(cbUpdate(41, 2001, issue("vote_confirm", ""))); !ok {
			t.Fatal("vote_confirm 未产生命令")
		}
		if _, ok := r.routeOne(cbUpdate(42, 2001, issue("explode", ""))); !ok {
			t.Fatal("explode 未产生命令")
		}
		if _, ok := r.routeOne(cbUpdate(43, 2001, issue("leave_game", ""))); !ok {
			t.Fatal("leave_game 未产生命令")
		}
		if _, ok := r.routeOne(cbUpdate(44, 2001, issue("rematch", ""))); !ok {
			t.Fatal("rematch 未产生命令")
		}
	})

	t.Run("遗言与治理", func(t *testing.T) {
		cmd, ok := r.routeOne(cbUpdate(51, 2001, issue("last_words", "我的遗言")))
		if !ok {
			t.Fatal("last_words 未产生命令")
		}
		if lw, ok := cmd.(game.LastWordsCommand); !ok || lw.Text != "我的遗言" {
			t.Fatalf("last_words = %T %+v, want Text=我的遗言", cmd, lw)
		}
		if _, ok := r.routeOne(cbUpdate(52, 2001, issue("governance_dissolve", ""))); !ok {
			t.Fatal("governance_dissolve 未产生命令")
		}
		if _, ok := r.routeOne(cbUpdate(53, 2001, issue("governance_dissolve_vote", ""))); !ok {
			t.Fatal("governance_dissolve_vote 未产生命令")
		}
		cmd, ok = r.routeOne(cbUpdate(54, 2001, issue("governance_kick", "2")))
		if !ok {
			t.Fatal("governance_kick 未产生命令")
		}
		if gk, ok := cmd.(game.GovernanceKickCommand); !ok || gk.Target != 2 {
			t.Fatalf("governance_kick = %T %+v, want Target=2", cmd, gk)
		}
		if _, ok := r.routeOne(cbUpdate(55, 2001, issue("governance_kick_vote", ""))); !ok {
			t.Fatal("governance_kick_vote 未产生命令")
		}
		cmd, ok = r.routeOne(cbUpdate(56, 2001, issue("host_dissolve", "confirm")))
		if !ok {
			t.Fatal("host_dissolve(confirm) 未产生命令")
		}
		if hd, ok := cmd.(game.HostDissolveCommand); !ok || !hd.Confirm {
			t.Fatalf("host_dissolve(confirm) = %T %+v, want Confirm=true", cmd, hd)
		}
	})

	t.Run("导演本地信号不映射为游戏命令", func(t *testing.T) {
		if _, ok := r.routeOne(cbUpdate(61, 2001, issue("end_speech", ""))); ok {
			t.Fatal("end_speech 不应映射为游戏命令（导演本地信号）")
		}
	})

	t.Run("旧命令退役", func(t *testing.T) {
		for _, action := range []string{"wolf_kill", "witch_use", "speak"} {
			if _, ok := r.routeOne(cbUpdate(70, 2001, issue(action, "3"))); ok {
				t.Fatalf("旧动作 %s 仍映射为命令，want 退役", action)
			}
		}
	})
}

// TestRouterDispatchAction 验证 B1-b：回调更新经 DispatchAction 校验 token、
// 携带 UpdateID/动作/目标交回接线层，重复 update 不重放，ACK 提交 cursor。
func TestRouterDispatchAction(t *testing.T) {
	r, store, tokens := newTestRouter(16)
	tok, err := tokens.Issue(TokenPayload{
		Owner: 2001, Action: "end_speech", Target: "",
		ExpectedPhase: game.PhaseDaySpeech, PhaseVersion: 4,
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	u := cbUpdate(801, 2001, tok)
	var got *CallbackAction
	if err := r.DispatchAction(context.Background(), u, func(ctx context.Context, act CallbackAction) error {
		got = &act
		return nil
	}); err != nil {
		t.Fatalf("DispatchAction: %v", err)
	}
	if got == nil {
		t.Fatal("apply 未收到 CallbackAction")
	}
	if got.UpdateID != 801 || got.Owner != 2001 || got.Action != "end_speech" ||
		got.ExpectedPhase != game.PhaseDaySpeech || got.PhaseVersion != 4 {
		t.Fatalf("CallbackAction = %+v, want UpdateID 801/end_speech/day_speech/4", *got)
	}
	// 重复 update 不重放。
	applied := 0
	if err := r.DispatchAction(context.Background(), u, func(ctx context.Context, act CallbackAction) error {
		applied++
		return nil
	}); err != nil {
		t.Fatalf("second DispatchAction: %v", err)
	}
	if applied != 0 {
		t.Fatalf("重复 update DispatchAction applied = %d, want 0", applied)
	}
	if saved := store.saved(); len(saved) != 1 || saved[0] != 801 {
		t.Fatalf("saved = %v, want [801]", saved)
	}
	// 无效 token：明确拒绝仍 ACK。
	if err := r.DispatchAction(context.Background(), cbUpdate(802, 2001, "BAD-TOKEN"), func(ctx context.Context, act CallbackAction) error {
		return nil
	}); err != nil {
		t.Fatalf("invalid token DispatchAction: %v", err)
	}
	if saved := store.saved(); len(saved) != 2 || saved[1] != 802 {
		t.Fatalf("saved = %v, want second 802（无效 token 也 ACK）", saved)
	}
}
