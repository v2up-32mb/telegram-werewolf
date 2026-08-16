package game

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 结算/积分/回大厅与再来一局测试（docs 游戏流程设计.md §结算 5-8、
// §积分系统 1、§恶意退出判定、§退出约束 1、§AI 补位积分、
// 阶段消息设计.md §15/§16）：
// 胜方、全员身份翻牌、胜利 +5 / 死亡躺赢 +2 / 失败 0 / 恶意退出且胜 0 /
// 恶意退出且败 -5；MVP 不评；Reducer 产出 PersistSettlementEffect 供
// 外层调用 Task 16 单事务；白天放逐落定后即时结算（docs §结算 1）；
// 房主「再来一局」回等待大厅：保留成员、沿用配置、≥15 秒退出窗口、
// 窗口内拒绝开始、窗口后放行。

// settleFixture 构造已完成胜负判定、等待结算的 PhaseSettlement 6 人局
// （屠城；狼 1/2、预言 3、女巫 4、平民 5/6；由用例按需修改死亡/退出）。
func settleFixture() State {
	return State{
		RoomID:       "SETTLE1",
		GameID:       "g-1",
		Phase:        PhaseSettlement,
		PhaseVersion: 9,
		Players:      sixLiveFixture(),
		Lobby:        LobbyState{Owner: 1, Config: DefaultCreateRoomConfig()},
		Settled:      SettledState{Winner: CampWolf},
		Settings:     DefaultRoomSettings(),
		Processed:    map[string]bool{},
	}
}

func markMalicious(players []Player, seat Seat) []Player {
	out := append([]Player(nil), players...)
	for i := range out {
		if out[i].Seat == seat {
			out[i].MaliciousExit = true
			out[i].Left = true
		}
	}
	return out
}

func findPersistSettlement(effects []Effect) (PersistSettlementEffect, bool) {
	for _, e := range effects {
		if pe, ok := e.(PersistSettlementEffect); ok {
			return pe, true
		}
	}
	return PersistSettlementEffect{}, false
}

func findMessage(effects []Effect, key string) (MessageEffect, bool) {
	for _, e := range effects {
		if m, ok := e.(MessageEffect); ok && m.Key == key {
			return m, true
		}
	}
	return MessageEffect{}, false
}

// TestSettleRevealsAllRolesAndScores 验证结算：全员身份翻牌与五分制积分
// （胜 +5 / 死亡躺赢 +2 / 失败 0 / 恶意退出且胜 0 / 恶意退出且败 -5）。
func TestSettleRevealsAllRolesAndScores(t *testing.T) {
	cases := []struct {
		name    string
		players []Player
		want    map[Seat]int
	}{
		{
			"狼胜：胜+5 败0 恶意胜0 恶意败-5",
			markMalicious(withDeaths(sixLiveFixture(), 4, 5), 6),
			map[Seat]int{1: 5, 2: 5, 3: 0, 4: 0, 5: 0, 6: -5},
		},
		{
			"好胜：死亡躺赢+2 恶意胜0 败0",
			markMalicious(withDeaths(sixLiveFixture(), 1, 2, 3, 6), 6),
			map[Seat]int{1: 0, 2: 0, 3: 2, 4: 5, 5: 5, 6: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := settleFixture()
			if strings.HasPrefix(tc.name, "好胜") {
				st.Settled.Winner = CampGood
			}
			st.Players = tc.players

			after, effects, err := settle(st, nil)
			if err != nil {
				t.Fatalf("settle error = %v", err)
			}
			if len(after.Settled.Revealed) != 6 {
				t.Fatalf("Revealed len = %d, want 6", len(after.Settled.Revealed))
			}
			bySeat := map[Seat]PlayerResult{}
			for _, r := range after.Settled.Revealed {
				bySeat[r.Seat] = r
			}
			for seat, wantScore := range tc.want {
				r := bySeat[seat]
				if r.Role == RoleUnknown || r.Camp == CampUnknown {
					t.Errorf("seat %d 未翻牌: %+v", seat, r)
				}
				if r.Score != wantScore {
					t.Errorf("seat %d score = %d, want %d", seat, r.Score, wantScore)
				}
			}
			if after.PhaseVersion != st.PhaseVersion {
				t.Errorf("PhaseVersion 变了: %d -> %d", st.PhaseVersion, after.PhaseVersion)
			}

			pe, ok := findPersistSettlement(effects)
			if !ok {
				t.Fatal("缺少 PersistSettlementEffect")
			}
			if pe.Result.RoomID != st.RoomID || pe.Result.Phase != PhaseSettlement {
				t.Errorf("持久化结果房间/阶段不符: %+v", pe.Result)
			}
			if len(pe.Result.Players) != 6 {
				t.Errorf("持久化参与人数 = %d, want 6", len(pe.Result.Players))
			}

			rep, ok := findMessage(effects, SettlementReportMessageKey)
			if !ok {
				t.Fatal("缺少 settlement.report 消息 Effect")
			}
			if rep.Audience != AudiencePublic {
				t.Errorf("settlement.report 受众 = %v, want public", rep.Audience)
			}
			if rep.Params["winner"] != st.Settled.Winner {
				t.Errorf("settlement.report 胜方 = %v, want %v", rep.Params["winner"], st.Settled.Winner)
			}
		})
	}
}

// TestSettleRejectsInvalidState 验证结算前置校验：仅 PhaseSettlement 且
// 胜方已判定才可结算。
func TestSettleRejectsInvalidState(t *testing.T) {
	st := settleFixture()
	st.Phase = PhaseNight
	if _, _, err := settle(st, nil); err == nil {
		t.Error("非结算阶段 settle 应报错")
	}
	st = settleFixture()
	st.Settled.Winner = CampUnknown
	if _, _, err := settle(st, nil); err == nil {
		t.Error("胜方未判定 settle 应报错")
	}
}

// TestSettleIdempotent 验证结算幂等：已结算状态重复调用不产生重复效果。
func TestSettleIdempotent(t *testing.T) {
	st := settleFixture()
	after, effects, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	if len(effects) == 0 {
		t.Fatal("首次结算没有产生效果")
	}
	_, second, err := settle(after, nil)
	if err != nil {
		t.Fatalf("第二次 settle error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("重复结算产生了 %d 个效果，want 0", len(second))
	}
}

// TestSettleNoMVPArtifact 验证 MVP 不评：结算与战报源码不得出现 MVP 字样
// （docs §结算 8：不评 MVP，后期再考虑）。
func TestSettleNoMVPArtifact(t *testing.T) {
	for _, file := range []string{"settlement.go", "report.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取 %s: %v", file, err)
		}
		if strings.Contains(strings.ToLower(string(src)), "mvp") {
			t.Errorf("%s 中出现 MVP 字样，违反「MVP 不评」约束", file)
		}
	}
}

// TestDayExileTriggersImmediateSettlement 验证白天放逐落定后的即时结算
// （docs §结算 1：白天为投票先触发者获胜）：狼 2 已死，放逐狼 1 → 狼全灭，
// 屠城好人胜，直接进入 PhaseSettlement 并产出持久化/战报效果，
// 不进入遗言窗口或黑夜。
func TestDayExileTriggersImmediateSettlement(t *testing.T) {
	st := State{
		RoomID:       "DAYSET",
		GameID:       "g-2",
		Phase:        PhaseDayVote,
		PhaseVersion: 8,
		Players:      withDeaths(sixLiveFixture(), 2),
		Lobby:        LobbyState{Owner: 1, Config: DefaultCreateRoomConfig()},
		Vote: VoteState{
			Stage:   VoteStageOpen,
			Ballots: map[Seat]Seat{3: 1, 4: 1, 5: 1, 6: 1},
			Locked:  map[Seat]bool{3: true, 4: true, 5: true, 6: true},
		},
		Settings:  DefaultRoomSettings(), // 屠城；默认不报身份（有遗言模式也应即时结算）
		Processed: map[string]bool{},
	}
	r := NewReducer().(reducer)
	after, effects, err := r.settleVote(st, time.Now())
	if err != nil {
		t.Fatalf("settleVote error = %v", err)
	}
	if after.Phase != PhaseSettlement {
		t.Fatalf("Phase = %v, want PhaseSettlement", after.Phase)
	}
	if after.Settled.Winner != CampGood {
		t.Errorf("Winner = %v, want CampGood", after.Settled.Winner)
	}
	if _, ok := findPersistSettlement(effects); !ok {
		t.Error("白天结算缺少 PersistSettlementEffect")
	}
	if _, ok := findMessage(effects, SettlementReportMessageKey); !ok {
		t.Error("白天结算缺少 settlement.report 消息")
	}
	for _, p := range after.Players {
		if p.Seat == 1 && !p.Dead {
			t.Error("被放逐的狼 1 应标记死亡")
		}
	}
}

// TestRematchCommand 验证「再来一局」：仅房主、仅结算阶段；回等待大厅
// 保留成员与配置、≥15 秒退出窗口（RematchReadyAt = ReceivedAt + 15s）、
// 窗口内 StartGameCommand 拒绝、窗口后放行。
func TestRematchCommand(t *testing.T) {
	st := settleFixture()
	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	t0 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	meta := func(id string, actor UserID, at time.Time) CommandMeta {
		return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseSettlement,
			PhaseVersion: settled.PhaseVersion, ReceivedAt: at}
	}
	r := NewReducer()

	// 非房主拒绝。
	if _, _, err := r.Reduce(settled, RematchCommand{Meta: meta("r0", 2, t0)}); !errors.Is(err, ErrNotHost) {
		t.Fatalf("非房主 rematch err = %v, want ErrNotHost", err)
	}

	after, effects, err := r.Reduce(settled, RematchCommand{Meta: meta("r1", 1, t0)})
	if err != nil {
		t.Fatalf("rematch error = %v", err)
	}
	if after.Phase != PhaseLobby {
		t.Fatalf("rematch 后 Phase = %v, want PhaseLobby", after.Phase)
	}
	if after.PhaseVersion != settled.PhaseVersion+1 {
		t.Errorf("rematch 后 PhaseVersion = %d, want %d", after.PhaseVersion, settled.PhaseVersion+1)
	}
	wantReady := t0.Add(RematchWindow)
	if !after.Lobby.RematchReadyAt.Equal(wantReady) {
		t.Errorf("RematchReadyAt = %v, want %v（窗口 %v）", after.Lobby.RematchReadyAt, wantReady, RematchWindow)
	}
	if RematchWindow < 15*time.Second {
		t.Errorf("RematchWindow = %v，必须 ≥ 15 秒", RematchWindow)
	}
	if _, ok := findMessage(effects, RematchMessageKey); !ok {
		t.Error("rematch 缺少 lobby.rematch 消息")
	}
	if after.Lobby.Owner != 1 {
		t.Errorf("rematch 后房主改变: %d", after.Lobby.Owner)
	}
	if !reflect.DeepEqual(after.Lobby.Config, settled.Lobby.Config) {
		t.Error("rematch 后房间配置未沿用")
	}
	if !reflect.DeepEqual(after.Settings, settled.Settings) {
		t.Error("rematch 后房间设置未沿用")
	}

	// 窗口内开始拒绝。
	startMeta := func(id string, at time.Time) CommandMeta {
		return CommandMeta{ID: id, Actor: 1, ExpectedPhase: PhaseLobby,
			PhaseVersion: after.PhaseVersion, ReceivedAt: at}
	}
	if _, _, err := r.Reduce(after, StartGameCommand{Meta: startMeta("s-early", t0.Add(10*time.Second))}); !errors.Is(err, ErrRematchWindowOpen) {
		t.Fatalf("窗口内开始 err = %v, want ErrRematchWindowOpen", err)
	}
	// 窗口后放行 → 进入发牌。
	afterStart, _, err := r.Reduce(after, StartGameCommand{Meta: startMeta("s-late", t0.Add(RematchWindow+time.Second))})
	if err != nil {
		t.Fatalf("窗口后开始 error = %v", err)
	}
	if afterStart.Phase != PhaseDeal {
		t.Errorf("窗口后开始 Phase = %v, want PhaseDeal", afterStart.Phase)
	}
}

// TestRematchResetsGameDataKeepsMembers 验证回大厅：未缺席成员保留且
// Dead 复位，缺席（Left）玩家保持缺席，对局数据复位。
func TestRematchResetsGameDataKeepsMembers(t *testing.T) {
	st := settleFixture()
	st.Players[2].Dead = true // 预言 3 死亡（房主 1 必须存活才能点击再来一局）
	st.Players[5].Left = true // 民 6 缺席
	st.Players[5].MaliciousExit = true
	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	t0 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	after, _, err := NewReducer().Reduce(settled, RematchCommand{Meta: CommandMeta{
		ID: "r2", Actor: 1, ExpectedPhase: PhaseSettlement,
		PhaseVersion: settled.PhaseVersion, ReceivedAt: t0}})
	if err != nil {
		t.Fatalf("rematch error = %v", err)
	}
	if len(after.Players) != 5 {
		t.Fatalf("成员数 = %d, want 5（缺席者不保留）", len(after.Players))
	}
	for _, p := range after.Players {
		if p.Seat == 3 && p.Dead {
			t.Error("死亡玩家回大厅后 Dead 未复位")
		}
		if p.Seat == 6 {
			t.Error("缺席玩家不应保留在回大厅成员中")
		}
	}
	if !reflect.DeepEqual(after.Deal, DealState{}) || !reflect.DeepEqual(after.Night, NightState{}) ||
		!reflect.DeepEqual(after.Day, DayState{}) || !reflect.DeepEqual(after.Vote, VoteState{}) ||
		!reflect.DeepEqual(after.Settled, SettledState{}) || !reflect.DeepEqual(after.Governance, GovernanceState{}) {
		t.Error("回大厅后对局数据未复位")
	}
}

// TestRematchAllowedForDeadHost 验证本局死亡但仍在房间的房主仍可发起
// 「再来一局」（docs §结算 5/6：房主控制不因死亡失效；死亡只约束游戏内
// 操作）：再来一局是结算阶段的大厅控制操作，不做存活校验。
func TestRematchAllowedForDeadHost(t *testing.T) {
	st := settleFixture()
	st.Players[0].Dead = true // 房主 1（狼 1）本局死亡
	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	t0 := time.Date(2026, 8, 16, 12, 30, 0, 0, time.UTC)
	after, _, err := NewReducer().Reduce(settled, RematchCommand{Meta: CommandMeta{
		ID: "r-dead-host", Actor: 1, ExpectedPhase: PhaseSettlement,
		PhaseVersion: settled.PhaseVersion, ReceivedAt: t0}})
	if err != nil {
		t.Fatalf("死亡房主 rematch error = %v", err)
	}
	if after.Phase != PhaseLobby {
		t.Fatalf("Phase = %v, want PhaseLobby", after.Phase)
	}
	for _, p := range after.Players {
		if p.Seat == 1 && p.Dead {
			t.Error("回大厅后房主 Dead 未复位")
		}
	}
}

// TestRematchWindowBoundary 验证退出窗口边界：开始命令 ReceivedAt 恰好
// 等于 RematchReadyAt 时放行（窗口「至少 15 秒」下限，docs §结算 6）。
func TestRematchWindowBoundary(t *testing.T) {
	st := settleFixture()
	settled, _, err := settle(st, nil)
	if err != nil {
		t.Fatalf("settle error = %v", err)
	}
	t0 := time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC)
	r := NewReducer()
	after, _, err := r.Reduce(settled, RematchCommand{Meta: CommandMeta{
		ID: "rb1", Actor: 1, ExpectedPhase: PhaseSettlement,
		PhaseVersion: settled.PhaseVersion, ReceivedAt: t0}})
	if err != nil {
		t.Fatalf("rematch error = %v", err)
	}
	start, _, err := r.Reduce(after, StartGameCommand{Meta: CommandMeta{
		ID: "rb2", Actor: 1, ExpectedPhase: PhaseLobby,
		PhaseVersion: after.PhaseVersion, ReceivedAt: t0.Add(RematchWindow)}})
	if err != nil {
		t.Fatalf("窗口恰好结束时开始 error = %v", err)
	}
	if start.Phase != PhaseDeal {
		t.Errorf("Phase = %v, want PhaseDeal", start.Phase)
	}
}
