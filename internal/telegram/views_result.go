package telegram

import "github.com/v2up-32mb/telegram-werewolf/internal/game"

// 结算战报与大厅控制消息视图（docs/阶段消息设计.md §15/§16）：
// 结算战报永久保留、不含任何按钮；大厅控制另发临时消息（再来一局/退出
// 房间/查看房间面板），不挂在永久战报上。本层只描述渲染输入，不执行
// Telegram 绘制；MarkdownV2 转义与按钮回调接线属后续任务（与
// views_lobby.go 同模式）。

// ResultPlayer 是战报中一名玩家的渲染输入（身份翻牌 + 结果/积分变化）。
type ResultPlayer struct {
	Seat          game.Seat
	Nickname      string
	Role          game.Role
	Camp          game.Camp
	Died          bool
	MaliciousExit bool
	ScoreDelta    int
}

// ResultEvent 是战报中的一条关键事件（由最终状态可推导；战报不伪装完整
// 回放）。
type ResultEvent struct {
	Phase game.Phase
	Text  string
}

// ResultReport 是结算战报的渲染输入：胜方、参与人（全员身份翻牌与积分
// 变化）与关键事件。永久发送，不含任何按钮（docs 阶段消息设计.md §15：
// 不把大厅操作按钮挂在永久战报上）。
type ResultReport struct {
	Winner    game.Camp
	Players   []ResultPlayer
	KeyEvents []ResultEvent
}

// LobbyControlButton 是临时大厅控制消息的一枚操作按钮。
type LobbyControlButton struct {
	// Key 是按钮稳定标识（后续接线用于回调 token）。
	Key string
	// Label 是展示文案。
	Label string
}

// 大厅控制消息按钮 key（docs 阶段消息设计.md §15：再来一局/退出房间/
// 查看房间面板；与既有 lobby.* / host.* 按钮 key 不冲突）。
const (
	// ResultButtonRematch 是「再来一局」（仅房主可见，docs §结算 6）。
	ResultButtonRematch = "result.rematch"
	// ResultButtonExit 是「退出房间」。
	ResultButtonExit = "result.exit"
	// ResultButtonPanel 是「查看房间面板」。
	ResultButtonPanel = "result.panel"
)

// LobbyControl 是独立临时大厅控制消息的渲染输入：游戏结束后房间回到
// 等待状态，至少保留 15 秒供玩家选择退出（docs §结算 6、阶段消息设计
// §15）；「再来一局」仅房主可见，非房主只显示退出房间/查看房间面板。
// 退出后删除自己的控制消息、新一局开始后删除旧控制消息（接线层）。
type LobbyControl struct {
	IsHost  bool
	Buttons []LobbyControlButton
}

// NewLobbyControl 按房主身份构造大厅控制消息按钮：房主 = 再来一局 +
// 退出房间 + 查看房间面板；非房主 = 退出房间 + 查看房间面板。
func NewLobbyControl(isHost bool) LobbyControl {
	buttons := []LobbyControlButton{
		{Key: ResultButtonExit, Label: "退出房间"},
		{Key: ResultButtonPanel, Label: "查看房间面板"},
	}
	if isHost {
		buttons = append([]LobbyControlButton{{Key: ResultButtonRematch, Label: "再来一局"}}, buttons...)
	}
	return LobbyControl{IsHost: isHost, Buttons: buttons}
}
