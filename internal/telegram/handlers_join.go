package telegram

import (
	"context"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// JoinService 是加入房间领域服务在 Telegram 适配层的 seam。
// game.JoinService.Apply 签名即满足本接口；生产注入与 Effects 渲染
// 属后续 P0 接线任务，本任务测试注入替身验证输入转换。
type JoinService interface {
	Apply(ctx context.Context, req game.JoinRequest) (game.JoinResult, []game.Effect, error)
}

// JoinHandler 是加入房间入口的 Telegram 适配器（docs §二.1 加入入口）：
// 只做输入转换——把 /join 文本命令、深链按钮、手输房间码归一化为
// 同一个领域请求，再调用同一个应用服务；三条入口不复制领域逻辑
// （密码校验、昵称唯一、满员/重入检查均在 game 包）。
type JoinHandler struct {
	service JoinService
}

// NewJoinHandler 构造加入适配器。
func NewJoinHandler(service JoinService) *JoinHandler {
	return &JoinHandler{service: service}
}

// JoinInput 是加入输入的归一化形态：三条入口都先落到本结构，
// 再由 Request 转换为领域请求（输入转换单点）。
type JoinInput struct {
	// CommandID 是幂等键（Router 的 update ID / 回调 token 语义）。
	CommandID string
	// Actor 是加入玩家的用户 ID。
	Actor game.UserID
	// RawCode 是规范化（大写）后的房间码；深链与手输均在此归一化，
	// 非法输入在入口解析或 Request 阶段被拒绝。
	RawCode string
	// Password 是房间密码意图：nil=未提供；非空=提供密码。
	Password *string
	// Nickname 是用户指定昵称：nil=随机分配；非空=指定昵称。
	Nickname *string
}

// FromJoinText 解析 /join 文本命令：单参数为房间码或 Telegram 邀请
// 深链。房间码统一规范化为大写；畸形文本（无参数、多参数、非法
// 字符、非 /join）返回 ok=false，不静默取首参。
func FromJoinText(text string) (JoinInput, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) != 2 || fields[0] != "/join" {
		return JoinInput{}, false
	}
	code, ok := normalizeJoinCode(fields[1])
	if !ok {
		return JoinInput{}, false
	}
	return JoinInput{RawCode: string(code)}, true
}

// FromInviteDeepLink 解析深链按钮输入：提取 start 参数房间码并
// 规范化为大写；非深链或无法解析返回 ok=false。
func FromInviteDeepLink(link string) (JoinInput, bool) {
	code, ok := game.ParseInviteDeepLink(link)
	if !ok {
		return JoinInput{}, false
	}
	return JoinInput{RawCode: string(code)}, true
}

// normalizeJoinCode 归一化房间码输入：深链取 start 参数（领域层已
// 大写规范化），普通文本按房间码规范化；结果必须是合法房间码
// （4～8 位字母数字，覆盖 6 位随机码），否则 ok=false。
func normalizeJoinCode(raw string) (game.RoomID, bool) {
	var code game.RoomID
	if strings.Contains(raw, "t.me/") {
		var ok bool
		code, ok = game.ParseInviteDeepLink(raw)
		if !ok {
			return "", false
		}
	} else {
		code = game.RoomID(game.NormalizeRoomCode(raw))
	}
	if !game.ValidCustomRoomCode(string(code)) {
		return "", false
	}
	return code, true
}

// Request 把归一化输入转换为领域加入请求。房间码再次规范化（幂等）
// 并校验合法性；畸形码返回 ok=false。
func (in JoinInput) Request() (game.JoinRequest, bool) {
	code := game.NormalizeRoomCode(in.RawCode)
	if !game.ValidCustomRoomCode(code) {
		return game.JoinRequest{}, false
	}
	return game.JoinRequest{
		CommandID: in.CommandID,
		Actor:     in.Actor,
		RoomID:    game.RoomID(code),
		Password:  in.Password,
		Nickname:  in.Nickname,
	}, true
}

// Join 执行加入：适配层只做输入转换并单点调用应用服务，
// 领域规则（密码、昵称、满员/重入）全部位于 game 包。
func (h *JoinHandler) Join(ctx context.Context, in JoinInput) (game.JoinResult, []game.Effect, error) {
	req, ok := in.Request()
	if !ok {
		return game.JoinResult{}, nil, game.ErrInvalidRoomCode
	}
	return h.service.Apply(ctx, req)
}
