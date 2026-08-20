package telegram

import "strings"

// 玩家文本命令解析（docs 游戏流程设计.md §一.6 创建入口、§命令清单）：
// /start /newgame /join /role /score /leave /help 七大命令 + /rank 占位。
// 解析层只做字形归一化（精确小写、容忍首尾空白、按命令允许的参数个数
// 收窄）；参数内容校验（房间码/深链/自定义码）复用既有 From* 入口，
// 不复制第二套规则。

// CommandKind 是玩家命令面支持的命令分类。
type CommandKind int

const (
	CommandUnknown CommandKind = iota
	CommandStart               // /start 主菜单（docs §一.6）
	CommandNewGame             // /newgame 创建房间（房主专属，可选自定义码）
	CommandJoin                // /join 加入房间（房间码或邀请深链）
	CommandRole                // /role 查看身份
	CommandScore               // /score 查看积分
	CommandLeave               // /leave 退出房间
	CommandHelp                // /help 帮助（命令清单 + 新手规则 + 发言自毁提示）
	CommandRank                // /rank 排行榜（仅返回「后续开放」说明）
)

// String 返回命令的英文短名，供测试与日志使用。
func (k CommandKind) String() string {
	switch k {
	case CommandStart:
		return "start"
	case CommandNewGame:
		return "newgame"
	case CommandJoin:
		return "join"
	case CommandRole:
		return "role"
	case CommandScore:
		return "score"
	case CommandLeave:
		return "leave"
	case CommandHelp:
		return "help"
	case CommandRank:
		return "rank"
	default:
		return "unknown"
	}
}

// ParsedCommand 是一条文本命令的解析结果。
type ParsedCommand struct {
	// Kind 是命令分类；CommandUnknown 表示不可识别。
	Kind CommandKind
	// Text 是命令原文（去首尾空白），供既有 From* 入口复用解析。
	Text string
	// Args 是命令名之后的参数原文：/newgame 至多 1 个（自定义码）；
	// /join 恰好 1 个（房间码/深链）；其余命令 0 个。
	Args []string
}

// ParseCommand 解析玩家文本命令：
//   - 精确小写匹配命令名、容忍首尾空白（与 router.normalizeCommand 一致）；
//   - /newgame 至多 1 参数（自定义码）；/join 恰好 1 参数（房间码或
//     Telegram 邀请深链）；/start /role /score /leave /help /rank 零参数，
//     带参明确拒绝（不静默取首参，与既有 From* 语义一致）；
//   - 不可识别文本或参数个数不符返回 ok=false。
//
// 房间码/自定义码/深链的内容校验不在本层复制：由对应 From* 入口
// （FromNewGameText/FromJoinText）单点完成。
func ParseCommand(text string) (ParsedCommand, bool) {
	trimmed := strings.TrimSpace(text)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ParsedCommand{}, false
	}
	kind, ok := commandKind(fields[0])
	if !ok {
		return ParsedCommand{}, false
	}
	args := fields[1:]
	switch kind {
	case CommandNewGame:
		if len(args) > 1 {
			return ParsedCommand{}, false
		}
	case CommandJoin:
		if len(args) != 1 {
			return ParsedCommand{}, false
		}
	default:
		if len(args) != 0 {
			return ParsedCommand{}, false
		}
	}
	if len(args) == 0 {
		args = nil
	}
	return ParsedCommand{Kind: kind, Text: trimmed, Args: args}, true
}

// commandKind 把命令名原文映射为分类；不可识别返回 ok=false。
func commandKind(raw string) (CommandKind, bool) {
	switch raw {
	case "/start":
		return CommandStart, true
	case "/newgame":
		return CommandNewGame, true
	case "/join":
		return CommandJoin, true
	case "/role":
		return CommandRole, true
	case "/score":
		return CommandScore, true
	case "/leave":
		return CommandLeave, true
	case "/help":
		return CommandHelp, true
	case "/rank":
		return CommandRank, true
	default:
		return CommandUnknown, false
	}
}

// IsPrivateChat 报告消息来源是否为 Bot 私聊：Telegram Bot API 中私聊的
// chat.id 等于发送者 user.id（docs 命令清单：命令在私聊 Bot 中使用）。
func IsPrivateChat(u Update) bool {
	return u.Message != nil && u.Message.ChatID == u.Message.UserID
}

// BotCommands 返回全部斜杠命令的 BotCommand 列表，供 setMyCommands
// 注册到 Telegram（用户输入 / 时自动弹出命令提示）。
// 描述文案与 i18n active.zh-CN.yaml 保持一致。
func BotCommands() []BotCommand {
	return []BotCommand{
		{Command: "start", Description: "主菜单"},
		{Command: "newgame", Description: "创建房间（可选自定义房间码）"},
		{Command: "join", Description: "加入房间（房间码或邀请链接）"},
		{Command: "role", Description: "查看身份"},
		{Command: "score", Description: "查看积分"},
		{Command: "leave", Description: "退出房间"},
		{Command: "help", Description: "帮助（命令清单+新手规则）"},
		{Command: "rank", Description: "排行榜"},
	}
}
