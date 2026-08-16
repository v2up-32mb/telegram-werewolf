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

// WolfVoteCommand 是狼人夜间的刀人选择（docs §夜间 2：讨论与投票并行、
// 确认前最终选择可覆盖）。Target=nil 表示主动空刀，
// 仅当 Settings.WolfMustKill=false 时允许（docs「狼人空刀」：
// 默认必须刀人，空刀默认关闭；「弃刀」仅作为超时惩罚存在）。
type WolfVoteCommand struct {
	Meta   CommandMeta
	Target *Seat
}

func (WolfVoteCommand) command() {}

// WolfConfirmCommand 锁定狼人本人当前选择（docs §夜间 2：每名存活狼人
// 选择后须点击「确认选择」，确认后本轮不能修改）。
type WolfConfirmCommand struct {
	Meta CommandMeta
}

func (WolfConfirmCommand) command() {}

// WitchUseCommand 是女巫夜间的用药选择（解药/毒药二选一）。
type WitchUseCommand struct {
	Meta   CommandMeta
	Action WitchAction
	Target Seat
}

func (WitchUseCommand) command() {}

// WitchSaveCommand 是女巫解药窗口的用药选择（docs §夜间 3、§8.2：
// 救/不救二选一，确认前可修改）：Use=true 表示使用解药（目标即今晚
// 刀口），false 表示不使用解药。
type WitchSaveCommand struct {
	Meta CommandMeta
	Use  bool
}

func (WitchSaveCommand) command() {}

// WitchPoisonCommand 是女巫毒药窗口的选择（docs §夜间 3、§8.2：
// 选择毒谁或不使用毒药，确认前可修改）：Target=nil 表示「不使用毒药」。
type WitchPoisonCommand struct {
	Meta   CommandMeta
	Target *Seat
}

func (WitchPoisonCommand) command() {}

// WitchConfirmCommand 锁定女巫当前窗口的待确认选择（docs §夜间 3：
// 确认后不能撤回；确认完成后可提前结束阶段）。
type WitchConfirmCommand struct {
	Meta CommandMeta
}

func (WitchConfirmCommand) command() {}

// SeerCheckCommand 是预言家夜间的查验选择。
type SeerCheckCommand struct {
	Meta   CommandMeta
	Target Seat
}

func (SeerCheckCommand) command() {}

// SeerConfirmCommand 锁定预言家的待确认查验目标（docs §夜间 4、§8.3：
// 确认前可修改，确认后立即返回二分结果并提前结束阶段）。
type SeerConfirmCommand struct {
	Meta CommandMeta
}

func (SeerConfirmCommand) command() {}

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

// VoteConfirmCommand 锁定投票人的待确认选择（docs §投票 1、§8.4：
// 确认前可改票，确认后锁定；弃权也需要确认；全部有票权玩家确认后
// 提前结束）。
type VoteConfirmCommand struct {
	Meta CommandMeta
}

func (VoteConfirmCommand) command() {}

// LastWordsCommand 是被票死者在遗言窗口发表的遗言（docs §结算 4：
// 仅「不报身份」模式有 30 秒遗言并正常转播；狼人自爆永远无遗言属
// Task 38 自爆路径，本任务不实现自爆）。
type LastWordsCommand struct {
	Meta CommandMeta
	Text string
}

func (LastWordsCommand) command() {}

// TimeoutCommand 是阶段超时命令，携带期望阶段与版本供校验
// （docs/技术选型.md §6.2）。
type TimeoutCommand struct {
	Meta CommandMeta
}

func (TimeoutCommand) command() {}

// ExplodeCommand 是狼人白天自爆（docs 游戏流程设计.md §狼人自爆、§投票 5）：
// 仅存活狼人可在白天任意时刻（PhaseDaySpeech/PhaseDayVote，含平票轮与
// 遗言窗口）发起；自爆优先打断发言/投票并直接进入黑夜（已投票作废、
// 无遗言）。自爆不是退出：Left 不置位（区分 leave.go 狼人白天退出按自爆
// 的主动退出路径）。
type ExplodeCommand struct {
	Meta CommandMeta
}

func (ExplodeCommand) command() {}

// LeaveGameCommand 是游戏进行中玩家主动退出（docs 游戏流程设计.md
// §恶意退出判定、§狼人自爆 2）：狼人白天退出按自爆处理；夜间退出按
// 恶意退出死亡公告（不误导身份）；其余存活主动退出标记死亡并触发
// 10 分钟跨局加入冷却。大厅等非游戏阶段拒绝（走大厅退出流程）。
type LeaveGameCommand struct {
	Meta CommandMeta
}

func (LeaveGameCommand) command() {}

// GovernanceDissolveCommand 是「发起投票解散」（docs 游戏流程设计.md
// §解散）：发起者计一票同意；仅存活玩家可发起，局内每人限 1 次、
// 每阶段限 1 次；超过三分之一同意即通过（不扣分）。
type GovernanceDissolveCommand struct {
	Meta CommandMeta
}

func (GovernanceDissolveCommand) command() {}

// GovernanceDissolveVoteCommand 是投票解散已发起后的同意票
// （docs §解散 2：仅存活玩家参与，超过三分之一同意即通过）。
type GovernanceDissolveVoteCommand struct {
	Meta CommandMeta
}

func (GovernanceDissolveVoteCommand) command() {}

// GovernanceKickCommand 是「发起投票踢人」（docs §投票踢人）：目标必须
// 为房间内存活玩家且非发起者本人；发起限制与投票解散一致；被踢者按
// 掉线处理（判负移除语义）。
type GovernanceKickCommand struct {
	Meta   CommandMeta
	Target Seat
}

func (GovernanceKickCommand) command() {}

// GovernanceKickVoteCommand 是投票踢人已发起后的同意票
// （docs §投票踢人 1：超过三分之一同意即通过）。
type GovernanceKickVoteCommand struct {
	Meta CommandMeta
}

func (GovernanceKickVoteCommand) command() {}

// HostDissolveCommand 是房主强制解散（docs §解散 1、§积分系统 2）：
// 二次确认（Confirm=false 请求确认，Confirm=true 生效）；生效扣 10 分
// （ScorePenaltyEffect），积分 ≤9 禁止；HostScore 由接线层从存储读取
// 后传入（game 核心不触碰 SQLite）。
type HostDissolveCommand struct {
	Meta      CommandMeta
	Confirm   bool
	HostScore int
}

func (HostDissolveCommand) command() {}

// RematchCommand 由房主在结算阶段点击「再来一局」发起（docs §结算 6）：
// 回到等待大厅、保留成员与配置、从点击起至少 15 秒退出窗口（窗口内
// StartGameCommand 拒绝）。Meta.ReceivedAt 为窗口计算时间源（reducer
// 不读取系统时间，docs/技术选型.md §5.1）。
type RematchCommand struct {
	Meta CommandMeta
}

func (RematchCommand) command() {}
