package game

import "fmt"

// 白天狼人自爆领域规则（docs 游戏流程设计.md §狼人自爆 1、§投票 5、
// §五.5 重大事件）：
//   - 仅存活狼人可在白天任意时刻自爆（PhaseDaySpeech / PhaseDayVote，
//     含平票轮与遗言窗口）；非狼人 → ErrNotWolf（复用 night_wolf.go 既有
//     哨兵，语义一致：只有狼人可执行该操作）；夜间/其他阶段 →
//     ErrWrongPhase（docs §投票 5：投票阶段狼人自爆优先）；
//   - 自爆优先：已投出的票（含已确认）与平票状态（Tie/TieRound/
//     Candidates/Excluded 等）全部作废，直接进入黑夜（Phase=PhaseNight、
//     PhaseVersion+1）；
//   - 永远无遗言：不进入 VoteStageLastWords，不产出 last_words.*
//     （docs §自爆 1）；
//   - 自爆结果写入当前白天主消息（wolves.explode，AudiencePublic，
//     params seat），不额外发送永久独立事件消息（docs §五.5）；
//   - 取消当前阶段计时器（TimerEffect Cancel）；处于投票阶段时删除投票
//     临时消息（vote.delete）；
//   - 自爆不是退出：自爆狼人 Left 不置位（与 leave.go 狼人白天退出按
//     自爆的主动退出路径区分，后者置 Left 并触发加入冷却）；
//   - 胜负即时判定与夜间主消息由接线层/既有结算任务负责，本文件不实现。

// WolfExplodeMessageKey 是自爆公共公告（写当前白天主消息，不额外发送
// 永久事件消息）：params seat。
const WolfExplodeMessageKey = "wolves.explode"

// explode 处理 ExplodeCommand（通用 validator 已校验重复 ID/阶段/版本/
// 在场/存活；此处校验自爆专用边界：白天阶段 + 狼人角色）。
func (r reducer) explode(st State, cmd ExplodeCommand) (State, []Effect, error) {
	if st.Phase != PhaseDaySpeech && st.Phase != PhaseDayVote {
		return st, nil, ErrWrongPhase
	}
	seat, ok := seatByUser(st.Players, cmd.Meta.Actor)
	if !ok {
		return st, nil, ErrNotInRoom
	}
	if roleAtSeat(st.Players, seat) != RoleWolf {
		return st, nil, ErrNotWolf
	}
	after, effects, err := r.applyWolfExplode(st, seat)
	if err != nil {
		return st, nil, err
	}
	after.Processed[cmd.Meta.ID] = true
	return after, effects, nil
}

// applyWolfExplode 执行自爆状态转换（标记死亡、投票/平票作废、直接进入
// 黑夜）并产出效果序列：当前阶段计时器取消 →（投票阶段）逐存活玩家
// vote.delete 删除投票临时消息 → wolves.explode 公共公告。
// leave.go 复用（狼人白天退出按自爆处理，docs §自爆 2）。
func (r reducer) applyWolfExplode(st State, seat Seat) (State, []Effect, error) {
	next := st.Copy()
	markPlayerDead(next.Players, seat)
	next.Vote = VoteState{}
	next.Phase = PhaseNight
	next.PhaseVersion++

	effects := make([]Effect, 0, len(next.Players)+2)
	effects = append(effects, TimerEffect{Phase: st.Phase, Cancel: true})
	if st.Phase == PhaseDayVote {
		for _, s := range aliveSeats(st.Players) {
			del, err := NewMessageEffect(AudienceActor, VoteDeleteMessageKey, map[string]any{"seat": s})
			if err != nil {
				return st, nil, fmt.Errorf("game: explode vote delete: %w", err)
			}
			effects = append(effects, del)
		}
	}
	ann, err := NewMessageEffect(AudiencePublic, WolfExplodeMessageKey, map[string]any{"seat": seat})
	if err != nil {
		return st, nil, fmt.Errorf("game: wolf explode announce: %w", err)
	}
	effects = append(effects, ann)
	return next, effects, nil
}
