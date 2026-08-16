package telegram

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 玩家命令与帮助入口测试（docs 游戏流程设计.md §一.6 创建入口、
// §命令清单、§新手引导、§发言 4 自毁提示）：命令解析（7 命令 + /rank
// 占位）、私聊限定、无房/死亡/发牌前状态反馈、MarkdownV2 转义；
// 所有回复经 i18n.Renderer 渲染（默认转义）后经 ReplySender seam 发出。

type sendRecord struct {
	chatID int64
	text   string
}

type fakeSender struct {
	sent []sendRecord
	err  error
}

func (f *fakeSender) Send(_ context.Context, chatID int64, text string) error {
	f.sent = append(f.sent, sendRecord{chatID: chatID, text: text})
	return f.err
}

type fakeCreate struct {
	err    error
	called int
	req    game.CreateRoomRequest
}

func (f *fakeCreate) CreateRoom(_ context.Context, req game.CreateRoomRequest) (game.State, []game.Effect, error) {
	f.called++
	f.req = req
	return game.State{}, nil, f.err
}

type fakeJoin struct {
	err    error
	called int
	req    game.JoinRequest
}

func (f *fakeJoin) Apply(_ context.Context, req game.JoinRequest) (game.JoinResult, []game.Effect, error) {
	f.called++
	f.req = req
	return game.JoinResult{}, nil, f.err
}

type fakeLeave struct {
	err    error
	called int
	actor  game.UserID
}

func (f *fakeLeave) Leave(_ context.Context, actor game.UserID, _ string) ([]game.Effect, error) {
	f.called++
	f.actor = actor
	return nil, f.err
}

type fakeRole struct {
	err    error
	reply  RoleReply
	called int
}

func (f *fakeRole) Role(_ context.Context, _ game.UserID) (RoleReply, error) {
	f.called++
	return f.reply, f.err
}

type fakeScore struct {
	err    error
	score  int64
	called int
}

func (f *fakeScore) Score(_ context.Context, _ game.UserID) (int64, error) {
	f.called++
	return f.score, f.err
}

func commandsTestRenderer(t *testing.T) *i18n.Renderer {
	t.Helper()
	r, err := i18n.NewRenderer("zh-CN")
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	return r
}

func newTestCommandsHandler(t *testing.T, sender *fakeSender, create *fakeCreate, join *fakeJoin, leave *fakeLeave, roles *fakeRole, scores *fakeScore) *CommandsHandler {
	t.Helper()
	h, err := NewCommandsHandler(commandsTestRenderer(t), sender, create, join, leave, roles, scores)
	if err != nil {
		t.Fatalf("NewCommandsHandler: %v", err)
	}
	return h
}

func commandIn(text string, priv bool) CommandInput {
	return CommandInput{
		CommandID:  "u1",
		Actor:      1001,
		ChatID:     1001,
		UserID:     1001,
		Text:       text,
		ReceivedAt: time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC),
		IsPrivate:  priv,
	}
}

// TestParseCommand 验证命令解析：7 命令 + /rank 占位；精确小写、容忍
// 首尾空白；/newgame 至多 1 参数、/join 恰好 1 参数、其余零参数带参拒绝。
func TestParseCommand(t *testing.T) {
	cases := []struct {
		text string
		want CommandKind
		ok   bool
		args []string
	}{
		{"/start", CommandStart, true, nil},
		{"  /start  ", CommandStart, true, nil},
		{"/START", CommandUnknown, false, nil},
		{"/newgame", CommandNewGame, true, nil},
		{"/newgame ABC123", CommandNewGame, true, []string{"ABC123"}},
		{"/newgame A B", CommandUnknown, false, nil},
		{"/join ABC123", CommandJoin, true, []string{"ABC123"}},
		{"/join", CommandUnknown, false, nil},
		{"/role", CommandRole, true, nil},
		{"/role 1", CommandUnknown, false, nil},
		{"/score", CommandScore, true, nil},
		{"/score 1", CommandUnknown, false, nil},
		{"/leave", CommandLeave, true, nil},
		{"/leave x", CommandUnknown, false, nil},
		{"/help", CommandHelp, true, nil},
		{"/help x", CommandUnknown, false, nil},
		{"/rank", CommandRank, true, nil},
		{"/rank x", CommandUnknown, false, nil},
		{"hello", CommandUnknown, false, nil},
		{"", CommandUnknown, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got, ok := ParseCommand(tc.text)
			if ok != tc.ok || got.Kind != tc.want {
				t.Fatalf("ParseCommand(%q) = (%v, %v), want (%v, %v)",
					tc.text, got.Kind, ok, tc.want, tc.ok)
			}
			if ok && !reflect.DeepEqual(got.Args, tc.args) {
				t.Errorf("Args = %v, want %v", got.Args, tc.args)
			}
		})
	}
}

// TestIsPrivateChat 验证私聊判定：Bot 私聊 ChatID == UserID。
func TestIsPrivateChat(t *testing.T) {
	if !IsPrivateChat(Update{Message: &IncomingMessage{ChatID: 7, UserID: 7}}) {
		t.Error("私聊应判定为 true")
	}
	if IsPrivateChat(Update{Message: &IncomingMessage{ChatID: -100, UserID: 7}}) {
		t.Error("群聊应判定为 false")
	}
	if IsPrivateChat(Update{}) {
		t.Error("无消息应判定为 false")
	}
}

// TestCommandsPrivateChatGate 验证私聊限定：群聊命令被拒绝并回复
// commands.private_only，不调用任何服务。
func TestCommandsPrivateChatGate(t *testing.T) {
	sender := &fakeSender{}
	create, join, leave := &fakeCreate{}, &fakeJoin{}, &fakeLeave{}
	roles, scores := &fakeRole{}, &fakeScore{}
	h := newTestCommandsHandler(t, sender, create, join, leave, roles, scores)

	if err := h.Handle(context.Background(), commandIn("/score", false)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("回复数 = %d, want 1", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].text, "私聊") {
		t.Errorf("群聊回复 = %q, want commands.private_only", sender.sent[0].text)
	}
	if scores.called != 0 {
		t.Error("群聊命令不应调用服务")
	}
}

// TestCommandsMenuAndHelp 验证 /start 主菜单与 /help 帮助（命令清单 +
// 新手规则 + 首次发言 3 秒自毁提示）。
func TestCommandsMenuAndHelp(t *testing.T) {
	renderer := commandsTestRenderer(t)
	h := newTestCommandsHandler(t, &fakeSender{}, &fakeCreate{}, &fakeJoin{}, &fakeLeave{}, &fakeRole{}, &fakeScore{})

	sender := &fakeSender{}
	h.sender = sender
	if err := h.Handle(context.Background(), commandIn("/start  ", true)); err != nil {
		t.Fatalf("/start: %v", err)
	}
	wantMenu, _ := renderer.Render("menu.main", nil)
	if len(sender.sent) != 1 || sender.sent[0].text != wantMenu {
		t.Fatalf("/start 回复 = %q, want menu.main 渲染结果", sender.sent[0].text)
	}
	if !strings.Contains(sender.sent[0].text, "/newgame") {
		t.Errorf("主菜单应包含 /newgame 入口")
	}

	sender.sent = nil
	if err := h.Handle(context.Background(), commandIn("/help", true)); err != nil {
		t.Fatalf("/help: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("/help 回复数 = %d, want 1", len(sender.sent))
	}
	help, _ := renderer.Render("help.commands", nil)
	rules, _ := renderer.Render("rules.intro", nil)
	hint, _ := renderer.Render("speech.self_destruct_hint", nil)
	wantHelp := strings.Join([]string{help, rules, hint}, "\n\n")
	if sender.sent[0].text != wantHelp {
		t.Errorf("/help 回复与三段文案拼接不一致")
	}
	if !strings.Contains(sender.sent[0].text, "/start") || !strings.Contains(sender.sent[0].text, "3 秒") {
		t.Errorf("/help 应包含命令清单与 3 秒自毁提示")
	}
}

// TestCommandsRankPlaceholder 验证 /rank 只返回「后续开放」占位说明，
// 不查询任何排行榜数据。
func TestCommandsRankPlaceholder(t *testing.T) {
	sender := &fakeSender{}
	scores := &fakeScore{}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, &fakeJoin{}, &fakeLeave{}, &fakeRole{}, scores)
	if err := h.Handle(context.Background(), commandIn("/rank", true)); err != nil {
		t.Fatalf("/rank: %v", err)
	}
	if scores.called != 0 {
		t.Error("/rank 不得查询积分/排行榜")
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "后续开放") {
		t.Errorf("/rank 回复 = %q, want rank.placeholder", sender.sent[0].text)
	}
}

// TestCommandsScore 验证 /score：显示当前积分；无房时反馈 commands.no_room。
func TestCommandsScore(t *testing.T) {
	sender := &fakeSender{}
	scores := &fakeScore{score: 42}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, &fakeJoin{}, &fakeLeave{}, &fakeRole{}, scores)
	if err := h.Handle(context.Background(), commandIn("/score", true)); err != nil {
		t.Fatalf("/score: %v", err)
	}
	if scores.called != 1 {
		t.Fatalf("Score 调用数 = %d, want 1", scores.called)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "42") {
		t.Errorf("/score 回复 = %q, want 包含 42", sender.sent[0].text)
	}

	sender.sent = nil
	scores.err = game.ErrNotInRoom
	if err := h.Handle(context.Background(), commandIn("/score", true)); err != nil {
		t.Fatalf("/score(无房): %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "不在任何房间") {
		t.Errorf("/score 无房回复 = %q, want commands.no_room", sender.sent[0].text)
	}
}

// TestCommandsRole 验证 /role 状态反馈与身份展示：无房/死亡/发牌前
// 分别映射 commands.no_room / commands.dead / commands.no_role_yet；
// 身份名经 MarkdownV2 转义（特殊字符不裸拼）。
func TestCommandsRole(t *testing.T) {
	sender := &fakeSender{}
	roles := &fakeRole{}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, &fakeJoin{}, &fakeLeave{}, roles, &fakeScore{})

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"无房", game.ErrNotInRoom, "不在任何房间"},
		{"死亡", game.ErrDeadPlayer, "已死亡"},
		{"发牌前", game.ErrWrongPhase, "尚未发牌"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender.sent = nil
			roles.err = tc.err
			if err := h.Handle(context.Background(), commandIn("/role", true)); err != nil {
				t.Fatalf("/role: %v", err)
			}
			if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, tc.want) {
				t.Errorf("/role(%s) 回复 = %q, want 含 %q", tc.name, sender.sent[0].text, tc.want)
			}
		})
	}

	// 正常身份展示 + MarkdownV2 转义（下划线 → \_）。
	sender.sent = nil
	roles.err = nil
	roles.reply = RoleReply{RoleName: "狼_人", CampName: "🐺"}
	if err := h.Handle(context.Background(), commandIn("/role", true)); err != nil {
		t.Fatalf("/role(身份): %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("/role 回复数 = %d, want 1", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].text, "狼\\_人") {
		t.Errorf("身份名未转义: %q", sender.sent[0].text)
	}
	if strings.Contains(sender.sent[0].text, "狼_人") {
		t.Errorf("身份名特殊字符未转义裸拼: %q", sender.sent[0].text)
	}
}

// TestCommandsNewGame 验证 /newgame：调用 CreateRoomService（自定义码
// 透传）；已在房间反馈 commands.already_in_room；非法输入回复
// error.invalid_input 且不调用服务。
func TestCommandsNewGame(t *testing.T) {
	sender := &fakeSender{}
	create := &fakeCreate{}
	h := newTestCommandsHandler(t, sender, create, &fakeJoin{}, &fakeLeave{}, &fakeRole{}, &fakeScore{})

	if err := h.Handle(context.Background(), commandIn("/newgame ABC123", true)); err != nil {
		t.Fatalf("/newgame: %v", err)
	}
	if create.called != 1 {
		t.Fatalf("CreateRoom 调用数 = %d, want 1", create.called)
	}
	if create.req.Host != 1001 || create.req.CustomCode != "ABC123" {
		t.Errorf("CreateRoom 请求 = %+v", create.req)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "创建") {
		t.Errorf("建房回复 = %q, want commands.newgame_done", sender.sent[0].text)
	}

	sender.sent = nil
	create.err = game.ErrHostInRoom
	if err := h.Handle(context.Background(), commandIn("/newgame", true)); err != nil {
		t.Fatalf("/newgame(已在房): %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "已在房间") {
		t.Errorf("已在房回复 = %q, want commands.already_in_room", sender.sent[0].text)
	}

	sender.sent = nil
	create.called = 0
	if err := h.Handle(context.Background(), commandIn("/newgame A B", true)); err != nil {
		t.Fatalf("/newgame(非法): %v", err)
	}
	if create.called != 0 {
		t.Error("非法建房参数不应调用服务")
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "输入无效") {
		t.Errorf("非法输入回复 = %q, want error.invalid_input", sender.sent[0].text)
	}
}

// TestCommandsJoin 验证 /join：调用 JoinService 且房间码透传；非法输入
// 回复 error.invalid_input 且不调用服务。
func TestCommandsJoin(t *testing.T) {
	sender := &fakeSender{}
	join := &fakeJoin{}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, join, &fakeLeave{}, &fakeRole{}, &fakeScore{})

	if err := h.Handle(context.Background(), commandIn("/join abc123", true)); err != nil {
		t.Fatalf("/join: %v", err)
	}
	if join.called != 1 {
		t.Fatalf("Join 调用数 = %d, want 1", join.called)
	}
	if join.req.Actor != 1001 || join.req.RoomID != "ABC123" {
		t.Errorf("Join 请求 = %+v", join.req)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "加入") {
		t.Errorf("加入回复 = %q, want commands.join_done", sender.sent[0].text)
	}

	sender.sent = nil
	join.called = 0
	if err := h.Handle(context.Background(), commandIn("/join 12", true)); err != nil {
		t.Fatalf("/join(非法): %v", err)
	}
	if join.called != 0 {
		t.Error("非法加入参数不应调用服务")
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "输入无效") {
		t.Errorf("非法输入回复 = %q, want error.invalid_input", sender.sent[0].text)
	}

	// 满员与密码错误反馈。
	sender.sent = nil
	join.err = game.ErrRoomFull
	if err := h.Handle(context.Background(), commandIn("/join abc123", true)); err != nil {
		t.Fatalf("/join(满员): %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "满员") {
		t.Errorf("满员回复 = %q, want commands.room_full", sender.sent[0].text)
	}

	sender.sent = nil
	join.err = game.ErrWrongPassword
	if err := h.Handle(context.Background(), commandIn("/join abc123", true)); err != nil {
		t.Fatalf("/join(密码错误): %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "密码错误") {
		t.Errorf("密码错误回复 = %q, want commands.wrong_password", sender.sent[0].text)
	}
}

// TestCommandsLeave 验证 /leave：调用 LeaveService；无房反馈
// commands.no_room；带参拒绝。
func TestCommandsLeave(t *testing.T) {
	sender := &fakeSender{}
	leave := &fakeLeave{}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, &fakeJoin{}, leave, &fakeRole{}, &fakeScore{})

	if err := h.Handle(context.Background(), commandIn("/leave", true)); err != nil {
		t.Fatalf("/leave: %v", err)
	}
	if leave.called != 1 || leave.actor != 1001 {
		t.Fatalf("Leave 调用 = (%d, %d), want (1, 1001)", leave.called, leave.actor)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "退出") {
		t.Errorf("退出回复 = %q, want commands.leave_done", sender.sent[0].text)
	}

	sender.sent = nil
	leave.err = game.ErrNotInRoom
	if err := h.Handle(context.Background(), commandIn("/leave", true)); err != nil {
		t.Fatalf("/leave(无房): %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "不在任何房间") {
		t.Errorf("无房退出回复 = %q, want commands.no_room", sender.sent[0].text)
	}

	sender.sent = nil
	leave.called = 0
	if err := h.Handle(context.Background(), commandIn("/leave x", true)); err != nil {
		t.Fatalf("/leave(带参): %v", err)
	}
	if leave.called != 0 {
		t.Error("带参 /leave 不应调用服务")
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "输入无效") {
		t.Errorf("带参回复 = %q, want error.invalid_input", sender.sent[0].text)
	}
}

// TestCommandsUnrecognizedText 验证不可识别文本回复 error.invalid_input。
func TestCommandsUnrecognizedText(t *testing.T) {
	sender := &fakeSender{}
	h := newTestCommandsHandler(t, sender, &fakeCreate{}, &fakeJoin{}, &fakeLeave{}, &fakeRole{}, &fakeScore{})
	if err := h.Handle(context.Background(), commandIn("hello world", true)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].text, "输入无效") {
		t.Errorf("回复 = %q, want error.invalid_input", sender.sent[0].text)
	}
}
