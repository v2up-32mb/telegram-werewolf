package game

import "errors"

// 通用拒绝规则的哨兵错误（docs/技术选型.md §5.1、§13.1）。
var (
	// ErrWrongPhase 表示命令期望阶段与当前阶段不一致。
	ErrWrongPhase = errors.New("game: command expected phase mismatch")
	// ErrStalePhaseVersion 表示命令携带过期的阶段版本。
	ErrStalePhaseVersion = errors.New("game: stale command phase version")
	// ErrNotInRoom 表示操作者不是房间内的玩家。
	ErrNotInRoom = errors.New("game: actor is not in room")
	// ErrDeadPlayer 表示死亡玩家执行无权操作。
	ErrDeadPlayer = errors.New("game: actor is dead")
	// ErrDuplicateCommand 表示命令 ID 已受理过（重复投递）。
	ErrDuplicateCommand = errors.New("game: duplicate command id")
	// ErrInvalidTarget 表示目标座位不在房间或不可选。
	ErrInvalidTarget = errors.New("game: invalid target seat")
	// ErrUnknownCommand 表示未注册的命令类型。
	ErrUnknownCommand = errors.New("game: unknown command type")
	// ErrNotImplemented 表示当前阶段 reducer 尚未实现（骨架阶段）。
	ErrNotImplemented = errors.New("game: reducer not implemented for this phase")
)
