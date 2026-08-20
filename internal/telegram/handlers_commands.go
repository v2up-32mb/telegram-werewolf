package telegram

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// 玩家命令与帮助入口适配层（docs 游戏流程设计.md §一.6 创建入口、
// §命令清单、§新手引导、§发言 4 自毁提示）：/start /newgame /join
// /role /score /leave /help + /rank 占位；私聊限定；无房/死亡/发牌前
// 状态反馈；所有回复经 i18n.Renderer 渲染（参数默认 MarkdownV2 转义）。
// 与 handlers_*.go 同风格：本层只做输入转换与回复渲染；生产注入
//（App/Room 分流、真实发送、角色/积分状态查询接线）属后续接线任务，
// 测试全部注入替身（不复制 reducer 逻辑：领域规则位于 game 服务）。

// 命令回复的 i18n 文案 key（active.zh-CN.yaml）。
const (
	CommandMenuMessageKey            = "menu.main"
	CommandHelpMessageKey            = "help.commands"
	CommandRulesMessageKey           = "rules.intro"
	CommandSelfDestructMessageKey    = "speech.self_destruct_hint"
	CommandRankMessageKey            = "rank.placeholder"
	CommandRoleMessageKey            = "commands.role"
	CommandScoreMessageKey           = "commands.score"
	CommandPrivateOnlyMessageKey     = "commands.private_only"
	CommandNoRoomMessageKey          = "commands.no_room"
	CommandDeadMessageKey            = "commands.dead"
	CommandNoRoleYetMessageKey       = "commands.no_role_yet"
	CommandAlreadyInRoomMessageKey   = "commands.already_in_room"
	CommandRoomFullMessageKey        = "commands.room_full"
	CommandWrongPasswordMessageKey   = "commands.wrong_password"
	CommandRoomNotFoundMessageKey    = "commands.room_not_found"
	CommandInvalidRoomCodeMessageKey = "commands.invalid_room_code"
	CommandNewGameDoneMessageKey     = "commands.newgame_done"
)

// RoleReply 是 /role 的查询结果：角色与阵营的中文展示名由接线层从
// game 状态与 i18n 准备（本层只渲染，不复制角色名映射）。
type RoleReply struct {
	RoleName string
	CampName string
}

// 命令面依赖的服务 seam（生产注入属后续接线任务）。CreateRoomService /
// JoinService 复用 handlers_create.go / handlers_join.go 的同包声明；
// 其余 seam 在此新增。
type (
	// LeaveService 是 /leave 的应用服务 seam：接线层按 Actor 解析所在房间
	// 并执行退出（领域规则在 game.LobbyLifecycleService.LeaveRoom，本层不
	// 复制）。
	LeaveService interface {
		Leave(ctx context.Context, actor game.UserID, commandID string) ([]game.Effect, error)
	}

	// RoleService 是 /role 的身份查询 seam（接线层从房间状态查询）。
	RoleService interface {
		Role(ctx context.Context, actor game.UserID) (RoleReply, error)
	}

	// ScoreService 是 /score 的积分查询 seam（接线层查 users.points；
	// 未知用户可返回 game.ErrNotInRoom 供本层映射无房反馈）。
	ScoreService interface {
		Score(ctx context.Context, actor game.UserID) (int64, error)
	}

	// ReplySender 是命令回复发送 seam：text 已是 i18n 渲染并转义后的
	// MarkdownV2 文本，由接线层按默认 MarkdownV2 解析模式发送。
	ReplySender interface {
		Send(ctx context.Context, chatID int64, text string) error
	}
)

// CommandInput 是玩家文本命令的归一化输入（App 分流接线层填充；
// IsPrivate 由命令面按 ChatID == UserID 判定）。
type CommandInput struct {
	CommandID  string
	Actor      game.UserID
	ChatID     int64
	UserID     int64
	Text       string
	ReceivedAt time.Time
	IsPrivate  bool
}

// CommandsHandler 处理玩家命令并产出 i18n 渲染回复。
type CommandsHandler struct {
	renderer *i18n.Renderer
	sender   ReplySender
	create   CreateRoomService
	join     JoinService
	leave    LeaveService
	roles    RoleService
	scores   ScoreService
}

// NewCommandsHandler 构造命令处理器；renderer 与 sender 必填。
func NewCommandsHandler(renderer *i18n.Renderer, sender ReplySender,
	create CreateRoomService, join JoinService, leave LeaveService,
	roles RoleService, scores ScoreService) (*CommandsHandler, error) {
	if renderer == nil || sender == nil {
		return nil, errors.New("telegram: commands handler requires renderer and sender")
	}
	return &CommandsHandler{
		renderer: renderer,
		sender:   sender,
		create:   create,
		join:     join,
		leave:    leave,
		roles:    roles,
		scores:   scores,
	}, nil
}

// Handle 处理一条命令输入：私聊限定 → 解析 → 分派；所有反馈经渲染后
// 发送，错误在适配层只做文案映射（领域判定在 game 服务）。
func (h *CommandsHandler) Handle(ctx context.Context, in CommandInput) error {
	if !in.IsPrivate {
		return h.reply(ctx, in.ChatID, CommandPrivateOnlyMessageKey, nil)
	}
	cmd, ok := ParseCommand(in.Text)
	if !ok {
		return h.replyInvalid(ctx, in)
	}
	switch cmd.Kind {
	case CommandStart:
		return h.reply(ctx, in.ChatID, CommandMenuMessageKey, nil)
	case CommandHelp:
		return h.replyHelp(ctx, in.ChatID)
	case CommandRank:
		// /rank 只返回「后续开放」占位说明，不查询任何排行榜数据
		//（docs §命令清单 2、§积分用途 4：排行榜后期）。
		return h.reply(ctx, in.ChatID, CommandRankMessageKey, nil)
	case CommandNewGame:
		return h.handleNewGame(ctx, in, cmd)
	case CommandJoin:
		return h.handleJoin(ctx, in, cmd)
	case CommandRole:
		return h.handleRole(ctx, in)
	case CommandScore:
		return h.handleScore(ctx, in)
	case CommandLeave:
		return h.handleLeave(ctx, in)
	default:
		return h.replyInvalid(ctx, in)
	}
}

// handleNewGame 复用 FromNewGameText 解析与 CreateRoomInput.Request，
// 单点调用建房服务（docs §一.6：/newgame 直接建）。
func (h *CommandsHandler) handleNewGame(ctx context.Context, in CommandInput, cmd ParsedCommand) error {
	parsed, ok := FromNewGameText(cmd.Text)
	if !ok {
		return h.replyInvalid(ctx, in)
	}
	parsed.CommandID = in.CommandID
	parsed.Actor = in.Actor
	_, _, err := h.create.CreateRoom(ctx, parsed.Request())
	if err != nil {
		return h.replyFeedback(ctx, in, err)
	}
	// 创建确认由领域 LobbyPanel 面板承担（含房间码、成员、邀请链接），
	// 不再单独发送 newgame_done 文案，避免双发消息。
	return nil
}

// handleJoin 复用 FromJoinText 解析与 JoinInput.Request，单点调用加入
// 服务（docs §二.1：/join 文本入口）。
func (h *CommandsHandler) handleJoin(ctx context.Context, in CommandInput, cmd ParsedCommand) error {
	parsed, ok := FromJoinText(cmd.Text)
	if !ok {
		return h.replyInvalid(ctx, in)
	}
	parsed.CommandID = in.CommandID
	parsed.Actor = in.Actor
	req, ok := parsed.Request()
	if !ok {
		return h.replyInvalid(ctx, in)
	}
	if _, _, err := h.join.Apply(ctx, req); err != nil {
		return h.replyFeedback(ctx, in, err)
	}
	// 成功不重复回复命令面确认：加入确认由领域 join.confirmed（含昵称/
	// 座位）承担，避免 #27 同语义双发（Task 46 冒烟缺陷修复）。
	return nil
}

// handleRole 查询身份视图并渲染；错误在适配层映射为状态反馈文案。
func (h *CommandsHandler) handleRole(ctx context.Context, in CommandInput) error {
	reply, err := h.roles.Role(ctx, in.Actor)
	if err != nil {
		return h.replyFeedback(ctx, in, err)
	}
	return h.reply(ctx, in.ChatID, CommandRoleMessageKey, map[string]any{
		"RoleName": reply.RoleName,
		"CampName": reply.CampName,
	})
}

// handleScore 查询并展示当前积分（users.points，接线层注入）。
func (h *CommandsHandler) handleScore(ctx context.Context, in CommandInput) error {
	points, err := h.scores.Score(ctx, in.Actor)
	if err != nil {
		return h.replyFeedback(ctx, in, err)
	}
	return h.reply(ctx, in.ChatID, CommandScoreMessageKey, map[string]any{
		"Points": points,
	})
}

// handleLeave 调用退出服务；成功不重复回复命令面确认——退出确认由
// 领域 lobby.left（含房间码）承担，避免 #27 同语义双发（Task 46 冒烟：
// /leave 真实环境发过 leave_done + lobby.left 两条重复文案）。
func (h *CommandsHandler) handleLeave(ctx context.Context, in CommandInput) error {
	if _, err := h.leave.Leave(ctx, in.Actor, in.CommandID); err != nil {
		return h.replyFeedback(ctx, in, err)
	}
	return nil
}

// reply 渲染一条回复并发送；参数由 Renderer 默认 MarkdownV2 转义。
func (h *CommandsHandler) reply(ctx context.Context, chatID int64, key string, data map[string]any) error {
	text, err := h.renderer.Render(key, data)
	if err != nil {
		return err
	}
	return h.sender.Send(ctx, chatID, text)
}

// replyInvalid 回复输入无效（error.invalid_input，Detail 原文经转义）。
func (h *CommandsHandler) replyInvalid(ctx context.Context, in CommandInput) error {
	return h.reply(ctx, in.ChatID, "error.invalid_input", map[string]any{"Detail": in.Text})
}

// replyFeedback 把服务返回的领域错误映射为状态反馈文案（无房/死亡/
// 发牌前/已在房间）；未识别错误回 error.generic，不把内部错误原文
// 泄漏给玩家。
func (h *CommandsHandler) replyFeedback(ctx context.Context, in CommandInput, err error) error {
	key := "error.generic"
	switch {
	case errors.Is(err, game.ErrNotInRoom):
		key = CommandNoRoomMessageKey
	case errors.Is(err, game.ErrDeadPlayer):
		key = CommandDeadMessageKey
	case errors.Is(err, game.ErrWrongPhase):
		// /role 发牌前无身份；其他「阶段不允许」按同类文案反馈。
		key = CommandNoRoleYetMessageKey
	case errors.Is(err, game.ErrHostInRoom):
		key = CommandAlreadyInRoomMessageKey
	case errors.Is(err, game.ErrAlreadyInRoom), errors.Is(err, game.ErrUserInRoom):
		// 重复加入本房 / 已在其他进行中房间：明确「已在房间」反馈
		//（Task 46 S7：此前未映射落入 error.generic）。
		key = CommandAlreadyInRoomMessageKey
	case errors.Is(err, game.ErrRoomNotFound), errors.Is(err, game.ErrRoomExpired):
		key = CommandRoomNotFoundMessageKey
	case errors.Is(err, game.ErrInvalidRoomCode):
		key = CommandInvalidRoomCodeMessageKey
	case errors.Is(err, game.ErrRoomFull):
		key = CommandRoomFullMessageKey
	case errors.Is(err, game.ErrWrongPassword):
		key = CommandWrongPasswordMessageKey
	case errors.Is(err, game.ErrCooldownActive):
		key = "commands.cooldown"
	}
	return h.reply(ctx, in.ChatID, key, nil)
}

// replyHelp 拼接命令清单 + 新手规则 + 首次发言 3 秒自毁提示后发送
// （docs §新手引导、§发言 4：入房时或首次发言前提前告知）。
func (h *CommandsHandler) replyHelp(ctx context.Context, chatID int64) error {
	var parts []string
	for _, key := range []string{
		CommandHelpMessageKey,
		CommandRulesMessageKey,
		CommandSelfDestructMessageKey,
	} {
		text, err := h.renderer.Render(key, nil)
		if err != nil {
			return err
		}
		parts = append(parts, text)
	}
	return h.sender.Send(ctx, chatID, strings.Join(parts, "\n\n"))
}
