package outbox

import (
	"errors"
	"sync/atomic"
)

// ErrQueueFull 表示目标 Chat 的队列已达到容量上限，本次入队被拒绝。
//
// 调用方应使用 errors.Is 识别；拒绝入队时会递增满队列计数，
// 禁止静默丢弃消息或无界占内存（docs/技术选型.md §7.1）。
var ErrQueueFull = errors.New("outbox: queue full")

// Queue 是单 Chat 的有界 FIFO 队列。
//
// 同一 Queue 只服务一个 Chat：出队顺序与入队顺序严格一致，
// priority 字段不参与本队列的排序。
type Queue struct {
	ch        chan Message
	fullCount atomic.Int64
}

// NewQueue 创建容量为 capacity 的有界 FIFO 队列。
//
// capacity 为该 Chat 在途与排队消息合计上限，必须大于 0。
func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		panic("outbox: queue capacity must be positive")
	}
	return &Queue{ch: make(chan Message, capacity)}
}

// Enqueue 将 msg 追加到队尾。
//
// 队列已满时返回 ErrQueueFull（可通过 errors.Is 识别），并递增满队列计数；
// 消息不会被部分入队或静默丢弃。
func (q *Queue) Enqueue(msg Message) error {
	select {
	case q.ch <- msg:
		return nil
	default:
		q.fullCount.Add(1)
		return ErrQueueFull
	}
}

// TryDequeue 非阻塞地取出队首消息。
//
// 队列为空时返回 false。该方法是供调度器消费与优雅关闭排空使用的唯一出队入口。
func (q *Queue) TryDequeue() (Message, bool) {
	select {
	case msg := <-q.ch:
		return msg, true
	default:
		return Message{}, false
	}
}

// Len 返回当前排队（未被消费）的消息数，供队列长度统计使用
// （docs/技术选型.md §11.3 Outbox 队列长度）。
func (q *Queue) Len() int {
	return len(q.ch)
}

// FullCount 返回因队列满而被拒绝入队的累计次数。
func (q *Queue) FullCount() int64 {
	return q.fullCount.Load()
}
