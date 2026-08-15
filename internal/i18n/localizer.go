package i18n

import (
	"fmt"
	"io/fs"

	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

// defaultLocale 是 MVP 唯一交付的语言。
const defaultLocale = "zh-CN"

// newLocalizer 构建 go-i18n Localizer：MVP 只支持 zh-CN，
// 语言资源从内嵌的 locales/*.yaml 加载。
func newLocalizer(locale string) (*i18n.Localizer, error) {
	if locale != defaultLocale {
		return nil, fmt.Errorf("i18n: 不支持的语言 %q，MVP 仅交付 zh-CN", locale)
	}

	bundle := i18n.NewBundle(language.Chinese)
	registerYAMLUnmarshaler(bundle)

	entries, err := fs.Glob(LocaleFS, "locales/*.yaml")
	if err != nil {
		return nil, fmt.Errorf("i18n: 枚举语言资源失败: %w", err)
	}
	for _, name := range entries {
		data, err := fs.ReadFile(LocaleFS, name)
		if err != nil {
			return nil, fmt.Errorf("i18n: 读取语言资源 %s 失败: %w", name, err)
		}
		if _, err := bundle.ParseMessageFileBytes(data, name); err != nil {
			return nil, fmt.Errorf("i18n: 解析语言资源 %s 失败: %w", name, err)
		}
	}

	return i18n.NewLocalizer(bundle, locale), nil
}
