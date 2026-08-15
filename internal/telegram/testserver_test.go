package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// requestRecord 记录一次发往 Fake Telegram API 的请求（multipart form 文本字段）。
type requestRecord struct {
	Method string
	Path   string
	Form   map[string]string
}

// apiBehavior 描述某端点的错误响应（Telegram 库按响应体 error_code 判定错误）。
type apiBehavior struct {
	ErrorCode   int
	Description string
	RetryAfter  int
}

// fakeAPI 是可配置的 httptest Telegram Bot API。
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	token    string
	records  []requestRecord
	updates  []json.RawMessage
	behavior map[string]apiBehavior
}

func newFakeAPI(t *testing.T, token string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		token:    token,
		behavior: make(map[string]apiBehavior),
	}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sep := strings.LastIndex(r.URL.Path, "/")
	if sep <= 0 {
		http.NotFound(w, r)
		return
	}
	method := r.URL.Path[sep+1:]
	_ = r.ParseMultipartForm(1 << 20)
	form := make(map[string]string)
	for k, vs := range r.PostForm {
		if len(vs) > 0 {
			form[k] = vs[0]
		}
	}

	f.mu.Lock()
	beh := f.behavior[method]
	updates := append([]json.RawMessage(nil), f.updates...)
	f.mu.Unlock()
	f.record(method, r.URL.Path, form)

	if beh.ErrorCode != 0 {
		writeAPIError(w, beh)
		return
	}
	switch method {
	case "getMe":
		writeAPIResult(w, map[string]any{
			"id": 12345, "is_bot": true, "first_name": "Werewolf", "username": "werewolf_test_bot",
		})
	case "getUpdates":
		writeAPIResult(w, updates)
	case "sendMessage", "editMessageText", "sendPhoto":
		writeAPIResult(w, map[string]any{
			"message_id": 100,
			"chat":       map[string]any{"id": 1, "type": "private"},
			"text":       form["text"],
		})
	case "deleteMessage", "answerCallbackQuery":
		writeAPIResult(w, true)
	default:
		http.Error(w, "unknown method "+method, http.StatusBadRequest)
	}
}

func (f *fakeAPI) record(method, path string, form map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, requestRecord{Method: method, Path: path, Form: form})
}

func (f *fakeAPI) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = nil
}

func (f *fakeAPI) requests() []requestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]requestRecord, len(f.records))
	copy(out, f.records)
	return out
}

func (f *fakeAPI) requestsFor(method string) []requestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []requestRecord
	for _, rec := range f.records {
		if rec.Method == method {
			out = append(out, rec)
		}
	}
	return out
}

func (f *fakeAPI) setUpdates(updates ...map[string]any) {
	raw := make([]json.RawMessage, 0, len(updates))
	for _, u := range updates {
		b, _ := json.Marshal(u)
		raw = append(raw, b)
	}
	f.mu.Lock()
	f.updates = raw
	f.mu.Unlock()
}

func (f *fakeAPI) setGetUpdatesBehavior(beh apiBehavior) {
	f.mu.Lock()
	f.behavior["getUpdates"] = beh
	f.mu.Unlock()
}

func writeAPIResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func writeAPIError(w http.ResponseWriter, beh apiBehavior) {
	body := map[string]any{
		"ok":          false,
		"error_code":  beh.ErrorCode,
		"description": beh.Description,
	}
	if beh.ErrorCode == http.StatusTooManyRequests {
		body["parameters"] = map[string]any{"retry_after": beh.RetryAfter}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(beh.ErrorCode)
	_ = json.NewEncoder(w).Encode(body)
}
