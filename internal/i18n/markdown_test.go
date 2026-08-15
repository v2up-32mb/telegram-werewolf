package i18n

import "testing"

// TestEscapeMarkdownV2 覆盖 MarkdownV2 全部特殊字符、中文昵称、URL、反引号、
// 玩家自由文本与控制字符处理。
func TestEscapeMarkdownV2(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"空字符串", "", ""},
		{"普通中文昵称", "稳重狐狸", "稳重狐狸"},
		{"中文昵称含下划线", "稳重_狐狸", "稳重\\_狐狸"},
		{"全部特殊字符", "_*[]()~`>#+-=|{}.!\\",
			"\\_\\*\\[\\]\\(\\)\\~\\`\\>\\#\\+\\-\\=\\|\\{\\}\\.\\!\\\\"},
		{"URL", "https://example.com/a_(b).html",
			"https://example\\.com/a\\_\\(b\\)\\.html"},
		{"括号与下划线", "(text)_", "\\(text\\)\\_"},
		{"反引号", "`code`", "\\`code\\`"},
		{"玩家自由文本", "我选了 3号 player!", "我选了 3号 player\\!"},
		{"保留换行制表回车", "a\nb\tc\rd", "a\nb\tc\rd"},
		{"丢弃其他控制字符", "a\x00b\x01c\x7fd\te", "abcd\te"},
		{"Emoji 与私密标记示例", "2号🐺 · 🟢 好人", "2号🐺 · 🟢 好人"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeMarkdownV2(tt.in)
			if got != tt.want {
				t.Errorf("EscapeMarkdownV2(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
