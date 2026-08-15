package i18n

import (
	"embed"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"go.yaml.in/yaml/v3"
)

//go:embed locales/*.yaml
var LocaleFS embed.FS

// registerYAMLUnmarshaler 为 go-i18n 注册 YAML 消息文件解码器，
// 使 locales/*.yaml 能被 bundle 加载。
func registerYAMLUnmarshaler(bundle *i18n.Bundle) {
	bundle.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
}
