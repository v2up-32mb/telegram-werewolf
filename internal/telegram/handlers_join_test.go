package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// fakeJoinService 记录收到的加入请求并返回脚本结果。
type fakeJoinService struct {
	reqs []game.JoinRequest
	res  game.JoinResult
	eff  []game.Effect
	err  error
}

func (f *fakeJoinService) Apply(_ context.Context, req game.JoinRequest) (game.JoinResult, []game.Effect, error) {
	f.reqs = append(f.reqs, req)
	return f.res, f.eff, f.err
}

func joinHandler(t *testing.T, fake *fakeJoinService) *JoinHandler {
	t.Helper()
	return NewJoinHandler(fake)
}

// TestJoinInputFromJoinText 验证 /join 文本命令解析：
// 单参数房间码或深链都可归一化；畸形输入明确拒绝。
func TestJoinInputFromJoinText(t *testing.T) {
	valid := []struct{ text, code string }{
		{"/join ABC123", "ABC123"},
		{"/join abc123", "ABC123"},
		{"/join https://t.me/xxxbot?start=def456", "DEF456"},
		{"/join  t.me/xxxbot?start=GHI789 ", "GHI789"},
	}
	for _, tc := range valid {
		in, ok := FromJoinText(tc.text)
		if !ok {
			t.Errorf("FromJoinText(%q) ok=false, want true", tc.text)
			continue
		}
		if in.RawCode != tc.code {
			t.Errorf("FromJoinText(%q).RawCode = %q, want %q", tc.text, in.RawCode, tc.code)
		}
	}
	invalid := []string{
		"", "/join", "/join ABC123 extra", "ABC 123", "/join 12", "notjoin ABC123", "/join AB@C",
	}
	for _, text := range invalid {
		if _, ok := FromJoinText(text); ok {
			t.Errorf("FromJoinText(%q) ok=true, want false", text)
		}
	}
}

// TestJoinInputRequestConversion 验证输入→领域请求：房间码规范化（大写）、
// 密码与昵称原样透传；非法房间码 ok=false。
func TestJoinInputRequestConversion(t *testing.T) {
	pw := "Ab12cd"
	nick := "wOLF"
	in := JoinInput{
		CommandID: "j1",
		Actor:     3001,
		RawCode:   "  abc123 ",
		Password:  &pw,
		Nickname:  &nick,
	}
	req, ok := in.Request()
	if !ok {
		t.Fatal("Request() ok=false, want true")
	}
	if req.CommandID != "j1" || req.Actor != 3001 {
		t.Errorf("CommandID/Actor = %q/%d, want j1/3001", req.CommandID, req.Actor)
	}
	if req.RoomID != game.RoomID("ABC123") {
		t.Errorf("RoomID = %q, want ABC123（规范化大写）", req.RoomID)
	}
	if req.Password == nil || *req.Password != "Ab12cd" {
		t.Errorf("Password = %v, want &Ab12cd", req.Password)
	}
	if req.Nickname == nil || *req.Nickname != "wOLF" {
		t.Errorf("Nickname = %v, want &wOLF", req.Nickname)
	}

	for _, bad := range []string{"", "12", "AB@C", "  AB  "} {
		in.RawCode = bad
		if _, ok := in.Request(); ok {
			t.Errorf("Request(RawCode=%q) ok=true, want false", bad)
		}
	}
}

// TestJoinHandlerDelegatesToService 验证适配层单点委托服务并原样返回结果。
func TestJoinHandlerDelegatesToService(t *testing.T) {
	res := game.JoinResult{Seat: 3, Nickname: "快乐小猫"}
	eff := []game.Effect{game.MessageEffect{Key: game.JoinConfirmedMessageKey, Audience: game.AudienceActor}}
	fake := &fakeJoinService{res: res, eff: eff}
	h := joinHandler(t, fake)

	in := JoinInput{CommandID: "j9", Actor: 9, RawCode: "abc123"}
	got, gotEff, err := h.Join(context.Background(), in)
	if err != nil {
		t.Fatalf("Join error = %v, want nil", err)
	}
	if len(fake.reqs) != 1 {
		t.Fatalf("服务收到请求数 = %d, want 1", len(fake.reqs))
	}
	want := game.JoinRequest{
		CommandID: "j9",
		Actor:     9,
		RoomID:    "ABC123",
	}
	if !reflect.DeepEqual(fake.reqs[0], want) {
		t.Errorf("服务收到请求 = %+v, want %+v", fake.reqs[0], want)
	}
	if !reflect.DeepEqual(got, res) {
		t.Errorf("返回结果 = %+v, want %+v", got, res)
	}
	if !reflect.DeepEqual(gotEff, eff) {
		t.Errorf("返回 Effects = %v, want %v", gotEff, eff)
	}
}

// TestJoinHandlerPropagatesError 验证适配层不吞错误、不复制领域校验。
func TestJoinHandlerPropagatesError(t *testing.T) {
	wantErr := errors.New("wrong password")
	fake := &fakeJoinService{err: wantErr}
	h := joinHandler(t, fake)

	_, _, err := h.Join(context.Background(), JoinInput{CommandID: "j10", Actor: 10, RawCode: "ABC123"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Join error = %v, want %v", err, wantErr)
	}
}

// TestJoinInputFromInviteDeepLink 验证深链按钮入口：提取房间码到 RawCode。
func TestJoinInputFromInviteDeepLink(t *testing.T) {
	in, ok := FromInviteDeepLink("https://t.me/xxxbot?start=abCD12")
	if !ok {
		t.Fatal("FromInviteDeepLink ok=false, want true")
	}
	if in.RawCode != "ABCD12" {
		t.Errorf("RawCode = %q, want ABCD12（规范化大写，房间码统一大写语义）", in.RawCode)
	}
	if _, ok := FromInviteDeepLink("https://example.com/x"); ok {
		t.Error("FromInviteDeepLink(非深链) ok=true, want false")
	}
}
