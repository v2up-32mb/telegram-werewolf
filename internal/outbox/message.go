package outbox

import "github.com/v2up-32mb/telegram-werewolf/internal/game"

// ChatID 唯一标识一个 Telegram 聊天（群、频道或私聊会话）。
//
// Telegram 聊天标识为 int64；本包自持类型，避免本任务改动 internal/game。
type ChatID int64

// Priority 是消息的发送优先级类别。
//
// 零值表示未显式设置；Task 17 只承载该语义字段，不参与同 Chat 内的
// 出队排序；限速、重排与合并行为由 Task 18 实现（docs/技术选型.md §7.1）。
type Priority int8

const (
	// PriorityCritical 表示关键消息（如审判结果、技能结算），不可被合并覆盖。
	PriorityCritical Priority = iota + 1
	// PriorityHigh 表示高优先级的阶段性通知。
	PriorityHigh
	// PriorityNormal 表示常规消息。
	PriorityNormal
	// PriorityLow 表示可被后期阶段更新合并的低优先级滚动更新。
	PriorityLow
)

// Message 是游戏引擎产生的待发送 Effect，由 Outbox 统一调度。
//
// 字段语义对应实施计划 Task 17：correlation ID、room ID、chat ID、
// operation、priority 与 coalesce key。
type Message struct {
	// CorrelationID 关联一次业务操作产生的多条消息，用于追踪与测试。
	CorrelationID string
	// RoomID 标识产生该 Effect 的房间。
	RoomID game.RoomID
	// ChatID 标识目标 Telegram 聊天。
	ChatID ChatID
	// Operation 描述要执行的发送操作（如 send_text、edit_message）。
	Operation string
	// Priority 表示消息优先级类别（本任务不据此重排）。
	Priority Priority
	// CoalesceKey 标识可被同 Key 更新合并/覆盖的滚动消息（Task 18 使用）。
	CoalesceKey string
	// Payload 是 operation 的执行参数（不透明载荷，由发送层按
	// Operation 断言具体类型；MVP 由 internal/telegram.Params 承载，
	// 本包不依赖 telegram 类型以避免依赖环）。
	Payload any
}
