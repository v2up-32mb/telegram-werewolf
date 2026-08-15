package telegram

import (
	"context"
	"sync"
)

// CursorStore 是 update cursor 的持久化边界接口。
//
// bot_update_cursor 表与 UpsertUpdateCursor/GetUpdateCursor 查询已在
// Task 13 就位（migrations/000001_initial.sql、queries/bot_state.sql、
// internal/storage/sqlc）；本任务只定义接口并在测试中用 SQLite 验证
// 重启恢复语义，产品侧实现由后续 App 装配（Task 22）注入。
type CursorStore interface {
	// Load 返回已 ACK 的最大 update_id；无记录时返回 0。
	Load(ctx context.Context) (int64, error)
	// Save 原子地把已 ACK 的 update_id 提交为最新 cursor。
	Save(ctx context.Context, updateID int64) error
}

// Deduper 是有界内存的 update_id 去重器，附带单调 high-watermark。
//
// 语义（docs/技术选型.md §update_id 有界去重）：
//   - Accept 原子处理：首次接受返回 true，窗口内重复或历史（<= 水位）返回 false；
//   - 窗口容量有界，超限淘汰最旧 ID（内存不随 update 数量增长）；
//   - high-watermark 单调推进，历史 ID 永不重放；
//   - Restore 从持久化 cursor（SQLite bot_update_cursor）恢复水位，
//     只有「入队但尚未 ACK」的 Update 在崩溃后会因水位未推进而重投，
//     由领域幂等约束安全承受。
type Deduper struct {
	mu   sync.Mutex
	cap  int
	ids  []int64
	seen map[int64]struct{}
	hw   int64
}

// NewDeduper 创建容量为 capacity 的有界去重器（必须大于 0）。
func NewDeduper(capacity int) *Deduper {
	if capacity <= 0 {
		capacity = 1
	}
	return &Deduper{cap: capacity, seen: make(map[int64]struct{})}
}

// Accept 判定 updateID 是否第一次被接受；接受则记录并推进水位。
//
// 线程安全：并发调用同一 ID 恰好一次返回 true。
func (d *Deduper) Accept(updateID int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if updateID <= d.hw {
		return false
	}
	if _, ok := d.seen[updateID]; ok {
		return false
	}
	d.seen[updateID] = struct{}{}
	d.ids = append(d.ids, updateID)
	if len(d.ids) > d.cap {
		old := d.ids[0]
		d.ids = d.ids[1:]
		delete(d.seen, old)
	}
	d.hw = updateID
	return true
}

// Restore 从持久化恢复 high-watermark（清空窗口）。
//
// 恢复后 <= hw 的 updateID 一律视为已处理（不重放），新 ID 正常接受。
func (d *Deduper) Restore(hw int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids = nil
	d.seen = make(map[int64]struct{})
	d.hw = hw
}

// HighWatermark 返回当前已接受的最大 update_id。
func (d *Deduper) HighWatermark() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hw
}
