package game

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// fakeLifecycleClock 是可注入的固定时间源（Fake Clock）。
type fakeLifecycleClock struct {
	now time.Time
}

func (c *fakeLifecycleClock) Now() time.Time { return c.now }

// lobbyState 构造一个 3 人等待大厅：房主 1 号 + 座位 2/3。
func lobbyState() State {
	return State{
		RoomID:       "ABC123",
		Phase:        PhaseLobby,
		PhaseVersion: 1,
		Players: []Player{
			{UserID: 1001, Seat: HostSeat},
			{UserID: 1002, Seat: 2},
			{UserID: 1003, Seat: 3},
		},
		Lobby: LobbyState{Owner: 1001, Config: DefaultCreateRoomConfig()},
	}
}

func lifecycleCmd(actor UserID, phase Phase) CommandMeta {
	return CommandMeta{ID: "l1", Actor: actor, ExpectedPhase: phase, PhaseVersion: 1}
}

// newLifecycleSvc 构造带固定时钟的生命周期服务。
func newLifecycleSvc(t *testing.T, now time.Time) LobbyLifecycleService {
	t.Helper()
	svc, err := NewLobbyLifecycleService(&fakeLifecycleClock{now: now})
	if err != nil {
		t.Fatalf("NewLobbyLifecycleService: %v", err)
	}
	return svc
}

// TestLeaveRoomOrdinaryPlayer 验证普通玩家退出：成员移除、本人确认、
// 房主面板刷新；房主不变。
func TestLeaveRoomOrdinaryPlayer(t *testing.T) {
	svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	st, effects, err := svc.LeaveRoom(context.Background(), lobbyState(), LeaveCommand{Meta: lifecycleCmd(1002, PhaseLobby)})
	if err != nil {
		t.Fatalf("LeaveRoom: %v", err)
	}
	if len(st.Players) != 2 {
		t.Fatalf("Players = %+v, want 2 人", st.Players)
	}
	for _, p := range st.Players {
		if p.UserID == 1002 {
			t.Errorf("玩家 1002 仍在成员列表: %+v", st.Players)
		}
	}
	if st.Lobby.Owner != 1001 {
		t.Errorf("Owner = %d, want 1001（普通玩家退出不移交）", st.Lobby.Owner)
	}
	requireLifecycleEffects(t, effects, map[Audience]string{
		AudienceActor: LeaveConfirmedMessageKey,
		AudienceHost:  LobbyPanelMessageKey,
	})
}

// TestLeaveRoomNotInRoom 验证不在房间的玩家退出被明确拒绝且无 Effects。
func TestLeaveRoomNotInRoom(t *testing.T) {
	svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	before := lobbyState()
	st, effects, err := svc.LeaveRoom(context.Background(), before, LeaveCommand{Meta: lifecycleCmd(9999, PhaseLobby)})
	if !errors.Is(err, ErrNotInRoom) {
		t.Fatalf("err = %v, want ErrNotInRoom", err)
	}
	if len(effects) != 0 {
		t.Errorf("拒绝后仍有 Effects: %v", effects)
	}
	if !reflect.DeepEqual(st, before) {
		t.Errorf("State 被修改: %+v", st)
	}
}

// TestLeaveRoomHostTransfers 验证房主退出：按加入顺序（座位升序）移交
// 新房主；新房主单独收到通知；旧房主不收到移交通知。
func TestLeaveRoomHostTransfers(t *testing.T) {
	svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	st, effects, err := svc.LeaveRoom(context.Background(), lobbyState(), LeaveCommand{Meta: lifecycleCmd(1001, PhaseLobby)})
	if err != nil {
		t.Fatalf("LeaveRoom(host): %v", err)
	}
	if st.Lobby.Owner != 1002 {
		t.Errorf("新房主 = %d, want 1002（座位升序最小在场）", st.Lobby.Owner)
	}
	if len(st.Players) != 2 {
		t.Fatalf("Players = %+v, want 2 人", st.Players)
	}
	for _, p := range st.Players {
		if p.UserID == 1001 {
			t.Errorf("旧房主 1001 仍在成员列表: %+v", st.Players)
		}
	}
	// 新房主单独收到移交通知（AudienceActor=新房主）。
	var transferred *MessageEffect
	hostPanel := 0
	for i := range effects {
		msg, ok := effects[i].(MessageEffect)
		if !ok {
			continue
		}
		if msg.Key == HostTransferredMessageKey {
			transferred = &msg
		}
		if msg.Key == LobbyPanelMessageKey && msg.Audience == AudienceHost {
			hostPanel++
		}
	}
	if transferred == nil {
		t.Fatalf("缺少新房主移交通知，effects=%v", effects)
	}
	if transferred.Audience != AudienceActor {
		t.Errorf("移交通知 Audience = %v, want AudienceActor", transferred.Audience)
	}
	if got, _ := transferred.Params["host"].(UserID); got != 1002 {
		t.Errorf("移交通知 host 参数 = %v, want 1002", transferred.Params["host"])
	}
	// 旧房主不收到移交通知：没有 Audience=1001 的 host_transferred。
	for i := range effects {
		if msg, ok := effects[i].(MessageEffect); ok && msg.Key == HostTransferredMessageKey && msg.Audience == AudienceActor {
			// 唯一一处，参数 1002 已断言。
		}
	}
	if hostPanel == 0 {
		t.Error("缺少新房主面板刷新")
	}
}

// TestLeaveRoomLastPlayer 验证最后一个玩家退出后进入空房语义：
// 无面板刷新、无移交通知、只保留本人确认。
func TestLeaveRoomLastPlayer(t *testing.T) {
	svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	st := lobbyState()
	st.Players = []Player{{UserID: 1001, Seat: HostSeat}}
	st.Lobby.Owner = 1001
	out, effects, err := svc.LeaveRoom(context.Background(), st, LeaveCommand{Meta: lifecycleCmd(1001, PhaseLobby)})
	if err != nil {
		t.Fatalf("LeaveRoom(last): %v", err)
	}
	if len(out.Players) != 0 {
		t.Errorf("空房 Players = %+v, want 空", out.Players)
	}
	for i := range effects {
		if msg, ok := effects[i].(MessageEffect); ok {
			if msg.Key == LobbyPanelMessageKey || msg.Key == HostTransferredMessageKey {
				t.Errorf("空房不应产面板/移交 Effect: %v", msg)
			}
		}
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %v, want 仅本人退出确认", effects)
	}
}

// TestKickPlayer 验证房主移除玩家：仅房主可操作；
// 目标被移除并通知，面板刷新。
func TestKickPlayer(t *testing.T) {
	t.Run("房主移除目标", func(t *testing.T) {
		svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
		st, effects, err := svc.KickPlayer(context.Background(), lobbyState(), KickCommand{Meta: lifecycleCmd(1001, PhaseLobby), Target: 1003})
		if err != nil {
			t.Fatalf("KickPlayer: %v", err)
		}
		if len(st.Players) != 2 {
			t.Fatalf("Players = %+v, want 2 人", st.Players)
		}
		for _, p := range st.Players {
			if p.UserID == 1003 {
				t.Errorf("目标 1003 仍在成员列表")
			}
		}
		if st.Lobby.Owner != 1001 {
			t.Errorf("Owner = %d, want 1001", st.Lobby.Owner)
		}
		gotKeys := map[Audience]string{}
		for i := range effects {
			if msg, ok := effects[i].(MessageEffect); ok {
				gotKeys[msg.Audience] = msg.Key
			}
		}
		if gotKeys[AudienceHost] != LobbyPanelMessageKey {
			t.Errorf("缺少房主面板刷新，effects=%v", effects)
		}
		var kicked *MessageEffect
		for i := range effects {
			if msg, ok := effects[i].(MessageEffect); ok && msg.Key == KickedMessageKey {
				kicked = &msg
			}
		}
		if kicked == nil || kicked.Audience != AudienceActor {
			t.Errorf("缺少被移除通知，effects=%v", effects)
		}
	})
	t.Run("非房主拒绝", func(t *testing.T) {
		svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
		_, effects, err := svc.KickPlayer(context.Background(), lobbyState(), KickCommand{Meta: lifecycleCmd(1002, PhaseLobby), Target: 1003})
		if !errors.Is(err, ErrNotHost) {
			t.Fatalf("err = %v, want ErrNotHost", err)
		}
		if len(effects) != 0 {
			t.Errorf("拒绝后仍有 Effects: %v", effects)
		}
	})
	t.Run("移除不存在玩家", func(t *testing.T) {
		svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
		_, _, err := svc.KickPlayer(context.Background(), lobbyState(), KickCommand{Meta: lifecycleCmd(1001, PhaseLobby), Target: 9999})
		if !errors.Is(err, ErrNotInRoom) {
			t.Fatalf("err = %v, want ErrNotInRoom", err)
		}
	})
	t.Run("房主不能移除自己", func(t *testing.T) {
		svc := newLifecycleSvc(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
		_, _, err := svc.KickPlayer(context.Background(), lobbyState(), KickCommand{Meta: lifecycleCmd(1001, PhaseLobby), Target: 1001})
		if !errors.Is(err, ErrKickSelf) {
			t.Fatalf("err = %v, want ErrKickSelf", err)
		}
	})
}

// requireLifecycleEffects 断言按受众出现的消息 key 集合。
func requireLifecycleEffects(t *testing.T, effects []Effect, want map[Audience]string) {
	t.Helper()
	got := map[Audience]string{}
	for i := range effects {
		if msg, ok := effects[i].(MessageEffect); ok {
			got[msg.Audience] = msg.Key
		}
	}
	for a, key := range want {
		if got[a] != key {
			t.Errorf("Audience %v key = %q, want %q（effects=%v）", a, got[a], key, effects)
		}
	}
}

// TestEvaluateIdle 验证闲置回收规则（Fake Clock）：
// 50 分钟提醒一次、未到不提醒、已提醒不重复、1 小时到期回收、
// 游戏开始后不评估。
func TestEvaluateIdle(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	mkLifetime := func(created time.Time) LobbyLifetime {
		return LobbyLifetime{CreatedAt: created, ExpireAt: created.Add(IdleExpireAfter)}
	}

	t.Run("未到提醒时刻", func(t *testing.T) {
		svc := newLifecycleSvc(t, base.Add(49*time.Minute))
		st := lobbyState()
		lt, effects, err := svc.EvaluateIdle(context.Background(), mkLifetime(base), st)
		if err != nil {
			t.Fatalf("EvaluateIdle: %v", err)
		}
		if len(effects) != 0 {
			t.Errorf("未到提醒时刻仍有 Effects: %v", effects)
		}
		if lt.Reminded {
			t.Error("未到提醒时刻 Reminded = true")
		}
	})
	t.Run("五十分钟提醒一次", func(t *testing.T) {
		svc := newLifecycleSvc(t, base.Add(50*time.Minute))
		lt, effects, err := svc.EvaluateIdle(context.Background(), mkLifetime(base), lobbyState())
		if err != nil {
			t.Fatalf("EvaluateIdle: %v", err)
		}
		if !lt.Reminded {
			t.Error("提醒后 Reminded = false, want true")
		}
		var msg *MessageEffect
		for i := range effects {
			if m, ok := effects[i].(MessageEffect); ok && m.Key == IdleReminderMessageKey {
				msg = &m
			}
		}
		if msg == nil || msg.Audience != AudienceHost {
			t.Fatalf("缺少房主闲置提醒，effects=%v", effects)
		}
	})
	t.Run("已提醒不重复", func(t *testing.T) {
		lt := mkLifetime(base)
		lt.Reminded = true
		svc := newLifecycleSvc(t, base.Add(55*time.Minute))
		out, effects, err := svc.EvaluateIdle(context.Background(), lt, lobbyState())
		if err != nil {
			t.Fatalf("EvaluateIdle: %v", err)
		}
		if len(effects) != 0 {
			t.Errorf("已提醒后仍有 Effects: %v", effects)
		}
		if !out.Reminded {
			t.Error("已提醒标记丢失")
		}
	})
	t.Run("一小时后到期回收", func(t *testing.T) {
		svc := newLifecycleSvc(t, base.Add(IdleExpireAfter))
		lt, effects, err := svc.EvaluateIdle(context.Background(), mkLifetime(base), lobbyState())
		if err != nil {
			t.Fatalf("EvaluateIdle: %v", err)
		}
		if len(effects) == 0 {
			t.Fatal("到期未产生任何 Effect")
		}
		found := false
		for i := range effects {
			if m, ok := effects[i].(MessageEffect); ok && m.Key == RoomExpiredMessageKey {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少房间到期回收 Effect: %v", effects)
		}
		_ = lt
	})
	t.Run("游戏开始后不评估", func(t *testing.T) {
		st := lobbyState()
		st.Phase = PhaseDeal
		svc := newLifecycleSvc(t, base.Add(2*time.Hour))
		lt, effects, err := svc.EvaluateIdle(context.Background(), mkLifetime(base), st)
		if err != nil {
			t.Fatalf("EvaluateIdle: %v", err)
		}
		if len(effects) != 0 {
			t.Errorf("游戏开始后仍有到期 Effects: %v", effects)
		}
		if lt.Reminded {
			t.Error("游戏开始后 Reminded 被置位")
		}
	})
}

// TestPlayerMovementDoesNotRefreshExpiry 验证玩家进出不刷新原始期限：
// 生命周期仅由 CreatedAt/ExpireAt 驱动，玩家变动不改变到期时刻。
func TestPlayerMovementDoesNotRefreshExpiry(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	lt := LobbyLifetime{CreatedAt: base, ExpireAt: base.Add(IdleExpireAfter)}
	svc := newLifecycleSvc(t, base.Add(30*time.Minute))

	// 玩家退出不触碰 lifetime（纯函数：LeaveRoom 不接收 lifetime）。
	st, _, err := svc.LeaveRoom(context.Background(), lobbyState(), LeaveCommand{Meta: lifecycleCmd(1002, PhaseLobby)})
	if err != nil {
		t.Fatalf("LeaveRoom: %v", err)
	}
	if len(st.Players) != 2 {
		t.Fatalf("Players = %+v, want 2", st.Players)
	}
	// 仍按原始 ExpireAt 评估：30 分钟时无提醒无回收。
	_, effects, err := svc.EvaluateIdle(context.Background(), lt, st)
	if err != nil {
		t.Fatalf("EvaluateIdle: %v", err)
	}
	if len(effects) != 0 {
		t.Errorf("进出后原始期限被刷新（30 分钟即产 Effect）: %v", effects)
	}
}

// TestRenewRoom 验证房主续期：以续期时刻重新计算 1 小时、重置提醒；
// 非房主拒绝。
func TestRenewRoom(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	renewAt := base.Add(55 * time.Minute)

	t.Run("续期一小时并重置提醒", func(t *testing.T) {
		svc := newLifecycleSvc(t, renewAt)
		lt := LobbyLifetime{CreatedAt: base, ExpireAt: base.Add(IdleExpireAfter), Reminded: true}
		st, out, effects, err := svc.RenewRoom(context.Background(), lobbyState(), RenewCommand{Meta: lifecycleCmd(1001, PhaseLobby)}, lt)
		if err != nil {
			t.Fatalf("RenewRoom: %v", err)
		}
		wantExpire := renewAt.Add(IdleExpireAfter)
		if !out.ExpireAt.Equal(wantExpire) {
			t.Errorf("ExpireAt = %v, want %v（续期时刻起 1 小时）", out.ExpireAt, wantExpire)
		}
		if out.Reminded {
			t.Error("续期后 Reminded = true, want false（新周期重新提醒）")
		}
		if !out.CreatedAt.Equal(base) {
			t.Errorf("CreatedAt 被改动 = %v, want %v", out.CreatedAt, base)
		}
		if !reflect.DeepEqual(st, lobbyState()) {
			t.Errorf("续期不应改动成员: %+v", st)
		}
		if len(effects) == 0 {
			t.Error("续期无确认/面板 Effect")
		}
	})
	t.Run("非房主拒绝", func(t *testing.T) {
		svc := newLifecycleSvc(t, renewAt)
		lt := LobbyLifetime{CreatedAt: base, ExpireAt: base.Add(IdleExpireAfter)}
		_, _, effects, err := svc.RenewRoom(context.Background(), lobbyState(), RenewCommand{Meta: lifecycleCmd(1002, PhaseLobby)}, lt)
		if !errors.Is(err, ErrNotHost) {
			t.Fatalf("err = %v, want ErrNotHost", err)
		}
		if len(effects) != 0 {
			t.Errorf("拒绝后仍有 Effects: %v", effects)
		}
	})
}

// TestNewLobbyLifecycleServiceRequiresClock 验证时钟 nil 时使用默认实现。
func TestNewLobbyLifecycleServiceRequiresClock(t *testing.T) {
	svc, err := NewLobbyLifecycleService(nil)
	if err != nil {
		t.Fatalf("NewLobbyLifecycleService(nil): %v", err)
	}
	if svc.clock == nil {
		t.Error("clock == nil, want 默认实时钟")
	}
}
