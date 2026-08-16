package game

// EvaluateVictory 判定给定玩家列表与胜负模式下是否已分出胜负
// （docs/游戏流程设计.md §结算 1）：
//   - 屠城（VictorySlaughter，6 人局默认）：狼人全灭 → 好人胜；
//     好人（神职+村民）全灭 → 狼人胜。
//   - 屠边（VictorySide）：神职全灭或村民全灭 → 狼人胜；狼人全灭 → 好人胜。
//
// 仅统计存活（!Dead）且已入座（Seat.Valid）的玩家。返回胜方阵营与
// 是否已结束；未分出胜负返回 (CampUnknown, false)。
//
// 胜负判定器与触发点无关：调用方（接线层）在狼人刀人、女巫救/毒、
// 白天投票等每个可能触发胜负的动作完成后立即调用（docs §结算 1：
// 先触发的胜利条件立即生效，后续行动作废；禁止只在夜末统一检查一次）。
// 同一结算周期内双方同时满足时按行动顺序由调用方分节点检查，
// 本函数只对静态玩家快照做判定。
func EvaluateVictory(players []Player, mode VictoryMode) (Camp, bool) {
	// 非法/未知模式不作判定（如 VictoryUnknown），返回未分胜负。
	if mode != VictorySlaughter && mode != VictorySide {
		return CampUnknown, false
	}
	var wolves, good, gods, villagers int
	for _, p := range players {
		if p.Dead || !p.Seat.Valid() {
			continue
		}
		switch p.Role {
		case RoleWolf:
			wolves++
		case RoleSeer, RoleWitch:
			gods++
			good++
		case RoleVillager:
			villagers++
			good++
		}
	}

	if wolves == 0 {
		return CampGood, true
	}
	switch mode {
	case VictorySlaughter:
		if good == 0 {
			return CampWolf, true
		}
	case VictorySide:
		if gods == 0 || villagers == 0 {
			return CampWolf, true
		}
	}
	return CampUnknown, false
}
