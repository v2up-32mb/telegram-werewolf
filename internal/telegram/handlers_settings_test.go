package telegram

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// fakeSettingsService 记录收到的设置命令并返回脚本结果。
type fakeSettingsService struct {
	cmds []game.SettingsCommand
	st   game.RoomSettings
	eff  []game.Effect
	err  error
}

func (f *fakeSettingsService) Apply(_ context.Context, cmd game.SettingsCommand) (game.RoomSettings, []game.Effect, error) {
	f.cmds = append(f.cmds, cmd)
	return f.st, f.eff, f.err
}

// settingsHandler 构造带 fake 服务的配置适配器。
func settingsHandler(t *testing.T, fake *fakeSettingsService) *SettingsHandler {
	t.Helper()
	return NewSettingsHandler(fake)
}

// TestSettingsInputCommandConversion 验证输入→领域命令的逐字段转换。
func TestSettingsInputCommandConversion(t *testing.T) {
	base := game.DefaultRoomSettings()
	base.FastMode = true
	pw := "Ab12cd"
	in := SettingsInput{
		CommandID:    "s1",
		Actor:        2001,
		RoomID:       "ABC123",
		Phase:        game.PhaseLobby,
		PhaseVersion: 1,
		Settings:     base,
		Password:     &pw,
	}
	cmd := in.Command()
	if cmd.Meta.ID != "s1" || cmd.Meta.Actor != 2001 {
		t.Errorf("Meta = %+v, want ID=s1 Actor=2001", cmd.Meta)
	}
	if cmd.Meta.ExpectedPhase != game.PhaseLobby || cmd.Meta.PhaseVersion != 1 {
		t.Errorf("ExpectedPhase/PhaseVersion = %v/%d, want PhaseLobby/1", cmd.Meta.ExpectedPhase, cmd.Meta.PhaseVersion)
	}
	if cmd.RoomID != "ABC123" {
		t.Errorf("RoomID = %q, want ABC123", cmd.RoomID)
	}
	if !reflect.DeepEqual(cmd.Settings, base) {
		t.Errorf("Settings = %+v, want %+v", cmd.Settings, base)
	}
	if cmd.Password == nil || *cmd.Password != "Ab12cd" {
		t.Errorf("Password = %v, want &Ab12cd", cmd.Password)
	}
}

// TestSettingsInputPasswordSemantics 验证密码三态在输入转换中原样保留：
// nil=不修改、空串=清除、非空=设置新密码。
func TestSettingsInputPasswordSemantics(t *testing.T) {
	clear := ""
	set := "NewPass1"
	cases := []struct {
		name  string
		input *string
		want  *string
	}{
		{"不修改密码", nil, nil},
		{"清除密码", &clear, &clear},
		{"设置新密码", &set, &set},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := SettingsInput{Password: tc.input}
			cmd := in.Command()
			if (cmd.Password == nil) != (tc.want == nil) {
				t.Fatalf("Command().Password = %v, want %v", cmd.Password, tc.want)
			}
			if cmd.Password != nil && *cmd.Password != *tc.want {
				t.Errorf("Command().Password = %q, want %q", *cmd.Password, *tc.want)
			}
		})
	}
}

// TestSettingsHandlerDelegatesToService 验证适配层把转换后的命令单点交给
// 应用服务并原样返回结果（不复制领域逻辑）。
func TestSettingsHandlerDelegatesToService(t *testing.T) {
	st := game.DefaultRoomSettings()
	st.SpeechMode = game.SpeechSoft
	eff := []game.Effect{game.MessageEffect{Key: game.SettingsUpdatedMessageKey, Audience: game.AudienceHost}}
	fake := &fakeSettingsService{st: st, eff: eff}
	h := settingsHandler(t, fake)

	in := SettingsInput{CommandID: "s9", Actor: 9, RoomID: "Z9Z9", Phase: game.PhaseLobby, Settings: st}
	gotSt, gotEff, err := h.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if len(fake.cmds) != 1 {
		t.Fatalf("服务收到命令数 = %d, want 1", len(fake.cmds))
	}
	want := game.SettingsCommand{
		Meta:     game.CommandMeta{ID: "s9", Actor: 9, ExpectedPhase: game.PhaseLobby},
		RoomID:   "Z9Z9",
		Settings: st,
	}
	if !reflect.DeepEqual(fake.cmds[0], want) {
		t.Errorf("服务收到命令 = %+v, want %+v", fake.cmds[0], want)
	}
	if !reflect.DeepEqual(gotSt, st) {
		t.Errorf("返回设置 = %+v, want %+v", gotSt, st)
	}
	if !reflect.DeepEqual(gotEff, eff) {
		t.Errorf("返回 Effects = %v, want %v", gotEff, eff)
	}
}

// TestSettingsHandlerPropagatesError 验证适配层不吞错误：服务失败原样上抛，
// 且转换后的请求仍被送达。
func TestSettingsHandlerPropagatesError(t *testing.T) {
	wantErr := errors.New("settings locked")
	fake := &fakeSettingsService{err: wantErr}
	h := settingsHandler(t, fake)

	in := SettingsInput{CommandID: "s10", Actor: 10, RoomID: "AB12", Phase: game.PhaseNight, Settings: game.DefaultRoomSettings()}
	_, _, err := h.Apply(context.Background(), in)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Apply error = %v, want %v", err, wantErr)
	}
	if len(fake.cmds) != 1 || fake.cmds[0].Meta.ExpectedPhase != game.PhaseNight {
		t.Errorf("服务收到的命令 = %+v, want ExpectedPhase=PhaseNight", fake.cmds)
	}
	// 适配层只转换不校验：发牌锁定由领域服务执行（ErrSettingsLocked 来自 service）。
}
