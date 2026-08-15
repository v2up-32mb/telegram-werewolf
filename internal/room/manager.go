package room

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// Room Manager 与生命周期注册表（docs/技术选型.md §4：Room Manager 管理
// 每个活跃房间一个 goroutine；docs/游戏流程设计.md §一.7/§二.4 唯一约束）。
var (
	// ErrUserInRoom 表示用户已在一个进行中房间（房主/玩家唯一约束）。
	ErrUserInRoom = errors.New("room: user already in an active room")
	// ErrRoomNotFound 表示注册表中不存在该房间码。
	ErrRoomNotFound = errors.New("room: room not found")
	// ErrCodeExhausted 表示房间码重试耗尽仍无法获得唯一码。
	ErrCodeExhausted = errors.New("room: failed to allocate unique room code")
)

// CodeRegistry 是房间码唯一性确认接口：对应 storage 层 unique constraint
// （docs/技术选型.md §5.2；Task 12 起由 storage 实现）。Reserve 返回 false
// 表示该码已被占用，需重新生成并重试。
type CodeRegistry interface {
	Reserve(ctx context.Context, code game.RoomID) (bool, error)
}

// Room 是注册在 Manager 中的一个活跃房间视图：持有唯一的 Actor 与元数据；
// 可变房间状态归 Actor 独占，Manager 不直接修改 Actor 内部状态。
type Room struct {
	ID   game.RoomID
	Host game.UserID

	actor *Actor
}

// Dispatch 将命令投递给房间 Actor（转发，状态由 Actor 独占处理）。
func (r *Room) Dispatch(ctx context.Context, cmd game.Command) (Result, error) {
	return r.actor.Dispatch(ctx, cmd)
}

// Stop 停止房间 Actor（幂等）。
func (r *Room) Stop() { r.actor.Stop() }

// defaultMaxCodeTries 是房间码唯一性重试上限。
const defaultMaxCodeTries = 8

// ManagerOptions 是 Manager 的构造选项。
type ManagerOptions struct {
	RNG          game.RNG     // 房间码随机源；nil 时使用 crypto/rand 实现
	Registry     CodeRegistry // storage unique constraint 占位；nil 时仅靠本地注册表去重
	MaxCodeTries int          // 唯一码重试上限；<=0 时用默认值
}

// Manager 管理多房间并发与生命周期：注册表 map 由互斥锁保护（最小同步
// 范围），房间内状态仍由各自 Actor 独占处理。
type Manager struct {
	mu       sync.Mutex
	rng      game.RNG
	reg      CodeRegistry
	maxTries int

	rooms  map[game.RoomID]*Room
	byUser map[game.UserID]game.RoomID
	closed bool
}

// NewManager 创建并初始化 Manager。
func NewManager(opts ManagerOptions) *Manager {
	if opts.RNG == nil {
		opts.RNG = game.CryptoRNG{}
	}
	maxTries := opts.MaxCodeTries
	if maxTries <= 0 {
		maxTries = defaultMaxCodeTries
	}
	return &Manager{
		rng:      opts.RNG,
		reg:      opts.Registry,
		maxTries: maxTries,
		rooms:    make(map[game.RoomID]*Room),
		byUser:   make(map[game.UserID]game.RoomID),
	}
}

// CreateRoom 为 host 创建一个进行中房间：校验用户唯一约束、分配唯一
// 房间码（碰撞重试）、创建该房间的 Actor 并登记注册表。
func (m *Manager) CreateRoom(ctx context.Context, host game.UserID, clock Clock, reducer game.Reducer) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if _, ok := m.byUser[host]; ok {
		return nil, ErrUserInRoom
	}

	code, err := m.allocateCode(ctx)
	if err != nil {
		return nil, err
	}
	st := game.State{
		RoomID:       code,
		Phase:        game.PhaseLobby,
		PhaseVersion: 1,
		Processed:    map[string]bool{},
	}
	r := &Room{
		ID:    code,
		Host:  host,
		actor: NewActor(st, reducer, clock, Options{}),
	}
	m.rooms[code] = r
	m.byUser[host] = code
	return r, nil
}

// allocateCode 在锁内生成唯一房间码：本地注册表与 storage 唯一约束
// （Registry）任一层碰撞即重新生成，重试耗尽返回 ErrCodeExhausted。
func (m *Manager) allocateCode(ctx context.Context) (game.RoomID, error) {
	for i := 0; i < m.maxTries; i++ {
		code, err := GenerateCode(m.rng, RoomCodeLength)
		if err != nil {
			return "", err
		}
		if _, ok := m.rooms[code]; ok {
			continue // 本地注册表碰撞
		}
		if m.reg != nil {
			ok, rerr := m.reg.Reserve(ctx, code)
			if rerr != nil {
				return "", fmt.Errorf("room: reserve code %q: %w", code, rerr)
			}
			if !ok {
				continue // storage 层唯一约束碰撞
			}
		}
		return code, nil
	}
	return "", ErrCodeExhausted
}

// Get 返回指定房间码对应的房间；不存在时返回 ErrRoomNotFound。
func (m *Manager) Get(code game.RoomID) (*Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	r, ok := m.rooms[code]
	if !ok {
		return nil, ErrRoomNotFound
	}
	return r, nil
}

// Remove 移除指定房间：注销注册与用户唯一约束，并停止该房间 Actor
// （Actor.Stop 等待 goroutine 干净退出）。
func (m *Manager) Remove(code game.RoomID) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	r, ok := m.rooms[code]
	if !ok {
		m.mu.Unlock()
		return ErrRoomNotFound
	}
	delete(m.rooms, code)
	if m.byUser[r.Host] == code {
		delete(m.byUser, r.Host)
	}
	m.mu.Unlock()

	r.actor.Stop() // 在锁外停止，避免阻塞注册表
	return nil
}

// Close 停止全部房间 Actor 并关闭注册表；之后 CreateRoom/Get/Remove
// 返回 ErrClosed。
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	m.rooms = make(map[game.RoomID]*Room)
	m.byUser = make(map[game.UserID]game.RoomID)
	m.mu.Unlock()

	for _, r := range rooms {
		r.actor.Stop() // 锁外逐个停止
	}
}
