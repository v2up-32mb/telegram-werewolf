package game

import (
	"strings"
	"testing"
)

// 战报测试（docs 游戏流程设计.md §结算 7、§记录 243、阶段消息设计.md
// §15）：战报包含参与人、全员身份翻牌、结果（含积分变化）与关键事件；
// 关键事件只含最终状态可推导条目，不伪装成完整回放。

// TestBuildReport 验证战报结构：胜方、参与人（身份翻牌与积分）、
// 可推导关键事件（狼人袭击 2 号、白天放逐 5 号）。
func TestBuildReport(t *testing.T) {
	st := settleFixture()
	kill, exile := Seat(2), Seat(5)
	st.Night = NightState{WolfKillTarget: &kill}
	st.Vote = VoteState{Exiled: &exile}
	st.Players = withDeaths(st.Players, 2, 5)

	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	rep, err := BuildReport(settled)
	if err != nil {
		t.Fatalf("BuildReport error = %v", err)
	}
	if rep.Winner != CampWolf {
		t.Errorf("战报胜方 = %v, want CampWolf", rep.Winner)
	}
	if len(rep.Players) != 6 {
		t.Fatalf("战报参与人数 = %d, want 6", len(rep.Players))
	}
	bySeat := map[Seat]PlayerResult{}
	for _, p := range rep.Players {
		bySeat[p.Seat] = p
	}
	if r := bySeat[2]; r.Role != RoleWolf || r.Camp != CampWolf || !r.Died || r.Score != 2 {
		t.Errorf("seat 2 战报不符（死亡躺赢 +2）: %+v", r)
	}
	if r := bySeat[5]; r.Role != RoleVillager || r.Camp != CampGood || !r.Died {
		t.Errorf("seat 5 战报不符: %+v", r)
	}
	if len(rep.KeyEvents) != 2 {
		t.Fatalf("关键事件数 = %d, want 2（狼袭 2 号 + 放逐 5 号）: %+v", len(rep.KeyEvents), rep.KeyEvents)
	}
	var hasKill, hasExile bool
	for _, ev := range rep.KeyEvents {
		if ev.Text == "" {
			t.Error("关键事件文本为空")
		}
		if strings.Contains(ev.Text, "2") && ev.Phase == PhaseNight {
			hasKill = true
		}
		if strings.Contains(ev.Text, "5") && ev.Phase == PhaseDayVote {
			hasExile = true
		}
	}
	if !hasKill || !hasExile {
		t.Errorf("关键事件缺失（kill=%v exile=%v）: %+v", hasKill, hasExile, rep.KeyEvents)
	}
}

// TestReportNotFullReplay 验证战报不伪装完整回放：关键事件数量等于最终
// 状态可推导的死亡数（每名死者至多一条可推导事件），不维护逐阶段完整
// 历史。
func TestReportNotFullReplay(t *testing.T) {
	st := settleFixture()
	kill := Seat(2)
	st.Night = NightState{WolfKillTarget: &kill}
	st.Players = withDeaths(st.Players, 2, 5) // 5 号死亡但无可见死因（只算可推导项）

	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	rep, err := BuildReport(settled)
	if err != nil {
		t.Fatalf("BuildReport error = %v", err)
	}
	if len(rep.KeyEvents) != len(deadSeats(settled)) {
		t.Errorf("关键事件数 = %d, 死亡数 = %d（应一一对应，不伪装完整回放）",
			len(rep.KeyEvents), len(deadSeats(settled)))
	}
}

// TestBuildReportRejectsUnsolved 验证未结算状态不能产出战报。
func TestBuildReportRejectsUnsolved(t *testing.T) {
	st := settleFixture()
	st.Phase = PhaseNight
	st.Settled = SettledState{}
	if _, err := BuildReport(st); err == nil {
		t.Error("未结算状态 BuildReport 应报错")
	}
}
