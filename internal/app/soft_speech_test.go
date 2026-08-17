package app

// I4 补充红测：软限时发言模式——到期只提醒一次（speech.time_left）不打断，
// 麦序不移交（docs 设计Q&A Q3）。

import (
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// drAdvance 用真实导演 advanceSpeech 推进麦序（测试辅助）。
func drAdvance(st game.State, dr *dirRoom) (game.State, []game.Effect, error) {
	d := &roomDirector{
		w:     &Wiring{now: time.Now},
		rooms: map[game.RoomID]*dirRoom{st.RoomID: dr},
	}
	return d.advanceSpeech(st.RoomID, st)
}

// TestSoftSpeechReminderOnlyReminds 验证软限时提醒不推进麦序。
func TestSoftSpeechReminderOnlyReminds(t *testing.T) {
	dr := &dirRoom{speech: &speechDir{order: []game.Seat{3, 4}, idx: 0}}
	st := game.State{
		RoomID: "SOFT", Phase: game.PhaseDaySpeech, PhaseVersion: 3,
		Players: []game.Player{
			{UserID: 3, Seat: 3},
			{UserID: 4, Seat: 4},
		},
		Day:       game.DayState{Speaker: 3},
		Settings:  game.DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
	next, fx, err := remindSpeechFn("SOFT", dr)(st)
	if err != nil {
		t.Fatalf("remindSpeechFn: %v", err)
	}
	foundRemind := false
	for _, e := range fx {
		if me, ok := e.(game.MessageEffect); ok && me.Key == game.SpeechTimeLeftMessageKey {
			foundRemind = true
		}
	}
	if !foundRemind {
		t.Fatal("软限时到期未产出提醒效果（speech.time_left）")
	}
	// 不推进：麦序 idx 不变、发言者不变、无 Timer/无投票进入。
	if next.Day.Speaker != 3 || dr.speech.idx != 0 {
		t.Fatalf("软限时提醒不应移交麦位：speaker=%d idx=%d，want 3/0", next.Day.Speaker, dr.speech.idx)
	}
	if next.Phase != game.PhaseDaySpeech {
		t.Fatalf("软限时提醒不应切阶段：phase=%v, want day_speech", next.Phase)
	}
}

// TestSoftSpeechEndStillWorks 验证软限时下玩家仍可点「结束发言」正常移交。
func TestSoftSpeechEndStillWorks(t *testing.T) {
	dr := &dirRoom{speech: &speechDir{order: []game.Seat{3, 4}, idx: 0, counter: game.NewRoundCounter(5)}}
	st := game.State{
		RoomID: "SOFT", Phase: game.PhaseDaySpeech, PhaseVersion: 3,
		Players: []game.Player{
			{UserID: 3, Seat: 3, Role: game.RoleVillager},
			{UserID: 4, Seat: 4, Role: game.RoleVillager},
		},
		Day:       game.DayState{Speaker: 3, SpeechOrder: []game.Seat{3, 4}},
		Settings:  game.DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
	next, fx, err := drAdvance(st, dr)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if next.Day.Speaker != 4 || dr.speech.idx != 1 {
		t.Fatalf("结束发言未移交下一位：speaker=%d idx=%d，want 4/1", next.Day.Speaker, dr.speech.idx)
	}
	if next.Phase != game.PhaseDaySpeech {
		t.Fatalf("未到麦序末尾不应进入投票：phase=%v", next.Phase)
	}
	_ = fx
}
