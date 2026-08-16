package game

import "testing"

// sixLiveFixture 构造 6 人全部存活的标准 MVP 玩家列表。
func sixLiveFixture() []Player {
	return []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 3, Seat: 3, Role: RoleSeer},
		{UserID: 4, Seat: 4, Role: RoleWitch},
		{UserID: 5, Seat: 5, Role: RoleVillager},
		{UserID: 6, Seat: 6, Role: RoleVillager},
	}
}

// withDeaths 返回将指定座位标记为死亡的副本。
func withDeaths(players []Player, dead ...Seat) []Player {
	out := append([]Player(nil), players...)
	for i := range out {
		for _, s := range dead {
			if out[i].Seat == s {
				out[i].Dead = true
			}
		}
	}
	return out
}

// TestEvaluateVictorySlaughter 验证屠城（6 人局默认）：狼人全灭 → 好人胜；
// 好人（神职+村民）全灭 → 狼人胜；未分胜负返回 false。
func TestEvaluateVictorySlaughter(t *testing.T) {
	cases := []struct {
		name    string
		players []Player
		want    Camp
		done    bool
	}{
		{"全员存活未分胜负", sixLiveFixture(), CampUnknown, false},
		{"狼人全灭好人胜", withDeaths(sixLiveFixture(), 1, 2), CampGood, true},
		{"好人全灭狼人胜", withDeaths(sixLiveFixture(), 3, 4, 5, 6), CampWolf, true},
		{"仅剩一狼一好人未分胜负", withDeaths(sixLiveFixture(), 1, 3, 4, 6), CampUnknown, false},
		{"狼人全灭且好人只剩平民", withDeaths(sixLiveFixture(), 1, 2, 3, 4), CampGood, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			winner, done := EvaluateVictory(tc.players, VictorySlaughter)
			if done != tc.done || winner != tc.want {
				t.Errorf("EvaluateVictory(屠城) = (%v, %v), want (%v, %v)", winner, done, tc.want, tc.done)
			}
		})
	}
}

// TestEvaluateVictorySide 验证屠边：神职全灭 → 狼人胜；村民全灭 → 狼人胜；
// 狼人全灭 → 好人胜。
func TestEvaluateVictorySide(t *testing.T) {
	cases := []struct {
		name    string
		players []Player
		want    Camp
		done    bool
	}{
		{"全员存活未分胜负", sixLiveFixture(), CampUnknown, false},
		{"神职全灭狼人胜", withDeaths(sixLiveFixture(), 3, 4), CampWolf, true},
		{"村民全灭狼人胜", withDeaths(sixLiveFixture(), 5, 6), CampWolf, true},
		{"狼人全灭好人胜", withDeaths(sixLiveFixture(), 1, 2), CampGood, true},
		{"神职村民都有存活未分胜负", withDeaths(sixLiveFixture(), 1, 4), CampUnknown, false},
		{"神职村民各剩一且狼人全灭好人胜", withDeaths(sixLiveFixture(), 1, 2, 4), CampGood, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			winner, done := EvaluateVictory(tc.players, VictorySide)
			if done != tc.done || winner != tc.want {
				t.Errorf("EvaluateVictory(屠边) = (%v, %v), want (%v, %v)", winner, done, tc.want, tc.done)
			}
		})
	}
}

// TestEvaluateVictoryIgnoresDeadAndUnseated 验证只统计存活且已入座的玩家：
// 死亡与席位非法的玩家不参与胜负计算。
func TestEvaluateVictoryIgnoresDeadAndUnseated(t *testing.T) {
	players := []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf, Dead: true},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 7, Seat: 0, Role: RoleVillager}, // 未入座
		{UserID: 8, Seat: 0, Role: RoleWitch},    // 未入座
	}
	winner, done := EvaluateVictory(players, VictorySlaughter)
	if !done || winner != CampWolf {
		t.Errorf("EvaluateVictory = (%v, %v), want (CampWolf, true)（存活狼 2 号；未入座者不计入好人）", winner, done)
	}
}

// TestEvaluateVictoryUnknownMode 验证非法/未知胜负模式不作判定。
func TestEvaluateVictoryUnknownMode(t *testing.T) {
	winner, done := EvaluateVictory(withDeaths(sixLiveFixture(), 1, 2), VictoryUnknown)
	if done || winner != CampUnknown {
		t.Errorf("EvaluateVictory(未知模式) = (%v, %v), want (CampUnknown, false)", winner, done)
	}
}

// TestEvaluateVictoryTriggerPointAgnostic 验证胜负判定器与触发点无关：
// 狼人刀后 / 女巫毒后 / 白天投票后的死亡状态均可直接判定（docs §结算 1：
// 每个可能触发胜负的动作完成后立即检查）。
func TestEvaluateVictoryTriggerPointAgnostic(t *testing.T) {
	t.Run("刀后触发", func(t *testing.T) {
		// 狼 2 号刀死最后好人 5 号 → 屠城好人全灭？不——仅剩狼 2：好人全灭。
		players := withDeaths(sixLiveFixture(), 1, 3, 4, 6)
		players = withDeaths(players, 5) // 刀死最后好人
		winner, done := EvaluateVictory(players, VictorySlaughter)
		if !done || winner != CampWolf {
			t.Errorf("刀后 = (%v, %v), want CampWolf", winner, done)
		}
	})
	t.Run("毒后触发", func(t *testing.T) {
		// 女巫毒死 2 号最后狼人 → 狼人全灭，好人胜。
		players := withDeaths(sixLiveFixture(), 1, 3, 4, 5, 6)
		players = withDeaths(players, 2)
		winner, done := EvaluateVictory(players, VictorySlaughter)
		if !done || winner != CampGood {
			t.Errorf("毒后 = (%v, %v), want CampGood", winner, done)
		}
	})
	t.Run("投票后触发", func(t *testing.T) {
		// 白天投票放逐 2 号最后狼人 → 狼人全灭，好人胜。
		players := withDeaths(sixLiveFixture(), 1, 3, 4, 5, 6)
		players = withDeaths(players, 2)
		winner, done := EvaluateVictory(players, VictorySlaughter)
		if !done || winner != CampGood {
			t.Errorf("投票后 = (%v, %v), want CampGood", winner, done)
		}
	})
}
