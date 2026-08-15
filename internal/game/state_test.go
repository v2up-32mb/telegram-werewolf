package game

import (
	"testing"
	"time"
)

// sampleState 构造一个覆盖全部可变字段的 State，供值语义测试使用。
func sampleState() State {
	seat2, seat4 := Seat(2), Seat(4)
	return State{
		RoomID:       "ABCDEF",
		GameID:       "g-1",
		Phase:        PhaseNight,
		PhaseVersion: 3,
		Players: []Player{
			{UserID: 1, Seat: 1, Role: RoleWolf},
			{UserID: 2, Seat: 2, Role: RoleSeer},
		},
		Lobby: LobbyState{
			Owner: 1,
			Config: GameConfig{
				PlayerCount: 6,
				Roles:       validRoles(),
				Victory:     VictorySlaughter,
			},
		},
		Deal: DealState{Confirmed: []Seat{1}},
		Night: NightState{
			WolfKillTarget:    &seat2,
			WitchPoisonTarget: &seat4,
			SeerChecked:       map[Seat]bool{2: true},
		},
		Day:  DayState{Speaker: 1, SpeechOrder: []Seat{1, 2}},
		Vote: VoteState{Ballots: map[Seat]Seat{1: 2}},
	}
}

// TestStateValueSemantics 验证 State 深复制/值语义：
// 修改副本的任何可变字段都不得影响原 State。
func TestStateValueSemantics(t *testing.T) {
	s := sampleState()
	c := s.Copy()

	c.Phase = PhaseDaySpeech
	c.PhaseVersion = 9
	c.Players[0].Seat = 5
	c.Lobby.Config.Victory = VictorySide
	c.Lobby.Config.Roles[0] = RoleVillager
	c.Deal.Confirmed[0] = 2
	*c.Night.WolfKillTarget = 6
	c.Night.SeerChecked[3] = true
	c.Day.SpeechOrder[0] = 2
	c.Vote.Ballots[2] = 1

	if s.Phase != PhaseNight {
		t.Errorf("原 State.Phase = %v, want night", s.Phase)
	}
	if s.PhaseVersion != 3 {
		t.Errorf("原 State.PhaseVersion = %d, want 3", s.PhaseVersion)
	}
	if s.Players[0].Seat != 1 {
		t.Errorf("原 Players[0].Seat = %d, want 1", s.Players[0].Seat)
	}
	if s.Lobby.Config.Victory != VictorySlaughter {
		t.Errorf("原 Victory = %v, want slaughter", s.Lobby.Config.Victory)
	}
	if s.Lobby.Config.Roles[0] != RoleWolf {
		t.Errorf("原 Roles[0] = %v, want wolf", s.Lobby.Config.Roles[0])
	}
	if s.Deal.Confirmed[0] != 1 {
		t.Errorf("原 Deal.Confirmed[0] = %d, want 1", s.Deal.Confirmed[0])
	}
	if got := *s.Night.WolfKillTarget; got != 2 {
		t.Errorf("原 WolfKillTarget = %d, want 2", got)
	}
	if s.Night.SeerChecked[3] {
		t.Error("修改副本后原 SeerChecked 出现 Seat(3)，值语义被破坏")
	}
	if s.Day.SpeechOrder[0] != 1 {
		t.Errorf("原 SpeechOrder[0] = %d, want 1", s.Day.SpeechOrder[0])
	}
	if _, ok := s.Vote.Ballots[2]; ok {
		t.Error("修改副本后原 Ballots 出现 Seat(2)，值语义被破坏")
	}
}

// TestStateCommandMeta 验证 CommandMeta 字段与值语义。
func TestStateCommandMeta(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	meta := CommandMeta{
		ID:            "cmd-1",
		Actor:         42,
		ExpectedPhase: PhaseNight,
		PhaseVersion:  7,
		ReceivedAt:    at,
	}
	if meta.ID != "cmd-1" || meta.Actor != 42 || meta.ExpectedPhase != PhaseNight ||
		meta.PhaseVersion != 7 || !meta.ReceivedAt.Equal(at) {
		t.Errorf("CommandMeta 字段不完整: %+v", meta)
	}
	cp := meta
	cp.PhaseVersion = 8
	cp.Actor = 1
	if meta.PhaseVersion != 7 || meta.Actor != 42 {
		t.Error("CommandMeta 值语义被共享引用破坏")
	}
}

// TestStateCommandTypes 验证常用命令均实现 Command 强类型联合。
func TestStateCommandTypes(t *testing.T) {
	var (
		_ Command = CreateRoomCommand{}
		_ Command = JoinRoomCommand{}
		_ Command = StartGameCommand{}
		_ Command = ConfirmRoleCommand{}
		_ Command = WolfKillCommand{}
		_ Command = WitchUseCommand{}
		_ Command = SeerCheckCommand{}
		_ Command = SpeakCommand{}
		_ Command = VoteCommand{}
		_ Command = TimeoutCommand{}
	)
	if !WitchActionSave.Valid() || !WitchActionPoison.Valid() || WitchActionUnknown.Valid() {
		t.Error("WitchAction 合法性判定错误")
	}
}

// TestStateEffectClassification 验证 Effect 分类与编译期类型断言。
func TestStateEffectClassification(t *testing.T) {
	var (
		_ Effect = MessageEffect{}
		_ Effect = TimerEffect{}
		_ Effect = PersistEffect{}
		_ Effect = EventEffect{}
	)
	effects := []Effect{
		TimerEffect{Phase: PhaseNight, Duration: 30_000_000_000},
		PersistEffect{Kind: PersistActiveGame},
		EventEffect{Kind: EventSendCompleted},
	}
	for _, e := range effects {
		switch e.(type) {
		case MessageEffect, TimerEffect, PersistEffect, EventEffect:
		default:
			t.Errorf("未知 Effect 分类: %T", e)
		}
	}
}

// TestStateSensitiveViewNotInPublicEffect 验证敏感视图不得混入公共 Effect：
// 狼人讨论、预言家结果等私密消息不能用 Public 受众；上帝视角可收狼人副本。
func TestStateSensitiveViewNotInPublicEffect(t *testing.T) {
	if _, err := NewMessageEffect(AudiencePublic, "room.info", nil); err != nil {
		t.Errorf("公共消息 room.info 被拒绝: %v, want nil", err)
	}
	if _, err := NewMessageEffect(AudienceWolf, "wolf.discussion", nil); err != nil {
		t.Errorf("狼人私密消息 wolf.discussion（Wolf 受众）被拒绝: %v, want nil", err)
	}
	if _, err := NewMessageEffect(AudiencePublic, "wolf.discussion", nil); err == nil {
		t.Error("狼人讨论被 Public 受众接受，敏感视图混入公共 Effect")
	}
	if _, err := NewMessageEffect(AudiencePublic, "seer.result", nil); err == nil {
		t.Error("预言家查验结果被 Public 受众接受，敏感视图混入公共 Effect")
	}
	if _, err := NewMessageEffect(AudienceGodView, "wolf.discussion", nil); err != nil {
		t.Errorf("上帝视角接收狼人副本被拒绝: %v, want nil", err)
	}
	if _, err := NewMessageEffect(AudienceActor, "role.card", nil); err != nil {
		t.Errorf("身份卡（Actor 受众）被拒绝: %v, want nil", err)
	}
	if _, err := NewMessageEffect(AudiencePublic, "role.card", nil); err == nil {
		t.Error("身份卡被 Public 受众接受，敏感视图混入公共 Effect")
	}
	if _, err := NewMessageEffect(AudienceUnknown, "room.info", nil); err == nil {
		t.Error("非法受众 AudienceUnknown 被接受，want error")
	}
}
