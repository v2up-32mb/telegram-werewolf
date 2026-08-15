package i18n

import (
	"testing"
	"unicode"
)

// FuzzEscapeMarkdownV2 保证任意 UTF-8 输入经转义后不 panic，
// 且输出不含除换行/制表/回车外的未转义控制字符。
func FuzzEscapeMarkdownV2(f *testing.F) {
	for _, seed := range []string{
		"",
		"稳重狐狸",
		"_*[]()~`>#+-=|{}.!\\",
		"https://example.com/a_(b).html",
		"a\x00b\x01c\x7fd\te\n",
		"🐺 狼人 · 2号「稳重狐狸」",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		out := EscapeMarkdownV2(string(input))
		for _, r := range out {
			if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
				t.Fatalf("输出 %q 包含未转义控制字符 %q", out, r)
			}
		}
	})
}
