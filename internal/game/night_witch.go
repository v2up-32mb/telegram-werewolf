package game

import (
	"errors"
	"fmt"
	"time"
)

// 女巫夜间阶段的窗口状态与消息 key（docs §夜间 3、§8.2 女巫）。
// WitchStage 是女巫夜间连续决策窗口的当前状态：
// 关闭/未开始 → 解药窗口 →（确认不使用解药后）→ 毒药窗口 → 结束。
type WitchStage int

const (
	// WitchStageClosed 表示女巫阶段未开始或已结束。
	WitchStageClosed WitchStage = iota
	// WitchStageSave 是解药窗口：先被告知刀口，再决定救/不救。
	WitchStageSave
	// WitchStagePoison 是毒药窗口：选择毒谁或不使用毒药。
	WitchStagePoison
)

// 女巫夜间消息 key。所有 key 带 witch. 私密前缀，只允许
// AudienceActor / AudienceGodView，绝不产生 AudiencePublic 消息
// （effect.go sensitiveAudiences 双保险）。
const (
	// WitchKillRevealMessageKey 告知女巫今晚狼人刀人目标（nil=平安夜）。
	WitchKillRevealMessageKey = "witch.kill_reveal"
	// WitchSavePromptMessageKey 是解药窗口 UI（AudienceActor）。
	WitchSavePromptMessageKey = "witch.save.prompt"
	// WitchPoisonPromptMessageKey 是毒药窗口 UI（AudienceActor）。
	WitchPoisonPromptMessageKey = "witch.poison.prompt"
	// WitchSaveLockedMessageKey 是解药选择确认锁定文案（AudienceActor）。
	WitchSaveLockedMessageKey = "witch.save.locked"
	// WitchPoisonLockedMessageKey 是毒药选择确认锁定文案（AudienceActor）。
	WitchPoisonLockedMessageKey = "witch.poison.locked"
	// WitchNoneMessageKey 是女巫超时默认提示（不用解药、不用毒药）。
	WitchNoneMessageKey = "witch.none"
)

// 女巫夜间领域规则的哨兵错误（docs §夜间 3、§8.2、§超时与默认选择；
// 与 wolf 哨兵语义区分，不重复通用一级哨兵）。
var (
	// ErrNotWitch 表示操作者不是女巫本人。
	ErrNotWitch = errors.New("game: only witch may do this")
	// ErrWitchActionClosed 表示女巫窗口未开启或已结束。
	ErrWitchActionClosed = errors.New("game: witch action is closed")
	// ErrWitchUsedTonight 表示本夜已用一瓶药（一夜一瓶，救/毒二选一）。
	ErrWitchUsedTonight = errors.New("game: witch already used one potion tonight")
	// ErrWitchNoSelection 表示未选择即确认。
	ErrWitchNoSelection = errors.New("game: witch must make a choice before confirming")
	// ErrWitchSaveUnavailable 表示解药已永久用完，不能再次使用。
	ErrWitchSaveUnavailable = errors.New("game: witch antidote already used up")
	// ErrWitchPoisonUnavailable 表示不能使用毒药：毒药已永久用完，
	// 或女巫当夜被刀且不能自救（docs：当夜死亡且不能自救时当夜不能用毒）。
	ErrWitchPoisonUnavailable = errors.New("game: witch poison unavailable tonight")
	// ErrWitchCannotSelfSave 表示自救被禁止：非首夜，或房主配置关闭
	// WitchSelfSaveFirstNight（自救仅首夜可选）。
	ErrWitchCannotSelfSave = errors.New("game: witch cannot save herself tonight")
	// ErrWitchNothingToSave 表示平安夜（WolfKillTarget=nil）无刀口可救。
	ErrWitchNothingToSave = errors.New("game: no kill target to save tonight")
)

// beginWitchPhase 在狼人阶段结束后开启女巫阶段（后续 P0 接线层在
// 收到狼人阶段结束钩子时调用；与 beginWolfPhase 的接线延期同理）。
// firstNight 表示是否为首夜（docs「自救仅首夜可选」；接线层从
// 阶段序号推导传入）：
//   - WitchStage=1（解药窗口）、WitchUsedTonight 重置为 false、
//     WitchFirstNight=firstNight、清空所有待确认选择；
//   - 女巫本人收到 witch.kill_reveal（刀口，nil=平安夜）与
//     witch.save.prompt（含解药/毒药是否已用）；死亡女巫也能收到
//     上帝视角副本由接线层按 AudienceGodView 策略执行；
//   - TimerEffect{Phase:PhaseNight, Duration=Settings 生效其他角色夜间
//     时长（默认 15 秒）}。
//
// 刀口与用药选择只出现在 witch.* 消息 params（AudienceActor/
// AudienceGodView），绝不进入 AudiencePublic（docs §死亡角色跳过行动
// 防泄密 + effect.go 双保险）。
func beginWitchPhase(st State, firstNight bool) (State, []Effect, error) {
	effects := make([]Effect, 0, 3)

	reveal, err := NewMessageEffect(AudienceActor, WitchKillRevealMessageKey, map[string]any{
		"kill_target": st.Night.WolfKillTarget,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch kill reveal: %w", err)
	}
	effects = append(effects, reveal)

	prompt, err := NewMessageEffect(AudienceActor, WitchSavePromptMessageKey, map[string]any{
		"save_used":   st.Night.WitchSaveUsed,
		"poison_used": st.Night.WitchPoisonUsed,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch save prompt: %w", err)
	}
	effects = append(effects, prompt)

	next := st.Copy()
	next.Night.WitchStage = WitchStageSave
	next.Night.WitchUsedTonight = false
	next.Night.WitchFirstNight = firstNight
	next.Night.WitchSaveChoice = nil
	next.Night.WitchPoisonChoice = nil
	next.Night.WitchPoisonSkip = false
	effects = append(effects, TimerEffect{Phase: PhaseNight, Duration: witchNightDuration(next.Settings)})
	return next, effects, nil
}

// witchNightDuration 返回女巫阶段限时（Settings 生效其他角色夜间时长，
// 默认 15 秒，docs「夜间限时」）；零值设置防御性回退默认值。
func witchNightDuration(s RoomSettings) time.Duration {
	_, _, otherSeconds := s.EffectiveDurations()
	if otherSeconds <= 0 {
		otherSeconds = DefaultOtherNightSeconds
	}
	return time.Duration(otherSeconds) * time.Second
}

// witchSeat 返回女巫座位；不存在返回 ok=false。
func witchSeat(players []Player) (Seat, bool) {
	for _, p := range players {
		if p.Role == RoleWitch && p.Seat.Valid() {
			return p.Seat, true
		}
	}
	return 0, false
}

// witchKilledTonight 报告女巫是否为本夜刀口。
func witchKilledTonight(st State) bool {
	ws, ok := witchSeat(st.Players)
	if !ok || st.Night.WolfKillTarget == nil {
		return false
	}
	return *st.Night.WolfKillTarget == ws
}

// canSelfSave 报告女巫本夜是否可自救：仅首夜且房主配置开启
// WitchSelfSaveFirstNight（docs「自救仅首夜可选，能否自救看房主配置」）。
func canSelfSave(st State) bool {
	return st.Night.WitchFirstNight && st.Settings.WitchSelfSaveFirstNight
}

// witchActorSeat 校验操作者是存活女巫本人，返回其座位。
func witchActorSeat(st State, actor UserID) (Seat, error) {
	seat, ok := seatByUser(st.Players, actor)
	if !ok {
		return 0, ErrNotInRoom
	}
	if roleAtSeat(st.Players, seat) != RoleWitch {
		return 0, ErrNotWitch
	}
	return seat, nil
}

// witchSave 处理解药窗口选择（用/不用解药，docs §夜间 3、§8.2）：
//   - 仅存活女巫（ErrNotWitch）且解药窗口开启（ErrWitchActionClosed）；
//   - 本夜已用一瓶（ErrWitchUsedTonight）；
//   - 使用解药在以下情况被拒：
//   - 解药已永久用完（ErrWitchSaveUnavailable）；
//   - 平安夜无刀口可救（ErrWitchNothingToSave）；
//   - 刀口为女巫本人且不能自救：非首夜或配置关闭（ErrWitchCannotSelfSave）；
//   - 确认前可反复修改（直接覆盖 WitchSaveChoice）。
func (r reducer) witchSave(st State, cmd WitchSaveCommand) (State, []Effect, error) {
	if st.Night.WitchStage != WitchStageSave {
		return st, nil, ErrWitchActionClosed
	}
	if _, err := witchActorSeat(st, cmd.Meta.Actor); err != nil {
		return st, nil, err
	}
	if st.Night.WitchUsedTonight {
		return st, nil, ErrWitchUsedTonight
	}
	if err := validateWitchSaveUse(st, cmd.Use); err != nil {
		return st, nil, err
	}

	next := st.Copy()
	use := cmd.Use
	next.Night.WitchSaveChoice = &use
	next.Processed[cmd.Meta.ID] = true
	return next, nil, nil
}

// validateWitchSaveUse 校验一次「使用解药」意图是否合法（选择与确认
// 复用，防止绕过选择直接确认时夹带非法意图）。
func validateWitchSaveUse(st State, use bool) error {
	if !use {
		return nil
	}
	if st.Night.WitchSaveUsed {
		return ErrWitchSaveUnavailable
	}
	if st.Night.WolfKillTarget == nil {
		return ErrWitchNothingToSave
	}
	if witchKilledTonight(st) && !canSelfSave(st) {
		return ErrWitchCannotSelfSave
	}
	return nil
}

// witchConfirm 锁定女巫当前窗口的待确认选择（docs §夜间 3：确认前可
// 修改、确认后不能撤回；确认完成后可提前结束阶段）：
//   - 解药窗口：确认使用解药 → 立即永久消耗解药、本夜已用一瓶、
//     阶段提前结束（本夜不能再用毒药）；确认不使用解药 → 进入毒药窗口
//     （重发毒药提示 UI）；
//   - 毒药窗口：确认目标 → 永久消耗毒药、记录 WitchPoisonTarget、
//     阶段提前结束；确认「不使用毒药」→ 不消耗、阶段提前结束。
func (r reducer) witchConfirm(st State, cmd WitchConfirmCommand) (State, []Effect, error) {
	if st.Night.WitchStage != WitchStageSave && st.Night.WitchStage != WitchStagePoison {
		return st, nil, ErrWitchActionClosed
	}
	if _, err := witchActorSeat(st, cmd.Meta.Actor); err != nil {
		return st, nil, err
	}
	if st.Night.WitchUsedTonight {
		return st, nil, ErrWitchUsedTonight
	}

	switch st.Night.WitchStage {
	case WitchStageSave:
		return r.confirmWitchSave(st, cmd)
	case WitchStagePoison:
		return r.confirmWitchPoison(st, cmd)
	default:
		return st, nil, ErrWitchActionClosed
	}
}

// confirmWitchSave 锁定解药窗口选择（见 witchConfirm）。
func (r reducer) confirmWitchSave(st State, cmd WitchConfirmCommand) (State, []Effect, error) {
	if st.Night.WitchSaveChoice == nil {
		return st, nil, ErrWitchNoSelection
	}
	use := *st.Night.WitchSaveChoice
	if err := validateWitchSaveUse(st, use); err != nil {
		return st, nil, err
	}

	next := st.Copy()
	next.Processed[cmd.Meta.ID] = true

	locked, err := NewMessageEffect(AudienceActor, WitchSaveLockedMessageKey, map[string]any{"used": use})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch save locked: %w", err)
	}
	effects := []Effect{locked}

	if use {
		// 确认使用解药后立即消耗，本夜不能再用毒药，阶段提前结束。
		next.Night.WitchSaveUsed = true
		next.Night.WitchUsedTonight = true
		next.Night.WitchStage = WitchStageClosed
		next.Night.WitchSaveChoice = nil
		return next, effects, nil
	}

	// 确认不使用解药后进入毒药步骤（重新发送毒药窗口 UI）。
	next.Night.WitchStage = WitchStagePoison
	next.Night.WitchSaveChoice = nil
	poisonPrompt, err := NewMessageEffect(AudienceActor, WitchPoisonPromptMessageKey, map[string]any{
		"targets":     aliveSeats(next.Players),
		"save_used":   next.Night.WitchSaveUsed,
		"poison_used": next.Night.WitchPoisonUsed,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch poison prompt: %w", err)
	}
	return next, append(effects, poisonPrompt), nil
}

// witchPoisonSelect 处理毒药窗口选择（毒谁/不使用毒药，docs §夜间 3）：
//   - 仅存活女巫（ErrNotWitch）且毒药窗口开启（ErrWitchActionClosed）；
//   - 本夜已用一瓶（ErrWitchUsedTonight）；
//   - 毒药已永久用完 → 只能选择「不使用毒药」（ErrWitchPoisonUnavailable）；
//   - 女巫当夜被刀且不能自救 → 当夜不能用毒（ErrWitchPoisonUnavailable），
//     仍可选择「不使用毒药」并确认提前结束；
//   - 确认前可反复修改（直接覆盖 WitchPoisonChoice/Skip）。
func (r reducer) witchPoisonSelect(st State, cmd WitchPoisonCommand) (State, []Effect, error) {
	if st.Night.WitchStage != WitchStagePoison {
		return st, nil, ErrWitchActionClosed
	}
	if _, err := witchActorSeat(st, cmd.Meta.Actor); err != nil {
		return st, nil, err
	}
	if st.Night.WitchUsedTonight {
		return st, nil, ErrWitchUsedTonight
	}
	if cmd.Target != nil {
		if st.Night.WitchPoisonUsed {
			return st, nil, ErrWitchPoisonUnavailable
		}
		if witchKilledTonight(st) && !canSelfSave(st) {
			return st, nil, ErrWitchPoisonUnavailable
		}
	}

	next := st.Copy()
	if cmd.Target == nil {
		next.Night.WitchPoisonChoice = nil
		next.Night.WitchPoisonSkip = true
	} else {
		target := *cmd.Target
		next.Night.WitchPoisonChoice = &target
		next.Night.WitchPoisonSkip = false
	}
	next.Processed[cmd.Meta.ID] = true
	return next, nil, nil
}

// confirmWitchPoison 锁定毒药窗口选择（见 witchConfirm）。
func (r reducer) confirmWitchPoison(st State, cmd WitchConfirmCommand) (State, []Effect, error) {
	if st.Night.WitchPoisonChoice == nil && !st.Night.WitchPoisonSkip {
		return st, nil, ErrWitchNoSelection
	}
	if st.Night.WitchPoisonChoice != nil {
		// 防御：选择路径已拦截，确认时再次校验（防止绕过选择夹带非法目标）。
		if st.Night.WitchPoisonUsed {
			return st, nil, ErrWitchPoisonUnavailable
		}
		if witchKilledTonight(st) && !canSelfSave(st) {
			return st, nil, ErrWitchPoisonUnavailable
		}
	}

	next := st.Copy()
	next.Processed[cmd.Meta.ID] = true

	var target *Seat
	if next.Night.WitchPoisonChoice != nil {
		v := *next.Night.WitchPoisonChoice
		target = &v
		next.Night.WitchPoisonUsed = true
		next.Night.WitchPoisonTarget = target
		next.Night.WitchUsedTonight = true
	}
	locked, err := NewMessageEffect(AudienceActor, WitchPoisonLockedMessageKey, map[string]any{"target": target})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch poison locked: %w", err)
	}
	next.Night.WitchStage = WitchStageClosed
	next.Night.WitchPoisonChoice = nil
	next.Night.WitchPoisonSkip = false
	return next, []Effect{locked}, nil
}

// witchTimeout 处理女巫超时（docs「超时与默认选择」：女巫超时 → 不用
// 解药、不用毒药）：不消耗任何药品、不随机用药，窗口关闭并收到
// witch.none 默认提示（提示文案不出现「已跳过」措辞）。
func (r reducer) witchTimeout(st State, cmd TimeoutCommand) (State, []Effect, error) {
	if st.Night.WitchStage < WitchStageSave {
		return st, nil, ErrWitchActionClosed
	}
	next := st.Copy()
	next.Processed[cmd.Meta.ID] = true
	next.Night.WitchStage = WitchStageClosed
	next.Night.WitchSaveChoice = nil
	next.Night.WitchPoisonChoice = nil
	next.Night.WitchPoisonSkip = false

	none, err := NewMessageEffect(AudienceActor, WitchNoneMessageKey, map[string]any{})
	if err != nil {
		return st, nil, fmt.Errorf("game: witch none message: %w", err)
	}
	return next, []Effect{none}, nil
}
