package game

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// 游戏内退出/恶意退出测试（docs 游戏流程设计.md §恶意退出判定、§狼人
// 自爆 2、§退出约束、§五.5 重大事件）：
// 狼人白天退出按自爆；夜间退出按恶意退出死亡（不误导身份）；存活主动
// 退出/连续 3 次超时强制移除/投票踢出触发 10 分钟冷却；正常死亡等不触发；
// 退出玩家不可重入同一局（沿用 JoinStore.HasLeft 契约）。

func daySpeechReady() State {
	st := voteReadyState(false)
	st.Phase = PhaseDaySpeech
	st.PhaseVersion = 3
	return st
}

// nightReady 构造可退出测试的夜间状态（PhaseNight、PhaseVersion=4）。
func nightReady() State {
	st := voteReadyState(false)
	st.Phase = PhaseNight
	st.PhaseVersion = 4
	return st
}

func leaveCmd(id string, actor UserID, phase Phase, version uint64) LeaveGameCommand {
	return LeaveGameCommand{
		Meta: CommandMeta{ID: id, Actor: actor, ExpectedPhase: phase, PhaseVersion: version},
	}
}

func findCooldown(effects []Effect) (CooldownEffect, bool) {
	for _, e := range effects {
		if ce, ok := e.(CooldownEffect); ok {
			return ce, true
		}
	}
	return CooldownEffect{}, false
}

// TestLeaveWolfDayActsAsExplode 验证狼人白天退出按自爆处理：直接黑夜、
// 无遗言、票作废、wolves.explode 公告、触发冷却（docs §自爆 2、§退出约束①）。
func TestLeaveWolfDayActsAsExplode(t *testing.T) {
	st := daySpeechReady()
	r := NewReducer()

	after, effects, err := r.Reduce(st, leaveCmd("l1", 1, PhaseDaySpeech, 3))
	if err != nil {
		t.Fatalf("狼人白天退出 error = %v", err)
	}
	if after.Phase != PhaseNight {
		t.Fatalf("Phase = %v, want PhaseNight（按自爆直接进黑夜）", after.Phase)
	}
	if !playerBySeat(after.Players, 1).Dead || !playerBySeat(after.Players, 1).Left {
		t.Fatalf("狼人白天退出后应死亡且 Left：%+v", playerBySeat(after.Players, 1))
	}
	if !reflect.DeepEqual(after.Vote, VoteState{}) {
		t.Fatalf("退出后 Vote = %+v, want 清空", after.Vote)
	}
	if !containsKey(messageKeys(effects), WolfExplodeMessageKey) {
		t.Errorf("按自爆处理缺少 wolves.explode 公告：%v", effects)
	}
	ce, ok := findCooldown(effects)
	if !ok {
		t.Fatalf("狼人白天退出应触发冷却：%v", effects)
	}
	if ce.Duration != LeaveCooldownSeconds {
		t.Fatalf("冷却时长 = %v, want %v", ce.Duration, LeaveCooldownSeconds)
	}
	if ce.Reason != LeaveReasonWolfExplode {
		t.Fatalf("冷却原因 = %v, want LeaveReasonWolfExplode", ce.Reason)
	}
}

// TestLeaveNightIsMaliciousDeath 验证夜间退出（含狼人）不算自爆、按
// 恶意退出死亡公告（docs §自爆 2：公告为「恶意退出死亡」，不误导身份）。
func TestLeaveNightIsMaliciousDeath(t *testing.T) {
	r := NewReducer()

	for _, actor := range []UserID{1, 5} { // 狼人 1、村民 5
		t.Run(voteID("actor", actor), func(t *testing.T) {
			st := nightReady()
			after, effects, err := r.Reduce(st, leaveCmd(voteID("l", actor), actor, PhaseNight, 4))
			if err != nil {
				t.Fatalf("夜间退出 error = %v", err)
			}
			if after.Phase != PhaseNight {
				t.Fatalf("夜间退出不得切换阶段：Phase = %v", after.Phase)
			}
			if !playerBySeat(after.Players, Seat(actor)).Dead || !playerBySeat(after.Players, Seat(actor)).Left {
				t.Fatalf("夜间退出玩家应死亡且 Left")
			}
			if containsKey(messageKeys(effects), WolfExplodeMessageKey) {
				t.Errorf("夜间退出不得按自爆公告：%v", effects)
			}
			if !containsKey(messageKeys(effects), LeaveMaliciousMessageKey) {
				t.Errorf("缺少 leave.malicious 恶意退出死亡公告：%v", effects)
			}
			ce, ok := findCooldown(effects)
			if !ok {
				t.Fatalf("夜间存活退出应触发冷却：%v", effects)
			}
			if ce.Reason != LeaveReasonMaliciousNight {
				t.Fatalf("冷却原因 = %v, want LeaveReasonMaliciousNight", ce.Reason)
			}
		})
	}
}

// TestLeaveActiveDayNonWolf 验证白天非狼人存活主动退出：死亡、公告、
// 冷却；白天继续（不强制进黑夜）。
func TestLeaveActiveDayNonWolf(t *testing.T) {
	st := daySpeechReady()
	r := NewReducer()

	after, effects, err := r.Reduce(st, leaveCmd("l3", 5, PhaseDaySpeech, 3))
	if err != nil {
		t.Fatalf("白天主动退出 error = %v", err)
	}
	if after.Phase != PhaseDaySpeech {
		t.Fatalf("Phase = %v, want PhaseDaySpeech（白天继续）", after.Phase)
	}
	if !playerBySeat(after.Players, 5).Dead || !playerBySeat(after.Players, 5).Left {
		t.Fatalf("主动退出玩家应死亡且 Left")
	}
	if !containsKey(messageKeys(effects), LeaveMaliciousMessageKey) {
		t.Errorf("缺少 leave.malicious 公告：%v", effects)
	}
	if _, ok := findCooldown(effects); !ok {
		t.Fatalf("存活主动退出应触发冷却：%v", effects)
	}
	if !hasPersist(effects, PersistGameLeave) {
		t.Errorf("退出应产出 PersistGameLeave 记录（供 JoinStore.HasLeft 接线）：%v", effects)
	}
}

// TestLeaveRejectsWrongPhase 验证大厅等非游戏阶段退出走大厅流程、
// LeaveGameCommand 被拒（ErrWrongPhase）。
func TestLeaveRejectsWrongPhase(t *testing.T) {
	st := voteReadyState(false)
	st.Phase = PhaseLobby
	st.PhaseVersion = 1
	if _, _, err := NewReducer().Reduce(st, leaveCmd("l4", 1, PhaseLobby, 1)); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("大厅退出 error = %v, want ErrWrongPhase", err)
	}
}

// TestLeaveRejectsDead 验证游戏进行中死亡玩家不再受理退出命令。
func TestLeaveRejectsDead(t *testing.T) {
	st := nightReady()
	st.Players[4].Dead = true // 5 号死亡
	if _, _, err := NewReducer().Reduce(st, leaveCmd("l5", 5, PhaseNight, 4)); !errors.Is(err, ErrDeadPlayer) {
		t.Fatalf("死亡玩家退出 error = %v, want ErrDeadPlayer", err)
	}
}

// TestCooldownFor 验证跨局加入冷却判定清单（docs §退出约束）：
// 触发=游戏进行中存活主动退出/连续 3 次超时强制移除/游戏中被投票踢出；
// 不触发=正常死亡后退出/游戏前退出/正常完成一局等。
func TestCooldownFor(t *testing.T) {
	cases := []struct {
		name   string
		reason LeaveReason
		want   bool
	}{
		{"存活主动退出", LeaveReasonMaliciousActive, true},
		{"狼人白天退出按自爆", LeaveReasonWolfExplode, true},
		{"夜间恶意退出死亡", LeaveReasonMaliciousNight, true},
		{"连续3次超时强制移除", LeaveReasonForcedTimeout, true},
		{"游戏中被投票踢出", LeaveReasonVoteKicked, true},
		{"正常死亡后退出", LeaveReasonNormalDeath, false},
		{"游戏开始前退出", LeaveReasonPreGame, false},
		{"正常完成一局/再来一局", LeaveReasonGameEnd, false},
		{"房主强制解散/投票解散", LeaveReasonRoomClosed, false},
		{"Bot重启中止", LeaveReasonAborted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CooldownFor(tc.reason); got != tc.want {
				t.Fatalf("CooldownFor(%v) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

// TestTimeoutStreakWarnsAndRemoves 验证连续超时计数：
// 整局累计连续（第 2 次私聊预警）、第 3 次被系统强制移除（死亡 +
// 冷却 + 当前时间段主消息公告）；中间操作清零（操作后超时重新计数）。
func TestTimeoutStreakWarnsAndRemoves(t *testing.T) {
	r := reducer{rng: CryptoRNG{}} // 具体类型：直接驱动 advanceTimeoutStreaks 助手
	st := beginVote(t, voteReadyState(true))

	// 场景：5 号连续三轮收票窗口未确认（其余玩家均已确认并锁定），
	// 每轮超时事件由 reducer 在 voteTimeout 前把未确认者交给
	// advanceTimeoutStreaks：第 1 次 streak=1 无预警、第 2 次私聊预警、
	// 第 3 次强制移除；随后 4 号（已操作窗口清零）再超时从 1 重新计数。
	var fx []Effect
	var err error
	for n := 1; n <= 3; n++ {
		st, fx, err = r.advanceTimeoutStreaks(st, time.Now(), []Seat{5})
		if err != nil {
			t.Fatalf("advance #%d error = %v", n, err)
		}
		p5 := playerBySeat(st.Players, 5)
		switch n {
		case 1:
			if p5.TimeoutStreak != 1 {
				t.Fatalf("第一次超时 streak = %d, want 1", p5.TimeoutStreak)
			}
			if countKey(messageKeys(fx), LeaveTimeoutWarningMessageKey) != 0 {
				t.Fatalf("第 1 次超时不应预警：%v", fx)
			}
		case 2:
			if p5.TimeoutStreak != 2 {
				t.Fatalf("第二次超时 streak = %d, want 2", p5.TimeoutStreak)
			}
			if countKey(messageKeys(fx), LeaveTimeoutWarningMessageKey) != 1 {
				t.Fatalf("第 2 次超时缺少私聊预警：%v", fx)
			}
			for _, e := range fx {
				if me, ok := e.(MessageEffect); ok && me.Key == LeaveTimeoutWarningMessageKey && me.Audience != AudienceActor {
					t.Fatalf("超时预警受众 = %v, want AudienceActor（不全局广播）", me.Audience)
				}
			}
		case 3:
			if !p5.Dead || !p5.Left {
				t.Fatalf("第 3 次超时 5 号应被强制移除：%+v", p5)
			}
			if p5.TimeoutStreak != 3 {
				t.Fatalf("移除后 streak = %d, want 3", p5.TimeoutStreak)
			}
			if !containsKey(messageKeys(fx), LeaveRemovedMessageKey) {
				t.Errorf("缺少 leave.removed 移除公告：%v", fx)
			}
			ce, ok := findCooldown(fx)
			if !ok || ce.Reason != LeaveReasonForcedTimeout || ce.Duration != LeaveCooldownSeconds {
				t.Fatalf("强制移除应触发 10 分钟冷却：%v", fx)
			}
		}
	}

	// 4 号此前每轮收票窗口均已操作（不在未确认集合），streak 被清零；
	// 本轮超时从 1 重新计数，且不预警。
	st4, fx4, err := r.advanceTimeoutStreaks(st, time.Now(), []Seat{4})
	if err != nil {
		t.Fatalf("advance #4 error = %v", err)
	}
	if got := playerBySeat(st4.Players, 4).TimeoutStreak; got != 1 {
		t.Fatalf("操作后超时重新计数 streak = %d, want 1", got)
	}
	if countKey(messageKeys(fx4), LeaveTimeoutWarningMessageKey) != 0 {
		t.Fatalf("重新计数第 1 次不应预警：%v", fx4)
	}
}

// TestVoteTimeoutWiresStreak 验证 reducer 分派已接入超时计数：白天投票
// 超时后未确认者 streak=1（真实端到端接入证据）。
func TestVoteTimeoutWiresStreak(t *testing.T) {
	st := beginVote(t, voteReadyState(true))
	r := NewReducer()

	// 2/3/4/5/6 确认投 1；1 未确认 → 超时。
	after := st
	var err error
	for _, actor := range []UserID{2, 3, 4, 5, 6} {
		after, _, err = r.Reduce(after, voteCmd(voteID("v", actor), actor, seatPtr(1)))
		if err != nil {
			t.Fatalf("VoteCommand %d error = %v", actor, err)
		}
		after, _, err = r.Reduce(after, voteConfirmCmd(voteID("c", actor), actor))
		if err != nil {
			t.Fatalf("VoteConfirmCommand %d error = %v", actor, err)
		}
	}
	after, _, err = r.Reduce(after, voteTimeoutCmd("t1"))
	if err != nil {
		t.Fatalf("voteTimeout error = %v", err)
	}
	if got := playerBySeat(after.Players, 1).TimeoutStreak; got != 1 {
		t.Fatalf("超时未确认者 streak = %d, want 1", got)
	}
	for _, seat := range []Seat{2, 3, 4, 5, 6} {
		if got := playerBySeat(after.Players, seat).TimeoutStreak; got != 0 {
			t.Fatalf("已确认者 %d streak = %d, want 0（操作清零）", seat, got)
		}
	}
}

func hasPersist(effects []Effect, kind PersistKind) bool {
	for _, e := range effects {
		if pe, ok := e.(PersistEffect); ok && pe.Kind == kind {
			return true
		}
	}
	return false
}

// TestStateCopyCarriesLeaveFields 验证 State.Copy 对新增退出/超时字段的
// 深拷贝：Left 与 TimeoutStreak 是 Player 值字段（随 Players 切片整体
// 复制），修改副本不得影响原状态（docs/技术选型.md §5.1 值语义）。
func TestStateCopyCarriesLeaveFields(t *testing.T) {
	st := voteReadyState(false)
	st.Players[0].Left = true
	st.Players[0].TimeoutStreak = 2

	c := st.Copy()
	c.Players[0].Left = false
	c.Players[0].TimeoutStreak = 0

	if !st.Players[0].Left || st.Players[0].TimeoutStreak != 2 {
		t.Fatalf("副本修改泄漏到原状态: %+v", st.Players[0])
	}
	if c.Players[0].Left || c.Players[0].TimeoutStreak != 0 {
		t.Fatalf("副本字段未生效: %+v", c.Players[0])
	}
}
