package game

import (
	"context"
	"errors"
	"fmt"
)

// LobbyRoomRegistry 是建房唯一性校验的领域 seam（docs §一.7、
// §房间码唯一性）：生产实现由 room.Manager/storage 唯一约束适配
// （后续 P0 接线任务注入），本任务测试注入替身。
type LobbyRoomRegistry interface {
	// HostActive 报告宿主是否已在一个进行中房间。
	HostActive(host UserID) bool
	// ReserveCode 尝试把房间码标记为占用；false 表示该码已被占用。
	ReserveCode(ctx context.Context, code RoomID) (bool, error)
}

// defaultMaxCodeTries 是随机码唯一性重试上限。
const defaultMaxCodeTries = 8

// LobbyService 是创建房间领域流程（docs/游戏流程设计.md §一、§房间码规范）。
// 纯核心：唯一性校验经 Registry seam，随机码经可注入 RNG，不触碰
// Telegram/SQLite/系统时间/全局随机源（docs/技术选型.md §5.1）。
type LobbyService struct {
	registry     LobbyRoomRegistry
	rng          RNG
	maxCodeTries int
}

// NewLobbyService 构造建房服务。registry 必填（唯一性是建房硬约束）；
// rng 为空时使用 crypto/rand 实现。
func NewLobbyService(registry LobbyRoomRegistry, rng RNG) (LobbyService, error) {
	if registry == nil {
		return LobbyService{}, errors.New("game: lobby service requires a registry")
	}
	if rng == nil {
		rng = CryptoRNG{}
	}
	return LobbyService{registry: registry, rng: rng, maxCodeTries: defaultMaxCodeTries}, nil
}

// DefaultCreateRoomConfig 返回 MVP 6 人局默认建房配置
// （docs「6 人局（MVP）默认配置总表」）：
// 6 人 = 2 狼 + 预言家 + 女巫 + 2 平民，默认屠城，无 AI。
func DefaultCreateRoomConfig() GameConfig {
	return GameConfig{
		PlayerCount: MVPPlayerCount,
		Roles:       StandardDeck(),
		UseAI:       false,
		Victory:     VictorySlaughter,
	}
}

// isZeroConfig 判断请求配置是否零值；零值表示调用方未显式指定，
// 使用 MVP 默认配置（docs「6 人局默认配置总表」）。
func isZeroConfig(c GameConfig) bool {
	return c.PlayerCount == 0 && len(c.Roles) == 0 && !c.UseAI && c.Victory == VictoryUnknown
}

// CreateRoom 执行创建房间领域流程：
//  1. 宿主唯一性：同一房主只能开 1 个进行中的房间；
//  2. 房间码：自定义码规范化（大写）并保留唯一性，占用即明确拒绝；
//     随机码生成且碰撞重试直至唯一；
//  3. 状态：房主自动占 1 席且座位固定为 1（HostSeat）；
//  4. 副作用：最小活跃局记录（创建确认/房间面板由命令面与接线层承担，
//     领域层只返回 PersistEffect，docs/测试验收清单.md Task 46 S3）。
//
// 任一拒绝都返回明确错误且不产生部分状态或 Effects。
func (s LobbyService) CreateRoom(ctx context.Context, req CreateRoomRequest) (State, []Effect, error) {
	if s.registry.HostActive(req.Host) {
		return State{}, nil, ErrHostInRoom
	}

	cfg := req.Config
	if isZeroConfig(cfg) {
		cfg = DefaultCreateRoomConfig()
	}
	if err := cfg.Validate(); err != nil {
		return State{}, nil, fmt.Errorf("game: create room config: %w", err)
	}

	code, err := s.allocateCode(ctx, req.CustomCode)
	if err != nil {
		return State{}, nil, err
	}

	st := State{
		RoomID:       code,
		Phase:        PhaseLobby,
		PhaseVersion: 1,
		Players:      []Player{{UserID: req.Host, Seat: HostSeat}},
		Lobby:        LobbyState{Owner: req.Host, Config: cfg},
		Processed:    map[string]bool{req.CommandID: true},
	}

	// 创建确认由命令面 commands.newgame_done 承担、房间面板由接线层
	// 推送（Task 46 冒烟修复：领域层不再产出 lobby.created 文案 effect，
	// 避免同一次 /newgame 连发三条消息与缺失文案的 und 渲染错误）。
	return st, []Effect{PersistEffect{Kind: PersistActiveGame}}, nil
}

// allocateCode 决定房间码：
//   - 自定义码：规范化为大写后校验 4～8 位字母数字，占用时返回
//     ErrRoomCodeTaken（绝不偷偷替换为随机码，docs「房间码规范」3）；
//   - 随机码：生成去混淆 6 位码并碰撞重试直至唯一，耗尽返回 ErrCodeExhausted。
func (s LobbyService) allocateCode(ctx context.Context, custom string) (RoomID, error) {
	if custom != "" {
		code := RoomID(NormalizeRoomCode(custom))
		if !ValidCustomRoomCode(string(code)) {
			return "", ErrInvalidRoomCode
		}
		ok, err := s.registry.ReserveCode(ctx, code)
		if err != nil {
			return "", fmt.Errorf("game: reserve room code %q: %w", code, err)
		}
		if !ok {
			return "", ErrRoomCodeTaken
		}
		return code, nil
	}
	for i := 0; i < s.maxCodeTries; i++ {
		code, err := GenerateRoomCode(s.rng, RandomRoomCodeLength)
		if err != nil {
			return "", err
		}
		ok, err := s.registry.ReserveCode(ctx, code)
		if err != nil {
			return "", fmt.Errorf("game: reserve room code %q: %w", code, err)
		}
		if ok {
			return code, nil
		}
	}
	return "", ErrCodeExhausted
}
