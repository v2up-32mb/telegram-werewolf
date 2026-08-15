package game

import (
	"fmt"
	"math/rand"
	"testing"
)

// randomCommand 从确定性随机源生成任意命令（合法/非法 Meta 混合，
// 覆盖 Actor 在场/不在场、死亡目标、越界目标、错误阶段/版本路径）。
func randomCommand(rng *rand.Rand, i int) Command {
	meta := CommandMeta{
		ID:            fmt.Sprintf("cmd-%d-%d", i, rng.Intn(10)),
		Actor:         UserID(1 + rng.Intn(8)), // 1..8，7/8 不在房间
		ExpectedPhase: Phase(rng.Intn(9)),      // 含非法值
		PhaseVersion:  uint64(rng.Intn(6)),
	}
	target := Seat(rng.Intn(10)) // 0..9，覆盖越界
	switch rng.Intn(9) {
	case 0:
		return CreateRoomCommand{Meta: meta, Config: GameConfig{PlayerCount: 6, Roles: StandardDeck()}}
	case 1:
		return JoinRoomCommand{Meta: meta}
	case 2:
		return StartGameCommand{Meta: meta}
	case 3:
		return ConfirmRoleCommand{Meta: meta}
	case 4:
		return WolfKillCommand{Meta: meta, Target: target}
	case 5:
		return WitchUseCommand{Meta: meta, Action: WitchAction(1 + rng.Intn(2)), Target: target}
	case 6:
		return SeerCheckCommand{Meta: meta, Target: target}
	case 7:
		return VoteCommand{Meta: meta, Target: &target}
	default:
		return TimeoutCommand{Meta: meta}
	}
}

// assertStateInvariants 断言状态核心不变量：阶段合法、
// 座位严格 1～6 且唯一、角色合法（docs/技术选型.md §13.2）。
func assertStateInvariants(t *testing.T, st State) {
	t.Helper()
	if !st.Phase.Valid() {
		t.Fatalf("非法阶段 %v", st.Phase)
	}
	seats := map[Seat]bool{}
	for _, p := range st.Players {
		if !p.Seat.Valid() {
			t.Fatalf("非法座位 %d", p.Seat)
		}
		if seats[p.Seat] {
			t.Fatalf("重复座位 %d", p.Seat)
		}
		seats[p.Seat] = true
		if !p.Role.Valid() {
			t.Fatalf("非法角色 %v", p.Role)
		}
	}
}

// FuzzReducer 对任意命令序列连续应用 Reduce：
// 不得 panic，且状态始终满足核心不变量。
func FuzzReducer(f *testing.F) {
	f.Add(int64(1))
	f.Add(int64(7))
	f.Fuzz(func(t *testing.T, seed int64) {
		rng := rand.New(rand.NewSource(seed))
		st := rejectionFixture()
		r := NewReducer()
		for i := 0; i < 30; i++ {
			cmd := randomCommand(rng, i)
			var err error
			st, _, err = r.Reduce(st, cmd)
			if err == nil {
				// 骨架阶段不应出现成功路径；若出现则保留新状态继续断言。
			}
			assertStateInvariants(t, st)
		}
	})
}
