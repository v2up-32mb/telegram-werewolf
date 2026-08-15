package game

import "time"

// Reducer 是纯游戏核心的输入输出协议：
//
//	新状态 + Effects = Reduce(旧状态, Command)
//
// Reducer 不直接调用 Telegram API、不直接访问 SQLite、不读取系统时间、
// 不创建真实计时器、不读取全局随机源（docs/技术选型.md §5.1）。
type Reducer interface {
	Reduce(State, Command) (State, []Effect, error)
}

// CommandMeta 携带命令的通用校验信息：操作者、期望阶段与阶段版本。
//
// phaseVersion 用于拒绝过期操作：Timer 到期与网络延迟到达的旧操作
// 会因版本不匹配被拒绝（docs/技术选型.md §6.2）。
type CommandMeta struct {
	ID            string
	Actor         UserID
	ExpectedPhase Phase
	PhaseVersion  uint64
	ReceivedAt    time.Time
}

// Command 是领域命令的强类型联合（marker 接口）。
// 具体命令以值类型实现，不使用自由字符串驱动核心规则。
type Command interface {
	command()
}

// WitchAction 是女巫夜间用药选择：解药 / 毒药，每夜只能用一瓶。
type WitchAction int

const (
	WitchActionUnknown WitchAction = iota
	WitchActionSave                // 使用解药
	WitchActionPoison              // 使用毒药
)

// Valid 报告用药选择是否为合法值。
func (a WitchAction) Valid() bool {
	return a >= WitchActionSave && a <= WitchActionPoison
}

// CreateRoomCommand 在等待大厅创建房间（房主指定配置）。
type CreateRoomCommand struct {
	Meta   CommandMeta
	Config GameConfig
}

func (CreateRoomCommand) command() {}

// JoinRoomCommand 让玩家加入等待中的房间。
type JoinRoomCommand struct {
	Meta CommandMeta
}

func (JoinRoomCommand) command() {}

// StartGameCommand 由房主在满员后开始游戏（进入发牌确认）。
type StartGameCommand struct {
	Meta CommandMeta
}

func (StartGameCommand) command() {}

// ConfirmRoleCommand 由玩家确认已查看身份（发牌确认阶段）。
type ConfirmRoleCommand struct {
	Meta CommandMeta
}

func (ConfirmRoleCommand) command() {}

// WolfKillCommand 是狼人夜间的刀人选择。
type WolfKillCommand struct {
	Meta   CommandMeta
	Target Seat
}

func (WolfKillCommand) command() {}

// WitchUseCommand 是女巫夜间的用药选择（解药/毒药二选一）。
type WitchUseCommand struct {
	Meta   CommandMeta
	Action WitchAction
	Target Seat
}

func (WitchUseCommand) command() {}

// SeerCheckCommand 是预言家夜间的查验选择。
type SeerCheckCommand struct {
	Meta   CommandMeta
	Target Seat
}

func (SeerCheckCommand) command() {}

// SpeakCommand 是白天麦序发言内容。
type SpeakCommand struct {
	Meta CommandMeta
	Text string
}

func (SpeakCommand) command() {}

// VoteCommand 是白天投票选择；Target 为 nil 表示弃权
// （游戏允许投弃权票，docs/游戏流程设计.md §投票）。
type VoteCommand struct {
	Meta   CommandMeta
	Target *Seat
}

func (VoteCommand) command() {}

// TimeoutCommand 是阶段超时命令，携带期望阶段与版本供校验
// （docs/技术选型.md §6.2）。
type TimeoutCommand struct {
	Meta CommandMeta
}

func (TimeoutCommand) command() {}
