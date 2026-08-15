package outbox

import "sync"

// Coalescer 对尚未发送的低优先级阶段更新按 (ChatID, CoalesceKey) 合并，
// 同一 key 只保留最新待发版本（docs/技术选型.md §7.1「对尚未发送的低
// 优先级阶段更新进行合并或覆盖」）。
//
// 不可合并的消息（CoalesceKey 为空，或优先级为关键，例如身份卡、
// 上帝视角实时行动记录、结算战报；docs/阶段消息设计.md §16）原样保留。
type Coalescer struct {
	mu    sync.Mutex
	queue []Message
	index map[coalesceKey]int
}

// coalesceKey 标识一个 Chat 内的可合并更新目标。
type coalesceKey struct {
	chat ChatID
	key  string
}

// NewCoalescer 创建空的合并器。
func NewCoalescer() *Coalescer {
	return &Coalescer{index: make(map[coalesceKey]int)}
}

// mergeable 报告消息是否可被同 key 更新合并。
//
// 可合并 = CoalesceKey 非空且不是关键优先级（普通滚动阶段更新）；
// 分页冻结后创建的续页使用新 key（docs/阶段消息设计.md §4.1），各页互不合并。
func mergeable(m Message) bool {
	return m.CoalesceKey != "" && m.Priority != PriorityCritical
}

// Submit 提交一条待发送消息。
//
// 若同 (ChatID, CoalesceKey) 已有尚未发送的版本且消息可合并，则原地替换
// 并返回 true；否则追加到队尾并返回 false。不可合并消息总是追加保留。
func (c *Coalescer) Submit(msg Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !mergeable(msg) {
		c.queue = append(c.queue, msg)
		return false
	}
	k := coalesceKey{chat: msg.ChatID, key: msg.CoalesceKey}
	if i, ok := c.index[k]; ok {
		c.queue[i] = msg
		return true
	}
	c.index[k] = len(c.queue)
	c.queue = append(c.queue, msg)
	return false
}

// Pending 返回当前待发消息数。
func (c *Coalescer) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// Next 非阻塞取出队首消息；可合并消息出队后从索引移除。
func (c *Coalescer) Next() (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return Message{}, false
	}
	msg := c.queue[0]
	c.queue = c.queue[1:]
	if mergeable(msg) {
		delete(c.index, coalesceKey{chat: msg.ChatID, key: msg.CoalesceKey})
	}
	return msg, true
}

// Flush 清空并返回全部待发消息（按 FIFO 顺序），供送入 Scheduler 使用。
func (c *Coalescer) Flush() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.queue
	c.queue = nil
	c.index = make(map[coalesceKey]int)
	return out
}
