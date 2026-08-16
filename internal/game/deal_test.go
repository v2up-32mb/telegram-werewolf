package game

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// dealLobbyFixture 构造满员 6 人等待大厅状态（房主 1 号，座位 1～6
// 已按加入顺序占位，角色未分配）。
func dealLobbyFixture() State {
	players := make([]Player, 0, MVPPlayerCount)
	for i := 1; i <= MVPPlayerCount; i++ {
		players = append(players, Player{UserID: UserID(i), Seat: Seat(i)})
	}
	return State{
		RoomID:       "DEAL01",
		Phase:        PhaseLobby,
		PhaseVersion: 1,
		Players:      players,
		Lobby: LobbyState{
			Owner:  1,
			Config: DefaultCreateRoomConfig(),
		},
		Processed: map[string]bool{},
	}
}

// lobbyMeta 构造 PhaseLobby/v1 的命令 Meta（开始游戏）。
func lobbyMeta(id string, actor UserID) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseLobby, PhaseVersion: 1}
}

// dealMeta 构造 PhaseDeal/v2 的命令 Meta（确认角色与超时）。
func dealMeta(id string, actor UserID) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: PhaseDeal, PhaseVersion: 2}
}

// dealSeed 是确定性的 Fisher–Yates 洗牌序列（与 deck_test 同款语义）。
var dealSeed = []int{3, 1, 0, 2, 4}

// reduceStart 使用注入 RNG 开始一局满员游戏并返回新状态。
func reduceStart(t *testing.T, r Reducer) State {
	t.Helper()
	after, _, err := r.Reduce(dealLobbyFixture(), StartGameCommand{Meta: lobbyMeta("start-1", 1)})
	if err != nil {
		t.Fatalf("StartGame error = %v, want nil", err)
	}
	return after
}

// playerAtSeat 返回指定座位的玩家；不存在返回零值 Player。
func playerAtSeat(players []Player, seat Seat) Player {
	for _, p := range players {
		if p.Seat == seat {
			return p
		}
	}
	return Player{}
}

// playersRoles 返回所有玩家的角色列表（顺序与 Players 一致）。
func playersRoles(players []Player) []Role {
	out := make([]Role, len(players))
	for i, p := range players {
		out[i] = p.Role
	}
	return out
}

// wantRoleCounts 是 MVP 标准牌组的构成。
func wantRoleCounts() map[Role]int {
	return map[Role]int{
		RoleWolf:     MVPWolfCount,
		RoleSeer:     MVPSeerCount,
		RoleWitch:    MVPWitchCount,
		RoleVillager: MVPVillagerCount,
	}
}

// assertStateUnchanged 断言拒绝命令后 State 与 Effect 均未变化。
func assertStateUnchanged(t *testing.T, before, after State, effects []Effect) {
	t.Helper()
	if len(effects) != 0 {
		t.Errorf("effects = %v, want empty", effects)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("拒绝命令后 State 被部分修改:\n got %+v\nwant %+v", after, before)
	}
}

// TestDealStartRejectsNotFull 验证非满员（5 人）开始被拒（ErrRoomNotFull），
// 状态与 Effects 均不变。
func TestDealStartRejectsNotFull(t *testing.T) {
	st := dealLobbyFixture()
	st.Players = st.Players[:5]
	before := st
	after, effects, err := NewReducer().Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 1)})
	if !errors.Is(err, ErrRoomNotFull) {
		t.Fatalf("Reduce error = %v, want ErrRoomNotFull", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestDealStartRejectsNotHost 验证非房主开始被拒（ErrNotHost）。
func TestDealStartRejectsNotHost(t *testing.T) {
	st := dealLobbyFixture()
	before := st
	after, effects, err := NewReducer().Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 2)})
	if !errors.Is(err, ErrNotHost) {
		t.Fatalf("Reduce error = %v, want ErrNotHost", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestDealStartRejectsInvalidConfig 验证非法配置（人数 5）开始被拒且
// 错误来自配置校验（start game config）。
func TestDealStartRejectsInvalidConfig(t *testing.T) {
	st := dealLobbyFixture()
	st.Lobby.Config.PlayerCount = 5
	before := st
	after, effects, err := NewReducer().Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 1)})
	if err == nil || !strings.Contains(err.Error(), "start game config") {
		t.Fatalf("Reduce error = %v, want config validation error", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestDealStartRejectsDuplicateStart 验证开始后再次开始被拒：同 ID 命中
// ErrDuplicateCommand，新 ID 命中 ErrWrongPhase（阶段已在 Deal）。
func TestDealStartRejectsDuplicateStart(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: dealSeed})
	st := reduceStart(t, r)

	after, effects, err := r.Reduce(st, StartGameCommand{Meta: lobbyMeta("start-1", 1)})
	if !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("同 ID 重复开始 error = %v, want ErrDuplicateCommand", err)
	}
	assertStateUnchanged(t, st, after, effects)

	after, effects, err = r.Reduce(st, StartGameCommand{Meta: lobbyMeta("start-2", 1)})
	if !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("阶段已到 Deal 再开始 error = %v, want ErrWrongPhase", err)
	}
	assertStateUnchanged(t, st, after, effects)
}

// TestDealStartRNGFailure 验证洗牌随机源失败时开始被拒绝且状态不变。
func TestDealStartRNGFailure(t *testing.T) {
	st := dealLobbyFixture()
	before := st
	after, effects, err := NewReducerWithRNG(&errRNG{}).Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 1)})
	if err == nil || !strings.Contains(err.Error(), "shuffle") {
		t.Fatalf("Reduce error = %v, want shuffle failure", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestDealStartOk 验证合法开始：
//   - Phase=Deal、PhaseVersion+1、DealState.Confirmed 为空、Processed 标记；
//   - 角色按座位序 = 注入 RNG 下标准牌组无偏洗牌的确定性结果；
//   - 牌组多重集不变（每人恰好一张）；
//   - 配置快照不可变（洗牌作用于副本，Lobby.Config 不被修改）；
//   - 每玩家私有角色卡 + 确认提示均为 AudienceActor 且按座位序；
//   - 狼人角色卡含 wolf_mates（另一狼人座位），非狼人不含；
//   - 不存在任何公共消息或全员身份列表泄漏；
//   - TimerEffect 为 10 秒 PhaseDeal。
func TestDealStartOk(t *testing.T) {
	st := dealLobbyFixture()

	expected := StandardDeck()
	if err := Shuffle(expected, &seqRNG{seq: dealSeed}); err != nil {
		t.Fatalf("expected shuffle: %v", err)
	}

	r := NewReducerWithRNG(&seqRNG{seq: dealSeed})
	after, effects, err := r.Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 1)})
	if err != nil {
		t.Fatalf("StartGame error = %v, want nil", err)
	}
	if after.Phase != PhaseDeal {
		t.Errorf("Phase = %v, want PhaseDeal", after.Phase)
	}
	if after.PhaseVersion != 2 {
		t.Errorf("PhaseVersion = %d, want 2（PhaseLobby→PhaseDeal +1）", after.PhaseVersion)
	}
	if len(after.Deal.Confirmed) != 0 {
		t.Errorf("Deal.Confirmed = %v, want 空", after.Deal.Confirmed)
	}
	if !after.Processed["s1"] {
		t.Error("Processed[s1] = false, want true（已受理标记）")
	}

	// 角色按座位序分配 = 确定性洗牌结果
	for i, want := range expected {
		seat := Seat(i + 1)
		if got := playerAtSeat(after.Players, seat); got.Role != want {
			t.Errorf("座位 %d 角色 = %v, want %v", seat, got.Role, want)
		}
	}
	if got, want := countRoles(playersRoles(after.Players)), wantRoleCounts(); !reflect.DeepEqual(got, want) {
		t.Errorf("发牌后角色多重集 = %v, want %v", got, want)
	}

	// 配置快照不可变：洗牌不得修改 Lobby.Config（锁定语义）
	if !reflect.DeepEqual(after.Lobby.Config, st.Lobby.Config) {
		t.Errorf("Lobby.Config 被修改:\n got %+v\nwant %+v", after.Lobby.Config, st.Lobby.Config)
	}

	// Effects：6 张角色卡 + 6 条确认提示（均 AudienceActor）+ 1 个 Timer
	if len(effects) != 13 {
		t.Fatalf("effects 数量 = %d, want 13", len(effects))
	}
	var roleCards, prompts []MessageEffect
	var timer *TimerEffect
	for _, e := range effects {
		switch e := e.(type) {
		case MessageEffect:
			if e.Audience != AudienceActor {
				t.Errorf("Effect %+v 受众 = %v, want AudienceActor（发牌无公共消息）", e, e.Audience)
			}
			switch e.Key {
			case DealRoleCardMessageKey:
				roleCards = append(roleCards, e)
			case DealConfirmPromptMessageKey:
				prompts = append(prompts, e)
			}
		case TimerEffect:
			timer = &e
		}
	}
	if len(roleCards) != 6 || len(prompts) != 6 {
		t.Fatalf("角色卡/确认提示数量 = %d/%d, want 6/6", len(roleCards), len(prompts))
	}
	for i := 0; i < 6; i++ {
		wantSeat := Seat(i + 1)
		if got, ok := roleCards[i].Params["seat"].(Seat); !ok || got != wantSeat {
			t.Errorf("角色卡[%d] seat 参数 = %v, want %d", i, roleCards[i].Params["seat"], wantSeat)
		}
		if got, ok := prompts[i].Params["seat"].(Seat); !ok || got != wantSeat {
			t.Errorf("确认提示[%d] seat 参数 = %v, want %d", i, prompts[i].Params["seat"], wantSeat)
		}
	}
	if timer == nil {
		t.Fatal("缺少 TimerEffect")
	} else if timer.Phase != PhaseDeal || timer.Duration != DealConfirmTimeout || timer.Cancel {
		t.Errorf("TimerEffect = %+v, want Phase=Deal Duration=10s Cancel=false", *timer)
	}

	// 狼人 wolf_mates：只出现在狼人角色卡，值为另一狼人座位；不泄漏全员身份
	wolves := map[Seat]bool{}
	for _, p := range after.Players {
		if p.Role == RoleWolf {
			wolves[p.Seat] = true
		}
	}
	for _, card := range roleCards {
		seat := card.Params["seat"].(Seat)
		if playerAtSeat(after.Players, seat).Role == RoleWolf {
			mates, ok := card.Params["wolf_mates"].([]Seat)
			if !ok {
				t.Fatalf("狼人座位 %d 角色卡缺少 wolf_mates 参数", seat)
			}
			wantMates := map[Seat]bool{}
			for ws := range wolves {
				if ws != seat {
					wantMates[ws] = true
				}
			}
			gotMates := map[Seat]bool{}
			for _, m := range mates {
				gotMates[m] = true
			}
			if !reflect.DeepEqual(gotMates, wantMates) {
				t.Errorf("狼人座位 %d wolf_mates = %v, want %v", seat, mates, wantMates)
			}
		} else if _, ok := card.Params["wolf_mates"]; ok {
			t.Errorf("非狼人座位 %d 角色卡不应包含 wolf_mates", seat)
		}
		for k := range card.Params {
			switch k {
			case "seat", "role", "camp", "wolf_mates":
			default:
				t.Errorf("角色卡参数出现未声明的私密键 %q", k)
			}
		}
	}
}

// TestDealConfirmAndTransition 验证发牌确认流程：
// 非最后一人确认后 Phase/版本不变、Confirmed 记录本人座位、本人收到
// deal.confirm_done；最后一人确认后：Timer Cancel、每玩家 deal.confirm_delete
// （删除在前）、AudiencePublic phase.night.start（phase_number=1）创建在后、
// Phase=Night、PhaseVersion+1。
func TestDealConfirmAndTransition(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: dealSeed})
	st := reduceStart(t, r)

	for i := 1; i <= 5; i++ {
		after, effects, err := r.Reduce(st, ConfirmRoleCommand{Meta: dealMeta(fmt.Sprintf("c%d", i), UserID(i))})
		if err != nil {
			t.Fatalf("玩家 %d 确认 error = %v, want nil", i, err)
		}
		if after.Phase != PhaseDeal || after.PhaseVersion != 2 {
			t.Errorf("玩家 %d 确认后 Phase/Version = %v/%d, want PhaseDeal/2", i, after.Phase, after.PhaseVersion)
		}
		wantConfirmed := make([]Seat, i)
		for j := 1; j <= i; j++ {
			wantConfirmed[j-1] = Seat(j)
		}
		if !reflect.DeepEqual(after.Deal.Confirmed, wantConfirmed) {
			t.Fatalf("玩家 %d 确认后 Confirmed = %v, want %v", i, after.Deal.Confirmed, wantConfirmed)
		}
		if len(effects) != 1 {
			t.Fatalf("玩家 %d 确认 effects = %v, want 仅本人 deal.confirm_done", i, effects)
		}
		done, ok := effects[0].(MessageEffect)
		if !ok || done.Key != DealConfirmDoneMessageKey || done.Audience != AudienceActor {
			t.Fatalf("玩家 %d 确认 effects[0] = %+v, want AudienceActor deal.confirm_done", i, effects[0])
		}
		if got, ok := done.Params["seat"].(Seat); !ok || got != Seat(i) {
			t.Errorf("玩家 %d confirm_done seat 参数 = %v, want %d", i, done.Params["seat"], i)
		}
		if !after.Processed[fmt.Sprintf("c%d", i)] {
			t.Errorf("玩家 %d 确认未标记 Processed", i)
		}
		st = after
	}

	after, effects, err := r.Reduce(st, ConfirmRoleCommand{Meta: dealMeta("c6", 6)})
	if err != nil {
		t.Fatalf("最后一人确认 error = %v, want nil", err)
	}
	if after.Phase != PhaseNight {
		t.Errorf("Phase = %v, want PhaseNight", after.Phase)
	}
	if after.PhaseVersion != 3 {
		t.Errorf("PhaseVersion = %d, want 3（PhaseDeal→PhaseNight +1）", after.PhaseVersion)
	}
	if !reflect.DeepEqual(after.Deal.Confirmed, []Seat{1, 2, 3, 4, 5, 6}) {
		t.Errorf("Deal.Confirmed = %v, want 全部座位", after.Deal.Confirmed)
	}
	if !after.Processed["c6"] {
		t.Error("最后确认未标记 Processed")
	}

	// 效果顺序：confirm_done(6) → Timer Cancel → 6× deal.confirm_delete → phase.night.start
	if len(effects) != 9 {
		t.Fatalf("最后确认 effects 数量 = %d, want 9", len(effects))
	}
	if done, ok := effects[0].(MessageEffect); !ok || done.Key != DealConfirmDoneMessageKey || done.Audience != AudienceActor {
		t.Errorf("effects[0] = %+v, want 本人 deal.confirm_done", effects[0])
	}
	cancel, ok := effects[1].(TimerEffect)
	if !ok || !cancel.Cancel || cancel.Phase != PhaseDeal {
		t.Errorf("effects[1] = %+v, want TimerEffect{Phase:Deal, Cancel:true}", effects[1])
	}
	for i := 0; i < 6; i++ {
		del, ok := effects[2+i].(MessageEffect)
		if !ok || del.Key != DealConfirmDeleteMessageKey || del.Audience != AudienceActor {
			t.Fatalf("effects[%d] = %+v, want AudienceActor deal.confirm_delete", 2+i, effects[2+i])
		}
		if got, ok := del.Params["seat"].(Seat); !ok || got != Seat(i+1) {
			t.Errorf("delete[%d] seat 参数 = %v, want %d", i, del.Params["seat"], i+1)
		}
	}
	night, ok := effects[8].(MessageEffect)
	if !ok || night.Key != PhaseNightStartMessageKey || night.Audience != AudiencePublic {
		t.Fatalf("effects[8] = %+v, want AudiencePublic phase.night.start", effects[8])
	}
	if got, ok := night.Params["phase_number"].(int); !ok || got != 1 {
		t.Errorf("phase.night.start phase_number = %v, want 1", night.Params["phase_number"])
	}
}

// TestDealConfirmDuplicate 验证重复确认被拒（ErrAlreadyConfirmed）且状态不变。
func TestDealConfirmDuplicate(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: dealSeed})
	st := reduceStart(t, r)
	st, _, err := r.Reduce(st, ConfirmRoleCommand{Meta: dealMeta("c1", 1)})
	if err != nil {
		t.Fatalf("首次确认 error = %v, want nil", err)
	}

	before := st
	after, effects, err := r.Reduce(st, ConfirmRoleCommand{Meta: dealMeta("c2", 1)})
	if !errors.Is(err, ErrAlreadyConfirmed) {
		t.Fatalf("重复确认 error = %v, want ErrAlreadyConfirmed", err)
	}
	assertStateUnchanged(t, before, after, effects)
}

// TestDealTimeout 验证 10 秒超时自动确认：全部座位进入 Confirmed，
// 效果顺序为 Timer Cancel → 每玩家 deal.confirm_delete → phase.night.start，
// Phase=Night、PhaseVersion+1。
func TestDealTimeout(t *testing.T) {
	r := NewReducerWithRNG(&seqRNG{seq: dealSeed})
	st := reduceStart(t, r)
	st, _, err := r.Reduce(st, ConfirmRoleCommand{Meta: dealMeta("c1", 1)})
	if err != nil {
		t.Fatalf("玩家 1 确认 error = %v, want nil", err)
	}

	after, effects, err := r.Reduce(st, TimeoutCommand{Meta: dealMeta("t1", 0)})
	if err != nil {
		t.Fatalf("Timeout error = %v, want nil", err)
	}
	if !reflect.DeepEqual(after.Deal.Confirmed, []Seat{1, 2, 3, 4, 5, 6}) {
		t.Errorf("Deal.Confirmed = %v, want 全部座位（超时自动确认）", after.Deal.Confirmed)
	}
	if after.Phase != PhaseNight || after.PhaseVersion != 3 {
		t.Errorf("Phase/Version = %v/%d, want PhaseNight/3", after.Phase, after.PhaseVersion)
	}
	if !after.Processed["t1"] {
		t.Error("Timeout 未标记 Processed")
	}

	// 效果：cancel + 6× delete + night start（无 confirm_done）
	if len(effects) != 8 {
		t.Fatalf("Timeout effects 数量 = %d, want 8", len(effects))
	}
	cancel, ok := effects[0].(TimerEffect)
	if !ok || !cancel.Cancel || cancel.Phase != PhaseDeal {
		t.Errorf("effects[0] = %+v, want TimerEffect{Phase:Deal, Cancel:true}", effects[0])
	}
	for i := 0; i < 6; i++ {
		del, ok := effects[1+i].(MessageEffect)
		if !ok || del.Key != DealConfirmDeleteMessageKey || del.Audience != AudienceActor {
			t.Fatalf("effects[%d] = %+v, want AudienceActor deal.confirm_delete", 1+i, effects[1+i])
		}
		if got, ok := del.Params["seat"].(Seat); !ok || got != Seat(i+1) {
			t.Errorf("delete[%d] seat 参数 = %v, want %d", i, del.Params["seat"], i+1)
		}
	}
	night, ok := effects[7].(MessageEffect)
	if !ok || night.Key != PhaseNightStartMessageKey || night.Audience != AudiencePublic {
		t.Fatalf("effects[7] = %+v, want AudiencePublic phase.night.start", effects[7])
	}
	_ = night
	if !sort.SliceIsSorted(after.Deal.Confirmed, func(i, j int) bool {
		return after.Deal.Confirmed[i] < after.Deal.Confirmed[j]
	}) {
		t.Error("Deal.Confirmed 应保持座位升序")
	}
}

// TestDealStartRejectsInvalidSeat 验证存在非法座位的房间开始被拒
// （发牌按座位序分配的前置防御，状态不变）。
func TestDealStartRejectsInvalidSeat(t *testing.T) {
	st := dealLobbyFixture()
	st.Players[5] = Player{UserID: 6, Seat: 0}
	before := st
	after, effects, err := NewReducer().Reduce(st, StartGameCommand{Meta: lobbyMeta("s1", 1)})
	if err == nil || !strings.Contains(err.Error(), "invalid seat") {
		t.Fatalf("Reduce error = %v, want invalid seat error", err)
	}
	assertStateUnchanged(t, before, after, effects)
}
