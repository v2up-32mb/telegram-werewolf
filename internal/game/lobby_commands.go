package game

import (
	"errors"
	"fmt"
	"strings"
)

// RandomRoomCodeLength 是默认随机房间码位数（docs「房间码规范」1）。
const RandomRoomCodeLength = 6

// lobbyRoomCodeAlphabet 是随机码字符集：大写字母 + 数字，去掉易混淆
// 字符 0/O、1/I（docs「房间码规范」1；与 internal/room 同规则）。
const lobbyRoomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// 创建房间领域规则的哨兵错误（docs §一.7、§房间码规范、§房间码唯一性）。
var (
	// ErrHostInRoom 表示房主已在一个进行中房间（同一时间只能开 1 个）。
	ErrHostInRoom = errors.New("game: host already in an active room")
	// ErrInvalidRoomCode 表示自定义码不符合 4～8 位字母数字规范。
	ErrInvalidRoomCode = errors.New("game: invalid custom room code")
	// ErrRoomCodeTaken 表示自定义码已被占用；应提示房主重新输入，
	// 绝不擅自替换为随机码（docs「房间码规范」3）。
	ErrRoomCodeTaken = errors.New("game: room code already taken")
	// ErrCodeExhausted 表示随机码唯一性重试耗尽仍无法分配。
	ErrCodeExhausted = errors.New("game: failed to allocate unique room code")
)

// CreateRoomRequest 是创建房间领域流程的统一入参。主菜单「创建房间」按钮
// 与 /newgame 文本两条 Telegram 入口都先转换为本请求，再进入同一个
// 应用服务（docs §一.6 创建入口）。
type CreateRoomRequest struct {
	// CommandID 是幂等键（Router 的 update ID / 回调 token 语义）。
	CommandID string
	// Host 是房主用户 ID。
	Host UserID
	// CustomCode 是房主自定义码；空表示生成 6 位去混淆随机码。
	// 大小写不敏感，统一按大写存储与显示（docs「房间码规范」2）。
	CustomCode string
	// Config 是建房配置快照；零值时使用 MVP 默认配置
	//（6 人 = 2 狼 + 预言家 + 女巫 + 2 平民，屠城，
	// docs「6 人局（MVP）默认配置总表」）。
	Config GameConfig
}

// NormalizeRoomCode 规范化自定义码：去首尾空白并统一为大写
// （docs「房间码规范」2：不区分大小写，统一转为大写存储和显示）。
func NormalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ValidCustomRoomCode 校验自定义码：4～8 位英文字母数字
// （docs「房间码规范」2）。入参应为 NormalizeRoomCode 的产物
// （大小写不敏感，校验统一按大写处理）。
func ValidCustomRoomCode(code string) bool {
	if len(code) < 4 || len(code) > 8 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			continue
		}
		return false
	}
	return true
}

// GenerateRoomCode 使用注入 RNG 生成 length 位随机码，字符全部来自
// 去混淆字符集（无偏采样由 RNG 保证，docs/技术选型.md §5.2）。
func GenerateRoomCode(rng RNG, length int) (RoomID, error) {
	if rng == nil {
		return "", fmt.Errorf("game: GenerateRoomCode: nil RNG")
	}
	if length <= 0 {
		return "", fmt.Errorf("game: GenerateRoomCode: length %d must be positive", length)
	}
	code := make([]byte, length)
	for i := range code {
		idx, err := rng.Intn(len(lobbyRoomCodeAlphabet))
		if err != nil {
			return "", fmt.Errorf("game: GenerateRoomCode: %w", err)
		}
		code[i] = lobbyRoomCodeAlphabet[idx]
	}
	return RoomID(code), nil
}
