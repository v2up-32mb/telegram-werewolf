package i18n

import (
	"fmt"

	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// SafeMarkdown 包装已经安全或有意保留的 MarkdownV2 内容；
// 作为模板参数传入 Render 时不再转义。
type SafeMarkdown string

// Renderer 是用户可见文本的唯一渲染入口：
// 模板参数默认全部经 EscapeMarkdownV2 转义，只有 SafeMarkdown 可绕过。
type Renderer struct {
	localizer *i18n.Localizer
}

// NewRenderer 以指定语言构建渲染器；MVP 仅支持 zh-CN。
func NewRenderer(locale string) (*Renderer, error) {
	localizer, err := newLocalizer(locale)
	if err != nil {
		return nil, err
	}
	return &Renderer{localizer: localizer}, nil
}

// Render 按 messageKey 取文案并代入模板数据渲染。
// 缺失 key 或渲染失败时返回错误；不 panic。
func (r *Renderer) Render(messageKey string, data map[string]any) (string, error) {
	escaped := make(map[string]any, len(data))
	for k, v := range data {
		if sm, ok := v.(SafeMarkdown); ok {
			escaped[k] = string(sm)
			continue
		}
		escaped[k] = EscapeMarkdownV2(fmt.Sprint(v))
	}
	return r.localizer.Localize(&i18n.LocalizeConfig{
		MessageID:    messageKey,
		TemplateData: escaped,
	})
}

// RenderPlainText 按 messageKey 取文案并原样返回（不转义、不代入模板）。
// 适用于 setMyCommands 等不需要 MarkdownV2 转义的场景。
func (r *Renderer) RenderPlainText(messageKey string) (string, error) {
	return r.localizer.Localize(&i18n.LocalizeConfig{
		MessageID: messageKey,
	})
}
