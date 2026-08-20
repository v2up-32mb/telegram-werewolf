package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

func lobbyKeyboardState() game.State {
	return game.State{
		RoomID:       "KB1234",
		Phase:        game.PhaseLobby,
		PhaseVersion: 9,
		Lobby:        game.LobbyState{Owner: 7001},
		Players: []game.Player{{
			UserID: 7001,
			Seat:   1,
		}},
	}
}

func TestLobbyPanelUsesOpaqueInlineKeyboard(t *testing.T) {
	w, _, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	st := lobbyKeyboardState()
	w.reg.create(st, st.Lobby.Owner, w.now())

	text, markup, err := w.buildPanel(st.RoomID)
	if err != nil {
		t.Fatalf("buildPanel: %v", err)
	}
	if strings.Contains(text, "操作：") || strings.Contains(text, "开始游戏") || strings.Contains(text, "解散房间") {
		t.Fatalf("面板正文仍包含按钮文案：%q", text)
	}
	if markup == nil || len(markup.Rows) != 1 || len(markup.Rows[0]) != 3 {
		t.Fatalf("markup = %#v, want one row with three buttons", markup)
	}

	want := []string{"start_game", "settings", "host_dissolve"}
	for i, button := range markup.Rows[0] {
		if button.Text == "" || button.CallbackData == "" {
			t.Fatalf("button[%d] = %#v, want text and token", i, button)
		}
		if button.CallbackData == want[i] || strings.Contains(button.CallbackData, want[i]) {
			t.Fatalf("button[%d] exposes action in callback_data: %q", i, button.CallbackData)
		}
		payload, err := w.tokens.Validate(button.CallbackData, st.Lobby.Owner)
		if err != nil {
			t.Fatalf("Validate button[%d]: %v", i, err)
		}
		if payload.Action != want[i] || payload.Owner != st.Lobby.Owner || payload.ExpectedPhase != game.PhaseLobby || payload.PhaseVersion != st.PhaseVersion {
			t.Fatalf("payload[%d] = %+v, want action=%q owner=%d phase=lobby version=%d", i, payload, want[i], st.Lobby.Owner, st.PhaseVersion)
		}
		if _, err := w.tokens.Validate(button.CallbackData, 9999); err == nil {
			t.Fatalf("button[%d] accepted by non-owner", i)
		}
	}
}

func TestFanOutLobbyPanelCarriesInlineKeyboard(t *testing.T) {
	w, rec, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	st := lobbyKeyboardState()
	w.reg.create(st, st.Lobby.Owner, w.now())
	panel, err := game.NewMessageEffect(game.AudienceHost, game.LobbyPanelMessageKey, nil)
	if err != nil {
		t.Fatalf("NewMessageEffect: %v", err)
	}
	if err := w.fanOut(st.RoomID, st, []game.Effect{panel}); err != nil {
		t.Fatalf("fanOut: %v", err)
	}
	w.drainCoalesced()

	msg := <-rec.ch
	params, ok := msg.Payload.(telegram.Params)
	if !ok {
		t.Fatalf("payload type = %T, want telegram.Params", msg.Payload)
	}
	if params.ReplyMarkup == nil || len(params.ReplyMarkup.Rows) != 1 || len(params.ReplyMarkup.Rows[0]) != 3 {
		t.Fatalf("fanout ReplyMarkup = %#v, want three buttons", params.ReplyMarkup)
	}
	if strings.Contains(params.Text, "操作：") {
		t.Fatalf("fanout panel text contains legacy button line: %q", params.Text)
	}
}

func TestApplyEffectsLobbyPanelCarriesInlineKeyboard(t *testing.T) {
	w, rec, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	st := lobbyKeyboardState()
	w.reg.create(st, st.Lobby.Owner, w.now())
	panel, err := game.NewMessageEffect(game.AudienceHost, game.LobbyPanelMessageKey, nil)
	if err != nil {
		t.Fatalf("NewMessageEffect: %v", err)
	}
	if err := w.applyEffects(context.Background(), "test", st.RoomID, st.Lobby.Owner, []game.Effect{panel}); err != nil {
		t.Fatalf("applyEffects: %v", err)
	}
	// Lobby panels are coalesced; explicitly flush the deterministic test queue.
	w.drainCoalesced()

	msg := <-rec.ch
	params, ok := msg.Payload.(telegram.Params)
	if !ok {
		t.Fatalf("payload type = %T, want telegram.Params", msg.Payload)
	}
	if params.ReplyMarkup == nil || len(params.ReplyMarkup.Rows) != 1 || len(params.ReplyMarkup.Rows[0]) != 3 {
		t.Fatalf("applyEffects ReplyMarkup = %#v, want three buttons", params.ReplyMarkup)
	}
}

func TestSettingsCallbackShowsPanel(t *testing.T) {
	w, rec, sched := newWiringSched(t, 16)
	defer func() { _ = sched.Close(context.Background()) }()

	// 创建房间使 settings 回调能找到 roomID
	st := lobbyKeyboardState()
	w.reg.create(st, st.Lobby.Owner, w.now())

	err := w.ActionHandler().Handle(context.Background(), telegram.CallbackAction{
		UpdateID:        77,
		Owner:           7001,
		Action:          "settings",
		CallbackQueryID: "cq-settings",
		ChatID:          7001,
		MessageID:       100,
		ReceivedAt:      time.Now(),
	})
	if err != nil {
		t.Fatalf("settings callback: %v", err)
	}

	// settings 回调现在产出 OpEditMessage（编辑面板展示设置 keyboard）
	// + OpAnswerCallback（顶部通知）。
	var gotEdit, gotAnswer bool
	for i := 0; i < 2; i++ {
		select {
		case msg := <-rec.ch:
			params, ok := msg.Payload.(telegram.Params)
			if !ok {
				t.Fatalf("payload = %T, want telegram.Params", msg.Payload)
			}
			switch msg.Operation {
			case telegram.OpEditMessage:
				gotEdit = true
				if params.ReplyMarkup == nil || len(params.ReplyMarkup.Rows) < 2 {
					t.Fatalf("settings panel markup missing, rows = %v", params.ReplyMarkup)
				}
			case telegram.OpAnswerCallback:
				gotAnswer = true
			}
		case <-time.After(5 * time.Second):
			t.Fatal("settings callback did not produce expected messages")
		}
	}
	if !gotEdit {
		t.Fatal("expected OpEditMessage for settings panel")
	}
	if !gotAnswer {
		t.Fatal("expected OpAnswerCallback for settings callback")
	}
}
