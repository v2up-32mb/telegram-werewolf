package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// 环境变量键名；敏感值只通过环境变量注入，不写入 YAML。
const (
	EnvBotToken      = "TELEGRAM_BOT_TOKEN"
	EnvWebhookSecret = "TELEGRAM_WEBHOOK_SECRET"
	// EnvBotAPIBaseURL 允许用环境变量覆盖 Bot API 基址（价值/接入点可配置，
	// 无需代理的内网中转服务；空则官方 api.telegram.org）。
	EnvBotAPIBaseURL = "TELEGRAM_BOT_API_BASE_URL"
)

// Load 按 默认值 → YAML 配置 → 环境变量覆盖 的顺序加载配置。
//
// path 为空表示不使用配置文件；文件缺失或解析失败时返回指明路径的错误。
// lookupEnv 以函数注入环境变量读取，便于测试控制。
// Load 只负责加载，不负责校验；启动校验由 (*Config).Validate 负责。
func Load(path string, lookupEnv func(string) (string, bool)) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("config: 读取配置文件失败 (%s): %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("config: 解析配置文件失败 (%s): %w", path, err)
		}
	}

	if v, ok := lookupEnv(EnvBotToken); ok {
		cfg.BotToken = v
	}
	if v, ok := lookupEnv(EnvWebhookSecret); ok {
		cfg.Webhook.Secret = v
	}
	if v, ok := lookupEnv(EnvBotAPIBaseURL); ok {
		cfg.BotAPIBaseURL = v
	}

	return &cfg, nil
}
