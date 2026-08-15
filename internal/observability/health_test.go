package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestHealthEndpoints 覆盖 /healthz、/readyz 与未知路径的状态码。
func TestHealthEndpoints(t *testing.T) {
	ok := func(context.Context) error { return nil }
	allOK := NewHealthHandler([]Check{
		{Name: "config", Func: ok},
		{Name: "database", Func: ok},
	})
	srv := httptest.NewServer(allOK)
	defer srv.Close()

	if code, body := get(t, srv.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200 (body %q)", code, body)
	}
	if code, body := get(t, srv.URL+"/readyz"); code != http.StatusOK {
		t.Errorf("GET /readyz（全过）= %d, want 200 (body %q)", code, body)
	}
	if code, _ := get(t, srv.URL+"/nope"); code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404", code)
	}

	failDB := NewHealthHandler([]Check{
		{Name: "config", Func: ok},
		{Name: "database", Func: func(context.Context) error { return errors.New("disk full") }},
	})
	srv2 := httptest.NewServer(failDB)
	defer srv2.Close()

	code, body := get(t, srv2.URL+"/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz（数据库失败）= %d, want 503", code)
	}
	if !strings.Contains(body, "database") || !strings.Contains(body, "disk full") {
		t.Errorf("503 响应 %q 缺少失败项/原因", body)
	}

	failTelegram := NewHealthHandler([]Check{
		{Name: "telegram", Func: func(context.Context) error { return errors.New("unauthorized") }},
	})
	srv3 := httptest.NewServer(failTelegram)
	defer srv3.Close()
	if code, body := get(t, srv3.URL+"/readyz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "telegram") {
		t.Errorf("GET /readyz（telegram 失败）= %d body %q, want 503 且含 telegram", code, body)
	}
}

// TestMetrics 覆盖 counters/gauges 的线程安全语义与预定义指标名。
func TestMetrics(t *testing.T) {
	m := NewMetrics()
	m.IncCounter(MetricTelegramSent)
	m.IncCounter(MetricTelegramSent)
	m.AddCounter(MetricRateLimited429, 3)
	m.SetGauge(MetricActiveRooms, 6)
	m.SetGauge(MetricOutboxQueueLength, 3)

	counters, gauges := m.Snapshot()
	if counters[MetricTelegramSent] != 2 {
		t.Errorf("telegram_messages_sent = %d, want 2", counters[MetricTelegramSent])
	}
	if counters[MetricRateLimited429] != 3 {
		t.Errorf("telegram_429_count = %d, want 3", counters[MetricRateLimited429])
	}
	if gauges[MetricActiveRooms] != 6 {
		t.Errorf("active_rooms = %d, want 6", gauges[MetricActiveRooms])
	}
	if gauges[MetricOutboxQueueLength] != 3 {
		t.Errorf("outbox_queue_length = %d, want 3", gauges[MetricOutboxQueueLength])
	}

	// 并发 smoke：8 goroutine 各 1000 次递增，最终计数必须准确（race 检测由 CI 承担）。
	const workers, perWorker = 8, 1000
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				m.IncCounter(MetricTelegramFailed)
			}
		}()
	}
	wg.Wait()
	counters2, _ := m.Snapshot()
	if want := int64(workers * perWorker); counters2[MetricTelegramFailed] != want {
		t.Errorf("telegram_messages_failed = %d, want %d", counters2[MetricTelegramFailed], want)
	}
}

// TestReadyzShortCircuit 验证首个 checker 失败后立即短路，不再执行后续 checker。
func TestReadyzShortCircuit(t *testing.T) {
	var calls []string
	h := NewHealthHandler([]Check{
		{Name: "a", Func: func(context.Context) error {
			calls = append(calls, "a")
			return errors.New("boom")
		}},
		{Name: "b", Func: func(context.Context) error {
			calls = append(calls, "b")
			return nil
		}},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	code, body := get(t, srv.URL+"/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz（首个失败）= %d, want 503 (body %q)", code, body)
	}
	if !strings.Contains(body, "a") || !strings.Contains(body, "boom") {
		t.Errorf("503 响应 %q 缺少首个失败项/原因", body)
	}
	if len(calls) != 1 || calls[0] != "a" {
		t.Errorf("未按失败即短路执行：calls = %v, want [a]", calls)
	}
}

// TestNewHealthHandlerRejectsNilCheck 验证构造时对 nil Func 立即报错（fail fast）。
func TestNewHealthHandlerRejectsNilCheck(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewHealthHandler 含 nil Func 未 panic")
		}
	}()
	NewHealthHandler([]Check{{Name: "db", Func: nil}})
}
