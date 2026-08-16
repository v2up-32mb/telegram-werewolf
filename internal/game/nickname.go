package game

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// 昵称领域规则的哨兵错误（docs/游戏流程设计.md §二.2 游戏昵称）。
var (
	// ErrNicknameInvalid 表示昵称不符合 2～10 字符及允许字符集规则。
	ErrNicknameInvalid = errors.New("game: invalid nickname")
	// ErrNicknameTaken 表示昵称在房间内已被占用（大小写无关唯一）。
	ErrNicknameTaken = errors.New("game: nickname already taken in room")
	// ErrNicknameLocked 表示游戏开始（发牌）后昵称已锁定不可修改。
	ErrNicknameLocked = errors.New("game: nickname locked after dealing started")
	// ErrNicknameExhausted 表示随机昵称冲突重生重试耗尽。
	ErrNicknameExhausted = errors.New("game: failed to allocate unique nickname")
)

// MinNicknameRunes / MaxNicknameRunes 是昵称长度边界（docs §二.2）：
// 2～10 个字符。
const (
	MinNicknameRunes = 2
	MaxNicknameRunes = 10
)

// NormalizeNickname 对输入做 Unicode NFKC 规范化并剥离首尾 ASCII 空格
// （docs §二.2：输入先做 Unicode NFKC 规范化）。NFKC 把全角字母数字
// 折叠为半角、全角空格折叠为半角空格，保证「ＡＢＣ」与「ABC」等价的
// 唯一性语义；换行、制表符等其他空白保留，由 ValidateNickname
// 按「不允许换行/控制字符」规则拒绝。
func NormalizeNickname(raw string) string {
	return strings.Trim(norm.NFKC.String(raw), " ")
}

// isAllowedNicknameRune 报告单个 rune 是否属于昵称允许字符集：
// 中文汉字（CJK 统一表意文字基本区）、英文字母、数字；
// 拒绝空格、换行、标点、Emoji、控制字符与零宽字符。
func isAllowedNicknameRune(r rune) bool {
	if r >= 0x4E00 && r <= 0x9FFF {
		return true // CJK 统一表意文字基本区
	}
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	return false
}

// ValidateNickname 校验昵称：先 NFKC 规范化，再检查长度
// （2～10 个 rune）与允许字符集。返回规范化后的昵称（显示保留
// 英文字母原始大小写——本函数不做折叠）。
func ValidateNickname(raw string) (string, error) {
	nick := NormalizeNickname(raw)
	n := utf8.RuneCountInString(nick)
	if n < MinNicknameRunes || n > MaxNicknameRunes {
		return "", fmt.Errorf("game: nickname length %d outside %d..%d: %w",
			n, MinNicknameRunes, MaxNicknameRunes, ErrNicknameInvalid)
	}
	for _, r := range nick {
		if !isAllowedNicknameRune(r) {
			return "", fmt.Errorf("game: nickname contains disallowed character %q: %w", r, ErrNicknameInvalid)
		}
	}
	return nick, nil
}

// FoldNickname 返回昵称的唯一性比较键：英文字母统一为小写
// （大小写无关唯一，docs §二.2），数字与中文保持不变。
// 入参应为 ValidateNickname 的产物（已完成 NFKC 规范化）。
func FoldNickname(nickname string) string {
	return strings.ToLower(nickname)
}

// NicknameGenerator 是随机中文昵称生成器 seam（docs §二.2：
// 默认昵称采用「中文形容词＋动物/物品」组合，冲突时重新生成）。
type NicknameGenerator interface {
	Generate() (string, error)
}

// randomNicknameAdjectives 是随机昵称的形容词词库。
var randomNicknameAdjectives = []string{
	"快乐", "傲娇", "机智", "温柔", "活泼", "神秘", "勇敢", "安静",
	"淘气", "高冷", "软萌", "帅气", "可爱", "霸气", "元气", "沉稳",
	"灵动", "腼腆", "果敢", "俏皮",
}

// randomNicknameNouns 是随机昵称的动物/物品词库。
var randomNicknameNouns = []string{
	"小猫", "狐狸", "兔子", "熊猫", "老虎", "小鹿", "海豚", "刺猬",
	"仓鼠", "白鹭", "锦鲤", "松鼠", "猎豹", "蜜獾", "树懒", "鲸鱼",
	"驯鹿", "水獭", "信鸽", "羚羊",
}

// RandomChineseNickname 使用注入 RNG 从内联词库生成
// 「中文形容词＋动物/物品」组合昵称（4 个汉字，符合 2～10 字符）。
// rng 为空时使用 crypto/rand 实现。
func RandomChineseNickname(rng RNG) (string, error) {
	if rng == nil {
		rng = CryptoRNG{}
	}
	ai, err := rng.Intn(len(randomNicknameAdjectives))
	if err != nil {
		return "", fmt.Errorf("game: pick nickname adjective: %w", err)
	}
	ni, err := rng.Intn(len(randomNicknameNouns))
	if err != nil {
		return "", fmt.Errorf("game: pick nickname noun: %w", err)
	}
	return randomNicknameAdjectives[ai] + randomNicknameNouns[ni], nil
}

// GenerateUniqueNickname 用生成器产出昵称并检查占用（fold 键）：
// 冲突（或生成器产出非法昵称）时重新生成，最多尝试 maxTries 次；
// 耗尽返回 ErrNicknameExhausted。
func GenerateUniqueNickname(gen NicknameGenerator, taken func(folded string) bool, maxTries int) (string, error) {
	if gen == nil {
		return "", errors.New("game: unique nickname requires a generator")
	}
	if maxTries <= 0 {
		return "", ErrNicknameExhausted
	}
	for i := 0; i < maxTries; i++ {
		raw, err := gen.Generate()
		if err != nil {
			return "", fmt.Errorf("game: generate nickname: %w", err)
		}
		nick, err := ValidateNickname(raw)
		if err != nil {
			continue // 生成器产出非法昵称：重试
		}
		if !taken(FoldNickname(nick)) {
			return nick, nil
		}
	}
	return "", ErrNicknameExhausted
}

// defaultNicknameMaxTries 是随机昵称冲突重生的默认重试上限。
const defaultNicknameMaxTries = 8

// nicknameGeneratorFunc 把函数适配为 NicknameGenerator。
type nicknameGeneratorFunc func() (string, error)

func (f nicknameGeneratorFunc) Generate() (string, error) { return f() }

// randNicknameGenerator 返回基于注入 RNG 的默认昵称生成器。
func randNicknameGenerator(rng RNG) NicknameGenerator {
	return nicknameGeneratorFunc(func() (string, error) {
		return RandomChineseNickname(rng)
	})
}

// NicknameStore 是昵称修改持久化/唯一性的领域 seam：装载房间阶段
// （开局锁定判定）、房间内占用昵称与保存。
type NicknameStore interface {
	// LoadRoomPhase 返回房间当前阶段；PhaseLobby 之外昵称锁定。
	LoadRoomPhase(ctx context.Context, roomID RoomID) (Phase, error)
	// ReservedNicknames 返回房间内已占用昵称的 fold 键集合。
	ReservedNicknames(ctx context.Context, roomID RoomID) (map[string]bool, error)
	// SetNickname 持久化玩家昵称。
	SetNickname(ctx context.Context, roomID RoomID, user UserID, nickname string) error
}

// SetNickname 执行昵称修改领域流程（docs §二.2）：
//  1. 开局锁定：房间阶段非 PhaseLobby（发牌后）一律 ErrNicknameLocked；
//  2. 校验（NFKC、2～10 字符、允许字符集）；
//  3. 房间内唯一（大小写无关）；
//  4. 持久化。
//
// 返回规范化后的昵称（保留英文字母原始大小写，供显示）。
func SetNickname(ctx context.Context, store NicknameStore, roomID RoomID, user UserID, raw string) (string, error) {
	phase, err := store.LoadRoomPhase(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("game: load room phase %q: %w", roomID, err)
	}
	if phase != PhaseLobby {
		return "", ErrNicknameLocked
	}
	nick, err := ValidateNickname(raw)
	if err != nil {
		return "", err
	}
	reserved, err := store.ReservedNicknames(ctx, roomID)
	if err != nil {
		return "", fmt.Errorf("game: load reserved nicknames of room %q: %w", roomID, err)
	}
	if reserved[FoldNickname(nick)] {
		return "", ErrNicknameTaken
	}
	if err := store.SetNickname(ctx, roomID, user, nick); err != nil {
		return "", fmt.Errorf("game: save nickname of room %q: %w", roomID, err)
	}
	return nick, nil
}
