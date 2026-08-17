// Package config 提供应用配置的加载与启动校验。
package config

import (
	"errors"
	"fmt"
	"net"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config 汇总应用启动所需的全部配置。
//
// 敏感值（BotToken、Webhook.Secret）不写入 YAML，
// 由环境变量 TELEGRAM_BOT_TOKEN / TELEGRAM_WEBHOOK_SECRET 提供并覆盖。
type Config struct {
	BotToken string `yaml:"bot_token"`
	// BotAPIBaseURL 是 Telegram Bot API 基址；空表示官方
	// https://api.telegram.org。生产可用 env TELEGRAM_BOT_API_BASE_URL
	// 指向无需代理的中转服务（如 https://tg-api.510222.xyz，Task 46
	// 冒烟：代理长轮询挂起导致收不到更新，改直连中转服务）。
	BotAPIBaseURL string        `yaml:"api_base_url"`
	DatabasePath  string        `yaml:"database_path"`
	UpdateMode    string        `yaml:"update_mode"`
	HealthAddress string        `yaml:"health_address"`
	LogFormat     string        `yaml:"log_format"`
	DefaultLocale string        `yaml:"default_locale"`
	Outbox        OutboxConfig  `yaml:"outbox"`
	Webhook       WebhookConfig `yaml:"webhook"`
}

// OutboxConfig 控制进程内 Outbox 的限速、超时与重试参数。
type OutboxConfig struct {
	GlobalRateLimitPerSecond  int      `yaml:"global_rate_limit_per_second"`
	PerChatRateLimitPerSecond int      `yaml:"per_chat_rate_limit_per_second"`
	SendTimeout               Duration `yaml:"send_timeout"`
	RetryInterval             Duration `yaml:"retry_interval"`
	MaxRetries                int      `yaml:"max_retries"`
}

// WebhookConfig 仅预留接入边界；MVP 禁止启用（Enabled 必须为 false）。
type WebhookConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	Secret  string `yaml:"secret"`
}

// Duration 支持在 YAML 中以 Go 时长字符串（如 "10s"、"2m"）配置 time.Duration。
type Duration struct {
	time.Duration
}

// UnmarshalYAML 将标量节点按字符串解析为 time.ParseDuration 可接受的格式。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("非法时长 %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

// defaults 返回 MVP 起步默认值；后续任务可按集成测试结果固化。
func defaults() Config {
	return Config{
		DatabasePath:  "data/werewolf.db",
		UpdateMode:    "polling",
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

// Validate 校验关键配置；每条错误均指明具体字段路径与修复提示。
func (c *Config) Validate() error {
	var errs []error

	if c.BotToken == "" {
		errs = append(errs, fieldError("bot_token", "不能为空；请通过环境变量 TELEGRAM_BOT_TOKEN 提供"))
	}
	if c.DatabasePath == "" {
		errs = append(errs, fieldError("database_path", "不能为空"))
	}
	switch c.UpdateMode {
	case "polling":
	case "webhook":
		errs = append(errs, fieldError("update_mode", "webhook 仅预留、MVP 禁止启用；当前仅支持 polling"))
	default:
		errs = append(errs, fieldError("update_mode", fmt.Sprintf("未知取值 %q；当前仅支持 polling", c.UpdateMode)))
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		errs = append(errs, fieldError("log_format", fmt.Sprintf("未知取值 %q；当前仅支持 text/json", c.LogFormat)))
	}
	if c.DefaultLocale != "zh-CN" {
		errs = append(errs, fieldError("default_locale", fmt.Sprintf("MVP 仅支持 zh-CN，当前为 %q", c.DefaultLocale)))
	}
	if c.HealthAddress != "" {
		if _, _, err := net.SplitHostPort(c.HealthAddress); err != nil {
			errs = append(errs, fieldError("health_address", fmt.Sprintf("必须是 host:port，当前 %q 无法解析", c.HealthAddress)))
		}
	}
	if c.Outbox.SendTimeout.Duration <= 0 {
		errs = append(errs, fieldError("outbox.send_timeout", "必须大于 0"))
	}
	if c.Outbox.RetryInterval.Duration < 0 {
		errs = append(errs, fieldError("outbox.retry_interval", "不能为负"))
	}
	if c.Outbox.GlobalRateLimitPerSecond < 0 {
		errs = append(errs, fieldError("outbox.global_rate_limit_per_second", "不能为负"))
	}
	if c.Outbox.PerChatRateLimitPerSecond < 0 {
		errs = append(errs, fieldError("outbox.per_chat_rate_limit_per_second", "不能为负"))
	}
	if c.Outbox.MaxRetries < 0 {
		errs = append(errs, fieldError("outbox.max_retries", "不能为负"))
	}
	if c.Webhook.Enabled {
		errs = append(errs, fieldError("webhook.enabled", "MVP 禁止启用 Webhook；请保持 false 并使用 polling"))
	}

	return errors.Join(errs...)
}

func fieldError(field, msg string) error {
	return fmt.Errorf("config: %s: %s", field, msg)
}
