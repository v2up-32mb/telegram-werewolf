package game

import (
	"reflect"
	"testing"
	"time"
)

// speechPlayers 构造 6 人列表（3/5 死亡），供麦序测试使用。
func speechPlayers() []Player {
	roles := []Role{RoleVillager, RoleVillager, RoleWolf, RoleSeer, RoleWitch, RoleWolf}
	ps := make([]Player, 0, 6)
	for i, role := range roles {
		s := Seat(i + 1)
		ps = append(ps, Player{UserID: UserID(700 + i), Seat: s, Role: role, Dead: s == 3 || s == 5})
	}
	return ps
}

// TestDayStartSeat 验证麦序起点：死者下一位 / 平安夜 1 号（docs §白天 3）。
func TestDayStartSeat(t *testing.T) {
	alive := []Seat{1, 2, 4, 6}
	cases := []struct {
		name    string
		victims []Seat
		want    Seat
	}{
		{"死者 3 号 → 4 号", []Seat{3}, 4},
		{"死者 5 号 → 6 号", []Seat{5}, 6},
		{"多死者取最小死者后一位", []Seat{3, 5}, 4},
		{"死者 4 号 → 6 号", []Seat{4}, 6},
		{"死者 6 号环绕 → 1 号", []Seat{6}, 1},
		{"平安夜 → 1 号", nil, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DayStartSeat(tc.victims, alive); got != tc.want {
				t.Errorf("DayStartSeat(%v) = %d, want %d", tc.victims, got, tc.want)
			}
		})
	}
}

// TestBuildSpeechOrder 验证固定一轮麦序：起点后按座位升序环绕。
func TestBuildSpeechOrder(t *testing.T) {
	ps := speechPlayers() // 存活 1,2,4,6；3/5 死亡
	order := BuildSpeechOrder([]Seat{3, 5}, ps)
	want := []Seat{4, 6, 1, 2}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("BuildSpeechOrder = %v, want %v", order, want)
	}
	peace := BuildSpeechOrder(nil, ps)
	if !reflect.DeepEqual(peace, []Seat{1, 2, 4, 6}) {
		t.Fatalf("平安夜麦序 = %v, want [1 2 4 6]", peace)
	}
}

// TestNextSpeech 验证结束即移交给下一位、越界返回 ok=false（docs §白天 4）。
func TestNextSpeech(t *testing.T) {
	order := []Seat{4, 6, 1, 2}
	cases := []struct {
		cur  Seat
		next Seat
		ok   bool
	}{
		{4, 6, true},
		{6, 1, true},
		{1, 2, true},
		{2, 0, false}, // 本轮结束
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			next, ok := NextSpeech(tc.cur, order)
			if next != tc.next || ok != tc.ok {
				t.Errorf("NextSpeech(%d) = (%d,%v), want (%d,%v)", tc.cur, next, ok, tc.next, tc.ok)
			}
		})
	}
	if _, ok := NextSpeech(9, order); ok {
		t.Error("未知座位 NextSpeech ok=true, want false")
	}
}

// TestSpeechControlStandard 验证标准模式：60 秒限时 + 控制效果要素
// （docs 阶段消息设计.md §10.2：每条上限/回合上限/限时/截止时刻/已发送）。
func TestSpeechControlStandard(t *testing.T) {
	st := State{RoomID: "S", Phase: PhaseDaySpeech, Settings: DefaultRoomSettings()}
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	effects, err := SpeechControl(st, 4, 1, 5, at)
	if err != nil {
		t.Fatalf("SpeechControl error = %v", err)
	}
	var turn *MessageEffect
	var timer *TimerEffect
	for _, e := range effects {
		switch v := e.(type) {
		case MessageEffect:
			if v.Key == SpeechTurnMessageKey {
				turn = &v
			}
		case TimerEffect:
			timer = &v
		}
	}
	if turn == nil {
		t.Fatal("缺少 speech.turn 效果")
	}
	if turn.Audience != AudienceActor {
		t.Errorf("speech.turn 受众 = %v, want actor", turn.Audience)
	}
	if turn.Params["speaker"] != Seat(4) || turn.Params["sent"] != 1 || turn.Params["total"] != 5 {
		t.Errorf("speech.turn params = %v", turn.Params)
	}
	wantDeadline := at.Add(60 * time.Second)
	if turn.Params["deadline"] != wantDeadline {
		t.Errorf("deadline = %v, want %v", turn.Params["deadline"], wantDeadline)
	}
	if timer == nil || timer.Phase != PhaseDaySpeech || timer.Duration != 60*time.Second {
		t.Errorf("计时器 = %+v, want day_speech 60s", timer)
	}
}

// TestSpeechControlFast 验证快速模式：发言时长减半（60→30s）。
func TestSpeechControlFast(t *testing.T) {
	st := State{RoomID: "S", Phase: PhaseDaySpeech}
	st.Settings = DefaultRoomSettings()
	st.Settings.FastMode = true
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	effects, err := SpeechControl(st, 1, 0, 5, at)
	if err != nil {
		t.Fatalf("SpeechControl error = %v", err)
	}
	var timer *TimerEffect
	for _, e := range effects {
		if v, ok := e.(TimerEffect); ok {
			timer = &v
		}
	}
	if timer == nil || timer.Duration != 30*time.Second {
		t.Errorf("快速模式计时 = %+v, want 30s", timer)
	}
}

// TestSpeechAcceptReject 验证转播/拒绝反馈效果的受众与参数。
func TestSpeechAcceptReject(t *testing.T) {
	acc, err := SpeechAccept(4, "大家好")
	if err != nil {
		t.Fatalf("SpeechAccept error = %v", err)
	}
	me, ok := acc.(MessageEffect)
	if !ok || me.Key != SpeechAcceptedMessageKey || me.Audience != AudiencePublic {
		t.Fatalf("SpeechAccept = %#v, want public speech.accepted", acc)
	}
	if me.Params["seat"] != Seat(4) || me.Params["text"] != "大家好" {
		t.Errorf("accept params = %v", me.Params)
	}

	rej, err := SpeechReject(4, SpeechRejectTooLong)
	if err != nil {
		t.Fatalf("SpeechReject error = %v", err)
	}
	rm, ok := rej.(MessageEffect)
	if !ok || rm.Key != SpeechRejectedMessageKey || rm.Audience != AudienceActor {
		t.Fatalf("SpeechReject = %#v, want actor speech.rejected", rej)
	}
	if rm.Params["reason"] != SpeechRejectTooLong {
		t.Errorf("reject reason = %v", rm.Params["reason"])
	}
}

// TestSpeechSelfDeleteDelay 验证原消息/错误提示通过延迟效果自毁
// （docs §发言限制 2：约 3 秒后自动删除；不在 handler sleep）。
func TestSpeechSelfDeleteDelay(t *testing.T) {
	e, err := SpeechSelfDelete(123456, 99)
	if err != nil {
		t.Fatalf("SpeechSelfDelete error = %v", err)
	}
	del, ok := e.(DelayEffect)
	if !ok {
		t.Fatalf("SpeechSelfDelete = %#v, want DelayEffect", e)
	}
	if del.After != SpeechSelfDeleteAfter || SpeechSelfDeleteAfter != 3*time.Second {
		t.Errorf("自毁延迟 = %v, want 3s", del.After)
	}
	me, ok := del.Inner.(MessageEffect)
	if !ok || me.Key != SpeechSelfDeleteMessageKey {
		t.Fatalf("Inner = %#v, want speech.self_delete", del.Inner)
	}
	if me.Params["chat_id"] != int64(123456) || me.Params["message_id"] != 99 {
		t.Errorf("self_delete params = %v", me.Params)
	}
}
