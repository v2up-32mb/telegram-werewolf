package telegram

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// Callback token 相关错误：业务层通过 errors.Is 识别。
var (
	// ErrTokenNotFound 表示 token 不存在或已被淘汰/阶段失效。
	ErrTokenNotFound = errors.New("telegram: callback token not found")
	// ErrTokenOwnerMismatch 表示点击者不是 token 的 owner（越权）。
	ErrTokenOwnerMismatch = errors.New("telegram: callback token owner mismatch")
)

// TokenPayload 是 token 关联的领域载荷。
type TokenPayload struct {
	// Owner 是按钮归属玩家（Token 校验防越权）。
	Owner game.UserID
	// Action 是回调动作（如 vote、wolf_kill、confirm_role、start_game）。
	Action string
	// Target 是动作目标（如座位号 / 用药编码），不暴露于 token 字符串。
	Target string
	// ExpectedPhase 是按钮适用阶段（阶段整体失效依据）。
	ExpectedPhase game.Phase
	// PhaseVersion 用于拒绝过期操作（docs/技术选型.md §6.2）。
	PhaseVersion uint64
}

// CallbackManager 维护不透明 callback token。
//
// token 由 crypto/rand 生成短 base64url（无 padding）随机值，回调数据只
// 暴露 token 本身；payload 仅存于服务端 map。Token 不一次点击即销毁，
// 可在规则下重复使用以覆盖最终选择（docs/阶段消息设计.md §可反复修改选择）。
type CallbackManager struct {
	mu       sync.Mutex
	tokens   map[string]TokenPayload
	order    []string
	capacity int
}

// NewCallbackManager 创建容量为 capacity 的 token 管理器（超限淘汰最旧）。
func NewCallbackManager(capacity int) *CallbackManager {
	if capacity <= 0 {
		capacity = 1
	}
	return &CallbackManager{tokens: make(map[string]TokenPayload), capacity: capacity}
}

// Issue 生成新 token 并关联 payload。
//
// token 为 16 字节熵经 RawURLEncoding 编码的 22 字符短值。
func (m *CallbackManager) Issue(payload TokenPayload) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[token] = payload
	m.order = append(m.order, token)
	if len(m.order) > m.capacity {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.tokens, oldest)
	}
	return token, nil
}

// Validate 校验 token 存在且 owner 匹配，返回 payload（不销毁 token）。
func (m *CallbackManager) Validate(token string, actor game.UserID) (*TokenPayload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.tokens[token]
	if !ok {
		return nil, ErrTokenNotFound
	}
	if p.Owner != actor {
		return nil, ErrTokenOwnerMismatch
	}
	return &p, nil
}

// InvalidatePhase 使指定阶段的所有 token 整体失效（阶段结束防旧按钮）。
func (m *CallbackManager) InvalidatePhase(phase game.Phase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	order := make([]string, 0, len(m.order))
	for token, p := range m.tokens {
		if p.ExpectedPhase == phase {
			delete(m.tokens, token)
		} else {
			order = append(order, token)
		}
	}
	m.order = order
}
