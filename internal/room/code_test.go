package room

import (
	"errors"
	"math/rand"
	"sync"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// errScriptedRNGExhausted 表示固定序列随机源已耗尽。
var errScriptedRNGExhausted = errors.New("scripted rng exhausted")

// scriptedRNG 按固定序列返回 Intn 结果，测试可控复现房间码。
type scriptedRNG struct {
	mu   sync.Mutex
	vals []int
	i    int
}

func newScriptedRNG(vals ...int) *scriptedRNG { return &scriptedRNG{vals: vals} }

func (r *scriptedRNG) Intn(n int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.i >= len(r.vals) {
		return 0, errScriptedRNGExhausted
	}
	v := r.vals[r.i]
	r.i++
	return v % n, nil
}

// TestCodeAlphabetExcludesConfusables 验证房间码字符集不含易混淆字符
// 0/O、1/I（docs/游戏流程设计.md §一.5；internal/game/id.go RoomID 注释）。
func TestCodeAlphabetExcludesConfusables(t *testing.T) {
	for _, c := range "OI01" {
		for i := 0; i < len(roomCodeAlphabet); i++ {
			if roomCodeAlphabet[i] == byte(c) {
				t.Errorf("alphabet 含易混淆字符 %q", c)
			}
		}
	}
}

// TestGenerateCodeLengthAndAlphabet 验证生成码长度与字符集合法性。
func TestGenerateCodeLengthAndAlphabet(t *testing.T) {
	rng := newScriptedRNG(0, 1, 2, 3, 4, 5)
	code, err := GenerateCode(rng, RoomCodeLength)
	if err != nil {
		t.Fatalf("GenerateCode error = %v, want nil", err)
	}
	if len(code) != RoomCodeLength {
		t.Errorf("code 长度 = %d, want %d", len(code), RoomCodeLength)
	}
	if !ValidRoomCode(code) {
		t.Errorf("code %q 未通过 ValidRoomCode", code)
	}
}

// TestGenerateCodeExcludesConfusableChars 验证固定 RNG 输出下生成码仍
// 不含 0/O、1/I。
func TestGenerateCodeExcludesConfusableChars(t *testing.T) {
	rng := newScriptedRNG(0, 0, 0, 0, 0, 0) // 恒定取字符集首位
	code, err := GenerateCode(rng, RoomCodeLength)
	if err != nil {
		t.Fatalf("GenerateCode error = %v, want nil", err)
	}
	for _, ch := range code {
		switch ch {
		case '0', 'O', '1', 'I':
			t.Errorf("code %q 含易混淆字符 %q", code, ch)
		}
	}
}

// TestGenerateCodeInvalidLength 验证非法长度的明确错误。
func TestGenerateCodeInvalidLength(t *testing.T) {
	if _, err := GenerateCode(newScriptedRNG(0), 0); err == nil {
		t.Error("length 0 应返回错误")
	}
	if _, err := GenerateCode(newScriptedRNG(0), -1); err == nil {
		t.Error("负 length 应返回错误")
	}
}

// TestGenerateCodePropagatesRNGError 验证 RNG 失败时错误向上传播。
func TestGenerateCodePropagatesRNGError(t *testing.T) {
	if _, err := GenerateCode(newScriptedRNG(0, 1, 2), RoomCodeLength); !errors.Is(err, errScriptedRNGExhausted) {
		t.Fatalf("GenerateCode err = %v, want errScriptedRNGExhausted", err)
	}
}

// TestValidRoomCode 验证校验函数拒绝错误长度与非法字符。
func TestValidRoomCode(t *testing.T) {
	if !ValidRoomCode("ABCDEF") {
		t.Error("ValidRoomCode(ABCDEF) = false, want true")
	}
	for _, bad := range []game.RoomID{"ABCDE", "0ABCDE", "OABCDE", "1ABCDE", "IABCDE", "abcdef", "ABCD EF"} {
		if ValidRoomCode(bad) {
			t.Errorf("ValidRoomCode(%q) = true, want false", bad)
		}
	}
}

// pseudoRNG 用固定种子的标准库伪随机源模拟均匀分布 RNG（仅测试用，
// 覆盖并发创建场景；docs/技术选型.md §5.2 允许测试使用脚本化随机源）。
type pseudoRNG struct {
	mu sync.Mutex
	r  *rand.Rand
}

func newPseudoRNG(seed int64) *pseudoRNG {
	return &pseudoRNG{r: rand.New(rand.NewSource(seed))}
}

func (p *pseudoRNG) Intn(n int) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.r.Intn(n), nil
}

// repeatVals 构造 length 个 value 的固定序列（用于恒定 RNG 场景）。
func repeatVals(value, length int) []int {
	vals := make([]int, length)
	for i := range vals {
		vals[i] = value
	}
	return vals
}
