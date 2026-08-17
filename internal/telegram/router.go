package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	return CallbackCommand(p, meta)
}

// CallbackAction 是回调 token 校验后的领域动作（B1-b）：供接线层把非
// reducer 动作（end_speech 等导演本地信号）分流给导演，reducer 动作再经
// CallbackCommand 转为领域命令。
type CallbackAction struct {
	UpdateID        int64
	Owner           game.UserID
	Action          string
	Target          string
	ExpectedPhase   game.Phase
	PhaseVersion    uint64
	ReceivedAt      time.Time
	CallbackQueryID string // B3：answerCallbackQuery 必答（docs §9）
}

// routeCallback 校验回调 token 并返回领域动作。
func (r *Router) routeCallback(u Update) (CallbackAction, bool) {
	payload, err := r.tokens.Validate(u.CallbackQuery.Data, game.UserID(u.CallbackQuery.UserID))
	if err != nil {
		return CallbackAction{}, false
	}
	return CallbackAction{
		UpdateID:        u.UpdateID,
		Owner:           payload.Owner,
		Action:          payload.Action,
		Target:          payload.Target,
		ExpectedPhase:   payload.ExpectedPhase,
		PhaseVersion:    payload.PhaseVersion,
		ReceivedAt:      u.ReceivedAt,
		CallbackQueryID: u.CallbackQuery.ID,
	}, true
}

// DispatchAction 单 dispatcher 处理一个回调查询动作：保持 update_id 去重、
// token 校验与 ACK cursor 语义；apply 收到的是动作而非命令，供导演分流
// （docs/阶段消息设计.md §9 顶部通知始终应答；不可识别动作属明确拒绝，ACK）。
func (r *Router) DispatchAction(ctx context.Context, u Update, apply func(context.Context, CallbackAction) error) error {
	if !r.dedupe.Accept(u.UpdateID) {
		return nil
	}
	act, ok := r.routeCallback(u)
	if !ok {
		return r.store.Save(ctx, u.UpdateID)
	}
	if err := apply(ctx, act); err != nil {
		return err
	}
	return r.store.Save(ctx, u.UpdateID)
}

// CallbackCommand 把回调动作映射为领域命令（B1-b：覆盖引擎新命令集与
// 治理/再来一局；旧的 wolf_kill/witch_use/speak 动作退役）。
// 非 reducer 动作（end_speech 等导演本地信号）返回 ok=false。
func CallbackCommand(p *TokenPayload, meta game.CommandMeta) (game.Command, bool) {
	targetPtr := func() *game.Seat {
		if p.Target == "" || p.Target == "abstain" {
			return nil
		}
		s := parseSeat(p.Target)
		return &s
	}
	switch p.Action {
	case "confirm_role":
		return game.ConfirmRoleCommand{Meta: meta}, true
	case "start_game":
		return game.StartGameCommand{Meta: meta}, true
	case "vote":
		return game.VoteCommand{Meta: meta, Target: targetPtr()}, true
	case "vote_confirm":
		return game.VoteConfirmCommand{Meta: meta}, true
	case "wolf_vote":
		return game.WolfVoteCommand{Meta: meta, Target: targetPtr()}, true
	case "wolf_confirm":
		return game.WolfConfirmCommand{Meta: meta}, true
	case "witch_save":
		return game.WitchSaveCommand{Meta: meta, Use: p.Target == "yes"}, true
	case "witch_poison":
		return game.WitchPoisonCommand{Meta: meta, Target: targetPtr()}, true
	case "witch_confirm":
		return game.WitchConfirmCommand{Meta: meta}, true
	case "seer_check":
		return game.SeerCheckCommand{Meta: meta, Target: parseSeat(p.Target)}, true
	case "seer_confirm":
		return game.SeerConfirmCommand{Meta: meta}, true
	case "explode":
		return game.ExplodeCommand{Meta: meta}, true
	case "leave_game":
		return game.LeaveGameCommand{Meta: meta}, true
	case "rematch":
		return game.RematchCommand{Meta: meta}, true
	case "last_words":
		return game.LastWordsCommand{Meta: meta, Text: p.Target}, true
	case "governance_dissolve":
		return game.GovernanceDissolveCommand{Meta: meta}, true
	case "governance_dissolve_vote":
		return game.GovernanceDissolveVoteCommand{Meta: meta}, true
	case "governance_kick":
		return game.GovernanceKickCommand{Meta: meta, Target: parseSeat(p.Target)}, true
	case "governance_kick_vote":
		return game.GovernanceKickVoteCommand{Meta: meta}, true
	case "host_dissolve":
		return game.HostDissolveCommand{Meta: meta, Confirm: p.Target == "confirm"}, true
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
