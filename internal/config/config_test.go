package config

import (
	"strings"
	"testing"
	"time"
)

// validConfig 返回一个可通过 Validate 的完整配置。
func validConfig() *Config {
	return &Config{
		BotToken:      "123456:test-token",
		DatabasePath:  "data/werewolf.db",
		UpdateMode:    "polling",
		HealthAddress: "",
		LogFormat:     "text",
		DefaultLocale: "zh-CN",
		Outbox: OutboxConfig{
			GlobalRateLimitPerSecond:  20,
			PerChatRateLimitPerSecond: 2,
			SendTimeout:               Duration{Duration: 10 * time.Second},
			RetryInterval:             Duration{Duration: time.Second},
			MaxRetries:                5,
		},
		Webhook: WebhookConfig{Enabled: false},
	}
}

// TestValidate 以表格覆盖校验规则：有效配置及各类非法字段，
// 断言错误信息包含具体字段路径。
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr []string // 错误信息中必须出现的字段路径
	}{
		{"有效配置", func(c *Config) {}, nil},
		{"缺 Token", func(c *Config) { c.BotToken = "" }, []string{"bot_token"}},
		{"缺数据库路径", func(c *Config) { c.DatabasePath = "" }, []string{"database_path"}},
		{"update_mode 为 webhook", func(c *Config) { c.UpdateMode = "webhook" }, []string{"update_mode"}},
		{"未知 update_mode", func(c *Config) { c.UpdateMode = "websocket" }, []string{"update_mode"}},
		{"非法 log_format", func(c *Config) { c.LogFormat = "xml" }, []string{"log_format"}},
		{"非法 default_locale", func(c *Config) { c.DefaultLocale = "en-US" }, []string{"default_locale"}},
		{"非法 health_address", func(c *Config) { c.HealthAddress = "bad-address" }, []string{"health_address"}},
		{"send_timeout 为零", func(c *Config) { c.Outbox.SendTimeout = Duration{} }, []string{"outbox.send_timeout"}},
		{"send_timeout 为负", func(c *Config) { c.Outbox.SendTimeout = Duration{Duration: -time.Second} }, []string{"outbox.send_timeout"}},
		{"retry_interval 为负", func(c *Config) { c.Outbox.RetryInterval = Duration{Duration: -time.Second} }, []string{"outbox.retry_interval"}},
		{"全局限速为负", func(c *Config) { c.Outbox.GlobalRateLimitPerSecond = -1 }, []string{"outbox.global_rate_limit_per_second"}},
		{"单聊限速为负", func(c *Config) { c.Outbox.PerChatRateLimitPerSecond = -1 }, []string{"outbox.per_chat_rate_limit_per_second"}},
		{"max_retries 为负", func(c *Config) { c.Outbox.MaxRetries = -1 }, []string{"outbox.max_retries"}},
		{"webhook 被启用", func(c *Config) { c.Webhook.Enabled = true }, []string{"webhook.enabled"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %v", tt.wantErr)
			}
			for _, field := range tt.wantErr {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("错误信息 %q 缺少字段路径 %q", err.Error(), field)
				}
			}
		})
	}
}
