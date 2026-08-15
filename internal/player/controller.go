// Package player 提供玩家控制的边界抽象：
// 将“该玩家依法可见的视图”转换为领域 Command（docs/技术选型.md §5.3）。
package player

import "github.com/v2up-32mb/telegram-werewolf/internal/game"

// PlayerView 是该玩家依法可见的视图，只包含该角色允许看到的信息，
// 不包含其他玩家的私密信息（狼人队友名单、预言家查验结果等由调用方过滤）。
type PlayerView struct {
	Self  game.Player
	Phase game.Phase
	Alive bool
}

// PlayerController 是将可见视图转换为领域 Command 的边界抽象。
//
// MVP 不实现 AI：HumanController（后续 Task）把真人输入转成领域 Command；
// 未来的 AIController 只能读取该 AI 角色依法可见的视图，并输出同一种
// 领域 Command。本接口不依赖任何模型供应商 SDK。
type PlayerController interface {
	NextCommand(view PlayerView) (game.Command, error)
}
