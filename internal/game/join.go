package game

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 加入房间领域流程的消息 key（docs §二 加入房间；渲染由后续任务
// 按 key + params 产出 MarkdownV2 文案）。
const (
	// JoinConfirmedMessageKey 是加入成功通知本人的消息 key。
	JoinConfirmedMessageKey = "join.confirmed"
	// LobbyPanelMessageKey 是房主房间面板刷新消息 key。
	LobbyPanelMessageKey = "lobby.panel"
)

// 加入房间领域规则的哨兵错误。
var (
	// ErrRoomNotFound 表示房间不存在（区别于已过期/已满）。
	ErrRoomNotFound = errors.New("game: room not found")
	// ErrRoomExpired 表示房间已过期/被回收（邀请链接失效原因之一）。
	ErrRoomExpired = errors.New("game: room expired")
	// ErrRoomFull 表示房间已满员、不可加入（docs §二.3）。
	ErrRoomFull = errors.New("game: room is full")
	// ErrAlreadyInRoom 表示用户已在该房间（重复加入）。
	ErrAlreadyInRoom = errors.New("game: user already in this room")
	// ErrAlreadyLeft 表示用户退出过该局，不可重入
	// （docs §「退出玩家不能重新加入已退出过的同一局游戏」）。
	ErrAlreadyLeft = errors.New("game: user already left this game")
	// ErrWrongPassword 表示房间密码错误；错误可无限次重试、不锁定
	// （docs「密码」）。
	ErrWrongPassword = errors.New("game: wrong room password")
	// ErrUserInRoom 表示用户已在一个进行中房间（同一时间只能加入
	// 1 个进行中的房间，docs §二.4）。
	ErrUserInRoom = errors.New("game: user already in another active room")
)

// telegramDeepLinkPrefix 是 Telegram 邀请深链主机前缀（t.me）。
const telegramDeepLinkPrefix = "t.me/"

// ParseInviteDeepLink 从 Telegram 邀请深链解析房间码：识别
// `https://t.me/<bot>?start=<CODE>` 与 `t.me/<bot>?start=<CODE>`
// 形态，提取 start 参数并返回规范化（大写）后的房间码
// （docs「房间码规范」2：大小写不敏感，统一转为大写存储和显示）；
// 合法性校验由 JoinRequest/JoinService 负责。
// 无法解析或 start 为空返回 ok=false。
func ParseInviteDeepLink(link string) (RoomID, bool) {
	raw := strings.TrimSpace(link)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	if !strings.HasPrefix(raw, telegramDeepLinkPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(raw, telegramDeepLinkPrefix)
	qidx := strings.IndexByte(rest, '?')
	if qidx < 0 {
		return "", false
	}
	query, err := url.ParseQuery(rest[qidx+1:])
	if err != nil {
		return "", false
	}
	code := query.Get("start")
	if strings.TrimSpace(code) == "" {
		return "", false
	}
	return RoomID(NormalizeRoomCode(code)), true
}

// JoinStore 是加入房间领域流程的持久化/唯一性 seam（docs/技术选型.md
// §5.1：核心不触碰 SQLite）。生产实现由 storage/room 适配（后续 P0
// 接线任务注入），本任务测试注入替身。
type JoinStore interface {
	// LoadPasswordHash 返回房间当前 bcrypt 密码哈希；空串=未设密码。
	LoadPasswordHash(ctx context.Context, roomID RoomID) (string, error)
	// CheckRoom 校验房间存在且未过期；nil=可尝试加入。
	// 不存在返回 ErrRoomNotFound，已过期/被回收返回 ErrRoomExpired。
	CheckRoom(ctx context.Context, roomID RoomID) error
	// HasPlayer 报告用户是否已在目标房间。
	HasPlayer(ctx context.Context, roomID RoomID, user UserID) (bool, error)
	// HasLeft 报告用户是否已退出过该局（不可重入）。
	HasLeft(ctx context.Context, roomID RoomID, user UserID) (bool, error)
	// UserInRoom 报告用户是否已在任一进行中房间（单活跃房间约束）。
	UserInRoom(ctx context.Context, user UserID) (bool, error)
	// ReservedNicknames 返回房间内已占用昵称的 fold 键集合。
	ReservedNicknames(ctx context.Context, roomID RoomID) (map[string]bool, error)
	// Join 把用户加入房间并返回座位；错误由实现映射为
	// ErrRoomNotFound/ErrRoomFull 等哨兵错误。
	Join(ctx context.Context, roomID RoomID, user UserID, nickname string) (Seat, error)
}

// JoinRequest 是加入房间领域流程的统一入参。/join 文本命令、
// 深链按钮、手输房间码三条 Telegram 入口都先转换为本请求。
type JoinRequest struct {
	// CommandID 是幂等键（Router 的 update ID / 回调 token 语义）。
	CommandID string
	// Actor 是加入玩家用户 ID。
	Actor UserID
	// RoomID 是规范化（大写）后的房间码。
	RoomID RoomID
	// Password 是房间密码：nil=未提供；非 nil 空串语义同未提供。
	// 房间设密码时必须匹配，错误返回 ErrWrongPassword 且可无限重试。
	Password *string
	// Nickname 是用户指定的游戏昵称：nil=随机分配默认中文昵称；
	// 非 nil=使用指定昵称（校验 + 房间内唯一）。
	Nickname *string
}

// JoinResult 是一次成功加入的结果。
type JoinResult struct {
	// Seat 是分配到的座位号。
	Seat Seat
	// Nickname 是本次加入生效的昵称（随机分配或用户指定）。
	Nickname string
}

// JoinService 是加入房间领域流程（docs §二 加入房间）。纯核心：
// 持久化/唯一性经 JoinStore seam，随机昵称经可注入 RNG，密码经
// bcrypt 校验，不触碰 Telegram/SQLite/系统时间（docs/技术选型.md §5.1）。
type JoinService struct {
	store        JoinStore
	gen          NicknameGenerator
	maxNameTries int
}

// NewJoinService 构造加入服务。store 必填（加入与唯一性是硬约束）；
// rng 为空时使用 crypto/rand 实现；默认昵称生成器基于 rng。
func NewJoinService(store JoinStore, rng RNG) (JoinService, error) {
	if store == nil {
		return JoinService{}, errors.New("game: join service requires a store")
	}
	if rng == nil {
		rng = CryptoRNG{}
	}
	return JoinService{
		store:        store,
		gen:          randNicknameGenerator(rng),
		maxNameTries: defaultNicknameMaxTries,
	}, nil
}

// Apply 执行一次加入请求：
//  1. 房间码合法性（规范化大写 + 4～8 位字母数字）；
//  2. 房间状态（不存在/已过期/满员由 CheckRoom 前置拒绝；
//     并发满员由 Join 阶段防御兜底）；
//  3. 单活跃房间约束（同一时间只能加入 1 个进行中房间）；
//  4. 重复加入、退出过同局不可重入；
//  5. 密码校验（设密码房间必须匹配，错误可无限重试不计数）；
//  6. 昵称分配（随机生成并冲突重生，或用户指定并校验唯一）；
//  7. 持久化座位；
//  8. 产出加入确认（本人）与房间面板刷新（房主）Effects。
func (s JoinService) Apply(ctx context.Context, req JoinRequest) (JoinResult, []Effect, error) {
	if !ValidCustomRoomCode(string(req.RoomID)) {
		return JoinResult{}, nil, ErrInvalidRoomCode
	}
	if err := s.store.CheckRoom(ctx, req.RoomID); err != nil {
		return JoinResult{}, nil, err
	}
	if in, err := s.store.HasPlayer(ctx, req.RoomID, req.Actor); err != nil {
		return JoinResult{}, nil, fmt.Errorf("game: check membership of room %q: %w", req.RoomID, err)
	} else if in {
		return JoinResult{}, nil, ErrAlreadyInRoom
	}
	if occupied, err := s.store.UserInRoom(ctx, req.Actor); err != nil {
		return JoinResult{}, nil, fmt.Errorf("game: check user active room: %w", err)
	} else if occupied {
		return JoinResult{}, nil, ErrUserInRoom
	}
	if left, err := s.store.HasLeft(ctx, req.RoomID, req.Actor); err != nil {
		return JoinResult{}, nil, fmt.Errorf("game: check left room %q: %w", req.RoomID, err)
	} else if left {
		return JoinResult{}, nil, ErrAlreadyLeft
	}

	if err := s.checkPassword(ctx, req); err != nil {
		return JoinResult{}, nil, err
	}
	nickname, err := s.resolveNickname(ctx, req)
	if err != nil {
		return JoinResult{}, nil, err
	}
	seat, err := s.store.Join(ctx, req.RoomID, req.Actor, nickname)
	if err != nil {
		return JoinResult{}, nil, err
	}

	params := map[string]any{
		"room_code": string(req.RoomID),
		"nickname":  nickname,
		"seat":      seat,
	}
	joined, err := NewMessageEffect(AudienceActor, JoinConfirmedMessageKey, params)
	if err != nil {
		return JoinResult{}, nil, fmt.Errorf("game: join message: %w", err)
	}
	panel, err := NewMessageEffect(AudienceHost, LobbyPanelMessageKey, map[string]any{
		"room_code": string(req.RoomID),
	})
	if err != nil {
		return JoinResult{}, nil, fmt.Errorf("game: lobby panel message: %w", err)
	}
	return JoinResult{Seat: seat, Nickname: nickname}, []Effect{joined, panel}, nil
}

// checkPassword 校验房间密码门槛：房间无密码（空哈希）直接放行；
// 有密码时必须校验通过，错误返回 ErrWrongPassword（不计数、不锁定，
// 可无限次重试）。
func (s JoinService) checkPassword(ctx context.Context, req JoinRequest) error {
	hash, err := s.store.LoadPasswordHash(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("game: load room password hash %q: %w", req.RoomID, err)
	}
	if hash == "" {
		return nil // 房间未设密码
	}
	if req.Password == nil || *req.Password == "" {
		return ErrWrongPassword
	}
	if !VerifyPassword(hash, *req.Password) {
		return ErrWrongPassword
	}
	return nil
}

// resolveNickname 决定加入生效的昵称：用户指定（校验 + 房间内唯一）
// 或随机分配（生成器 + 冲突重生）。
func (s JoinService) resolveNickname(ctx context.Context, req JoinRequest) (string, error) {
	reserved, err := s.store.ReservedNicknames(ctx, req.RoomID)
	if err != nil {
		return "", fmt.Errorf("game: load reserved nicknames of room %q: %w", req.RoomID, err)
	}
	if req.Nickname != nil {
		nick, err := ValidateNickname(*req.Nickname)
		if err != nil {
			return "", err
		}
		if reserved[FoldNickname(nick)] {
			return "", ErrNicknameTaken
		}
		return nick, nil
	}
	return GenerateUniqueNickname(s.gen, func(folded string) bool {
		return reserved[folded]
	}, s.maxNameTries)
}
