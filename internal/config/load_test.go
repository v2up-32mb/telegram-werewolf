package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envWithToken 提供 TELEGRAM_BOT_TOKEN，供无需 Secret 的加载测试使用。
func envWithToken(key string) (string, bool) {
	if key == EnvBotToken {
		return "token-from-env", true
	}
	return "", false
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入临时文件失败: %v", err)
	}
}

func yamlPath(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, content)
	return path
}

// TestLoadDefaults 验证无配置文件时返回默认值，Token 来自环境变量。
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("", envWithToken)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BotToken != "token-from-env" {
		t.Errorf("BotToken = %q, want token-from-env", cfg.BotToken)
	}
	if cfg.DatabasePath != "data/werewolf.db" {
		t.Errorf("DatabasePath = %q, want data/werewolf.db", cfg.DatabasePath)
	}
	if cfg.UpdateMode != "polling" {
		t.Errorf("UpdateMode = %q, want polling", cfg.UpdateMode)
	}
	if cfg.HealthAddress != "" {
		t.Errorf("HealthAddress = %q, want 空（禁用）", cfg.HealthAddress)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
	if cfg.DefaultLocale != "zh-CN" {
		t.Errorf("DefaultLocale = %q, want zh-CN", cfg.DefaultLocale)
	}
	if want := (Duration{Duration: 10 * time.Second}); cfg.Outbox.SendTimeout != want {
		t.Errorf("Outbox.SendTimeout = %v, want %v", cfg.Outbox.SendTimeout, want)
	}
	if want := (Duration{Duration: time.Second}); cfg.Outbox.RetryInterval != want {
		t.Errorf("Outbox.RetryInterval = %v, want %v", cfg.Outbox.RetryInterval, want)
	}
	if cfg.Outbox.MaxRetries != 5 {
		t.Errorf("Outbox.MaxRetries = %d, want 5", cfg.Outbox.MaxRetries)
	}
	if cfg.Outbox.GlobalRateLimitPerSecond != 20 {
		t.Errorf("Outbox.GlobalRateLimitPerSecond = %d, want 20", cfg.Outbox.GlobalRateLimitPerSecond)
	}
	if cfg.Outbox.PerChatRateLimitPerSecond != 2 {
		t.Errorf("Outbox.PerChatRateLimitPerSecond = %d, want 2", cfg.Outbox.PerChatRateLimitPerSecond)
	}
	if cfg.Webhook.Enabled {
		t.Error("Webhook.Enabled = true, want false")
	}
}

// TestLoadFromYAML 验证 YAML 显式字段生效、未写字段保持默认。
func TestLoadFromYAML(t *testing.T) {
	path := yamlPath(t, `
database_path: /tmp/custom.db
update_mode: polling
health_address: "127.0.0.1:9090"
log_format: json
default_locale: zh-CN
outbox:
  global_rate_limit_per_second: 30
  per_chat_rate_limit_per_second: 3
  send_timeout: 15s
  retry_interval: 2s
  max_retries: 7
`)
	cfg, err := Load(path, envWithToken)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BotToken != "token-from-env" {
		t.Errorf("BotToken = %q, want token-from-env", cfg.BotToken)
	}
	if cfg.DatabasePath != "/tmp/custom.db" {
		t.Errorf("DatabasePath = %q, want /tmp/custom.db", cfg.DatabasePath)
	}
	if cfg.HealthAddress != "127.0.0.1:9090" {
		t.Errorf("HealthAddress = %q, want 127.0.0.1:9090", cfg.HealthAddress)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
	if cfg.Outbox.GlobalRateLimitPerSecond != 30 {
		t.Errorf("GlobalRateLimitPerSecond = %d, want 30", cfg.Outbox.GlobalRateLimitPerSecond)
	}
	if want := (Duration{Duration: 15 * time.Second}); cfg.Outbox.SendTimeout != want {
		t.Errorf("SendTimeout = %v, want %v", cfg.Outbox.SendTimeout, want)
	}
	if want := (Duration{Duration: 2 * time.Second}); cfg.Outbox.RetryInterval != want {
		t.Errorf("RetryInterval = %v, want %v", cfg.Outbox.RetryInterval, want)
	}
	if cfg.Outbox.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want 7", cfg.Outbox.MaxRetries)
	}
}

// TestLoadEnvOverride 验证环境变量覆盖 YAML 中的敏感值。
func TestLoadEnvOverride(t *testing.T) {
	path := yamlPath(t, "bot_token: fake-token-in-yaml\nwebhook:\n  secret: fake-secret-in-yaml\n")
	lookup := func(key string) (string, bool) {
		switch key {
		case EnvBotToken:
			return "token-from-env", true
		case EnvWebhookSecret:
			return "secret-from-env", true
		}
		return "", false
	}
	cfg, err := Load(path, lookup)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BotToken != "token-from-env" {
		t.Errorf("BotToken = %q, want token-from-env（环境变量覆盖 YAML）", cfg.BotToken)
	}
	if cfg.Webhook.Secret != "secret-from-env" {
		t.Errorf("Webhook.Secret = %q, want secret-from-env（环境变量覆盖 YAML）", cfg.Webhook.Secret)
	}
}

// TestLoadMissingFile 验证指向不存在文件时报错且包含路径。
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	_, err := Load(path, envWithToken)
	if err == nil {
		t.Fatal("Load() = nil error, want 读取失败错误")
	}
	if !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("错误 %q 缺少文件路径", err.Error())
	}
}

// TestLoadInvalidYAML 验证 YAML 语法错误时返回解析错误。
func TestLoadInvalidYAML(t *testing.T) {
	path := yamlPath(t, "bot_token: [unclosed\n")
	_, err := Load(path, envWithToken)
	if err == nil {
		t.Fatal("Load() = nil error, want 解析失败错误")
	}
}

// TestLoadInvalidDuration 验证 YAML 中非法时长字符串返回包含字段的错误。
func TestLoadInvalidDuration(t *testing.T) {
	path := yamlPath(t, "outbox:\n  send_timeout: not-a-duration\n")
	_, err := Load(path, envWithToken)
	if err == nil {
		t.Fatal("Load() = nil error, want 时长解析失败错误")
	}
	// yaml.v3 的自定义解码器错误不带字段名；Load 解析期错误指明非法值本身，
	// 字段级错误（含 outbox.send_timeout）由 Validate() 保证，见 TestValidate。
	if !strings.Contains(err.Error(), "not-a-duration") {
		t.Errorf("错误 %q 缺少非法时长值 not-a-duration", err.Error())
	}
}

// TestLoadBotAPIBaseURLFromEnv 是缺陷回归（红测）：生产必须支持自定义
// Bot API 基址（Task 46 无代理直连 tg-api.510222.xyz）——此前配置无该
// 字段，TELEGRAM_BOT_API_BASE_URL 被忽略，bot 只能走默认 api.telegram.org
// 并经系统代理长轮询（代理挂起导致收不到更新）。
func TestLoadBotAPIBaseURLFromEnv(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key == EnvBotToken {
			return "token-from-env", true
		}
		if key == EnvBotAPIBaseURL {
			return "https://tg-api.510222.xyz", true
		}
		return "", false
	}
	cfg, err := Load("", lookup)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BotAPIBaseURL != "https://tg-api.510222.xyz" {
		t.Errorf("BotAPIBaseURL = %q, want https://tg-api.510222.xyz（env 覆盖）", cfg.BotAPIBaseURL)
	}
}
