package game

import (
	"errors"
	"fmt"
	"time"
)

// 预言家夜间阶段的消息 key（docs §夜间 4、§8.3 预言家）。所有 key 带
// seer. 私密前缀，只允许 AudienceSeer / AudienceGodView，绝不产生
// AudiencePublic 消息（effect.go sensitiveAudiences 双保险）。
const (
	// SeerPromptMessageKey 是查验选择 UI（AudienceSeer，含存活目标）。
	SeerPromptMessageKey = "seer.prompt"
	// SeerResultMessageKey 是确认后的二分查验结果（AudienceSeer）。
	SeerResultMessageKey = "seer.result"
	// SeerNoneMessageKey 是超时空验默认提示（AudienceSeer）。
	SeerNoneMessageKey = "seer.none"
)

// 预言家夜间领域规则的哨兵错误（docs §夜间 4、§8.3；与 wolf/witch
// 哨兵语义区分，不重复通用一级哨兵）。
var (
	// ErrNotSeer 表示操作者不是预言家本人。
	ErrNotSeer = errors.New("game: only seer may do this")
	// ErrSeerActionClosed 表示查验窗口未开启或已结束。
	ErrSeerActionClosed = errors.New("game: seer action is closed")
	// ErrSeerNoSelection 表示未选择目标即确认查验。
	ErrSeerNoSelection = errors.New("game: seer must select a target before confirming")
)

// BeginSeerPhase 在女巫阶段结束后开启预言家阶段（后续 P0 接线层在
// 收到女巫阶段结束钩子时调用；与 BeginWolfPhase/BeginWitchPhase 的
// 接线延期同理）：
//   - SeerActive=true、SeerPending=nil（清空待确认选择）；
//   - 查验历史（SeerResults/SeerChecked）跨夜持续携带，不清空；
//   - 预言家本人收到 seer.prompt（AudienceSeer，含存活目标列表）；
//   - TimerEffect{Phase:PhaseNight, Duration=Settings 生效其他角色夜间
//     时长（默认 15 秒）}。
//
// 查验目标与结果只出现在 seer.* 消息 params（AudienceSeer/
// AudienceGodView），绝不进入 AudiencePublic（docs §5 私密标记仅
// 预言家本人可见 + effect.go 双保险）。
func BeginSeerPhase(st State) (State, []Effect, error) {
	prompt, err := NewMessageEffect(AudienceSeer, SeerPromptMessageKey, map[string]any{
		"targets": aliveSeats(st.Players),
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: seer prompt: %w", err)
	}

	next := st.Copy()
	next.Night.SeerActive = true
	next.Night.SeerPending = nil
	return next, []Effect{prompt, TimerEffect{Phase: PhaseNight, Duration: seerNightDuration(next.Settings)}}, nil
}

// seerNightDuration 返回预言家阶段限时（Settings 生效其他角色夜间
// 时长，默认 15 秒，docs「夜间限时」）；零值设置防御性回退默认值。
func seerNightDuration(s RoomSettings) time.Duration {
	_, _, otherSeconds := s.EffectiveDurations()
	if otherSeconds <= 0 {
		otherSeconds = DefaultOtherNightSeconds
	}
	return time.Duration(otherSeconds) * time.Second
}

// seerActorSeat 校验操作者是存活预言家本人，返回其座位。
func seerActorSeat(st State, actor UserID) (Seat, error) {
	seat, ok := seatByUser(st.Players, actor)
	if !ok {
		return 0, ErrNotInRoom
	}
	if roleAtSeat(st.Players, seat) != RoleSeer {
		return 0, ErrNotSeer
	}
	return seat, nil
}

// seerCheck 处理查验选择目标（docs §夜间 4、§8.3：先选择存活目标，
// 确认前可修改，确认前不产生结果）：
//   - 仅存活预言家（ErrNotSeer）且查验窗口开启（ErrSeerActionClosed）；
//   - 目标存活校验由通用 validator 保证（死亡/越界 ErrInvalidTarget）；
//   - 重复选择直接覆盖 SeerPending（确认前最终选择有效）。
func (r reducer) seerCheck(st State, cmd SeerCheckCommand) (State, []Effect, error) {
	if !st.Night.SeerActive {
		return st, nil, ErrSeerActionClosed
	}
	if _, err := seerActorSeat(st, cmd.Meta.Actor); err != nil {
		return st, nil, err
	}

	next := st.Copy()
	target := cmd.Target
	next.Night.SeerPending = &target
	next.Processed[cmd.Meta.ID] = true
	return next, nil, nil
}

// seerConfirm 处理确认查验（docs §夜间 4、§8.3：确认后立即返回二分
// 结果并可提前结束阶段）：
//   - 仅存活预言家（ErrNotSeer）且窗口开启（ErrSeerActionClosed）；
//   - 未选择即确认 → ErrSeerNoSelection；
//   - 确认后写入 SeerResults[target]=Camp（狼人/好人二分，不区分具体
//     神职）、SeerChecked[target]=true、SeerActive=false（阶段提前结束）；
//   - 私密结果消息 seer.result（AudienceSeer，含 target+camp）。
func (r reducer) seerConfirm(st State, cmd SeerConfirmCommand) (State, []Effect, error) {
	if !st.Night.SeerActive {
		return st, nil, ErrSeerActionClosed
	}
	if _, err := seerActorSeat(st, cmd.Meta.Actor); err != nil {
		return st, nil, err
	}
	if st.Night.SeerPending == nil {
		return st, nil, ErrSeerNoSelection
	}
	target := *st.Night.SeerPending
	camp := roleAtSeat(st.Players, target).Camp()
	if camp != CampWolf && camp != CampGood {
		return st, nil, fmt.Errorf("game: seer target %d has invalid camp %v", target, camp)
	}

	next := st.Copy()
	if next.Night.SeerResults == nil {
		next.Night.SeerResults = map[Seat]Camp{}
	}
	if next.Night.SeerChecked == nil {
		next.Night.SeerChecked = map[Seat]bool{}
	}
	next.Night.SeerResults[target] = camp
	next.Night.SeerChecked[target] = true
	next.Night.SeerActive = false
	next.Night.SeerPending = nil
	next.Processed[cmd.Meta.ID] = true

	result, err := NewMessageEffect(AudienceSeer, SeerResultMessageKey, map[string]any{
		"target": target,
		"camp":   camp,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: seer result: %w", err)
	}
	return next, []Effect{result}, nil
}

// seerTimeout 处理预言家超时（docs「超时与默认选择」：预言家超时 →
// 本轮空验（不随机查验、无结果））：不写入任何查验结果，窗口关闭并
// 收到 seer.none 默认提示（提示文案不出现「已跳过」措辞）。
// I4：存活且仍处于查验窗口的预言家计入连续超时计数。
func (r reducer) seerTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if !st.Night.SeerActive {
		return st, nil, ErrSeerActionClosed
	}
	st1, fx1, err := r.advanceTimeoutStreaks(st, cmd.Meta.ReceivedAt, unresponsiveSeer(st))
	if err != nil {
		return st, nil, err
	}
	next := st1.Copy()
	next.Processed[cmd.Meta.ID] = true
	next.Night.SeerActive = false
	next.Night.SeerPending = nil

	none, err := NewMessageEffect(AudienceSeer, SeerNoneMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: seer none message: %w", err)
	}
	return next, append(fx1, none), nil
}

// unresponsiveSeer 返回预言家超时时是否计入连续计数：存活预言家且查验
// 窗口仍开启（未确认查验）。
func unresponsiveSeer(st State) []Seat {
	seat, ok := seerSeat(st.Players)
	if !ok {
		return nil
	}
	p := playerBySeat(st.Players, seat)
	if p.Dead || p.Left {
		return nil
	}
	if st.Night.SeerActive {
		return []Seat{seat}
	}
	return nil
}

// seerSeat 返回预言家座位；不存在返回 ok=false。
func seerSeat(players []Player) (Seat, bool) {
	for _, p := range players {
		if p.Role == RoleSeer && p.Seat.Valid() {
			return p.Seat, true
		}
	}
	return 0, false
}
