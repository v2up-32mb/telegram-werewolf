package telegram

import (
	"context"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// LobbyLifecycleService 是大厅生命周期领域服务在 Telegram 适配层的
// seam。game.LobbyLifecycleService 各方法签名即满足本接口；生产接线
// 由 room actor 注入 State/LobbyLifetime（后续 P0 任务），本任务测试
// 注入替身验证输入转换。
type LobbyLifecycleService interface {
	LeaveRoom(ctx context.Context, st game.State, cmd game.LeaveCommand) (game.State, []game.Effect, error)
	KickPlayer(ctx context.Context, st game.State, cmd game.KickCommand) (game.State, []game.Effect, error)
	RenewRoom(ctx context.Context, st game.State, cmd game.RenewCommand, lt game.LobbyLifetime) (game.State, game.LobbyLifetime, []game.Effect, error)
	EvaluateIdle(ctx context.Context, lt game.LobbyLifetime, st game.State) (game.LobbyLifetime, []game.Effect, error)
}

// LobbyHandler 是大厅操作入口的 Telegram 适配器（docs §命令：
// /leave 退出房间；房主移除/续期/解散按钮）：只做输入转换，把文本
// 命令与按钮归一化为领域命令并单点调用领域服务，不复制领域逻辑
// （移交、回收、房主校验均在 game 包）。
type LobbyHandler struct {
	service LobbyLifecycleService
}

// NewLobbyHandler 构造大厅操作适配器。
func NewLobbyHandler(service LobbyLifecycleService) *LobbyHandler {
	return &LobbyHandler{service: service}
}

// LeaveInput 是玩家退出命令的归一化输入。
type LeaveInput struct {
	CommandID    string
	Actor        game.UserID
	Phase        game.Phase
	PhaseVersion uint64
}

// Command 把归一化输入转换为领域退出命令。
func (in LeaveInput) Command() game.LeaveCommand {
	return game.LeaveCommand{
		Meta: game.CommandMeta{
			ID:            in.CommandID,
			Actor:         in.Actor,
			ExpectedPhase: in.Phase,
			PhaseVersion:  in.PhaseVersion,
		},
	}
}

// FromLeaveText 解析 /leave 文本命令：仅精确的单个 /leave（容忍首尾
// 空白清洗）；带参数或非 /leave 文本返回 ok=false（与 router 小写精确
// 匹配一致，不引入两套命令解析）。/leave 无参数，返回的输入字段
// （CommandID/Actor）由 Router 装配时从 Update 填充。
func FromLeaveText(text string) (LeaveInput, bool) {
	if strings.TrimSpace(text) != "/leave" {
		return LeaveInput{}, false
	}
	return LeaveInput{}, true
}

// KickInput 是房主移除玩家命令的归一化输入。
type KickInput struct {
	CommandID    string
	Actor        game.UserID
	Phase        game.Phase
	PhaseVersion uint64
	Target       game.UserID
}

// Command 把归一化输入转换为领域移除命令。
func (in KickInput) Command() game.KickCommand {
	return game.KickCommand{
		Meta: game.CommandMeta{
			ID:            in.CommandID,
			Actor:         in.Actor,
			ExpectedPhase: in.Phase,
			PhaseVersion:  in.PhaseVersion,
		},
		Target: in.Target,
	}
}

// RenewInput 是房主续期命令的归一化输入。
type RenewInput struct {
	CommandID    string
	Actor        game.UserID
	Phase        game.Phase
	PhaseVersion uint64
}

// Command 把归一化输入转换为领域续期命令。
func (in RenewInput) Command() game.RenewCommand {
	return game.RenewCommand{
		Meta: game.CommandMeta{
			ID:            in.CommandID,
			Actor:         in.Actor,
			ExpectedPhase: in.Phase,
			PhaseVersion:  in.PhaseVersion,
		},
	}
}

// Leave 执行玩家退出：适配层只做输入转换并单点调用领域服务。
func (h *LobbyHandler) Leave(ctx context.Context, in LeaveInput, st game.State) (game.State, []game.Effect, error) {
	return h.service.LeaveRoom(ctx, st, in.Command())
}

// Kick 执行房主移除玩家：适配层只做输入转换并单点调用领域服务。
func (h *LobbyHandler) Kick(ctx context.Context, in KickInput, st game.State) (game.State, []game.Effect, error) {
	return h.service.KickPlayer(ctx, st, in.Command())
}

// Renew 执行房主续期：适配层只做输入转换并单点调用领域服务。
func (h *LobbyHandler) Renew(ctx context.Context, in RenewInput, st game.State, lt game.LobbyLifetime) (game.State, game.LobbyLifetime, []game.Effect, error) {
	return h.service.RenewRoom(ctx, st, in.Command(), lt)
}
