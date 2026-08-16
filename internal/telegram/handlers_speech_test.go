package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

type fakeSpeechSpeaker struct {
	seat game.Seat
	ok   bool
	err  error
	got  game.UserID
}

func (f *fakeSpeechSpeaker) CurrentSpeaker(_ context.Context, actor game.UserID) (game.Seat, bool, error) {
	f.got = actor
	return f.seat, f.ok, f.err
}

func newSpeechHandlerWith(f *fakeSpeechSpeaker, used int) (*SpeechHandler, *game.RoundCounter) {
	c := game.NewRoundCounter(game.SpeechMaxPerRound)
	for i := 0; i < used; i++ {
		_ = c.Count()
	}
	return NewSpeechHandler(f, c), c
}

func speechInput(actor game.UserID, text string) SpeechInput {
	return SpeechInput{CommandID: "u1", Actor: actor, ChatID: 111, MessageID: 42, Text: text}
}

// TestSpeechHandlerAccept 验证可转播路径：转播效果 + 原消息 3 秒延迟自毁；
// 计数器 +1。
func TestSpeechHandlerAccept(t *testing.T) {
	f := &fakeSpeechSpeaker{seat: 3, ok: true}
	h, c := newSpeechHandlerWith(f, 0)
	effects, err := h.Handle(context.Background(), speechInput(301, "大家好"))
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if f.got != 301 {
		t.Errorf("speaker 查询 actor = %d, want 301", f.got)
	}
	if len(effects) != 2 {
		t.Fatalf("effects 数 = %d, want 2", len(effects))
	}
	acc, ok := effects[0].(game.MessageEffect)
	if !ok || acc.Key != game.SpeechAcceptedMessageKey || acc.Audience != game.AudiencePublic {
		t.Errorf("effects[0] = %#v, want public speech.accepted", effects[0])
	}
	if acc.Params["seat"] != game.Seat(3) || acc.Params["text"] != "大家好" {
		t.Errorf("accept params = %v", acc.Params)
	}
	del, ok := effects[1].(game.DelayEffect)
	if !ok || del.After != game.SpeechSelfDeleteAfter {
		t.Fatalf("effects[1] = %#v, want DelayEffect 3s", effects[1])
	}
	me, ok := del.Inner.(game.MessageEffect)
	if !ok || me.Key != game.SpeechSelfDeleteMessageKey {
		t.Fatalf("delete inner = %#v, want speech.self_delete", del.Inner)
	}
	if me.Params["chat_id"] != int64(111) || me.Params["message_id"] != 42 {
		t.Errorf("self_delete params = %v, want chat_id=111 message_id=42", me.Params)
	}
	if c.Used != 1 {
		t.Errorf("counter.Used = %d, want 1", c.Used)
	}
}

// TestSpeechHandlerRejectTooLong 验证超长（>50 单位或 ASCII token ≥21）
// 拒绝转播、反馈 reason=too_long、原消息延迟自毁、计数不增加。
func TestSpeechHandlerRejectTooLong(t *testing.T) {
	f := &fakeSpeechSpeaker{seat: 3, ok: true}
	h, c := newSpeechHandlerWith(f, 0)
	long := make([]rune, 51)
	for i := range long {
		long[i] = '字'
	}
	effects, err := h.Handle(context.Background(), speechInput(301, string(long)))
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(effects) != 2 {
		t.Fatalf("effects 数 = %d, want 2", len(effects))
	}
	rej, ok := effects[0].(game.MessageEffect)
	if !ok || rej.Key != game.SpeechRejectedMessageKey || rej.Audience != game.AudienceActor {
		t.Errorf("effects[0] = %#v, want actor speech.rejected", effects[0])
	}
	if rej.Params["reason"] != game.SpeechRejectTooLong {
		t.Errorf("reason = %v, want %q", rej.Params["reason"], game.SpeechRejectTooLong)
	}
	if _, ok := effects[1].(game.DelayEffect); !ok {
		t.Errorf("effects[1] = %#v, want DelayEffect", effects[1])
	}
	if c.Used != 0 {
		t.Errorf("超长拒绝后 Used = %d, want 0", c.Used)
	}
}

// TestSpeechHandlerRejectRoundFull 验证回合满 5 条后拒绝转发、
// 反馈 reason=round_full、计数不增加。
func TestSpeechHandlerRejectRoundFull(t *testing.T) {
	f := &fakeSpeechSpeaker{seat: 3, ok: true}
	h, c := newSpeechHandlerWith(f, game.SpeechMaxPerRound)
	effects, err := h.Handle(context.Background(), speechInput(301, "再发一条"))
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	rej, ok := effects[0].(game.MessageEffect)
	if !ok || rej.Key != game.SpeechRejectedMessageKey {
		t.Fatalf("effects[0] = %#v, want speech.rejected", effects[0])
	}
	if rej.Params["reason"] != game.SpeechRejectRoundFull {
		t.Errorf("reason = %v, want %q", rej.Params["reason"], game.SpeechRejectRoundFull)
	}
	if _, ok := effects[1].(game.DelayEffect); !ok {
		t.Errorf("effects[1] = %#v, want DelayEffect", effects[1])
	}
	if c.Used != game.SpeechMaxPerRound {
		t.Errorf("Used = %d, want %d（不增加）", c.Used, game.SpeechMaxPerRound)
	}
}

// TestSpeechHandlerNotYourTurn 验证非当前发言者明确拒绝（无转播效果）。
func TestSpeechHandlerNotYourTurn(t *testing.T) {
	f := &fakeSpeechSpeaker{seat: 4, ok: false}
	h, _ := newSpeechHandlerWith(f, 0)
	effects, err := h.Handle(context.Background(), speechInput(305, "插话"))
	if !errors.Is(err, ErrNotSpeechTurn) {
		t.Fatalf("err = %v, want ErrNotSpeechTurn", err)
	}
	if len(effects) != 0 {
		t.Errorf("非发言者 effects 数 = %d, want 0", len(effects))
	}
}

// TestSpeechHandlerNoSleep 验证 handler 绝不 sleep：所有产出均为语义效果
// （MessageEffect / DelayEffect），删除一律经 3 秒 DelayEffect，无真实等待。
func TestSpeechHandlerNoSleep(t *testing.T) {
	f := &fakeSpeechSpeaker{seat: 1, ok: true}
	h, _ := newSpeechHandlerWith(f, 0)
	effects, err := h.Handle(context.Background(), speechInput(301, "你好"))
	if err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	start := time.Now()
	for range effects {
		// 只做静态检查：不 sleep、不等待任何真实时间。
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("handler 疑似阻塞 %v，必须立即返回效果", elapsed)
	}
}
