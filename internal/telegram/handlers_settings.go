package telegram

import (
	"context"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// SettingsService 是房间配置修改领域服务在 Telegram 适配层的 seam。
// game.SettingsService.Apply 签名即满足本接口；生产注入属后续 P0 接线任务。
type SettingsService interface {
	Apply(ctx context.Context, cmd game.SettingsCommand) (game.RoomSettings, []game.Effect, error)
}

// SettingsHandler 是房间配置表单输入的 Telegram 适配器（docs §一.6
// 房主控制面板设置入口）：只做输入转换，把配置表单按钮/文本归一化为
// game.SettingsCommand 并单点调用应用服务，不复制领域逻辑（默认值、
// 校验、发牌锁定、bcrypt 均在 game 包）。
type SettingsHandler struct {
	service SettingsService
}

// NewSettingsHandler 构造配置适配器。
func NewSettingsHandler(service SettingsService) *SettingsHandler {
	return &SettingsHandler{service: service}
}

// SettingsInput 是配置表单按钮的归一化输入（输入转换单点）。
type SettingsInput struct {
	// CommandID 是幂等键（Router 的 update ID / 回调 token 语义）。
	CommandID string
	// Actor 是发起修改的用户（房主）。
	Actor game.UserID
	// RoomID 是目标房间。
	RoomID game.RoomID
	// Phase 是当前阶段：仅 PhaseLobby 可修改（发牌后锁定由领域服务拒绝）。
	Phase game.Phase
	// PhaseVersion 是期望阶段版本。
	PhaseVersion uint64
	// Settings 是目标完整配置快照。
	Settings game.RoomSettings
	// Password 是密码修改意图：nil=不修改；空串=清除；非空=设置新密码
	//（明文经领域层 bcrypt 哈希，绝不下落存储层）。
	Password *string
}

// Command 把归一化输入转换为领域命令；ExpectedPhase 取输入 Phase，
// 发牌后由 SettingsService 以 ErrSettingsLocked 拒绝。
func (in SettingsInput) Command() game.SettingsCommand {
	return game.SettingsCommand{
		Meta: game.CommandMeta{
			ID:            in.CommandID,
			Actor:         in.Actor,
			ExpectedPhase: in.Phase,
			PhaseVersion:  in.PhaseVersion,
		},
		RoomID:   in.RoomID,
		Settings: in.Settings,
		Password: in.Password,
	}
}

// Apply 执行配置修改：适配层只做输入转换并单点调用应用服务。
func (h *SettingsHandler) Apply(ctx context.Context, in SettingsInput) (game.RoomSettings, []game.Effect, error) {
	return h.service.Apply(ctx, in.Command())
}
