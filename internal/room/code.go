package room

import (
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// RoomCodeLength 是随机房间码的默认位数（docs/游戏流程设计.md §一.5）。
const RoomCodeLength = 6

// roomCodeAlphabet 是房间码允许字符集：大写字母 + 数字，去掉易混淆字符
// 0/O、1/I（internal/game/id.go RoomID 注释）。
const roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GenerateCode 使用注入的 RNG 生成 length 位随机短码，字符全部来自
// roomCodeAlphabet（无偏采样由 RNG 保证，docs/技术选型.md §5.2）。
func GenerateCode(rng game.RNG, length int) (game.RoomID, error) {
	if rng == nil {
		return "", fmt.Errorf("room: GenerateCode: nil RNG")
	}
	if length <= 0 {
		return "", fmt.Errorf("room: GenerateCode: length %d must be positive", length)
	}
	code := make([]byte, length)
	for i := range code {
		idx, err := rng.Intn(len(roomCodeAlphabet))
		if err != nil {
			return "", fmt.Errorf("room: GenerateCode: %w", err)
		}
		code[i] = roomCodeAlphabet[idx]
	}
	return game.RoomID(code), nil
}

// ValidRoomCode 校验房间码长度与字符集（随机码与房主自定义码共用规则）。
func ValidRoomCode(code game.RoomID) bool {
	if len(code) != RoomCodeLength {
		return false
	}
	for i := 0; i < len(code); i++ {
		if strings.IndexByte(roomCodeAlphabet, code[i]) < 0 {
			return false
		}
	}
	return true
}
