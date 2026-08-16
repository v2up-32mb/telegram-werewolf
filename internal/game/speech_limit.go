package game

import "errors"

// 发言限制纯组件（docs 游戏流程设计.md §发言限制 1）：
//   - 单条发言上限 50 单位（中英混算：汉字=1、英文单词=1、标点也算，
//     空白不计数）；
//   - 单个连续 ASCII 英文 token 最多 20 个字母，达到 21 个及以上则
//     提示并拒绝整条发言转播；
//   - 一个发言回合内最多 5 条，后续消息拒绝转发。
//
// 本组件是纯函数/纯计数器：不持有 State，由接线层按回合注入。

const (
	// SpeechMaxUnits 是单条发言上限（单位）。
	SpeechMaxUnits = 50
	// SpeechMaxPerRound 是一个发言回合内最多转播条数。
	SpeechMaxPerRound = 5
	// SpeechMaxTokenASCII 是单个连续 ASCII 字母 token 的长度上限
	//（达到 21 个及以上整条拒绝）。
	SpeechMaxTokenASCII = 20
)

// ErrSpeechRoundFull 表示本回合已发满 SpeechMaxPerRound 条。
var ErrSpeechRoundFull = errors.New("game: speech round full")

// isCJK 报告 rune 是否属于 CJK 统一表意文字（按“汉字=1 单位”计费）。
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

// isASCIILetter 报告 rune 是否为 ASCII 英文字母。
func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// isASCIIWordRune 报告 rune 是否属于 ASCII 单词字符（字母或数字；
// 数字并入英文单词的连续段计费）。
func isASCIIWordRune(r rune) bool {
	return isASCIILetter(r) || (r >= '0' && r <= '9')
}

// CountSpeechUnits 计算一条发言的“单位数”（docs §发言限制 1）：
// 汉字每字 1 单位；连续 ASCII 字母/数字单词每词 1 单位；标点等非空白
// 符号每字符 1 单位；空白不计数。
func CountSpeechUnits(text string) int {
	units := 0
	inWord := false
	for _, r := range text {
		switch {
		case isCJK(r):
			inWord = false
			units++
		case isASCIIWordRune(r):
			if !inWord {
				inWord = true
				units++
			}
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			inWord = false
		default:
			inWord = false
			units++
		}
	}
	return units
}

// LongestASCIIToken 返回文本中单个连续 ASCII 英文字母 token 的最大长度
// （数字与标点会断开 token）。
func LongestASCIIToken(text string) int {
	longest := 0
	cur := 0
	for _, r := range text {
		if isASCIILetter(r) {
			cur++
			if cur > longest {
				longest = cur
			}
			continue
		}
		cur = 0
	}
	return longest
}

// CheckSpeechAccept 判定一条发言是否可转播：
//   - 返回实际单位数与是否通过；
//   - 单位数 > SpeechMaxUnits 或最长连续 ASCII token >= 21 时 ok=false
//     （docs §发言限制 1：达到 21 个及以上提示并拒绝整条）。
func CheckSpeechAccept(text string) (units int, ok bool) {
	units = CountSpeechUnits(text)
	if units > SpeechMaxUnits {
		return units, false
	}
	if LongestASCIIToken(text) >= SpeechMaxTokenASCII+1 {
		return units, false
	}
	return units, true
}

// RoundCounter 是“一个发言回合内最多 N 条”的纯计数器（默认
// SpeechMaxPerRound）。由接线层在每个发言回合重新构造；Used 只统计
// 已成功转播的条数（超长/超条拒绝不增加）。
//
// 非并发安全：按 docs 技术选型.md §6.1「同房间严格有序」，计数器归
// 房间 Actor 单 goroutine 持有，外部不得并发调用。
type RoundCounter struct {
	Used int
	Max  int
}

// NewRoundCounter 创建计数上限 max 的回合计数器。
func NewRoundCounter(max int) *RoundCounter {
	return &RoundCounter{Max: max}
}

// CanSend 报告本回合是否还能转播（Used < Max）。
func (c *RoundCounter) CanSend() bool {
	return c.Used < c.Max
}

// Count 记录一条成功转播；已满时返回 ErrSpeechRoundFull 且不修改计数。
func (c *RoundCounter) Count() error {
	if !c.CanSend() {
		return ErrSpeechRoundFull
	}
	c.Used++
	return nil
}
