package telegram

import "github.com/v2up-32mb/telegram-werewolf/internal/game"

// 房主控制面板视图（docs 游戏流程设计.md §房主控制面板 97-98）：房主
// 额外拥有一组管理按钮（投票踢人/强制解散/投票解散），与普通游戏操作
// 按钮分开展示。本层只描述渲染输入，不执行 Telegram 绘制；MarkdownV2
// 转义与按钮回调接线属后续任务（与 views_lobby.go 同模式）。

// HostGovernanceButton 是房主管理面板的一枚操作按钮。
type HostGovernanceButton struct {
	// Key 是按钮稳定标识（后续接线用于回调 token）。
	Key string
	// Label 是展示文案。
	Label string
}

// 房主管理按钮 key（docs §房主控制面板：投票踢人/强制解散/投票解散；
// 与普通游戏操作按钮分开呈现，不与既有普通按钮 key 冲突）。
const (
	HostButtonDissolveVote  = "host.dissolve_vote"  // 投票解散
	HostButtonKickVote      = "host.kick_vote"      // 投票踢人
	HostButtonForceDissolve = "host.force_dissolve" // 强制解散
)

// HostGovernancePanel 是游戏进行中房主控制面板的渲染输入：当前阶段、
// 存活人数（供投票阈值展示）与三枚管理按钮；与普通游戏操作按钮分开
// 呈现。
type HostGovernancePanel struct {
	Phase      game.Phase
	AliveCount int
	Buttons    []HostGovernanceButton
}
