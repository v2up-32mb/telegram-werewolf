package game

import (
	"fmt"
	"strings"
	"time"
)

// Effect 是副作用联合的 marker 接口，由外层执行：
// Outbox（消息）、Timer（计时器）、Storage（持久化）等
// （docs/技术选型.md §5.1）。
type Effect interface {
	effect()
}

// Audience 表示消息 Effect 的可见视图范围。
// 敏感视图（狼人讨论、预言家结果等）绝不能以 Public 受众广播。
type Audience int

const (
	AudienceUnknown Audience = iota
	AudiencePublic           // 所有玩家可见（公共）
	AudienceWolf             // 仅狼人可见
	AudienceSeer             // 仅预言家可见
	AudienceGodView          // 上帝视角（死亡玩家与观战）
	AudienceActor            // 仅操作者本人
	AudienceHost             // 仅房主
)

// Valid 报告受众是否为合法值。
func (a Audience) Valid() bool {
	return a >= AudiencePublic && a <= AudienceHost
}

// String 返回受众的英文短名，供日志与错误消息使用。
func (a Audience) String() string {
	switch a {
	case AudiencePublic:
		return "public"
	case AudienceWolf:
		return "wolf"
	case AudienceSeer:
		return "seer"
	case AudienceGodView:
		return "god_view"
	case AudienceActor:
		return "actor"
	case AudienceHost:
		return "host"
	default:
		return "unknown"
	}
}

// sensitiveAudiences 是允许接收对应敏感消息前缀的受众集合。
// wolf.*、seer.* 与 role.* 都是私密视图，禁止以 Public 受众广播。
var sensitiveAudiences = map[string][]Audience{
	"wolf.": {AudienceWolf, AudienceGodView},
	"seer.": {AudienceSeer, AudienceGodView},
	"role.": {AudienceActor, AudienceGodView},
}

// MessageEffect 表示一条待发送的语义化消息（key + 参数），
// 由 Outbox 按 Chat 与受众调度。敏感消息 key 不得以 Public 受众构造。
type MessageEffect struct {
	Audience Audience
	Key      string
	Params   map[string]any
}

func (MessageEffect) effect() {}

// NewMessageEffect 构造消息 Effect 并校验受众边界：
//   - 受众必须合法；
//   - wolf.*、seer.* 等敏感私密消息前缀只能发给对应角色与上帝视角，
//     禁止以 Public（公共）受众产生，防止敏感视图混入公共 Effect。
func NewMessageEffect(a Audience, key string, params map[string]any) (MessageEffect, error) {
	if !a.Valid() {
		return MessageEffect{}, fmt.Errorf("game: invalid audience %v", a)
	}
	if allowed, sensitive := sensitiveAudiences[keyPrefix(key)]; sensitive {
		for _, candidate := range allowed {
			if a == candidate {
				return MessageEffect{Audience: a, Key: key, Params: params}, nil
			}
		}
		return MessageEffect{}, fmt.Errorf("game: sensitive message %q cannot be sent to audience %v", key, a)
	}
	return MessageEffect{Audience: a, Key: key, Params: params}, nil
}

// keyPrefix 返回消息 key 的静态私密前缀（如 wolf.、seer.）。
func keyPrefix(key string) string {
	for _, prefix := range []string{"wolf.", "seer.", "role."} {
		if strings.HasPrefix(key, prefix) {
			return prefix
		}
	}
	return ""
}

// TimerEffect 表示启动、替换或停止阶段计时器。Duration 为 0 且
// Cancel 为 true 时表示停止当前阶段计时器。
type TimerEffect struct {
	Phase    Phase
	Duration time.Duration
	Cancel   bool
}

func (TimerEffect) effect() {}

// PersistKind 是最小持久化记录分类。
type PersistKind int

const (
	PersistActiveGame PersistKind = iota // 写入最小活跃局记录
	PersistReport                        // 事务化保存战报与积分
)

// PersistEffect 表示写入持久化存储的最小副作用。
type PersistEffect struct {
	Kind PersistKind
}

func (PersistEffect) effect() {}

// EventKind 是需要回到房间 Actor 的完成事件分类。
type EventKind int

const (
	EventSendCompleted EventKind = iota // 消息发送完成
)

// EventEffect 表示产生需要回到房间 Actor 的完成事件。
type EventEffect struct {
	Kind EventKind
}

func (EventEffect) effect() {}
