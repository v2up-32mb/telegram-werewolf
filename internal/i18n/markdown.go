package i18n

import (
	"strings"
	"unicode"
)

// markdownV2Specials 是 Telegram MarkdownV2 需要转义的全部字符，
// 含反斜杠本身（保证字面反斜杠可安全呈现）。
const markdownV2Specials = "_*[]()~`>#+-=|{}.!\\"

// EscapeMarkdownV2 对所有 MarkdownV2 特殊字符加反斜杠转义；
// 除 \n、\t、\r 外的控制字符直接丢弃，避免产生无法发送的控制字符。
// 实现对任意 UTF-8 输入安全（基于 rune 遍历，不 panic）。
func EscapeMarkdownV2(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\n', '\t', '\r':
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if strings.ContainsRune(markdownV2Specials, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
