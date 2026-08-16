package game

import (
	"errors"
	"testing"
)

// TestCountSpeechUnitsChinese 验证汉字=1 单位、标点也计 1（docs §发言限制 1）。
func TestCountSpeechUnitsChinese(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"四字", "你好世界", 4},
		{"带标点", "你好，世界！", 6},
		{"重复标点", "！！！", 3},
		{"空白不计数", "  你好  世界  ", 4},
		{"空串", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountSpeechUnits(tc.text); got != tc.want {
				t.Errorf("CountSpeechUnits(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestCountSpeechUnitsEnglish 验证英文单词=1 单位、标点也计 1。
func TestCountSpeechUnitsEnglish(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"两词", "hello world", 2},
		{"带标点", "hello, world!", 4},
		{"带数字空格分隔", "go 1 25 rocks", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CountSpeechUnits(tc.text); got != tc.want {
				t.Errorf("CountSpeechUnits(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestCountSpeechUnitsMixed 验证中英混算。
func TestCountSpeechUnitsMixed(t *testing.T) {
	text := "你好 abc 世界"
	if got := CountSpeechUnits(text); got != 5 {
		t.Errorf("CountSpeechUnits(%q) = %d, want 5", text, got)
	}
}

// TestLongestASCIIToken 验证连续 ASCII 字母 token 最大长度（数字断开）。
func TestLongestASCIIToken(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"普通词", "hello", 5},
		{"数字断开", "abc123def", 3},
		{"标点断开", "abc,def", 3},
		{"混合文本", "你好 abcdefghij kl", 10},
		{"空串", "", 0},
		{"中文连续", "啦啦啦", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LongestASCIIToken(tc.text); got != tc.want {
				t.Errorf("LongestASCIIToken(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

// TestCheckSpeechAccept 验证 50 单位上限与 20/21 字母 token 边界
// （docs §发言限制 1：达到 21 个及以上拒绝整条）。
func TestCheckSpeechAccept(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		units int
		ok    bool
	}{
		{"50 字恰好", chineseN(50), 50, true},
		{"51 字超长", chineseN(51), 51, false},
		{"20 字母 token 可接受（1 个英文单词=1 单位）", wordN(20), 1, true},
		{"21 字母 token 拒绝（1 个英文单词=1 单位）", wordN(21), 1, false},
		{"中英混算 50 内", "你好 world 世界啊", 6, true},
		{"空串", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			units, ok := CheckSpeechAccept(tc.text)
			if units != tc.units || ok != tc.ok {
				t.Errorf("CheckSpeechAccept(%q) = (%d,%v), want (%d,%v)", tc.text, units, ok, tc.units, tc.ok)
			}
		})
	}
}

// TestRoundCounter 验证回合内最多 5 条，第 6 条拒绝（docs §发言限制 1）。
func TestRoundCounter(t *testing.T) {
	c := NewRoundCounter(SpeechMaxPerRound)
	for i := 0; i < SpeechMaxPerRound; i++ {
		if !c.CanSend() {
			t.Fatalf("第 %d 条 CanSend = false", i+1)
		}
		if err := c.Count(); err != nil {
			t.Fatalf("第 %d 条 Count error = %v", i+1, err)
		}
	}
	if c.CanSend() {
		t.Error("第 6 条 CanSend = true, want false")
	}
	if err := c.Count(); !errors.Is(err, ErrSpeechRoundFull) {
		t.Fatalf("第 6 条 Count err = %v, want ErrSpeechRoundFull", err)
	}
	if c.Used != SpeechMaxPerRound {
		t.Errorf("Used = %d, want %d", c.Used, SpeechMaxPerRound)
	}
}

// chineseN 返回 n 个「字」组成的纯中文字符串。
func chineseN(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = '字'
	}
	return string(b)
}

// wordN 返回 n 个连续小写字母 ASCII token。
func wordN(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a' + byte(i%26)
	}
	return string(b)
}
