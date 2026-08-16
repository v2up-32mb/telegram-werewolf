package telegram

import (
	"context"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// CreateRoomService 是建房领域服务在 Telegram 适配层的 seam。
// game.LobbyService.CreateRoom 签名即满足本接口；生产注入与 Effects
// 渲染属后续 P0 接线任务，本任务测试注入替身验证输入转换。
type CreateRoomService interface {
	CreateRoom(ctx context.Context, req game.CreateRoomRequest) (game.State, []game.Effect, error)
}

// CreateRoomHandler 是建房入口的 Telegram 适配器（docs §一.6 创建入口）：
// 只做输入转换——把主菜单「创建房间」按钮与 /newgame 文本归一化为同一个
// 领域请求，再调用同一个应用服务；两条入口不复制逻辑。
type CreateRoomHandler struct {
	service CreateRoomService
}

// NewCreateRoomHandler 构造建房适配器。
func NewCreateRoomHandler(service CreateRoomService) *CreateRoomHandler {
	return &CreateRoomHandler{service: service}
}

// CreateRoomInput 是建房输入的归一化形态：主菜单按钮与文本命令
// 都先落到本结构，再由 Request 转换为领域请求（输入转换单点）。
type CreateRoomInput struct {
	// CommandID 是幂等键（Router 的 update ID / 回调 token 语义）。
	CommandID string
	// Actor 是发起建房的用户 ID。
	Actor game.UserID
	// CustomCode 是房主自定义码；空表示使用随机码。
	CustomCode string
}

// FromMenuButton 返回主菜单「创建房间」按钮入口输入：无自定义码，
// 语义等价于 /newgame 无参数。
func FromMenuButton() CreateRoomInput {
	return CreateRoomInput{}
}

// FromNewGameText 解析 /newgame 文本命令：
//   - 无参数 → 随机码；
//   - 单参数 → 自定义码（大小写规范化是领域层职责，适配层原样透传）；
//   - 非 /newgame 或参数多于一个 → ok=false（明确拒绝，不静默取首参）。
//
// 与 internal/telegram/router.go 的文本路由保持一致（小写精确匹配，
// 不引入两套命令解析）。
func FromNewGameText(text string) (CreateRoomInput, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || fields[0] != "/newgame" || len(fields) > 2 {
		return CreateRoomInput{}, false
	}
	var code string
	if len(fields) == 2 {
		code = fields[1]
	}
	return CreateRoomInput{CustomCode: code}, true
}

// Request 把归一化输入转换为领域创建请求。主菜单与 /newgame 共用
// 本转换，保证两入口进入同一个应用服务、不复制逻辑。
func (in CreateRoomInput) Request() game.CreateRoomRequest {
	return game.CreateRoomRequest{
		CommandID:  in.CommandID,
		Host:       in.Actor,
		CustomCode: in.CustomCode,
	}
}

// Create 执行建房：适配层只做输入转换并单点调用应用服务，
// 领域规则（默认配置、唯一性、房间码规范化）全部位于 game 包。
func (h *CreateRoomHandler) Create(ctx context.Context, in CreateRoomInput) (game.State, []game.Effect, error) {
	return h.service.CreateRoom(ctx, in.Request())
}
