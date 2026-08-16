package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// fakeCreateRoomService 记录收到的建房请求并返回脚本结果，
// 用于验证适配层把两条入口汇聚到同一个应用服务。
type fakeCreateRoomService struct {
	reqs []game.CreateRoomRequest
	st   game.State
	eff  []game.Effect
	err  error
}

func (f *fakeCreateRoomService) CreateRoom(_ context.Context, req game.CreateRoomRequest) (game.State, []game.Effect, error) {
	f.reqs = append(f.reqs, req)
	return f.st, f.eff, f.err
}

// createHandler 构造带 fake 服务的建房适配器。
func createHandler(t *testing.T, fake *fakeCreateRoomService) *CreateRoomHandler {
	t.Helper()
	return NewCreateRoomHandler(fake)
}

// TestCreateRoomMenuAndNewGameShareService 验证主菜单按钮与 /newgame
// 两条入口进入同一个应用服务、产生完全相同的领域请求（不复制逻辑，
// docs §一.6 创建入口）。
func TestCreateRoomMenuAndNewGameShareService(t *testing.T) {
	fake := &fakeCreateRoomService{}
	h := createHandler(t, fake)

	menu := FromMenuButton()
	menu.CommandID = "u7"
	menu.Actor = 2001

	text, ok := FromNewGameText("/newgame")
	if !ok {
		t.Fatal("FromNewGameText(\"/newgame\") = !ok, want ok")
	}
	text.CommandID = "u7"
	text.Actor = 2001

	if _, _, err := h.Create(context.Background(), menu); err != nil {
		t.Fatalf("主菜单入口 Create error = %v, want nil", err)
	}
	if _, _, err := h.Create(context.Background(), text); err != nil {
		t.Fatalf("/newgame 入口 Create error = %v, want nil", err)
	}
	if len(fake.reqs) != 2 {
		t.Fatalf("服务收到请求数 = %d, want 2", len(fake.reqs))
	}
	if !reflect.DeepEqual(fake.reqs[0], fake.reqs[1]) {
		t.Errorf("两入口请求不一致:\n 主菜单: %+v\n /newgame: %+v", fake.reqs[0], fake.reqs[1])
	}
	want := game.CreateRoomRequest{CommandID: "u7", Host: 2001}
	if !reflect.DeepEqual(fake.reqs[0], want) {
		t.Errorf("请求 = %+v, want %+v（配置与自定义码由领域层默认/规范化）", fake.reqs[0], want)
	}
}

// TestCreateRoomTextWithCustomCode 验证 /newgame 单参数传递自定义码
// （大小写规范化是领域层职责，适配层原样透传）。
func TestCreateRoomTextWithCustomCode(t *testing.T) {
	in, ok := FromNewGameText("/newgame abC12")
	if !ok {
		t.Fatal("FromNewGameText(\"/newgame abC12\") = !ok, want ok")
	}
	if in.CustomCode != "abC12" {
		t.Errorf("CustomCode = %q, want %q", in.CustomCode, "abC12")
	}
	req := in.Request()
	if req.CustomCode != "abC12" {
		t.Errorf("Request().CustomCode = %q, want %q", req.CustomCode, "abC12")
	}
}

// TestParseNewGameTextRejects 验证非建房文本与多余参数被明确拒绝
// （与 router 的文本路由行为一致：命令必须为小写 /newgame）。
func TestParseNewGameTextRejects(t *testing.T) {
	for _, text := range []string{"", "hello", "/join", "/NEWGAME", "/newgame a b", "/start"} {
		if _, ok := FromNewGameText(text); ok {
			t.Errorf("FromNewGameText(%q) = ok, want !ok", text)
		}
	}
}

// TestMenuButtonIsEmptyInput 验证主菜单按钮入口不携带自定义码。
func TestMenuButtonIsEmptyInput(t *testing.T) {
	if got := FromMenuButton(); !reflect.DeepEqual(got, CreateRoomInput{}) {
		t.Errorf("FromMenuButton() = %+v, want 零值（随机码）", got)
	}
}

// TestCreateRoomRequestConversion 验证输入→领域请求转换字段映射。
func TestCreateRoomRequestConversion(t *testing.T) {
	in := CreateRoomInput{CommandID: "u1", Actor: 42, CustomCode: "abc12"}
	req := in.Request()
	want := game.CreateRoomRequest{CommandID: "u1", Host: 42, CustomCode: "abc12"}
	if !reflect.DeepEqual(req, want) {
		t.Errorf("Request() = %+v, want %+v", req, want)
	}
}

// TestCreateRoomHandlerPropagatesServiceResult 验证适配层把服务结果
// 原样返回（只做输入转换与单点汇聚，不吞错误）。
func TestCreateRoomHandlerPropagatesServiceResult(t *testing.T) {
	st := game.State{RoomID: "ABC123", Phase: game.PhaseLobby, PhaseVersion: 1}
	eff := []game.Effect{game.PersistEffect{Kind: game.PersistActiveGame}}
	wantErr := errors.New("boom")
	fake := &fakeCreateRoomService{st: st, eff: eff, err: wantErr}
	h := createHandler(t, fake)

	in := CreateRoomInput{CommandID: "u9", Actor: 9}
	gotSt, gotEff, err := h.Create(context.Background(), in)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(gotSt, st) {
		t.Errorf("State = %+v, want %+v", gotSt, st)
	}
	if !reflect.DeepEqual(gotEff, eff) {
		t.Errorf("Effects = %v, want %v", gotEff, eff)
	}
	// 即使服务失败，请求仍必须被正确转换并送达。
	if len(fake.reqs) != 1 || fake.reqs[0].CommandID != "u9" || fake.reqs[0].Host != 9 {
		t.Errorf("服务收到的请求 = %+v, want CommandID=u9 Host=9", fake.reqs)
	}
}
