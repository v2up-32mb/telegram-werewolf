package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testToken = "12345:TEST-TOKEN"

func mustClient(t *testing.T, f *fakeAPI) Client {
	t.Helper()
	c, err := NewClient(testToken, WithServerURL(f.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestClientGetMe(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	f.reset() // 忽略 NewClient 初始化 getMe
	me, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != 12345 || me.Username != "werewolf_test_bot" {
		t.Fatalf("Me = %+v, want id 12345 username werewolf_test_bot", me)
	}
	if recs := f.requestsFor("getMe"); len(recs) != 1 {
		t.Fatalf("getMe requests = %d, want 1", len(recs))
	}
}

func TestClientSendMessage(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	got, err := c.SendMessage(context.Background(), SendMessageParams{
		ChatID: 42, Text: "hello", ParseMode: "MarkdownV2",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got.MessageID != 100 {
		t.Fatalf("MessageID = %d, want 100", got.MessageID)
	}
	recs := f.requestsFor("sendMessage")
	if len(recs) != 1 {
		t.Fatalf("sendMessage requests = %d, want 1", len(recs))
	}
	if recs[0].Form["chat_id"] != "42" || recs[0].Form["text"] != "hello" {
		t.Fatalf("form = %v, want chat_id=42 text=hello", recs[0].Form)
	}
	if recs[0].Form["parse_mode"] != "MarkdownV2" {
		t.Fatalf("parse_mode = %q, want MarkdownV2", recs[0].Form["parse_mode"])
	}
}

func TestClientSendMessageInlineKeyboard(t *testing.T) {
	// B1-c：sendMessage 支持 inline keyboard（reply_markup：按钮 label +
	// callback_data 不透明 token），导演据此下发角色操作按钮。
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	rm := &ReplyMarkup{
		Rows: [][]InlineButton{
			{{Text: "1号", CallbackData: "tok-a"}, {Text: "2号🐺", CallbackData: "tok-b"}, {Text: "3号", CallbackData: "tok-c"}},
			{{Text: "确认选择", CallbackData: "tok-confirm"}},
		},
	}
	if _, err := c.SendMessage(context.Background(), SendMessageParams{
		ChatID: 42, Text: "请选择击杀目标", ReplyMarkup: rm,
	}); err != nil {
		t.Fatalf("SendMessage with ReplyMarkup: %v", err)
	}
	recs := f.requestsFor("sendMessage")
	if len(recs) != 1 {
		t.Fatalf("sendMessage requests = %d, want 1", len(recs))
	}
	raw, ok := recs[0].Form["reply_markup"]
	if !ok {
		t.Fatalf("form 缺 reply_markup，got %v", recs[0].Form)
	}
	for _, want := range []string{"1号", "确认选择", "tok-a", "tok-confirm", "callback_data"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("reply_markup 缺 %q，raw=%s", want, raw)
		}
	}
}

func TestClientEditMessageText(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	if _, err := c.EditMessageText(context.Background(), EditMessageParams{
		ChatID: 42, MessageID: 7, Text: "updated", ParseMode: "MarkdownV2",
	}); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	recs := f.requestsFor("editMessageText")
	if len(recs) != 1 {
		t.Fatalf("editMessageText requests = %d, want 1", len(recs))
	}
	if recs[0].Form["chat_id"] != "42" || recs[0].Form["message_id"] != "7" || recs[0].Form["text"] != "updated" {
		t.Fatalf("form = %v, want chat_id=42 message_id=7 text=updated", recs[0].Form)
	}
}

func TestClientDeleteMessage(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	if err := c.DeleteMessage(context.Background(), DeleteMessageParams{ChatID: 42, MessageID: 7}); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	recs := f.requestsFor("deleteMessage")
	if len(recs) != 1 {
		t.Fatalf("deleteMessage requests = %d, want 1", len(recs))
	}
	if recs[0].Form["chat_id"] != "42" || recs[0].Form["message_id"] != "7" {
		t.Fatalf("form = %v, want chat_id=42 message_id=7", recs[0].Form)
	}
}

func TestClientSendPhotoMarkdownV2Caption(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	if _, err := c.SendPhoto(context.Background(), SendPhotoParams{
		ChatID: 42, FileID: "AgAA-file-id", Caption: "你的身份卡",
	}); err != nil {
		t.Fatalf("SendPhoto: %v", err)
	}
	recs := f.requestsFor("sendPhoto")
	if len(recs) != 1 {
		t.Fatalf("sendPhoto requests = %d, want 1", len(recs))
	}
	if recs[0].Form["caption"] != "你的身份卡" {
		t.Fatalf("caption = %q, want 你的身份卡", recs[0].Form["caption"])
	}
	if recs[0].Form["parse_mode"] != "MarkdownV2" {
		t.Fatalf("parse_mode = %q, want MarkdownV2（sendPhoto Caption 统一 MarkdownV2）", recs[0].Form["parse_mode"])
	}
	if got := strings.Trim(recs[0].Form["photo"], `"`); got != "AgAA-file-id" {
		t.Fatalf("photo = %q, want file_id 透传", recs[0].Form["photo"])
	}
}

func TestClientAnswerCallbackQuery(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	if err := c.AnswerCallbackQuery(context.Background(), AnswerCallbackParams{
		CallbackQueryID: "cq-1", Text: "已确认",
	}); err != nil {
		t.Fatalf("AnswerCallbackQuery: %v", err)
	}
	recs := f.requestsFor("answerCallbackQuery")
	if len(recs) != 1 {
		t.Fatalf("answerCallbackQuery requests = %d, want 1（每次点击都必须应答）", len(recs))
	}
	if recs[0].Form["callback_query_id"] != "cq-1" {
		t.Fatalf("callback_query_id = %q, want cq-1", recs[0].Form["callback_query_id"])
	}
	if v, ok := recs[0].Form["show_alert"]; ok && v != "false" {
		t.Fatalf("show_alert = %q, want false/缺省（顶部通知）", v)
	}
}

func TestClientSetMyCommands(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	f.reset() // 忽略 NewClient 初始化 getMe
	commands := BotCommands()
	if err := c.SetMyCommands(context.Background(), commands); err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	recs := f.requestsFor("setMyCommands")
	if len(recs) != 1 {
		t.Fatalf("setMyCommands requests = %d, want 1", len(recs))
	}
	raw := recs[0].Form["commands"]
	for _, want := range []string{"start", "newgame", "join", "role", "score", "leave", "help", "rank"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("commands JSON 缺 %q, raw=%s", want, raw)
		}
	}
}

func TestClientForbiddenError(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.behavior["sendMessage"] = apiBehavior{ErrorCode: http.StatusForbidden, Description: "Forbidden: bot was blocked by the user"}
	c := mustClient(t, f)
	_, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: 42, Text: "x"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want errors.Is ErrForbidden（403 用户屏蔽）", err)
	}
}

func TestClientBadRequestError(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.behavior["editMessageText"] = apiBehavior{ErrorCode: http.StatusBadRequest, Description: "Bad Request: message is not modified"}
	c := mustClient(t, f)
	_, err := c.EditMessageText(context.Background(), EditMessageParams{ChatID: 42, MessageID: 7, Text: "same"})
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want errors.Is ErrBadRequest（400 不可编辑）", err)
	}
}

func TestClientRateLimitError(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.behavior["sendMessage"] = apiBehavior{ErrorCode: http.StatusTooManyRequests, Description: "Too Many Requests: retry after 3", RetryAfter: 3}
	c := mustClient(t, f)
	_, err := c.SendMessage(context.Background(), SendMessageParams{ChatID: 42, Text: "x"})
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError（429 RetryAfter 可读）", err)
	}
	if rle.RetryAfter != 3*time.Second {
		t.Fatalf("RetryAfter = %v, want 3s", rle.RetryAfter)
	}
}

func TestTransportDispatch(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	tr := NewTransport(c)
	ctx := context.Background()

	cases := []struct {
		op     string
		params Params
		method string
		key    string
		value  string
	}{
		{OpSendText, Params{ChatID: 1, Text: "t"}, "sendMessage", "text", "t"},
		{OpEditMessage, Params{ChatID: 1, MessageID: 2, Text: "e"}, "editMessageText", "message_id", "2"},
		{OpDeleteMessage, Params{ChatID: 1, MessageID: 2}, "deleteMessage", "message_id", "2"},
		{OpSendPhoto, Params{ChatID: 1, FileID: "f", Caption: "c"}, "sendPhoto", "parse_mode", "MarkdownV2"},
		{OpAnswerCallback, Params{ChatID: 1, CallbackQueryID: "cq"}, "answerCallbackQuery", "callback_query_id", "cq"},
	}
	for _, tc := range cases {
		if err := tr.Send(ctx, tc.op, tc.params); err != nil {
			t.Fatalf("Send(%s): %v", tc.op, err)
		}
		recs := f.requestsFor(tc.method)
		if len(recs) == 0 {
			t.Fatalf("op %s: no request to %s", tc.op, tc.method)
		}
		if recs[len(recs)-1].Form[tc.key] != tc.value {
			t.Fatalf("op %s: form[%s] = %q, want %q", tc.op, tc.key, recs[len(recs)-1].Form[tc.key], tc.value)
		}
	}
}

func TestTransportUnknownOperation(t *testing.T) {
	f := newFakeAPI(t, testToken)
	c := mustClient(t, f)
	tr := NewTransport(c)
	f.reset() // 忽略 NewClient 初始化 getMe
	err := tr.Send(context.Background(), "no_such_op", Params{ChatID: 1})
	if err == nil {
		t.Fatal("unknown operation returned nil, want error")
	}
	if recs := f.requests(); len(recs) != 0 {
		t.Fatalf("unknown operation issued %d requests, want 0", len(recs))
	}
}
