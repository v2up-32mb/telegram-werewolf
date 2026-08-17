package game

// I4 红测：夜间阶段（狼人/女巫/预言家）超时也计入连续超时计数
//（docs 游戏流程设计.md §恶意退出判定②：整局累计连续计算、中间操作过则
// 重置；达到 2 预警、达到 3 强制移除并触发冷却）。

import (
	"testing"
	"time"
)

// nightStreakState 构造夜间狼人阶段状态：2 狼（狼1 已锁定，狼2 未锁）。
func nightStreakState() State {
	return State{
		Phase:        PhaseNight,
		PhaseVersion: 1,
		Players: []Player{
			{UserID: 1, Seat: 1, Role: RoleWolf},
			{UserID: 2, Seat: 2, Role: RoleWolf},
			{UserID: 3, Seat: 3, Role: RoleVillager},
		},
		Night: NightState{
			WolfRound:  1,
			WolfVotes:  map[Seat]*Seat{},
			WolfLocked: map[Seat]bool{1: true},
		},
		Settings:  DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
}

// streakMeta 构造夜间超时命令元信息。
func streakMeta(id string) CommandMeta {
	return CommandMeta{ID: id, ExpectedPhase: PhaseNight, PhaseVersion: 1, ReceivedAt: time.Now()}
}

func streakOf(st State, seat Seat) int {
	for _, p := range st.Players {
		if p.Seat == seat {
			return p.TimeoutStreak
		}
	}
	return -1
}

// TestWolfNightTimeoutStreakAndRemoval 验证：狼人夜间超时计入连续计数，
// 已锁定者重置；第 3 次超时强制移除 + 冷却。
func TestWolfNightTimeoutStreakAndRemoval(t *testing.T) {
	rd := NewReducer()

	// 第 1 次超时：狼2 未锁 → streak=1；狼1 已锁 → 重置 0。
	st, fx, err := rd.Reduce(nightStreakState(), TimeoutCommand{Meta: streakMeta("t1")})
	if err != nil {
		t.Fatalf("timeout#1: %v", err)
	}
	if got := streakOf(st, 2); got != 1 {
		t.Fatalf("狼2 streak = %d, want 1", got)
	}
	if got := streakOf(st, 1); got != 0 {
		t.Fatalf("狼1（已锁定）streak = %d, want 0（操作重置）", got)
	}

	// 第 2 次超时（重开狼轮）：狼2 → 2，产出私聊预警。
	st2 := st.Copy()
	st2.Night.WolfRound = 1
	st2, fx2, err := rd.Reduce(st2, TimeoutCommand{Meta: streakMeta("t2")})
	if err != nil {
		t.Fatalf("timeout#2: %v", err)
	}
	if got := streakOf(st2, 2); got != 2 {
		t.Fatalf("狼2 streak = %d, want 2", got)
	}
	foundWarn := false
	for _, e := range append(fx, fx2...) {
		if me, ok := e.(MessageEffect); ok && me.Key == LeaveTimeoutWarningMessageKey {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatal("第 2 次超时未产出连续超时预警（leave.timeout_warning）")
	}

	// 第 3 次超时：狼2 被强制移除（死亡 + Left + 冷却）。
	st3 := st2.Copy()
	st3.Night.WolfRound = 1
	st3, fx3, err := rd.Reduce(st3, TimeoutCommand{Meta: streakMeta("t3")})
	if err != nil {
		t.Fatalf("timeout#3: %v", err)
	}
	wolf2 := playerBySeat(st3.Players, 2)
	if !wolf2.Dead || !wolf2.Left || !wolf2.MaliciousExit {
		t.Fatalf("狼2 未被强制移除：dead=%v left=%v malicious=%v", wolf2.Dead, wolf2.Left, wolf2.MaliciousExit)
	}
	foundRemoved, foundCooldown := false, false
	for _, e := range append(append(fx, fx2...), fx3...) {
		switch te := e.(type) {
		case MessageEffect:
			if te.Key == LeaveRemovedMessageKey {
				foundRemoved = true
			}
		case CooldownEffect:
			if te.User == 2 {
				foundCooldown = true
			}
		}
	}
	if !foundRemoved || !foundCooldown {
		t.Fatalf("第 3 次超时缺少移除公告(%v)/冷却(%v)", foundRemoved, foundCooldown)
	}
}

// TestDeadRoleTimeoutNoStreak 验证：死亡神职超时不计入连续计数（docs §夜间
// 6「也不算超时」；advanceTimeoutStreaks 跳过死亡玩家）。
func TestDeadRoleTimeoutNoStreak(t *testing.T) {
	st := nightStreakState()
	st.Night.WolfRound = 0 // 狼阶段结束
	st.Night.WolfLocked = map[Seat]bool{}
	// 女巫窗口开启，但女巫已死亡（前置夜被刀）。
	st.Players = append(st.Players, Player{UserID: 4, Seat: 4, Role: RoleWitch, Dead: true})
	st.Night.WitchStage = WitchStageSave
	st.Night.WitchFirstNight = true

	rd := NewReducer()
	after, fx, err := rd.Reduce(st, TimeoutCommand{Meta: streakMeta("tw")})
	if err != nil {
		t.Fatalf("witch timeout: %v", err)
	}
	if got := streakOf(after, 4); got != 0 {
		t.Fatalf("死亡女巫超时 streak = %d, want 0（死亡不计入连续超时）", got)
	}
	for _, e := range fx {
		if me, ok := e.(MessageEffect); ok && me.Key == LeaveTimeoutWarningMessageKey {
			t.Fatal("死亡女巫超时不应产出连续超时预警")
		}
	}
}
