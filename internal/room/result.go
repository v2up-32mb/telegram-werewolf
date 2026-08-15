package room

import (
	"errors"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// Room Actor 的通用可观察错误。
var (
	// ErrInboxFull 表示 bounded inbox 已满，Dispatch 拒绝接收且不静默丢命令。
	ErrInboxFull = errors.New("room: inbox is full")
	// ErrDeadlinePassed 表示命令在阶段截止时间之后到达。
	ErrDeadlinePassed = errors.New("room: command received after phase deadline")
	// ErrClosed 表示 Actor 已停止，不再接收新命令。
	ErrClosed = errors.New("room: actor is closed")
)

// Result 是一次命令处理的结果快照，包含处理后状态、待执行 Effects 与错误。
type Result struct {
	State   game.State
	Effects []game.Effect
	Err     error
}
