package telegram

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"
)

func messageUpdate(id int64, text string, chatID, userID int64) map[string]any {
	return map[string]any{
		"update_id": id,
		"message": map[string]any{
			"message_id": 1,
			"chat":       map[string]any{"id": chatID},
			"from":       map[string]any{"id": userID},
			"text":       text,
		},
	}
}

func callbackUpdate(id int64, cqID, data string, chatID, userID, messageID int64) map[string]any {
	return map[string]any{
		"update_id": id,
		"callback_query": map[string]any{
			"id":   cqID,
			"from": map[string]any{"id": userID},
			"message": map[string]any{
				"message_id": messageID,
				"date":       1700000000,
				"chat":       map[string]any{"id": chatID},
			},
			"data": data,
		},
	}
}

func TestSourcePreservesUpdateOrderAndMapsDTO(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.setUpdates(
		messageUpdate(101, "/start", 1001, 2001),
		callbackUpdate(102, "cq-9", "vote:3", 1001, 2001, 5),
	)
	src, err := NewLongPollingSource(testToken, 0, WithSourceServerURL(f.URL))
	if err != nil {
		t.Fatalf("NewLongPollingSource: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	var got []Update
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case u := <-src.Updates():
			got = append(got, u)
		case err := <-src.Errors():
			t.Fatalf("unexpected source error: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for 2 updates, got %d", len(got))
		}
	}

	if got[0].UpdateID != 101 || got[1].UpdateID != 102 {
		t.Fatalf("update order = [%d %d], want [101 102]（update_id 保序）", got[0].UpdateID, got[1].UpdateID)
	}
	if got[0].ReceivedAt.IsZero() {
		t.Fatal("ReceivedAt 为零值：解码后必须立即记录 ReceivedAt")
	}
	if d := time.Since(got[0].ReceivedAt); d < 0 || d > 5*time.Second {
		t.Fatalf("ReceivedAt = %v, 不在合理窗口内", got[0].ReceivedAt)
	}
	m := got[0].Message
	if m == nil || m.ChatID != 1001 || m.UserID != 2001 || m.Text != "/start" {
		t.Fatalf("message DTO = %+v, want chat 1001 user 2001 text /start", m)
	}
	cq := got[1].CallbackQuery
	if cq == nil || cq.ID != "cq-9" || cq.UserID != 2001 || cq.ChatID != 1001 || cq.MessageID != 5 || cq.Data != "vote:3" {
		t.Fatalf("callback DTO = %+v, want 完整字段映射", cq)
	}
}

func TestSourceConflict409Reported(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.setGetUpdatesBehavior(apiBehavior{ErrorCode: http.StatusConflict, Description: "Conflict: terminated by other getUpdates request"})
	src, err := NewLongPollingSource(testToken, 0, WithSourceServerURL(f.URL))
	if err != nil {
		t.Fatalf("NewLongPollingSource: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src.Start(ctx)

	select {
	case err := <-src.Errors():
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("conflict error = %v, want errors.Is ErrConflict（409 双实例冲突必须被识别）", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for conflict error")
	}
}

func TestSourceStopsOnContextCancel(t *testing.T) {
	f := newFakeAPI(t, testToken)
	f.setUpdates(messageUpdate(1, "/start", 1, 2))
	src, err := NewLongPollingSource(testToken, 0, WithSourceServerURL(f.URL))
	if err != nil {
		t.Fatalf("NewLongPollingSource: %v", err)
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	src.Start(ctx)
	select {
	case u := <-src.Updates():
		if u.UpdateID != 1 {
			t.Fatalf("UpdateID = %d, want 1", u.UpdateID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first update")
	}
	cancel()

	// 等待 Updates 通道关闭（Source 停止）。
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-src.Updates():
			if !ok {
				goto stopped
			}
			t.Fatal("unexpected update after cancel")
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("Updates 通道未在 ctx 取消后关闭")
		}
	}
stopped:
	// 等待 goroutine 收敛。
	deadline2 := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline2) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}
