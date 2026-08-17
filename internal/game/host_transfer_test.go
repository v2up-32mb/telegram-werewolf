package game

// I6 红测：游戏内房主不在/掉线 → 按加入顺序自动移交房主给下一位在场玩家，
// 仅通知新房主（docs 游戏流程设计.md §房主移交）。

import (
	"testing"
	"time"
)

// hostTransferState 构造进行中夜间状态：房主 1 号存活，其余在场。
func hostTransferState() State {
	return State{
		RoomID: "HT01", Phase: PhaseNight, PhaseVersion: 1,
		Players: []Player{
			{UserID: 1, Seat: 1, Role: RoleWolf},
			{UserID: 2, Seat: 2, Role: RoleWolf},
			{UserID: 3, Seat: 3, Role: RoleSeer},
			{UserID: 4, Seat: 4, Role: RoleWitch},
			{UserID: 5, Seat: 5, Role: RoleVillager},
			{UserID: 6, Seat: 6, Role: RoleVillager},
		},
		Lobby:     LobbyState{Owner: 1, Config: DefaultCreateRoomConfig()},
		Settings:  DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
}

func TestHostTransfersOnInGameLeave(t *testing.T) {
	rd := NewReducer()
	st := hostTransferState()
	// 房主 1 号游戏内主动退出（夜间 → 恶意退出死亡）。
	cmd := LeaveGameCommand{Meta: CommandMeta{
		ID: "l1", Actor: 1, ExpectedPhase: PhaseNight, PhaseVersion: 1, ReceivedAt: nowForTest(),
	}}
	next, fx, err := rd.Reduce(st, cmd)
	if err != nil {
		t.Fatalf("leaveGame: %v", err)
	}
	if next.Lobby.Owner != 2 {
		t.Fatalf("新房主 = %d, want 2（按加入顺序下一位在场玩家）", next.Lobby.Owner)
	}
	// 仅通知新房主（AudienceActor → 新 host）。
	foundTransfer := false
	for _, e := range fx {
		if me, ok := e.(MessageEffect); ok && me.Key == HostTransferredMessageKey {
			if me.Audience != AudienceActor {
				t.Fatalf("移交通知受众 = %v, want Actor（仅通知新房主）", me.Audience)
			}
			if u, ok := me.Params["host"]; !ok || u.(UserID) != 2 {
				t.Fatalf("移交通知 host 参数 = %v, want 2", u)
			}
			foundTransfer = true
		}
	}
	if !foundTransfer {
		t.Fatal("房主游戏内退出未产出新房主移交通知（lobby.host_transferred）")
	}
}

func TestHostNotTransferredWhenNoAliveRemains(t *testing.T) {
	rd := NewReducer()
	st := hostTransferState()
	// 除房主外全部死亡。
	for i := 2; i <= 6; i++ {
		st.Players[i-1].Dead = true
	}
	next, fx, err := rd.Reduce(st, LeaveGameCommand{Meta: CommandMeta{
		ID: "l2", Actor: 1, ExpectedPhase: PhaseNight, PhaseVersion: 1, ReceivedAt: nowForTest(),
	}})
	if err != nil {
		t.Fatalf("leaveGame: %v", err)
	}
	if next.Lobby.Owner != 1 {
		t.Fatalf("无存活玩家时房主不应移交：Owner = %d, want 1", next.Lobby.Owner)
	}
	for _, e := range fx {
		if me, ok := e.(MessageEffect); ok && me.Key == HostTransferredMessageKey {
			t.Fatal("无存活玩家时不应产出移交通知")
		}
	}
}

func TestHostTransfersWhenNonHostLeaves(t *testing.T) {
	rd := NewReducer()
	st := hostTransferState()
	next, fx, err := rd.Reduce(st, LeaveGameCommand{Meta: CommandMeta{
		ID: "l3", Actor: 3, ExpectedPhase: PhaseNight, PhaseVersion: 1, ReceivedAt: nowForTest(),
	}})
	if err != nil {
		t.Fatalf("leaveGame: %v", err)
	}
	if next.Lobby.Owner != 1 {
		t.Fatalf("非房主退出不应移交房主：Owner = %d, want 1", next.Lobby.Owner)
	}
	for _, e := range fx {
		if me, ok := e.(MessageEffect); ok && me.Key == HostTransferredMessageKey {
			t.Fatal("非房主退出不应产出移交通知")
		}
	}
}

// nowForTest 返回测试时间。
func nowForTest() time.Time { return time.Unix(1700000000, 0) }
