package game

import (
	"errors"
	"testing"
)

// 游戏中治理机制测试（docs 游戏流程设计.md §解散 87-90、§投票踢人 92-95、
// §房主控制面板 97-98、§积分系统 100-104、§掉线处理 183-216）：
// 投票解散/投票踢人仅存活玩家参与、超过三分之一同意即通过、局内每人
// 限发起 1 次、每个阶段限发起 1 次；踢人走掉线规则（判负移除语义）；
// 投票解散不扣分；房主强制解散需二次确认、扣 10 分、积分 ≤9 禁止。

// govReady 构造可进入治理流程的领域状态：6 名存活玩家、房主 1 号、
// PhaseNight v4（治理在 PhaseDeal/Night/DaySpeech/DayVote 均受理）。
func govReady() State {
	st := voteReadyState(false)
	st.Lobby.Owner = 1
	st.Phase = PhaseNight
	st.PhaseVersion = 4
	return st
}

func govMeta(id string, actor UserID, st State) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: st.Phase, PhaseVersion: st.PhaseVersion}
}

func dissolveCmd(id string, actor UserID, st State) GovernanceDissolveCommand {
	return GovernanceDissolveCommand{Meta: govMeta(id, actor, st)}
}

func dissolveVoteCmd(id string, actor UserID, st State) GovernanceDissolveVoteCommand {
	return GovernanceDissolveVoteCommand{Meta: govMeta(id, actor, st)}
}

func kickCmd(id string, actor UserID, st State, target Seat) GovernanceKickCommand {
	return GovernanceKickCommand{Meta: govMeta(id, actor, st), Target: target}
}

func kickVoteCmd(id string, actor UserID, st State) GovernanceKickVoteCommand {
	return GovernanceKickVoteCommand{Meta: govMeta(id, actor, st)}
}

func hostCmd(id string, actor UserID, st State, confirm bool, score int) HostDissolveCommand {
	return HostDissolveCommand{Meta: govMeta(id, actor, st), Confirm: confirm, HostScore: score}
}

func findDissolve(effects []Effect) (DissolveEffect, bool) {
	for _, e := range effects {
		if de, ok := e.(DissolveEffect); ok {
			return de, true
		}
	}
	return DissolveEffect{}, false
}

func findScorePenalty(effects []Effect) (ScorePenaltyEffect, bool) {
	for _, e := range effects {
		if se, ok := e.(ScorePenaltyEffect); ok {
			return se, true
		}
	}
	return ScorePenaltyEffect{}, false
}

// TestGovernanceDissolvePassesAtThreshold 验证投票解散：超过三分之一
// 同意即通过（6 人局 3 票 = 严格 > 1/3），通过后不扣分、清空本轮投票。
func TestGovernanceDissolvePassesAtThreshold(t *testing.T) {
	st := govReady()
	r := NewReducer()

	var err error
	st, _, err = r.Reduce(st, dissolveCmd("d1", 1, st)) // 发起者 1 号计同意票
	if err != nil {
		t.Fatalf("发起投票解散 error = %v", err)
	}
	if len(st.Governance.DissolveVotes) != 1 {
		t.Fatalf("发起后同意票数 = %d, want 1", len(st.Governance.DissolveVotes))
	}

	st, effects, err := r.Reduce(st, dissolveVoteCmd("d2", 2, st))
	if err != nil {
		t.Fatalf("同意票 2 号 error = %v", err)
	}
	st, effects, err = r.Reduce(st, dissolveVoteCmd("d3", 3, st))
	if err != nil {
		t.Fatalf("同意票 3 号 error = %v", err)
	}
	de, ok := findDissolve(effects)
	if !ok {
		t.Fatalf("3 票同意应触发解散：%v", effects)
	}
	if de.Reason != DissolveVoted {
		t.Fatalf("解散原因 = %v, want DissolveVoted", de.Reason)
	}
	if !containsKey(messageKeys(effects), GovernanceDissolvePassedMessageKey) {
		t.Errorf("缺少 governance.dissolve.passed 公告：%v", effects)
	}
	if _, ok := findScorePenalty(effects); ok {
		t.Errorf("投票解散不得扣分：%v", effects)
	}
	if len(st.Governance.DissolveVotes) != 0 {
		t.Errorf("通过后本轮投票应清空：%+v", st.Governance.DissolveVotes)
	}
}

// TestGovernanceDissolveStaysOpenBelowThreshold 验证未达 1/3 阈值时
// 投票保持开启（同意票继续累计，不产出解散效果）。
func TestGovernanceDissolveStaysOpenBelowThreshold(t *testing.T) {
	st := govReady()
	r := NewReducer()

	st, _, err := r.Reduce(st, dissolveCmd("d1", 1, st))
	if err != nil {
		t.Fatalf("发起 error = %v", err)
	}
	st, effects, err := r.Reduce(st, dissolveVoteCmd("d2", 2, st))
	if err != nil {
		t.Fatalf("同意票 error = %v", err)
	}
	if _, ok := findDissolve(effects); ok {
		t.Fatalf("2 票（6 人局）不应解散：%v", effects)
	}
	if len(st.Governance.DissolveVotes) != 2 {
		t.Fatalf("同意票数 = %d, want 2（保持开启）", len(st.Governance.DissolveVotes))
	}
}

// TestGovernanceRejectsNonParticipants 验证仅存活玩家参与：不在房间/
// 已死亡玩家发起或投票均被拒。
func TestGovernanceRejectsNonParticipants(t *testing.T) {
	r := NewReducer()

	t.Run("不在房间", func(t *testing.T) {
		st := govReady()
		if _, _, err := r.Reduce(st, dissolveCmd("d-x", 999, st)); !errors.Is(err, ErrNotInRoom) {
			t.Fatalf("不在房间发起 error = %v, want ErrNotInRoom", err)
		}
	})
	t.Run("死亡玩家发起", func(t *testing.T) {
		st := govReady()
		st.Players[3].Dead = true // 4 号死亡
		if _, _, err := r.Reduce(st, dissolveCmd("d-d", 4, st)); !errors.Is(err, ErrDeadPlayer) {
			t.Fatalf("死亡玩家发起 error = %v, want ErrDeadPlayer", err)
		}
	})
	t.Run("死亡玩家投票", func(t *testing.T) {
		st := govReady()
		st.Players[3].Dead = true
		st, _, err := r.Reduce(st, dissolveCmd("d1", 1, st))
		if err != nil {
			t.Fatalf("发起 error = %v", err)
		}
		if _, _, err := r.Reduce(st, dissolveVoteCmd("d-dv", 4, st)); !errors.Is(err, ErrDeadPlayer) {
			t.Fatalf("死亡玩家投票 error = %v, want ErrDeadPlayer", err)
		}
	})
}

// TestGovernanceInitiationLimits 验证发起限制：局内每人限发起 1 次、
// 每个阶段限发起 1 次；阶段切换重置每阶段限制、每局发起记录保留。
func TestGovernanceInitiationLimits(t *testing.T) {
	r := NewReducer()

	st, _, err := r.Reduce(govReady(), dissolveCmd("d1", 1, govReady()))
	if err != nil {
		t.Fatalf("1 号发起 error = %v", err)
	}
	if _, _, err := r.Reduce(st, dissolveCmd("d1b", 1, st)); !errors.Is(err, ErrAlreadyInitiated) {
		t.Fatalf("1 号再次发起 error = %v, want ErrAlreadyInitiated", err)
	}
	if _, _, err := r.Reduce(st, dissolveCmd("d2", 2, st)); !errors.Is(err, ErrPhaseAlreadyInitiated) {
		t.Fatalf("同阶段 2 号发起 error = %v, want ErrPhaseAlreadyInitiated", err)
	}

	// 阶段切换（PhaseVersion 变化）：2 号可发起；1 号仍受每局限制。
	next := st
	next.Phase = PhaseDaySpeech
	next.PhaseVersion = 5
	if _, _, err := r.Reduce(next, dissolveCmd("d2b", 2, next)); err != nil {
		t.Fatalf("新阶段 2 号发起 error = %v, want 允许", err)
	}
	if _, _, err := r.Reduce(next, dissolveCmd("d1c", 1, next)); !errors.Is(err, ErrAlreadyInitiated) {
		t.Fatalf("新阶段 1 号再次发起 error = %v, want ErrAlreadyInitiated（每局一次）", err)
	}
}

// TestGovernanceVoteRejects 验证投票边界：未发起先投票、重复投票被拒。
func TestGovernanceVoteRejects(t *testing.T) {
	r := NewReducer()

	t.Run("未发起先投票", func(t *testing.T) {
		st := govReady()
		if _, _, err := r.Reduce(st, dissolveVoteCmd("v0", 2, st)); !errors.Is(err, ErrNotInitiated) {
			t.Fatalf("未发起投票 error = %v, want ErrNotInitiated", err)
		}
		if _, _, err := r.Reduce(st, kickVoteCmd("k0", 2, st)); !errors.Is(err, ErrNotInitiated) {
			t.Fatalf("未发起踢人投票 error = %v, want ErrNotInitiated", err)
		}
	})
	t.Run("重复投票", func(t *testing.T) {
		st, _, err := r.Reduce(govReady(), dissolveCmd("d1", 1, govReady()))
		if err != nil {
			t.Fatalf("发起 error = %v", err)
		}
		st, _, err = r.Reduce(st, dissolveVoteCmd("v2", 2, st))
		if err != nil {
			t.Fatalf("2 号投票 error = %v", err)
		}
		if _, _, err := r.Reduce(st, dissolveVoteCmd("v2b", 2, st)); !errors.Is(err, ErrAlreadyVoted) {
			t.Fatalf("2 号重复投票 error = %v, want ErrAlreadyVoted", err)
		}
	})
}

// TestGovernanceKickTargetValidation 验证投票踢人目标校验：不在房间/
// 已死亡/发起者本人均被拒。
func TestGovernanceKickTargetValidation(t *testing.T) {
	r := NewReducer()

	if _, _, err := r.Reduce(govReady(), kickCmd("k1", 1, govReady(), 9)); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("目标不在房间 error = %v, want ErrInvalidTarget", err)
	}
	st := govReady()
	st.Players[3].Dead = true
	if _, _, err := r.Reduce(st, kickCmd("k2", 1, st, 4)); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("目标已死亡 error = %v, want ErrInvalidTarget", err)
	}
	if _, _, err := r.Reduce(govReady(), kickCmd("k3", 1, govReady(), 1)); !errors.Is(err, ErrGovernanceKickSelf) {
		t.Fatalf("踢自己 error = %v, want ErrGovernanceKickSelf", err)
	}
}

// TestGovernanceKickStaysOpenBelowThreshold 验证投票踢人未达 1/3 阈值时
// 投票保持开启（同意票继续累计、目标保留、不产出通过效果），与
// TestGovernanceDissolveStaysOpenBelowThreshold 对称。
func TestGovernanceKickStaysOpenBelowThreshold(t *testing.T) {
	st := govReady()
	r := NewReducer()

	st, _, err := r.Reduce(st, kickCmd("k1", 1, st, 5))
	if err != nil {
		t.Fatalf("发起踢人 error = %v", err)
	}
	st, effects, err := r.Reduce(st, kickVoteCmd("k2", 2, st))
	if err != nil {
		t.Fatalf("同意票 error = %v", err)
	}
	if _, ok := findDissolve(effects); ok {
		t.Fatalf("2 票（6 人局）不应踢人：%v", effects)
	}
	if len(st.Governance.KickVotes) != 2 {
		t.Fatalf("同意票数 = %d, want 2（保持开启）", len(st.Governance.KickVotes))
	}
	if st.Governance.KickTarget == nil || *st.Governance.KickTarget != 5 {
		t.Fatalf("踢人目标应保留：%+v", st.Governance.KickTarget)
	}
	if p := playerBySeat(st.Players, 5); p.Dead || p.Left {
		t.Fatalf("未达阈值不得触发掉线：%+v", p)
	}
}

// TestGovernanceKickDropsTarget 验证投票踢人通过：被踢者按掉线处理
// （判负移除语义）——死亡 + Left + 10 分钟跨局加入冷却 + PersistGameLeave
// + 公共公告；游戏阶段不变。
func TestGovernanceKickDropsTarget(t *testing.T) {
	st := govReady()
	r := NewReducer()

	var err error
	st, _, err = r.Reduce(st, kickCmd("k1", 1, st, 5)) // 踢 5 号，发起者计同意票
	if err != nil {
		t.Fatalf("发起踢人 error = %v", err)
	}
	st, effects, err := r.Reduce(st, kickVoteCmd("k2", 2, st))
	if err != nil {
		t.Fatalf("同意票 2 号 error = %v", err)
	}
	st, effects, err = r.Reduce(st, kickVoteCmd("k3", 3, st))
	if err != nil {
		t.Fatalf("同意票 3 号 error = %v", err)
	}

	if st.Phase != PhaseNight {
		t.Fatalf("踢人不得切换阶段：Phase = %v", st.Phase)
	}
	target := playerBySeat(st.Players, 5)
	if !target.Dead || !target.Left {
		t.Fatalf("被踢者应按掉线处理（死亡 + Left）：%+v", target)
	}
	if !containsKey(messageKeys(effects), GovernanceKickPassedMessageKey) {
		t.Errorf("缺少 governance.kick.passed 公告：%v", effects)
	}
	ce, ok := findCooldown(effects)
	if !ok {
		t.Fatalf("被踢者应触发跨局冷却：%v", effects)
	}
	if ce.Duration != LeaveCooldown || ce.Reason != LeaveReasonVoteKicked {
		t.Fatalf("冷却 = %+v, want %v/LeaveReasonVoteKicked", ce, LeaveCooldown)
	}
	if !hasPersist(effects, PersistGameLeave) {
		t.Errorf("被踢者应产出 PersistGameLeave：%v", effects)
	}
	if len(st.Governance.KickVotes) != 0 || st.Governance.KickTarget != nil {
		t.Errorf("通过后本轮踢人投票应清空：%+v", st.Governance)
	}
}

// TestGovernanceHostDissolve 验证房主强制解散：仅房主、二次确认、
// 积分 ≤9 禁止、确认后扣 10 分并解散（不经过投票）。
func TestGovernanceHostDissolve(t *testing.T) {
	r := NewReducer()

	t.Run("非房主被拒", func(t *testing.T) {
		if _, _, err := r.Reduce(govReady(), hostCmd("h1", 2, govReady(), false, 100)); !errors.Is(err, ErrNotHost) {
			t.Fatalf("非房主 error = %v, want ErrNotHost", err)
		}
	})
	t.Run("未先确认直接确认被拒", func(t *testing.T) {
		st := govReady()
		if _, _, err := r.Reduce(st, hostCmd("h2", 1, st, true, 100)); !errors.Is(err, ErrHostDissolveNotConfirmed) {
			t.Fatalf("未先确认 error = %v, want ErrHostDissolveNotConfirmed", err)
		}
	})
	t.Run("二次确认流程", func(t *testing.T) {
		st := govReady()

		// 第一次：请求二次确认，不解散。
		after, effects, err := r.Reduce(st, hostCmd("h3a", 1, st, false, 100))
		if err != nil {
			t.Fatalf("请求二次确认 error = %v", err)
		}
		if !after.Governance.HostDissolvePending {
			t.Fatal("请求确认后 HostDissolvePending 应为 true")
		}
		if !containsKey(messageKeys(effects), GovernanceHostDissolveConfirmMessageKey) {
			t.Fatalf("缺少二次确认提示：%v", effects)
		}
		for _, e := range effects {
			if me, ok := e.(MessageEffect); ok && me.Key == GovernanceHostDissolveConfirmMessageKey && me.Audience != AudienceHost {
				t.Fatalf("二次确认提示受众 = %v, want AudienceHost", me.Audience)
			}
		}
		if _, ok := findDissolve(effects); ok {
			t.Fatal("请求确认阶段不得解散")
		}

		// 积分 ≤9 禁止。
		if _, _, err := r.Reduce(after, hostCmd("h3b", 1, after, true, 9)); !errors.Is(err, ErrInsufficientScore) {
			t.Fatalf("积分 9 强制解散 error = %v, want ErrInsufficientScore", err)
		}

		// 积分 >9：确认后扣 10 分并解散。
		done, effects, err := r.Reduce(after, hostCmd("h3c", 1, after, true, 10))
		if err != nil {
			t.Fatalf("确认解散 error = %v", err)
		}
		de, ok := findDissolve(effects)
		if !ok || de.Reason != HostForced {
			t.Fatalf("强制解散效果 = %v, want DissolveEffect{HostForced}", effects)
		}
		se, ok := findScorePenalty(effects)
		if !ok || se.Amount != 10 {
			t.Fatalf("应扣 10 分：%v", effects)
		}
		if !containsKey(messageKeys(effects), GovernanceHostDissolvePassedMessageKey) {
			t.Errorf("缺少 governance.host_dissolve.passed 公告：%v", effects)
		}
		if done.Governance.HostDissolvePending {
			t.Error("解散后 HostDissolvePending 应复位")
		}
	})
}

// TestGovernanceRejectsWrongPhase 验证大厅/结算阶段治理命令被拒。
func TestGovernanceRejectsWrongPhase(t *testing.T) {
	r := NewReducer()

	st := govReady()
	st.Phase = PhaseLobby
	st.PhaseVersion = 1
	if _, _, err := r.Reduce(st, dissolveCmd("d-l", 1, st)); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("大厅投票解散 error = %v, want ErrWrongPhase", err)
	}
	st = govReady()
	st.Phase = PhaseSettlement
	st.PhaseVersion = 5
	if _, _, err := r.Reduce(st, kickCmd("k-s", 1, st, 5)); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("结算阶段踢人 error = %v, want ErrWrongPhase", err)
	}
}

// TestStateCopyCarriesGovernance 验证 State.Copy 深拷贝 GovernanceState
// 的全部可变字段（map 与指针），修改副本不得影响原状态。
func TestStateCopyCarriesGovernance(t *testing.T) {
	st := govReady()
	target := Seat(5)
	st.Governance = GovernanceState{
		PhaseVersion:        4,
		DissolveVotes:       map[Seat]bool{1: true},
		DissolveBy:          map[Seat]bool{1: true},
		DissolveInitiated:   true,
		KickVotes:           map[Seat]bool{2: true},
		KickBy:              map[Seat]bool{2: true},
		KickInitiated:       true,
		KickTarget:          &target,
		HostDissolvePending: true,
	}

	c := st.Copy()
	delete(c.Governance.DissolveVotes, 1)
	delete(c.Governance.DissolveBy, 1)
	c.Governance.DissolveInitiated = false
	c.Governance.KickVotes[3] = true
	c.Governance.KickBy[3] = true
	c.Governance.KickInitiated = false
	*c.Governance.KickTarget = 6
	c.Governance.HostDissolvePending = false
	c.Governance.PhaseVersion = 9

	if st.Governance.PhaseVersion != 4 || !st.Governance.DissolveInitiated || !st.Governance.KickInitiated || !st.Governance.HostDissolvePending {
		t.Errorf("副本修改泄漏到原状态：%+v", st.Governance)
	}
	if len(st.Governance.DissolveVotes) != 1 || len(st.Governance.DissolveBy) != 1 {
		t.Errorf("原状态 map 被修改：%+v", st.Governance)
	}
	if len(st.Governance.KickVotes) != 1 || len(st.Governance.KickBy) != 1 {
		t.Errorf("原状态 Kick map 被修改：%+v", st.Governance)
	}
	if st.Governance.KickTarget == nil || *st.Governance.KickTarget != 5 {
		t.Errorf("原状态 KickTarget 被修改：%+v", st.Governance.KickTarget)
	}
}
