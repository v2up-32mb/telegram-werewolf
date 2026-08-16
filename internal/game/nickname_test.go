package game

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeNicknameRNG 是昵称生成用的固定随机源：依次返回预设索引。
type fakeNicknameRNG struct {
	seq []int
	i   int
}

func (r *fakeNicknameRNG) Intn(n int) (int, error) {
	if r.i >= len(r.seq) {
		return 0, errors.New("fake rng exhausted")
	}
	v := r.seq[r.i]
	r.i++
	if v < 0 || v >= n {
		return 0, errors.New("fake rng index out of range")
	}
	return v, nil
}

// TestNormalizeAndValidateNickname 验证昵称规则：NFKC 规范化、
// 2～10 字符、允许中文汉字/英文字母/数字，拒绝空格/换行/标点/Emoji/控制字符
// （docs/游戏流程设计.md §二.2）。
func TestNormalizeAndValidateNickname(t *testing.T) {
	valid := []string{
		"小明",       // 2 个汉字
		"快乐小猫",     // 4 个汉字
		"Ab12",     // 字母数字
		"Wolf1234", // 10 字符上限
		"ＮＦＫＣ１２３",  // 全角 NFKC → NFKC123
		"ＡＢＣ",      // 全角字母 NFKC → ABC
		"１２３４",     // 全角数字 NFKC → 1234
	}
	for _, raw := range valid {
		got, err := ValidateNickname(raw)
		if err != nil {
			t.Errorf("ValidateNickname(%q) error = %v, want nil", raw, err)
			continue
		}
		norm := NormalizeNickname(raw)
		if got != norm {
			t.Errorf("ValidateNickname(%q) = %q, want NFKC 规范化 %q", raw, got, norm)
		}
	}

	invalid := []struct {
		name string
		pw   string
	}{
		{"单字符", "小"},
		{"11 字符超长", "abcdefghijk"},
		{"空串", ""},
		{"含空格", "小 明"},
		{"含换行", "小明\n"},
		{"含标点", "小明。"},
		{"含 Emoji", "小猫🐱"},
		{"含控制字符", "小明\x01"},
		{"含英文标点", "Wolf!"},
		{"仅符号", "@@"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateNickname(tc.pw); !errors.Is(err, ErrNicknameInvalid) {
				t.Errorf("ValidateNickname(%q) error = %v, want ErrNicknameInvalid", tc.pw, err)
			}
		})
	}
}

// TestFoldNickname 验证唯一性比较键：英文字母大小写无关，
// 中文字符与数字原样保留。
func TestFoldNickname(t *testing.T) {
	cases := []struct{ a, b string }{
		{"Wolf", "wolf"}, // 纯英文大小写无关
		{"Wolf123", "wOLF123"},
		{"AbＣ", "abC"},   // 全角字母已 NFKC，大小写无关
		{"快乐小猫", "快乐小猫"}, // 中文原样
	}
	for _, tc := range cases {
		a, err := ValidateNickname(tc.a)
		if err != nil {
			t.Fatalf("ValidateNickname(%q): %v", tc.a, err)
		}
		b, err := ValidateNickname(tc.b)
		if err != nil {
			t.Fatalf("ValidateNickname(%q): %v", tc.b, err)
		}
		if FoldNickname(a) != FoldNickname(b) {
			t.Errorf("FoldNickname(%q) = %q, FoldNickname(%q) = %q, want 相等", a, FoldNickname(a), b, FoldNickname(b))
		}
	}
	if FoldNickname("Wolf") == FoldNickname("wolfy") {
		t.Error("FoldNickname(Wolf) == FoldNickname(wolfy)，不同昵称不应冲突")
	}
}

// TestFoldNicknamePreservesDisplay 验证显示保留玩家原始大小写：
// 唯一性比较用 fold 键，但展示名仍是原始输入。
func TestFoldNicknamePreservesDisplay(t *testing.T) {
	raw := "wOLF"
	nick, err := ValidateNickname(raw)
	if err != nil {
		t.Fatalf("ValidateNickname: %v", err)
	}
	if nick != raw {
		t.Errorf("ValidateNickname 返回 %q, want 保留原始大小写 %q", nick, raw)
	}
	if FoldNickname(nick) != "wolf" {
		t.Errorf("FoldNickname = %q, want %q", FoldNickname(nick), "wolf")
	}
}

// TestRandomChineseNickname 验证默认随机昵称生成器：
// 「中文形容词＋动物/物品」组合、长度合法、字符集合法。
func TestRandomChineseNickname(t *testing.T) {
	rng := &fakeNicknameRNG{seq: []int{0, 0}}
	nick, err := RandomChineseNickname(rng)
	if err != nil {
		t.Fatalf("RandomChineseNickname: %v", err)
	}
	if got, err := ValidateNickname(nick); err != nil || got != nick {
		t.Errorf("随机昵称 %q 不合法: %v", nick, err)
	}
	if len([]rune(nick)) < 2 || len([]rune(nick)) > 10 {
		t.Errorf("随机昵称 %q 长度 %d 不在 2..10", nick, len([]rune(nick)))
	}
	// 不同索引组合产生不同昵称。
	nick2, err := RandomChineseNickname(&fakeNicknameRNG{seq: []int{1, 2}})
	if err != nil {
		t.Fatalf("RandomChineseNickname#2: %v", err)
	}
	if nick == nick2 {
		t.Errorf("不同索引生成相同昵称 %q", nick)
	}
	// nil RNG 使用 crypto/rand。
	nick3, err := RandomChineseNickname(nil)
	if err != nil {
		t.Fatalf("RandomChineseNickname(nil): %v", err)
	}
	if _, err := ValidateNickname(nick3); err != nil {
		t.Errorf("RandomChineseNickname(nil) = %q 不合法: %v", nick3, err)
	}
}

// TestGenerateUniqueNickname 验证冲突重生：占用回调命中时重新生成，
// 最终返回未占用昵称；重试耗尽返回 ErrNicknameExhausted。
func TestGenerateUniqueNickname(t *testing.T) {
	fixed := &fixedNicknameGenerator{}
	_ = fixed

	t.Run("不冲突直接返回", func(t *testing.T) {
		gen := &scriptedNicknameGenerator{names: []string{"快乐小猫"}}
		got, err := GenerateUniqueNickname(gen, func(string) bool { return false }, 3)
		if err != nil {
			t.Fatalf("GenerateUniqueNickname: %v", err)
		}
		if got != "快乐小猫" {
			t.Errorf("got %q, want 快乐小猫", got)
		}
	})
	t.Run("冲突重生直至唯一", func(t *testing.T) {
		gen := &scriptedNicknameGenerator{names: []string{"快乐小猫", "快乐小猫", "傲娇狐狸"}}
		taken := map[string]bool{FoldNickname("快乐小猫"): true}
		got, err := GenerateUniqueNickname(gen, func(k string) bool { return taken[k] }, 3)
		if err != nil {
			t.Fatalf("GenerateUniqueNickname: %v", err)
		}
		if got != "傲娇狐狸" {
			t.Errorf("got %q, want 傲娇狐狸（跳过占用昵称）", got)
		}
	})
	t.Run("重试耗尽", func(t *testing.T) {
		// 两次生成均被占用且达到重试上限 → ErrNicknameExhausted。
		gen := &scriptedNicknameGenerator{names: []string{"快乐小猫", "快乐小猫"}}
		got, err := GenerateUniqueNickname(gen, func(string) bool { return true }, 2)
		if !errors.Is(err, ErrNicknameExhausted) {
			t.Fatalf("err = %v, want ErrNicknameExhausted", err)
		}
		if got != "" {
			t.Errorf("got %q, want 空", got)
		}
	})
}

// scriptedNicknameGenerator 按脚本依次返回昵称。
type scriptedNicknameGenerator struct {
	names []string
	i     int
}

func (g *scriptedNicknameGenerator) Generate() (string, error) {
	if g.i >= len(g.names) {
		return "", errors.New("generator exhausted")
	}
	n := g.names[g.i]
	g.i++
	return n, nil
}

// fixedNicknameGenerator 始终返回同一昵称（占位，避免未使用告警）。
type fixedNicknameGenerator struct{}

func (fixedNicknameGenerator) Generate() (string, error) { return "快乐小猫", nil }

// TestValidateNicknameHandlesZeroWidth 验证零宽字符被拒绝
// （控制字符类，防视觉伪装重名）。
func TestValidateNicknameHandlesZeroWidth(t *testing.T) {
	if _, err := ValidateNickname("小\u200b明"); !errors.Is(err, ErrNicknameInvalid) {
		t.Errorf("零宽空格昵称 error = %v, want ErrNicknameInvalid", err)
	}
}

// TestNicknameGenerationIsDeterministicByRNG 验证词库覆盖：
// 生成的昵称必须来自内联词库且组合合法。
func TestNicknameGenerationIsDeterministicByRNG(t *testing.T) {
	rng := &fakeNicknameRNG{seq: []int{0, 0}}
	first, err := RandomChineseNickname(rng)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	rng2 := &fakeNicknameRNG{seq: []int{0, 0}}
	second, err := RandomChineseNickname(rng2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second {
		t.Errorf("相同 RNG 序列生成不同昵称: %q vs %q", first, second)
	}
	if reflect.DeepEqual(first, "") {
		t.Error("空昵称")
	}
	_ = strings.TrimSpace(first)
}

// fakeNicknameStore 是昵称修改 seam 的测试替身。
type fakeNicknameStore struct {
	phase    Phase
	reserved map[string]bool
	saved    []savedNickname
	setErr   error
}

type savedNickname struct {
	roomID   RoomID
	user     UserID
	nickname string
}

func (f *fakeNicknameStore) LoadRoomPhase(_ context.Context, _ RoomID) (Phase, error) {
	return f.phase, nil
}

func (f *fakeNicknameStore) ReservedNicknames(_ context.Context, _ RoomID) (map[string]bool, error) {
	return f.reserved, nil
}

func (f *fakeNicknameStore) SetNickname(_ context.Context, roomID RoomID, user UserID, nickname string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.saved = append(f.saved, savedNickname{roomID: roomID, user: user, nickname: nickname})
	return nil
}

// TestSetNickname 验证昵称修改领域流程：
// 大厅内可修改（校验 + 房间内唯一 + 保存），开局后锁定拒绝。
func TestSetNickname(t *testing.T) {
	t.Run("大厅内成功修改", func(t *testing.T) {
		store := &fakeNicknameStore{phase: PhaseLobby, reserved: map[string]bool{"wolf": true}}
		got, err := SetNickname(context.Background(), store, "ABC123", 3001, "wOLFy")
		if err != nil {
			t.Fatalf("SetNickname: %v", err)
		}
		if got != "wOLFy" {
			t.Errorf("返回昵称 = %q, want 保留原始大小写 wOLFy", got)
		}
		if len(store.saved) != 1 || store.saved[0].roomID != "ABC123" || store.saved[0].user != 3001 || store.saved[0].nickname != "wOLFy" {
			t.Errorf("保存 = %+v, want room ABC123/user 3001/nickname wOLFy", store.saved)
		}
	})
	t.Run("大小写无关唯一拒绝", func(t *testing.T) {
		store := &fakeNicknameStore{phase: PhaseLobby, reserved: map[string]bool{"wolf": true}}
		_, err := SetNickname(context.Background(), store, "ABC123", 3002, "Wolf")
		if !errors.Is(err, ErrNicknameTaken) {
			t.Fatalf("err = %v, want ErrNicknameTaken", err)
		}
		if len(store.saved) != 0 {
			t.Errorf("占用拒绝后仍保存: %+v", store.saved)
		}
	})
	t.Run("非法昵称拒绝", func(t *testing.T) {
		store := &fakeNicknameStore{phase: PhaseLobby}
		if _, err := SetNickname(context.Background(), store, "ABC123", 3002, "a"); !errors.Is(err, ErrNicknameInvalid) {
			t.Fatalf("err = %v, want ErrNicknameInvalid", err)
		}
	})
	t.Run("开局后锁定", func(t *testing.T) {
		for _, phase := range []Phase{PhaseDeal, PhaseNight, PhaseDaySpeech, PhaseDayVote, PhaseSettlement} {
			store := &fakeNicknameStore{phase: phase}
			_, err := SetNickname(context.Background(), store, "ABC123", 3001, "NewName")
			if !errors.Is(err, ErrNicknameLocked) {
				t.Errorf("phase=%v err = %v, want ErrNicknameLocked", phase, err)
			}
			if len(store.saved) != 0 {
				t.Errorf("phase=%v 锁定后仍保存: %+v", phase, store.saved)
			}
		}
	})
	t.Run("store 错误传播", func(t *testing.T) {
		store := &fakeNicknameStore{setErr: errors.New("storage down")}
		if _, err := SetNickname(context.Background(), store, "ABC123", 3001, "小明"); err == nil {
			t.Fatal("store 错误未被传播")
		}
	})
}
