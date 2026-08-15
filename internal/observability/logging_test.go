package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestNewLoggerInvalidFormat 验证非法日志格式返回明确错误。
func TestNewLoggerInvalidFormat(t *testing.T) {
	if _, err := NewLogger("xml", &bytes.Buffer{}); err == nil {
		t.Fatal("NewLogger(xml) = nil error, want error")
	}
}

// TestNewLoggerText 验证 text handler 输出包含级别、消息与注入字段。
func TestNewLoggerText(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewLogger("text", &buf)
	if err != nil {
		t.Fatalf("NewLogger(text) error = %v, want nil", err)
	}
	logger.Info("hello", "room_id", "r1")
	out := buf.String()
	for _, want := range []string{"level=INFO", "msg=hello", "room_id=r1"} {
		if !strings.Contains(out, want) {
			t.Errorf("text 日志 %q 缺少 %q", out, want)
		}
	}
}

// TestNewLoggerJSON 验证 json handler 输出为合法 JSON 且字段完整。
func TestNewLoggerJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewLogger("json", &buf)
	if err != nil {
		t.Fatalf("NewLogger(json) error = %v, want nil", err)
	}
	logger.Info("hello", "room_id", "r1")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("json 日志不是合法 JSON: %v\n%s", err, buf.String())
	}
	if rec["msg"] != "hello" {
		t.Errorf("json 日志 msg = %v, want hello", rec["msg"])
	}
	if rec["room_id"] != "r1" {
		t.Errorf("json 日志 room_id = %v, want r1", rec["room_id"])
	}
}

// TestSensitiveFieldRedaction 验证 Token/Secret 字段在 text 与 json
// 两种输出中均不泄漏原始值且可见掩码。
func TestSensitiveFieldRedaction(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := NewLogger(format, &buf)
			if err != nil {
				t.Fatalf("NewLogger(%s) error = %v, want nil", format, err)
			}
			logger.Info("start", "bot_token", "123456:ABC-SECRET", "webhook_secret", "wh-s3cret")
			out := buf.String()
			if strings.Contains(out, "123456:ABC-SECRET") {
				t.Errorf("%s 日志泄漏 bot_token 原始值: %s", format, out)
			}
			if strings.Contains(out, "wh-s3cret") {
				t.Errorf("%s 日志泄漏 webhook_secret 原始值: %s", format, out)
			}
			if !strings.Contains(out, "***") {
				t.Errorf("%s 日志未见脱敏掩码: %s", format, out)
			}
		})
	}
}

// TestFieldInjection 验证常用字段（room_id/game_id/phase）注入输出。
func TestFieldInjection(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewLogger("text", &buf)
	if err != nil {
		t.Fatalf("NewLogger(text) error = %v, want nil", err)
	}
	logger.Info("phase started", "room_id", "room-1", "game_id", "g-1", "phase", "night")
	out := buf.String()
	for _, want := range []string{"room_id=room-1", "game_id=g-1", "phase=night"} {
		if !strings.Contains(out, want) {
			t.Errorf("text 日志 %q 缺少注入字段 %q", out, want)
		}
	}
}

// TestSensitiveAnyRedaction 验证 slog.Any 传入的嵌套 map 中敏感键同样被脱敏。
func TestSensitiveAnyRedaction(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := NewLogger(format, &buf)
			if err != nil {
				t.Fatalf("NewLogger(%s) error = %v, want nil", format, err)
			}
			logger.Info("start", slog.Any("meta", map[string]any{
				"ok":             true,
				"webhook_secret": "wh-LEAK",
				"bot_token":      "123456:ANY-LEAK",
			}))
			out := buf.String()
			for _, leaked := range []string{"wh-LEAK", "ANY-LEAK"} {
				if strings.Contains(out, leaked) {
					t.Errorf("%s 日志泄漏 %q 原始值: %s", format, leaked, out)
				}
			}
			if !strings.Contains(out, "***") {
				t.Errorf("%s 日志未见脱敏掩码: %s", format, out)
			}
		})
	}
}

// TestExtendedSensitiveKeys 验证常见密钥键名（Authorization/password/api_key）同样脱敏。
func TestExtendedSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	logger, err := NewLogger("json", &buf)
	if err != nil {
		t.Fatalf("NewLogger(json) error = %v, want nil", err)
	}
	logger.Info("api call",
		"Authorization", "Bearer AUTH-LEAK",
		"password", "pw-LEAK",
		"X-Api-Key", "key-LEAK",
	)
	out := buf.String()
	for _, leaked := range []string{"AUTH-LEAK", "pw-LEAK", "key-LEAK"} {
		if strings.Contains(out, leaked) {
			t.Errorf("json 日志泄漏 %q 原始值: %s", leaked, out)
		}
	}
}

// TestSensitiveRedactionWithWith 验证 logger.With 注入与 WithGroup 嵌套路径仍脱敏。
func TestSensitiveRedactionWithWith(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			logger, err := NewLogger(format, &buf)
			if err != nil {
				t.Fatalf("NewLogger(%s) error = %v, want nil", format, err)
			}
			logger.With("bot_token", "123456:WITH-LEAK").
				WithGroup("meta").
				Info("start", "webhook_secret", "wh-WITH-LEAK")
			out := buf.String()
			for _, leaked := range []string{"WITH-LEAK", "wh-WITH-LEAK"} {
				if strings.Contains(out, leaked) {
					t.Errorf("%s 日志泄漏 %q 原始值: %s", format, leaked, out)
				}
			}
			if !strings.Contains(out, "***") {
				t.Errorf("%s 日志未见脱敏掩码: %s", format, out)
			}
		})
	}
}
