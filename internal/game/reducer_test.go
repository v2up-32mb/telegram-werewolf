package game

import (
	"errors"
	"reflect"
	"testing"
)

// unknownCommand 是未注册的命令类型，用于验证 ErrUnknownCommand。
type unknownCommand struct{}

func (unknownCommand) command() {}

// rejectionFixture 构造处于 Night 阶段的 6 人合法状态：
// 3 号玩家已死亡，Processed 中已有 "dup-1"。
func rejectionFixture() State {
	players := []Player{
		{UserID: 1, Seat: 1, Role: RoleWolf},
		{UserID: 2, Seat: 2, Role: RoleWolf},
		{UserID: 3, Seat: 3, Role: RoleSeer, Dead: true},
		{UserID: 4, Seat: 4, Role: RoleWitch},
		{UserID: 5, Seat: 5, Role: RoleVillager},
		{UserID: 6, Seat: 6, Role: RoleVillager},
	}
	return State{
		RoomID:       "ABCDEF",
		GameID:       "g-1",
		Phase:        PhaseNight,
		PhaseVersion: 2,
		Players:      players,
		Night:        NightState{SeerChecked: map[Seat]bool{}},
		Processed:    map[string]bool{"dup-1": true},
	}
}

// cmdMeta 构造一个基础 Meta，便于表格测试覆盖单个拒绝维度。
func cmdMeta(id string, actor UserID, phase Phase, version uint64) CommandMeta {
	return CommandMeta{ID: id, Actor: actor, ExpectedPhase: phase, PhaseVersion: version}
}

// TestReducerRejects 表格驱动覆盖通用拒绝规则；每种拒绝都必须是
// 哨兵错误且不得部分修改 State（docs/技术选型.md §13.1）。
func TestReducerRejects(t *testing.T) {
	deadSeat := Seat(3)
	cases := []struct {
		name string
		cmd  Command
		want error
	}{
		{"错误阶段",
			WolfKillCommand{Meta: cmdMeta("w1", 1, PhaseDayVote, 2), Target: 2},
			ErrWrongPhase},
		{"错误 phaseVersion",
			WolfKillCommand{Meta: cmdMeta("w1", 1, PhaseNight, 1), Target: 2},
			ErrStalePhaseVersion},
		{"非房间玩家",
			WolfKillCommand{Meta: cmdMeta("w1", 999, PhaseNight, 2), Target: 2},
			ErrNotInRoom},
		{"死亡玩家",
			WolfKillCommand{Meta: cmdMeta("w1", 3, PhaseNight, 2), Target: 2},
			ErrDeadPlayer},
		{"重复 Command",
			WolfKillCommand{Meta: cmdMeta("dup-1", 1, PhaseNight, 2), Target: 2},
			ErrDuplicateCommand},
		{"非法目标越界",
			WolfKillCommand{Meta: cmdMeta("w1", 1, PhaseNight, 2), Target: 7},
			ErrInvalidTarget},
		{"非法目标不在房间",
			WolfKillCommand{Meta: cmdMeta("w1", 1, PhaseNight, 2), Target: 0},
			ErrInvalidTarget},
		{"查验死亡目标",
			SeerCheckCommand{Meta: cmdMeta("s1", 4, PhaseNight, 2), Target: 3},
			ErrInvalidTarget},
		{"投票死亡目标",
			VoteCommand{Meta: cmdMeta("v1", 5, PhaseNight, 2), Target: &deadSeat},
			ErrInvalidTarget},
		{"未知命令类型",
			unknownCommand{},
			ErrUnknownCommand},
	}
	r := NewReducer()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := rejectionFixture()
			after, _, err := r.Reduce(before, tc.cmd)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Reduce error = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(after, before) {
				t.Errorf("拒绝命令后 State 被部分修改:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// TestReducerDispatch 验证通过 validator 的合法命令进入分派：
// 骨架阶段返回明确未实现错误且状态不变。
func TestReducerDispatch(t *testing.T) {
	r := NewReducer()

	t.Run("合法夜间命令进入分派", func(t *testing.T) {
		st := rejectionFixture()
		cmd := WolfKillCommand{Meta: cmdMeta("w-ok", 1, PhaseNight, 2), Target: 2}
		after, effects, err := r.Reduce(st, cmd)
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("Reduce error = %v, want ErrNotImplemented", err)
		}
		if len(effects) != 0 {
			t.Errorf("effects = %v, want empty", effects)
		}
		if !reflect.DeepEqual(after, st) {
			t.Error("分派失败不应修改 State")
		}
	})

	t.Run("Timeout 系统命令豁免在场校验", func(t *testing.T) {
		st := rejectionFixture()
		cmd := TimeoutCommand{Meta: cmdMeta("t-ok", 0, PhaseNight, 2)}
		after, _, err := r.Reduce(st, cmd)
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("Timeout Reduce error = %v, want ErrNotImplemented", err)
		}
		if !reflect.DeepEqual(after, st) {
			t.Error("Timeout 分派失败不应修改 State")
		}
	})

	t.Run("Lobby 创建命令无需已在 Players", func(t *testing.T) {
		st := State{
			RoomID:       "ABCDEF",
			Phase:        PhaseLobby,
			PhaseVersion: 1,
			Lobby:        LobbyState{Owner: 1},
		}
		cmd := CreateRoomCommand{
			Meta:   cmdMeta("c-ok", 1, PhaseLobby, 1),
			Config: GameConfig{PlayerCount: 6, Roles: StandardDeck(), Victory: VictorySlaughter},
		}
		after, _, err := r.Reduce(st, cmd)
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("CreateRoom Reduce error = %v, want ErrNotImplemented", err)
		}
		if !reflect.DeepEqual(after, st) {
			t.Error("CreateRoom 分派失败不应修改 State")
		}
	})
}
