package outbox

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed 表示调度器已关闭，不再接收新消息。
var ErrClosed = errors.New("outbox: scheduler closed")

// SendFunc 把一条消息交给上层发送。
//
// Task 17 不做实际 Telegram 发送、限速、重试或合并；调用方负责发送语义。
type SendFunc func(ctx context.Context, msg Message) error

// Scheduler 是全局 ready 调度器：按 ChatID 分组队列，每 Chat 一个
// worker goroutine 严格串行消费，不同 Chat 之间并行推进。
//
// goroutine 数量上界为活跃 Chat 数，不随消息数量增长。
type Scheduler struct {
	mu       sync.Mutex
	queues   map[ChatID]*Queue
	send     SendFunc
	capacity int
	closed   bool
	stopCh   chan struct{}
	baseCtx  context.Context
	cancel   context.CancelFunc
	onErr    func(msg Message, err error)
	wg       sync.WaitGroup
}

// SchedulerOption 配置 Scheduler 构造。
type SchedulerOption func(*Scheduler)

// WithSendErrorHandler 注册发送错误回调：worker 发送失败时上报
// （消息与错误），供上层记录日志/指标。默认 nil 表示静默
// （Task 46 冒烟缺陷：发送失败被静默重试丢弃、无任何日志，
// 导致 newgame 创建确认缺失无法排查）。
func WithSendErrorHandler(h func(msg Message, err error)) SchedulerOption {
	return func(s *Scheduler) { s.onErr = h }
}

// NewScheduler 创建全局调度器。
//
// send 是消息发送回调；capacity 是每个 Chat 队列的容量上限，必须大于 0
// （配置层尚无容量字段，容量作为构造参数传入，见 Task 17 决策）。
// 可选 SchedulerOption（如 WithSendErrorHandler）用于发送失败观测。
func NewScheduler(send SendFunc, capacity int, opts ...SchedulerOption) *Scheduler {
	if capacity <= 0 {
		panic("outbox: scheduler capacity must be positive")
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{
		queues:   make(map[ChatID]*Queue),
		send:     send,
		capacity: capacity,
		stopCh:   make(chan struct{}),
		baseCtx:  baseCtx,
		cancel:   cancel,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Enqueue 将 msg 投递到对应 Chat 的队列。
//
// 首次出现某 Chat 时创建该 Chat 的队列与 worker goroutine；
// 队列已满返回 ErrQueueFull，调度器已关闭返回 ErrClosed。
func (s *Scheduler) Enqueue(msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	q, ok := s.queues[msg.ChatID]
	if !ok {
		q = NewQueue(s.capacity)
		s.queues[msg.ChatID] = q
		s.wg.Add(1)
		go s.worker(q)
	}
	// 持锁入队：与 Close 完全串行，杜绝消息在 worker 已退出后滞留。
	return q.Enqueue(msg)
}

// worker 串行消费单个 Chat 的队列。
//
// 正常路径使用可取消的 baseCtx 发送（Close 可中断在途处理）；
// 收到关闭信号后排空剩余消息（排空用后台上下文，不静默丢弃），然后退出。
func (s *Scheduler) worker(q *Queue) {
	defer s.wg.Done()
	for {
		select {
		case msg := <-q.ch:
			if err := s.send(s.baseCtx, msg); err != nil {
				// 不再静默丢弃：TDD 红线（Task 46 冒烟缺陷）——发送失败必须
				// 上报 onErr（上层记日志），否则 400/429 类失败无从排查。
				if s.onErr != nil {
					s.onErr(msg, err)
				}
			}
		case <-s.stopCh:
			drain(s.send, q, s.onErr)
			return
		}
	}
}

// drain 在关闭阶段尽力发送队列中剩余的消息后返回。
func drain(send SendFunc, q *Queue, onErr func(msg Message, err error)) {
	for {
		msg, ok := q.TryDequeue()
		if !ok {
			return
		}
		if err := send(context.Background(), msg); err != nil {
			if onErr != nil {
				onErr(msg, err)
			}
		}
	}
}

// Close 优雅关闭调度器：停止接收新消息，取消在途发送，等待所有
// worker 排空队列后退出。
//
// 幂等；ctx 取消时返回 ctx.Err()，worker 仍会在后台尽力排空。
func (s *Scheduler) Close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.stopCh)
	s.cancel()
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
