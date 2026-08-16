package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// SpeechMode 是发言限时模式（docs/游戏流程设计.md「6 人局默认配置总表」4：
// 默认「固定限时」，可配置固定/软限时）。
type SpeechMode int

const (
	SpeechUnknown SpeechMode = iota
	SpeechFixed              // 固定限时（默认）
	SpeechSoft               // 软限时
)

// Valid 报告发言模式是否为合法值。
func (m SpeechMode) Valid() bool {
	return m >= SpeechFixed && m <= SpeechSoft
}

// String 返回发言模式的英文短名，供日志与错误消息使用。
func (m SpeechMode) String() string {
	switch m {
	case SpeechFixed:
		return "fixed"
	case SpeechSoft:
		return "soft"
	default:
		return "unknown"
	}
}

// SettingsUpdatedMessageKey 是配置保存成功通知房主的消息 key
// （后续渲染任务按 key + params 产出 MarkdownV2 文案）。
const SettingsUpdatedMessageKey = "settings.updated"

// MVP 默认时长（docs「6 人局默认配置总表」5/6/7）。
const (
	DefaultSpeechSeconds     = 60
	DefaultWolfNightSeconds  = 30
	DefaultOtherNightSeconds = 15
	// MinFastModeSeconds 是快速模式减半后的最短时长下限（docs「快速模式取整」2）。
	MinFastModeSeconds = 5
)

// RoomSettings 是建房后房主可修改的房间配置快照（docs「6 人局默认配置总表」、
// 「狼人空刀」、「房间设置修改截止」）。MVP 仅覆盖已敲定且当前阶段需要的配置项。
type RoomSettings struct {
	SpeechMode              SpeechMode
	SpeechSeconds           int
	WolfNightSeconds        int
	OtherNightSeconds       int
	FastMode                bool
	Victory                 VictoryMode
	WitchSelfSaveFirstNight bool // 女巫首夜自救（默认 true＝可自救）
	RevealRoleOnDeath       bool // 死讯是否公开身份（默认 false＝不报身份）
	WolfMustKill            bool // 狼人空刀：true＝必须刀人（默认）；false＝允许空刀
}

// DefaultRoomSettings 返回 MVP 默认房间配置（docs「6 人局默认配置总表」）：
// 固定限时 60 秒、狼人夜间 30 秒、其他角色夜间 15 秒、屠城、首夜可自救、
// 不报身份、必须刀人、快速模式关闭。
func DefaultRoomSettings() RoomSettings {
	return RoomSettings{
		SpeechMode:              SpeechFixed,
		SpeechSeconds:           DefaultSpeechSeconds,
		WolfNightSeconds:        DefaultWolfNightSeconds,
		OtherNightSeconds:       DefaultOtherNightSeconds,
		FastMode:                false,
		Victory:                 VictorySlaughter,
		WitchSelfSaveFirstNight: true,
		RevealRoleOnDeath:       false,
		WolfMustKill:            true,
	}
}

// EffectiveDurations 返回生效时长（秒）：标准模式=原值；快速模式=减半后
// 奇数秒向上取整（如 7.5→8 秒）且最短不低于 5 秒（docs「快速模式取整」）。
// 返回值依次为 发言/狼人夜间/其他角色夜间。
func (s RoomSettings) EffectiveDurations() (speech, wolf, other int) {
	if !s.FastMode {
		return s.SpeechSeconds, s.WolfNightSeconds, s.OtherNightSeconds
	}
	return fastDuration(s.SpeechSeconds), fastDuration(s.WolfNightSeconds), fastDuration(s.OtherNightSeconds)
}

// fastDuration 把标准时长换算为快速模式时长：减半后奇数秒向上取整，
// 且最短不低于 5 秒。
func fastDuration(seconds int) int {
	half := (seconds + 1) / 2
	if half < MinFastModeSeconds {
		return MinFastModeSeconds
	}
	return half
}

// Validate 校验配置边界：发言模式与胜负模式合法、三组时长均为正
// （快速模式取整与 5 秒下限由 EffectiveDurations 恒满足）；任一违反返回错误。
func (s RoomSettings) Validate() error {
	if !s.SpeechMode.Valid() {
		return fmt.Errorf("game: unsupported speech mode %v", s.SpeechMode)
	}
	if s.SpeechSeconds <= 0 || s.WolfNightSeconds <= 0 || s.OtherNightSeconds <= 0 {
		return fmt.Errorf("game: room settings durations must be positive (speech=%d wolf=%d other=%d)",
			s.SpeechSeconds, s.WolfNightSeconds, s.OtherNightSeconds)
	}
	if !s.Victory.Valid() {
		return fmt.Errorf("game: unsupported victory mode %v", s.Victory)
	}
	return nil
}

// 房间配置领域规则的哨兵错误（docs「密码」、「房间设置修改截止」）。
var (
	// ErrSettingsLocked 表示发牌开始后配置修改被拒绝。
	ErrSettingsLocked = errors.New("game: room settings locked after dealing started")
	// ErrPasswordInvalid 表示密码不符合 4～16 位英文字母或数字规则。
	ErrPasswordInvalid = errors.New("game: invalid room password")
)

// ValidatePassword 校验房间密码：4～16 位英文字母或数字，区分大小写；
// 不允许空格、中文和特殊符号（docs/游戏流程设计.md §密码）。合法返回 nil。
func ValidatePassword(pw string) error {
	if len(pw) < 4 || len(pw) > 16 {
		return fmt.Errorf("game: password length %d outside 4..16: %w", len(pw), ErrPasswordInvalid)
	}
	for i := 0; i < len(pw); i++ {
		c := pw[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			continue
		}
		return fmt.Errorf("game: password contains non-alphanumeric character: %w", ErrPasswordInvalid)
	}
	return nil
}

// HashPassword 使用 bcrypt 计算密码哈希（docs/技术选型.md §密码安全；
// 明文只在本函数内出现，此后全程只传递哈希，绝不下落持久化层）。
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("game: hash password: %w", err)
	}
	return string(b), nil
}

// VerifyPassword 校验明文与 bcrypt 哈希是否匹配。
func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// MarshalSettings 把设置快照序列化为 JSON（持久化 / 后续「再来一局」沿用）。
func MarshalSettings(s RoomSettings) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("game: marshal settings: %w", err)
	}
	return string(b), nil
}

// UnmarshalSettings 反序列化设置快照；非法 JSON 返回明确错误。
// 合法值域校验由调用方按需执行 Validate。
func UnmarshalSettings(raw string) (RoomSettings, error) {
	var out RoomSettings
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return RoomSettings{}, fmt.Errorf("game: unmarshal settings: %w", err)
	}
	return out, nil
}

// SettingsRepository 是房间配置持久化的领域 seam（docs/技术选型.md §5.1：
// 核心不触碰 SQLite）。生产实现由 storage 适配（后续接线任务注入），
// 本任务测试注入替身。
type SettingsRepository interface {
	// SaveSettings 持久化设置快照与密码哈希。只接收哈希，绝不接收明文。
	SaveSettings(ctx context.Context, roomID RoomID, settings RoomSettings, passwordHash string) error
	// LoadPasswordHash 读取当前密码哈希；房间不存在返回明确错误。
	LoadPasswordHash(ctx context.Context, roomID RoomID) (string, error)
}

// SettingsCommand 是修改房间配置的领域命令（docs「房间设置修改截止」：
// 发牌前可修改，发牌后全部锁定）。由 Telegram 配置表单按钮/文本输入构造，
// 经 SettingsService.Apply 处理；接线进房间 Reducer 属后续 P0 任务。
type SettingsCommand struct {
	Meta CommandMeta
	// RoomID 是目标房间。
	RoomID RoomID
	// Settings 是目标完整配置快照。
	Settings RoomSettings
	// Password 是密码修改意图：nil=维持现有哈希；非 nil 且空串=清除密码；
	// 非 nil 且非空=设置新密码（明文仅在本包内 bcrypt 哈希，随后只传递哈希）。
	Password *string
}

func (SettingsCommand) command() {}

// SettingsService 是房间配置修改领域流程（docs「房间设置修改截止」）。
// 纯核心：持久化经 Repository seam，bcrypt 仅在此完成，不触碰
// Telegram/SQLite/系统时间（docs/技术选型.md §5.1）。
type SettingsService struct {
	repo SettingsRepository
}

// NewSettingsService 构造设置服务；repo 必填（持久化是配置修改的硬约束）。
func NewSettingsService(repo SettingsRepository) (SettingsService, error) {
	if repo == nil {
		return SettingsService{}, errors.New("game: settings service requires a repository")
	}
	return SettingsService{repo: repo}, nil
}

// Apply 应用一次配置修改：
//  1. 发牌锁定：命令期望阶段必须为 PhaseLobby，否则 ErrSettingsLocked；
//  2. 设置校验（枚举/时长/胜负模式）；
//  3. 密码意图解析：nil=读取并保留现有哈希；非 nil 空串=清除；
//     非 nil 明文=校验并 bcrypt 哈希（明文不出本层，Repository 只收哈希）；
//  4. 经 Repository 持久化；
//  5. 产出房主确认消息 Effect。
func (s SettingsService) Apply(ctx context.Context, cmd SettingsCommand) (RoomSettings, []Effect, error) {
	if cmd.Meta.ExpectedPhase != PhaseLobby {
		return RoomSettings{}, nil, ErrSettingsLocked
	}
	if err := cmd.Settings.Validate(); err != nil {
		return RoomSettings{}, nil, err
	}

	hash, err := s.resolvePasswordHash(ctx, cmd)
	if err != nil {
		return RoomSettings{}, nil, err
	}
	if err := s.repo.SaveSettings(ctx, cmd.RoomID, cmd.Settings, hash); err != nil {
		return RoomSettings{}, nil, fmt.Errorf("game: save room settings %q: %w", cmd.RoomID, err)
	}

	msg, err := NewMessageEffect(AudienceHost, SettingsUpdatedMessageKey, map[string]any{
		"room_code":    string(cmd.RoomID),
		"password_set": hash != "",
	})
	if err != nil {
		return RoomSettings{}, nil, fmt.Errorf("game: settings message: %w", err)
	}
	return cmd.Settings, []Effect{msg}, nil
}

// resolvePasswordHash 按命令携带的密码意图解析最终密码哈希：
// nil=保留现有、空串=清除、明文=校验并 bcrypt 哈希。
func (s SettingsService) resolvePasswordHash(ctx context.Context, cmd SettingsCommand) (string, error) {
	if cmd.Password == nil {
		h, err := s.repo.LoadPasswordHash(ctx, cmd.RoomID)
		if err != nil {
			return "", fmt.Errorf("game: load room password hash %q: %w", cmd.RoomID, err)
		}
		return h, nil
	}
	if *cmd.Password == "" {
		return "", nil // 清除密码
	}
	if err := ValidatePassword(*cmd.Password); err != nil {
		return "", err
	}
	return HashPassword(*cmd.Password)
}
