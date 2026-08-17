package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// Router 把 Update 输解析为领域 Command，不直接修改房间状态
// （docs/技术选型.md §接入边界：业务模块接收统一领域输入）。
//
// 单 dispatcher 语义：Dispatch 按顺序处理 update，应用层 apply 返回 nil
// （处理成功或明确拒绝）后提交 cursor；apply 返回错误时 cursor 不推进，
// 崩溃重投由领域幂等约束安全承受。
type Router struct {
	dedupe *Deduper
	store  CursorStore
	tokens *CallbackManager
}

// NewRouter 创建路由器。
func NewRouter(dedupe *Deduper, store CursorStore, tokens *CallbackManager) *Router {
	return &Router{dedupe: dedupe, store: store, tokens: tokens}
}

// InitialOffset 返回传给 Long Polling 的初始偏移（已 ACK 水位 + 1）。
func (r *Router) InitialOffset(ctx context.Context) int64 {
	hw, err := r.store.Load(ctx)
	if err != nil {
		return 0
	}
	return hw + 1
}

// Dispatch 单 dispatcher 处理一个 update：
//   - 重复/历史 updateID：直接丢弃（不重放，不写 cursor）；
//   - 不可识别输入 / token 无效 / 越权等明确拒绝：无命令但 ACK（提交 cursor）；
//   - apply 返回 nil：ACK（Mark 已在 Accept 完成，随后提交 cursor）；
//   - apply 返回错误：不提交 cursor（崩溃后重投，领域幂等承受）。
func (r *Router) Dispatch(ctx context.Context, u Update, apply func(context.Context, game.Command) error) error {
	if !r.dedupe.Accept(u.UpdateID) {
		return nil
	}
	cmd, ok := r.routeOne(u)
	if !ok {
		return r.store.Save(ctx, u.UpdateID)
	}
	if err := apply(ctx, cmd); err != nil {
		return err
	}
	return r.store.Save(ctx, u.UpdateID)
}

// DispatchText 是文本命令的独立分派入口（App 接线层使用）：保持
// update_id 去重与 ACK cursor 语义，但把原始 Update 原样交给接线层解析
// ——Task 41 命令面（CommandsHandler）需要保留 /newgame 自定义码等文本
// 参数，不能折叠为 game.Command。与 Dispatch 仅差 apply 的入参形态。
func (r *Router) DispatchText(ctx context.Context, u Update, apply func(context.Context, Update) error) error {
	if !r.dedupe.Accept(u.UpdateID) {
		return nil
	}
	if err := apply(ctx, u); err != nil {
		return err
	}
	return r.store.Save(ctx, u.UpdateID)
}

// routeOne 把 update 解析为领域命令；不产生命令时返回 ok=false
// （不可识别文本 / 越权或失效 token / 未知 action）。
func (r *Router) routeOne(u Update) (game.Command, bool) {
	if u.Message != nil {
		meta := textCommandMeta(u)
		switch normalizeCommand(u.Message.Text) {
		case "/start", "/newgame":
			return game.CreateRoomCommand{Meta: meta}, true
		case "/join":
			return game.JoinRoomCommand{Meta: meta}, true
		default:
			return nil, false
		}
	}
	if u.CallbackQuery != nil {
		payload, err := r.tokens.Validate(u.CallbackQuery.Data, game.UserID(u.CallbackQuery.UserID))
		if err != nil {
			return nil, false
		}
		meta := game.CommandMeta{
			ID:            fmt.Sprintf("u%d", u.UpdateID),
			Actor:         payload.Owner,
			ExpectedPhase: payload.ExpectedPhase,
			PhaseVersion:  payload.PhaseVersion,
			ReceivedAt:    u.ReceivedAt,
		}
		return callbackCommand(payload, meta)
	}
	return nil, false
}

// textCommandMeta 为文本命令构造 Meta（阶段/版本由领域后续校验填充为 0）。
func textCommandMeta(u Update) game.CommandMeta {
	return game.CommandMeta{
		ID:         fmt.Sprintf("u%d", u.UpdateID),
		Actor:      game.UserID(u.Message.UserID),
		ReceivedAt: u.ReceivedAt,
	}
}

// callbackCommand 按 token action 构造领域命令。
func callbackCommand(p *TokenPayload, meta game.CommandMeta) (game.Command, bool) {
	switch p.Action {
	case "confirm_role":
		return game.ConfirmRoleCommand{Meta: meta}, true
	case "start_game":
		return game.StartGameCommand{Meta: meta}, true
	case "vote":
		if p.Target == "" || p.Target == "abstain" {
			return game.VoteCommand{Meta: meta, Target: nil}, true
		}
		target := parseSeat(p.Target)
		return game.VoteCommand{Meta: meta, Target: &target}, true
	case "wolf_kill":
		return game.WolfKillCommand{Meta: meta, Target: parseSeat(p.Target)}, true
	case "seer_check":
		return game.SeerCheckCommand{Meta: meta, Target: parseSeat(p.Target)}, true
	case "witch_use":
		action, target := parseWitchUse(p.Target)
		return game.WitchUseCommand{Meta: meta, Action: action, Target: target}, true
	case "speak":
		return game.SpeakCommand{Meta: meta, Text: p.Target}, true
	default:
		return nil, false
	}
}

// normalizeCommand 规范化文本命令（去掉首尾空白）。
func normalizeCommand(text string) string {
	return strings.TrimSpace(text)
}

// parseSeat 把 "3" 解析为座位号；非法时返回 0。
func parseSeat(s string) game.Seat {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 8)
	if err != nil {
		return 0
	}
	return game.Seat(n)
}

// parseWitchUse 把 "save:3" / "poison:3" 解析为用药动作与目标。
func parseWitchUse(target string) (game.WitchAction, game.Seat) {
	parts := strings.SplitN(target, ":", 2)
	switch parts[0] {
	case "save":
		if len(parts) == 2 {
			return game.WitchActionSave, parseSeat(parts[1])
		}
	case "poison":
		if len(parts) == 2 {
			return game.WitchActionPoison, parseSeat(parts[1])
		}
	}
	return game.WitchActionUnknown, 0
}
