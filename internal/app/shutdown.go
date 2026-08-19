package app

import (
	"context"
	"errors"
	"time"
)

// defaultStopTimeout 是优雅停止的总有上限时长：Outbox drain、健康 HTTP
// Shutdown 与组件等待都受此约束，避免停机阶段无限阻塞。
const defaultStopTimeout = 10 * time.Second

// Stop 按序优雅停止应用（docs/技术选型.md §4 生命周期）：
//
//	① 停止 Update Source（取消 source ctx，消费循环退出）
//	② 停止接收新 Command（commandsOpen 门控）
//	③ 停止 Room Actors（room.Manager.Close，等待 Actor goroutine 干净退出）
//	④ 有上限 drain Outbox（Scheduler.Close 幂等，drain 不静默丢弃）
//	⑤ 关闭健康检查 HTTP 与 DB
//
// 每步输出带 step 标记的结构化日志，供集成测试按序断言；单步失败不阻断
// 后续步骤，最后以 errors.Join 汇总返回。
func (a *App) Stop(ctx context.Context) error {
	var errs []error

	a.log.Info("app: shutdown", "step", "source_stopped")
	if a.sourceCancel != nil {
		a.sourceCancel()
	}

	a.commandsOpen.Store(false)
	a.log.Info("app: shutdown", "step", "commands_closed")

	a.roomsClosed.Store(true)
	a.rooms.Close()
	// B3：生产玩法 Actor 由 Wiring 创建（不归 room.Manager 管），停机时
	// 在此统一停止并等待退出，防止泄漏 goroutine/Timer。
	if a.wiring != nil {
		a.wiring.stopActors()
		a.wiring.stopCoalescerFlusher()
	}
	a.log.Info("app: shutdown", "step", "rooms_stopped")

	a.outboxClosed.Store(true)
	if err := a.outbox.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	a.log.Info("app: shutdown", "step", "outbox_drained")

	if a.health != nil {
		a.log.Info("app: shutdown", "step", "health_stopped")
		if err := a.health.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if err := a.db.Close(); err != nil {
		errs = append(errs, err)
	}
	a.log.Info("app: shutdown", "step", "db_closed")

	return errors.Join(errs...)
}
