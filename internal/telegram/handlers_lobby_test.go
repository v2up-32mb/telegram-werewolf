package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// fakeLobbyService 记录收到的生命周期请求并返回脚本结果。
type fakeLobbyService struct {
	leaves []game.LeaveCommand
	kicks  []game.KickCommand
	renews []game.RenewCommand

	st  game.State
	lt  game.LobbyLifetime
	eff []game.Effect
	err error
}

func (f *fakeLobbyService) LeaveRoom(_ context.Context, st game.State, cmd game.LeaveCommand) (game.State, []game.Effect, error) {
	f.leaves = append(f.leaves, cmd)
	return f.st, f.eff, f.err
}

func (f *fakeLobbyService) KickPlayer(_ context.Context, st game.State, cmd game.KickCommand) (game.State, []game.Effect, error) {
	f.kicks = append(f.kicks, cmd)
	return f.st, f.eff, f.err
}

func (f *fakeLobbyService) RenewRoom(_ context.Context, st game.State, cmd game.RenewCommand, lt game.LobbyLifetime) (game.State, game.LobbyLifetime, []game.Effect, error) {
	f.renews = append(f.renews, cmd)
	return f.st, f.lt, f.eff, f.err
}

func (f *fakeLobbyService) EvaluateIdle(_ context.Context, lt game.LobbyLifetime, st game.State) (game.LobbyLifetime, []game.Effect, error) {
	return f.lt, f.eff, f.err
}

func lobbyHandler(t *testing.T, fake *fakeLobbyService) *LobbyHandler {
	t.Helper()
	return NewLobbyHandler(fake)
}

func lobbyInputCmd() game.CommandMeta {
	return game.CommandMeta{ID: "l1", Actor: 1001, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1}
}

// TestFromLeaveText 验证 /leave 文本命令解析：容忍首尾空白清洗，
// 拒绝带参数/非 /leave 文本。
func TestFromLeaveText(t *testing.T) {
	for _, text := range []string{"/leave", "  /leave  ", "\t/leave\n"} {
		if _, ok := FromLeaveText(text); !ok {
			t.Errorf("FromLeaveText(%q) ok=false, want true（首尾空白清洗）", text)
		}
	}
	for _, text := range []string{"", "/leave ABC123", "leave", "/newgame", "/join x"} {
		if _, ok := FromLeaveText(text); ok {
			t.Errorf("FromLeaveText(%q) ok=true, want false", text)
		}
	}
}

// TestLobbyInputCommandConversion 验证三类输入→领域命令的逐字段转换。
func TestLobbyInputCommandConversion(t *testing.T) {
	in := LeaveInput{CommandID: "l2", Actor: 1002, Phase: game.PhaseLobby, PhaseVersion: 2}
	cmd := in.Command()
	want := game.LeaveCommand{Meta: game.CommandMeta{ID: "l2", Actor: 1002, ExpectedPhase: game.PhaseLobby, PhaseVersion: 2}}
	if !reflect.DeepEqual(cmd, want) {
		t.Errorf("LeaveCommand = %+v, want %+v", cmd, want)
	}

	kin := KickInput{CommandID: "k1", Actor: 1001, Phase: game.PhaseLobby, PhaseVersion: 1, Target: 1003}
	kcmd := kin.Command()
	kwant := game.KickCommand{Meta: game.CommandMeta{ID: "k1", Actor: 1001, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1}, Target: 1003}
	if !reflect.DeepEqual(kcmd, kwant) {
		t.Errorf("KickCommand = %+v, want %+v", kcmd, kwant)
	}

	rin := RenewInput{CommandID: "r1", Actor: 1001, Phase: game.PhaseLobby, PhaseVersion: 1}
	rcmd := rin.Command()
	rwant := game.RenewCommand{Meta: game.CommandMeta{ID: "r1", Actor: 1001, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1}}
	if !reflect.DeepEqual(rcmd, rwant) {
		t.Errorf("RenewCommand = %+v, want %+v", rcmd, rwant)
	}
}

// TestLobbyHandlerDelegates 验证适配层把命令单点交给服务并原样返回。
func TestLobbyHandlerDelegates(t *testing.T) {
	st := game.State{RoomID: "ABC123", Phase: game.PhaseLobby}
	eff := []game.Effect{game.MessageEffect{Key: game.LobbyPanelMessageKey, Audience: game.AudienceHost}}
	lt := game.LobbyLifetime{CreatedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	fake := &fakeLobbyService{st: st, eff: eff, lt: lt}
	h := lobbyHandler(t, fake)

	leaveIn := LeaveInput{CommandID: "a", Actor: 1002, Phase: game.PhaseLobby, PhaseVersion: 1}
	gotSt, gotEff, err := h.Leave(context.Background(), leaveIn, st)
	if err != nil {
		t.Fatalf("Leave: %v", err)
	}
	wantLeave := game.LeaveCommand{Meta: game.CommandMeta{ID: "a", Actor: 1002, ExpectedPhase: game.PhaseLobby, PhaseVersion: 1}}
	if len(fake.leaves) != 1 || !reflect.DeepEqual(fake.leaves[0], wantLeave) {
		t.Errorf("Leave 命令 = %+v, want %+v", fake.leaves, wantLeave)
	}
	_ = gotSt
	if !reflect.DeepEqual(gotEff, eff) {
		t.Errorf("Leave Effects = %v, want %v", gotEff, eff)
	}

	_, _, err = h.Kick(context.Background(), KickInput{CommandID: "b", Actor: 1001, Phase: game.PhaseLobby, PhaseVersion: 1, Target: 1003}, st)
	if err != nil {
		t.Fatalf("Kick: %v", err)
	}
	if len(fake.kicks) != 1 || fake.kicks[0].Target != 1003 {
		t.Errorf("Kick 命令 = %+v, want Target=1003", fake.kicks)
	}

	_, _, _, err = h.Renew(context.Background(), RenewInput{CommandID: "c", Actor: 1001, Phase: game.PhaseLobby, PhaseVersion: 1}, st, lt)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if len(fake.renews) != 1 {
		t.Errorf("Renew 调用数 = %d, want 1", len(fake.renews))
	}
}

// TestLobbyHandlerPropagatesError 验证适配层不吞错误。
func TestLobbyHandlerPropagatesError(t *testing.T) {
	wantErr := errors.New("only host")
	fake := &fakeLobbyService{err: wantErr}
	h := lobbyHandler(t, fake)

	st := game.State{RoomID: "ABC123", Phase: game.PhaseLobby}
	_, _, err := h.Leave(context.Background(), LeaveInput{CommandID: "x", Actor: 1002, Phase: game.PhaseLobby}, st)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Leave error = %v, want %v", err, wantErr)
	}
}
