package game

import (
	"fmt"
	"time"
)

// 夜间结算与胜负消息 key（docs §结算 1、§白天 1 死讯播报）。
// 这些 key 无敏感前缀，允许 AudiencePublic；params 全部结构化
// （victims []Seat / winner Camp），渲染由后续接线按 key+params 执行。
const (
	// NightDeathMessageKey 是死讯播报：默认只公布谁死亡，不公布身份
	// 与具体死亡原因（AudiencePublic）。
	NightDeathMessageKey = "night.death"
	// NightPeaceMessageKey 是平安夜消息（AudiencePublic）。
	NightPeaceMessageKey = "night.peace"
	// SettlementVictoryMessageKey 是游戏结束的胜利方公告（AudiencePublic）。
	SettlementVictoryMessageKey = "settlement.victory"
)

// resolveNight 按固定行动顺序结算一夜死亡并即时判定胜负
// （docs 游戏流程设计.md §结算 1、§白天 1）：
//
//  1. 狼人刀人结算：WolfKillTarget 死亡，当且仅当女巫当晚未用解药
//     救该刀口（平安夜无刀口则跳过）；
//  2. 刀人后立即胜负判定：狼人先触发的胜利立即生效，女巫毒药等后续
//     行动全部作废（毒药目标保持存活，作为作废证据）；
//  3. 女巫毒药结算：当晚用毒时 WitchPoisonTarget 死亡；
//  4. 毒药后立即胜负判定：毒药触发或仍未分胜负时进入白天。
//
// 仅在「夜末统一结算时检查一次」不符合 docs（每个可能触发胜负的动作
// 完成后立即检查）；本函数在刀后与毒后分节点检查，EvaluateVictory
// 可被接线层用于白天投票等其他触发点。
//
// 分胜负：Phase=PhaseSettlement、Settled.Winner=胜方、PhaseVersion+1，
// 返回后任何 PhaseNight 命令再执行都会因阶段不匹配被拒绝（ErrWrongPhase，
// 后续行动作废）。未分胜负：Phase=PhaseDaySpeech、PhaseVersion+1。
// 两种路径都清理夜间窗口（WolfRound/WitchStage/SeerActive 与待确认
// 选择），并产出死讯/平安夜与胜利消息效果。
func resolveNight(st State) (State, []Effect, error) {
	next := st.Copy()
	effects := make([]Effect, 0, 2)
	victims := []Seat(nil)

	// 1) 狼人刀人结算。savedTonight 唯一推导：本夜已用一瓶且该瓶是
	// 解药（WitchUsedTonight && WitchSaveUsed && !WitchPoisonUsed）；
	// 上一夜用过解药、今夜用毒时不会被误判为今夜救过刀口。
	savedTonight := st.Night.WitchUsedTonight && st.Night.WitchSaveUsed && !st.Night.WitchPoisonUsed
	if kill := st.Night.WolfKillTarget; kill != nil && !savedTonight && !playerBySeat(next.Players, *kill).Dead {
		victims = append(victims, *kill)
		markPlayerDead(next.Players, *kill)
	}

	// 2) 刀人后立即胜负判定（狼人先触发者获胜，后续毒药作废）。
	if winner, done := EvaluateVictory(next.Players, next.Settings.Victory); done {
		return settleNight(next, winner, victims, effects)
	}

	// 3) 女巫毒药结算（当晚用毒）。
	if st.Night.WitchUsedTonight && st.Night.WitchPoisonUsed && st.Night.WitchPoisonTarget != nil {
		target := *st.Night.WitchPoisonTarget
		if !playerBySeat(next.Players, target).Dead {
			victims = append(victims, target)
			markPlayerDead(next.Players, target)
		}
	}

	// 4) 毒药后立即胜负判定。
	if winner, done := EvaluateVictory(next.Players, next.Settings.Victory); done {
		return settleNight(next, winner, victims, effects)
	}

	// 5) 未分胜负：死讯/平安夜消息 → 进入白天。
	msg, err := deathOrPeaceMessage(victims)
	if err != nil {
		return st, nil, err
	}
	effects = append(effects, msg)
	next = clearNightWindows(next)
	next.Phase = PhaseDaySpeech
	next.PhaseVersion++
	return next, effects, nil
}

// settleNight 执行胜利结算：清理夜间窗口、Phase=PhaseSettlement、
// Settled.Winner=胜方、PhaseVersion+1，并产出死讯/平安夜消息与胜利
// 公告。所有 Effects 先构造成功（原子性：任一失败不部分修改状态）。
func settleNight(st State, winner Camp, victims []Seat, prior []Effect) (State, []Effect, error) {
	effects := make([]Effect, 0, len(prior)+2)
	effects = append(effects, prior...)

	msg, err := deathOrPeaceMessage(victims)
	if err != nil {
		return st, nil, err
	}
	effects = append(effects, msg)

	victory, err := NewMessageEffect(AudiencePublic, SettlementVictoryMessageKey, map[string]any{
		"winner": winner,
	})
	if err != nil {
		return st, nil, fmt.Errorf("game: settlement victory message: %w", err)
	}
	effects = append(effects, victory)

	next := clearNightWindows(st)
	next.Settled.Winner = winner
	next.Phase = PhaseSettlement
	next.PhaseVersion++
	return next, effects, nil
}

// deathOrPeaceMessage 构造死讯消息（有死者）或平安夜消息（无死者）。
func deathOrPeaceMessage(victims []Seat) (Effect, error) {
	if len(victims) > 0 {
		msg, err := NewMessageEffect(AudiencePublic, NightDeathMessageKey, map[string]any{
			"victims": victims,
		})
		if err != nil {
			return nil, fmt.Errorf("game: night death message: %w", err)
		}
		return msg, nil
	}
	msg, err := NewMessageEffect(AudiencePublic, NightPeaceMessageKey, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("game: night peace message: %w", err)
	}
	return msg, nil
}

// clearNightWindows 关闭夜间各角色窗口并清空待确认选择，供结算后
// 进入白天/结算阶段使用（下一夜由各 begin*Phase 重新初始化）。
func clearNightWindows(st State) State {
	next := st.Copy()
	next.Night.WolfRound = 0
	next.Night.WitchStage = WitchStageClosed
	next.Night.WitchUsedTonight = false
	next.Night.WitchSaveChoice = nil
	next.Night.WitchPoisonChoice = nil
	next.Night.WitchPoisonSkip = false
	next.Night.SeerActive = false
	next.Night.SeerPending = nil
	return next
}

// markPlayerDead 把指定座位标记为死亡（无该座位时静默）。
func markPlayerDead(players []Player, seat Seat) {
	for i := range players {
		if players[i].Seat == seat {
			players[i].Dead = true
			return
		}
	}
}

// DeadRoleStageDuration 返回死亡神职角色阶段的假等待时长
// （docs §夜间 6：角色在行动窗口开始前已死亡时，阶段仍按固定流程进入，
// 等待原时长的 2/3 后再进入下一阶段，避免存活玩家通过「阶段变快/跳过」
// 推断出该角色已死）。向下取整；非正输入返回 0，供接线层设置计时器。
func DeadRoleStageDuration(normal time.Duration) time.Duration {
	if normal <= 0 {
		return 0
	}
	return normal * 2 / 3
}
