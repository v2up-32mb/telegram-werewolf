package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		// 遵守真实 Telegram 的 offset 语义：已确认（update_id < offset）的
		// update 不再下发。否则库的下一轮轮询会重复投递已确认 update，
		// 污染 cancel 语义测试（CI race job 曾因此 flake）。
		out := updates
		if off, err := strconv.ParseInt(form["offset"], 10, 64); err == nil && off > 0 {
			kept := make([]json.RawMessage, 0, len(updates))
			for _, u := range updates {
				var id struct {
					UpdateID int64 `json:"update_id"`
				}
				if json.Unmarshal(u, &id) != nil || id.UpdateID >= off {
					kept = append(kept, u)
				}
			}
			out = kept
		}
		writeAPIResult(w, out)
	case "sendMessage", "editMessageText", "sendPhoto":
		writeAPIResult(w, map[string]any{
			"message_id": 100,
			"chat":       map[string]any{"id": 1, "type": "private"},
			"text":       form["text"],
		})
	case "deleteMessage", "answerCallbackQuery":
		writeAPIResult(w, true)
	case "setMyCommands":
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

// TestFakeAPIHonorsOffset 锁定 fake Telegram 的 offset 确认语义：
// update_id < offset 的已确认 update 不再下发（与真实 API 一致）。
// 这是 TestSourceStopsOnContextCancel 不 flake 的基础。
func TestFakeAPIHonorsOffset(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.setUpdates(messageUpdate(1, "/start", 1, 2), messageUpdate(2, "/newgame", 1, 2))

	body := func(offset string) []int64 {
		t.Helper()
		var form map[string][]string
		if offset != "" {
			form = map[string][]string{"offset": {offset}}
		}
		resp, err := http.PostForm(f.URL+"/bot"+testToken+"/getUpdates", form)
		if err != nil {
			t.Fatalf("getUpdates: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Result []struct {
				UpdateID int64 `json:"update_id"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		ids := make([]int64, 0, len(out.Result))
		for _, u := range out.Result {
			ids = append(ids, u.UpdateID)
		}
		return ids
	}

	if ids := body(""); len(ids) != 2 {
		t.Fatalf("无 offset 时 ids = %v, want [1 2]", ids)
	}
	if ids := body("2"); len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("offset=2 时 ids = %v, want [2]（已确认的 1 不重发）", ids)
	}
	if ids := body("3"); len(ids) != 0 {
		t.Fatalf("offset=3 时 ids = %v, want []", ids)
	}
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
