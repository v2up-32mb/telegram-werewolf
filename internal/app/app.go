// Package app 提供应用级装配与生命周期：手动依赖注入串起配置、DB、Outbox、
// Room Manager、Telegram 与健康服务（docs/技术选型.md §4 单一进程）。
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/v2up-32mb/telegram-werewolf/internal/config"
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/observability"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/room"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// AbortedRoom 描述启动扫描发现的一个遗留 active 房间（docs/技术选型.md §10：
// 重启后未结束对局统一中止并通知参与者；rooms 表仅保存中止清场所需最小字段）。
type AbortedRoom struct {
	// Code 是遗留房间码。
	Code game.RoomID
	// HostUserID 是房主用户 ID（可联系的参与者之一；完整玩家名单查询属后续任务）。
	HostUserID game.UserID
	// Phase 是遗留房间最后阶段。
	Phase string
}

// AbortScanner 是遗留房间扫描边界；默认实现使用 storage.RoomRepository.ListActive。
type AbortScanner interface {
	// ListLeftover 返回启动时仍处于 active 的房间。
	ListLeftover(ctx context.Context) ([]AbortedRoom, error)
}

// AbortNotifier 是中止通知边界；默认实现向房主私聊入队 Outbox 消息。
// 通知失败只记日志不阻断启动（docs/技术选型.md §10：通知可联系的参与者）。
type AbortNotifier interface {
	// NotifyAbort 通知一个遗留房间的参与者 Bot 已重启中止。
	NotifyAbort(ctx context.Context, room AbortedRoom) error
}

// TextHandler 是消息类 Update 的文本命令消费边界：与 CommandHandler
// 同级 seam，但接收原始 Update（保留命令文本与 ChatID/私聊标记），
// 供 Task 41 CommandsHandler 使用。两者由接线层按 Update 类型互斥接管。
type TextHandler interface {
	HandleText(ctx context.Context, u telegram.Update) error
}

// CommandHandler 是 Update → 领域命令的消费边界。
//
// 玩法执行（建房、邀请、加入、大厅、发牌等）属阶段 P0，由后续任务在
// 本 seam 上接线；MVP 只保证命令流可达且可被停止门控拒绝。
type CommandHandler interface {
	// Handle 消费一条路由后的领域命令。
	Handle(ctx context.Context, cmd game.Command) error
}

// CallbackActionHandler 是回调动作的消费边界（B1-b）：Router.DispatchAction
// 校验 token 后把原始动作（含 UpdateID/owner/action/target）交回接线层，
// 供 reducer 动作与导演本地信号（end_speech 等）分流。
type CallbackActionHandler interface {
	// Handle 消费一条校验后的回调动作。
	Handle(ctx context.Context, act telegram.CallbackAction) error
}

// App 是装配后的应用实例。
type App struct {
	cfg       *config.Config
	log       *slog.Logger
	db        *sql.DB
	outbox    *outbox.Scheduler
	coalescer *outbox.Coalescer
	rooms     *room.Manager
	source    telegram.UpdateSource
	router    *telegram.Router
	health    *http.Server // HealthAddress 为空时 nil
	metrics   *observability.Metrics
	scanner   AbortScanner
	notifier  AbortNotifier
	handler   CommandHandler
	text      TextHandler
	action    CallbackActionHandler

	sourceCancel context.CancelFunc

	migrated      atomic.Bool
	sourceStarted atomic.Bool
	conflict      atomic.Bool
	commandsOpen  atomic.Bool
	outboxClosed  atomic.Bool
	roomsClosed   atomic.Bool
}

// readyChecks 返回 /readyz 使用的就绪检查（与 Ready 同一套语义）：
// migrations 完成、DB Ping 成功、Update Source 已启动且未发生 409、
// Outbox 与 Room Manager 均可接收工作。
func (a *App) readyChecks() []observability.Check {
	return []observability.Check{
		{Name: "migrations", Func: func(ctx context.Context) error {
			if !a.migrated.Load() {
				return errors.New("app: migrations not applied")
			}
			return nil
		}},
		{Name: "db", Func: func(ctx context.Context) error {
			return a.db.PingContext(ctx)
		}},
		{Name: "source", Func: func(ctx context.Context) error {
			if !a.sourceStarted.Load() {
				return errors.New("app: update source not started")
			}
			if a.conflict.Load() {
				return errors.New("app: update source conflict (409 long polling)")
			}
			return nil
		}},
		{Name: "outbox", Func: func(ctx context.Context) error {
			if a.outboxClosed.Load() {
				return errors.New("app: outbox closed")
			}
			return nil
		}},
		{Name: "rooms", Func: func(ctx context.Context) error {
			if a.roomsClosed.Load() {
				return errors.New("app: room manager closed")
			}
			return nil
		}},
	}
}

// Ready 聚合返回就绪状态；全部检查通过才返回 nil。
func (a *App) Ready(ctx context.Context) error {
	var errs []error
	for _, c := range a.readyChecks() {
		if err := c.Func(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Run 启动全部组件并阻塞至 ctx 取消，随后按序优雅停止。
//
// 启动序列：启动 Update Source 消费循环（保序、去重、ACK cursor）→
// 扫描并通知遗留 active 房间中止 → 启动健康检查 HTTP（若配置）→ 等待 ctx。
func (a *App) Run(ctx context.Context) error {
	sourceCtx, cancel := context.WithCancel(ctx)
	a.sourceCancel = cancel
	a.commandsOpen.Store(true)
	a.sourceStarted.Store(true)
	go a.source.Start(sourceCtx)
	go a.consumeUpdates(sourceCtx)
	if err := a.scanLeftoverAborts(ctx); err != nil {
		a.log.Error("app: startup abort scan failed", "error", err)
	}
	if a.health != nil {
		go func() {
			if err := a.health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log.Error("app: health server", "error", err)
			}
		}()
	}
	<-ctx.Done()

	// 停止阶段使用独立的有上限超时：Run 收到的 ctx 已取消，直接传给
	// Stop 会让 outbox.Close 立即返回 ctx.Err()，无法完成有上限 drain。
	stopCtx, cancel := context.WithTimeout(context.Background(), defaultStopTimeout)
	defer cancel()
	return a.Stop(stopCtx)
}

// consumeUpdates 串行消费 Update 流与错误流；ctx 取消即退出。
func (a *App) consumeUpdates(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-a.source.Errors():
			if !ok {
				return
			}
			if err == nil {
				// 源层空错误（库长轮询抖动）：忽略，不记 ERROR。
				continue
			}
			if errors.Is(err, telegram.ErrConflict) {
				a.conflict.Store(true)
				a.log.Error("app: telegram 409 conflict", "error", err)
				continue
			}
			a.log.Error("app: update source error", "error", err)
		case u, ok := <-a.source.Updates():
			if !ok {
				return
			}
			if err := a.dispatchUpdate(ctx, u); err != nil {
				a.log.Error("app: dispatch update", "error", err)
			}
		}
	}
}

// dispatchUpdate 走 Router（去重、路由、ACK cursor），命令交给 CommandHandler；
// 停止阶段通过 commandsOpen 门控拒绝新命令。
func (a *App) dispatchUpdate(ctx context.Context, u telegram.Update) error {
	if !a.commandsOpen.Load() {
		return errors.New("app: not accepting commands (stopping)")
	}
	if a.text != nil && u.Message != nil {
		// 文本命令：接线层自持解析（Task 41 命令面），保持去重/ACK 语义。
		return a.router.DispatchText(ctx, u, func(ctx context.Context, up telegram.Update) error {
			if !a.commandsOpen.Load() {
				return errors.New("app: not accepting commands (stopping)")
			}
			return a.text.HandleText(ctx, up)
		})
	}
	if a.action != nil && u.CallbackQuery != nil {
		// 回调动作：Router 校验 token 后交回接线层分流（B1-b）。
		return a.router.DispatchAction(ctx, u, func(ctx context.Context, act telegram.CallbackAction) error {
			if !a.commandsOpen.Load() {
				return errors.New("app: not accepting commands (stopping)")
			}
			return a.action.Handle(ctx, act)
		})
	}
	return a.router.Dispatch(ctx, u, func(ctx context.Context, cmd game.Command) error {
		if !a.commandsOpen.Load() {
			return errors.New("app: not accepting commands (stopping)")
		}
		return a.handler.Handle(ctx, cmd)
	})
}

// scanLeftoverAborts 在启动时扫描遗留 active 房间并逐一通知
// （docs/技术选型.md §10；通知失败只记日志，不阻断启动）。
func (a *App) scanLeftoverAborts(ctx context.Context) error {
	leftover, err := a.scanner.ListLeftover(ctx)
	if err != nil {
		return fmt.Errorf("app: list leftover rooms: %w", err)
	}
	for _, r := range leftover {
		if err := a.notifier.NotifyAbort(ctx, r); err != nil {
			a.log.Error("app: abort notify failed", "room", string(r.Code), "error", err)
			continue
		}
		a.log.Info("app: notified leftover room abort", "room", string(r.Code), "host", int64(r.HostUserID))
	}
	return nil
}
