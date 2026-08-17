package game

import (
	"errors"
	"fmt"
	"sort"
)

// 白天播报消息 key（docs 游戏流程设计.md §白天 1、阶段消息设计.md §12）。
// day.* 是公共消息前缀：绝不允许携带死因等敏感载荷；身份仅在房主配置
// RevealRoleOnDeath 时进入公共死讯。
const (
	// DayDeathMessageKey 是公共死讯：params {victims []Seat}，报身份开启
	// 时附 {roles map[Seat]Role}；任何情况下都不含死因。
	DayDeathMessageKey = "day.death"
	// DayPeaceMessageKey 是平安夜公共播报（无死者）。
	DayPeaceMessageKey = "day.peace"
	// DayDeathPrivateMessageKey 是死者本人私聊说明（AudienceActor）：
	// params {seat Seat, role Role, cause DeathCause}；仅发死者本人。
	DayDeathPrivateMessageKey = "day.death_private"
)

// ErrDayNotInDayPhase 表示 DayStart 的前置阶段不是 PhaseDaySpeech
// （夜间结算 ResolveNight 已切换阶段后才可播报白天死讯）。
var ErrDayNotInDayPhase = errors.New("game: day start requires PhaseDaySpeech")

// DeathCause 描述死者真实死因（docs §白天 1：死者私聊可见真实死因）。
// 死因是私密载荷：只允许进入 day.death_private，绝不进入 Public params。
type DeathCause int

const (
	CauseUnknown     DeathCause = iota
	CauseWolfKill               // 被狼人刀杀
	CauseWitchPoison            // 被女巫毒杀
)

// Valid 报告死因是否为 MVP 已知取值。
func (c DeathCause) Valid() bool {
	return c >= CauseWolfKill && c <= CauseWitchPoison
}

// String 返回死因英文短名，供日志与死者私聊渲染使用。
func (c DeathCause) String() string {
	switch c {
	case CauseWolfKill:
		return "wolf_kill"
	case CauseWitchPoison:
		return "witch_poison"
	default:
		return "unknown"
	}
}

// DayOutcome 是夜结算后的白天播报输入（接线层在 ResolveNight 之后构造；
// 测试直接构造）。Victims 为当夜死者座位（升序归一后进入公共 params），
// Cause 只允许记录 Victims 中的座位。
type DayOutcome struct {
	Victims []Seat
	Cause   map[Seat]DeathCause
}

// DayStart 生成白天死讯播报 Effect 序列（docs 游戏流程设计.md §白天 1、
// 阶段消息设计.md §12）：
//
//  1. 有死者：day.death（AudiencePublic），params.victims 升序；
//     Settings.RevealRoleOnDeath=true 时附 params.roles（死者身份），
//     任何情况下都不含死因；
//  2. 平安夜（Victims 为空）：day.peace（AudiencePublic）；
//  3. 每名死者一条 day.death_private（AudienceActor）：本人真实身份
//     与真实死因（Cause 缺省/非法时归一为 CauseUnknown）。
//
// 前置条件：st.Phase 必须已是 PhaseDaySpeech（ResolveNight 已切阶段并
// 清理夜间窗口）；DayStart 不重复改动 Phase/PhaseVersion，也不修改状态。
// 校验失败时返回原状态与明确错误。
func DayStart(st State, out DayOutcome) (State, []Effect, error) {
	if st.Phase != PhaseDaySpeech {
		return st, nil, ErrDayNotInDayPhase
	}

	victims, err := normalizedVictims(st, out.Victims)
	if err != nil {
		return st, nil, err
	}

	if len(victims) == 0 {
		peace, err := NewMessageEffect(AudiencePublic, DayPeaceMessageKey, map[string]any{})
		if err != nil {
			return st, nil, err
		}
		return st, []Effect{peace}, nil
	}

	params := map[string]any{"victims": victims}
	if st.Settings.RevealRoleOnDeath {
		roles := make(map[Seat]Role, len(victims))
		for _, s := range victims {
			roles[s] = playerBySeat(st.Players, s).Role
		}
		params["roles"] = roles
	}
	pub, err := NewMessageEffect(AudiencePublic, DayDeathMessageKey, params)
	if err != nil {
		return st, nil, err
	}

	effects := []Effect{pub}
	for _, s := range victims {
		cause := out.Cause[s]
		if !cause.Valid() {
			cause = CauseUnknown
		}
		priv, err := NewMessageEffect(AudienceActor, DayDeathPrivateMessageKey, map[string]any{
			"seat":  s,
			"role":  playerBySeat(st.Players, s).Role,
			"cause": cause,
		})
		if err != nil {
			return st, nil, err
		}
		effects = append(effects, priv)
	}
	return st, effects, nil
}

// normalizedVictims 校验并去重 victim 座位，返回升序切片。
func normalizedVictims(st State, in []Seat) ([]Seat, error) {
	seen := make(map[Seat]bool, len(in))
	out := make([]Seat, 0, len(in))
	for _, s := range in {
		if !s.Valid() {
			return nil, fmt.Errorf("game: invalid victim seat %d", s)
		}
		if playerBySeat(st.Players, s).UserID == 0 {
			return nil, fmt.Errorf("game: victim seat %d not in players", s)
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
