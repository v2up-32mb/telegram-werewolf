package game

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeSettingsRepo 是 SettingsRepository seam 的测试替身：记录保存的
// （设置, 密码哈希）对，断言持久化出口永远只有哈希、绝无明文。
type fakeSettingsRepo struct {
	saved   []savedSettings
	loadErr error
	hash    string
	loads   int
	saveErr error
}

type savedSettings struct {
	roomID   RoomID
	settings RoomSettings
	hash     string
}

func (f *fakeSettingsRepo) SaveSettings(_ context.Context, roomID RoomID, settings RoomSettings, passwordHash string) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedSettings{roomID: roomID, settings: settings, hash: passwordHash})
	return nil
}

func (f *fakeSettingsRepo) LoadPasswordHash(_ context.Context, _ RoomID) (string, error) {
	f.loads++
	return f.hash, f.loadErr
}

// settingsCommand 构造一个 PhaseLobby 的合法设置命令。
func settingsCommand() SettingsCommand {
	return SettingsCommand{
		Meta:     CommandMeta{ID: "s1", Actor: 1001, ExpectedPhase: PhaseLobby, PhaseVersion: 1},
		RoomID:   "ABC123",
		Settings: DefaultRoomSettings(),
	}
}

// newTestSettings 构造测试用 SettingsService；构造失败直接失败测试。
func newTestSettings(t *testing.T, repo SettingsRepository) SettingsService {
	t.Helper()
	svc, err := NewSettingsService(repo)
	if err != nil {
		t.Fatalf("NewSettingsService error = %v, want nil", err)
	}
	return svc
}

// TestDefaultRoomSettings 验证 MVP 默认房间配置（docs「6 人局默认配置总表」）。
func TestDefaultRoomSettings(t *testing.T) {
	s := DefaultRoomSettings()
	if s.SpeechMode != SpeechFixed {
		t.Errorf("SpeechMode = %v, want SpeechFixed（默认固定限时）", s.SpeechMode)
	}
	if s.SpeechSeconds != 60 || s.WolfNightSeconds != 30 || s.OtherNightSeconds != 15 {
		t.Errorf("默认时长 = %d/%d/%d, want 60/30/15", s.SpeechSeconds, s.WolfNightSeconds, s.OtherNightSeconds)
	}
	if s.FastMode {
		t.Error("默认 FastMode = true, want false")
	}
	if s.Victory != VictorySlaughter {
		t.Errorf("Victory = %v, want VictorySlaughter（6 人局默认屠城）", s.Victory)
	}
	if !s.WitchSelfSaveFirstNight {
		t.Error("默认 WitchSelfSaveFirstNight = false, want true（首夜默认可自救）")
	}
	if s.RevealRoleOnDeath {
		t.Error("默认 RevealRoleOnDeath = true, want false（默认不报身份）")
	}
	if !s.WolfMustKill {
		t.Error("默认 WolfMustKill = false, want true（狼人默认必须刀人）")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("DefaultRoomSettings().Validate() error = %v, want nil", err)
	}
}

// TestRoomSettingsValidateRejectsInvalid 表格测试非法组合被明确拒绝。
func TestRoomSettingsValidateRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*RoomSettings)
	}{
		{"非法发言模式", func(s *RoomSettings) { s.SpeechMode = SpeechUnknown }},
		{"非法发言模式越界", func(s *RoomSettings) { s.SpeechMode = SpeechMode(99) }},
		{"发言时长为负", func(s *RoomSettings) { s.SpeechSeconds = -1 }},
		{"狼人夜间时长为零", func(s *RoomSettings) { s.WolfNightSeconds = 0 }},
		{"其他角色时长为负", func(s *RoomSettings) { s.OtherNightSeconds = -5 }},
		{"非法胜负模式", func(s *RoomSettings) { s.Victory = VictoryUnknown }},
		{"非法胜负模式越界", func(s *RoomSettings) { s.Victory = VictoryMode(99) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultRoomSettings()
			tc.mut(&s)
			if err := s.Validate(); err == nil {
				t.Error("Validate() = nil, want error")
			}
		})
	}
}

// TestEffectiveDurationsFastMode 验证快速模式：减半后奇数秒向上取整
// （15→8），且最短不低于 5 秒（docs「快速模式取整」）。
func TestEffectiveDurationsFastMode(t *testing.T) {
	base := DefaultRoomSettings()
	t.Run("标准模式原值", func(t *testing.T) {
		sp, w, o := base.EffectiveDurations()
		if sp != 60 || w != 30 || o != 15 {
			t.Errorf("标准模式时长 = %d/%d/%d, want 60/30/15", sp, w, o)
		}
	})
	cases := []struct {
		name     string
		raw      int
		wantFast int
	}{
		{"60 减半", 60, 30},
		{"30 减半", 30, 15},
		{"15 向上取整(7.5→8)", 15, 8},
		{"9 向上取整(4.5→5)", 9, 5},
		{"7 减半后低于下限提升到 5", 7, 5},
		{"6 减半后为 3 提升到 5", 6, 5},
		{"5 减半后保持 5", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultRoomSettings()
			s.FastMode = true
			s.SpeechSeconds, s.WolfNightSeconds, s.OtherNightSeconds = tc.raw, tc.raw, tc.raw
			sp, w, o := s.EffectiveDurations()
			if sp != tc.wantFast || w != tc.wantFast || o != tc.wantFast {
				t.Errorf("快速模式(标准 %d) 时长 = %d/%d/%d, want 全部 = %d", tc.raw, sp, w, o, tc.wantFast)
			}
		})
	}
}

// TestValidatePassword 表格测试密码规则：4～16 位英文字母或数字，
// 区分大小写；不允许空格、中文和特殊符号（docs「密码」）。
func TestValidatePassword(t *testing.T) {
	for _, pw := range []string{"Ab12", "a1b2C3D4", "12345678", "ABCDabcd12", "Zz09"} {
		if err := ValidatePassword(pw); err != nil {
			t.Errorf("ValidatePassword(%q) error = %v, want nil", pw, err)
		}
	}
	cases := []struct {
		name string
		pw   string
	}{
		{"过短(3)", "abc"},
		{"过长(17)", "a1b2c3d4e5f6g7h8i9"},
		{"空串", ""},
		{"含空格", "ab 12"},
		{"含中文", "abc密"},
		{"含特殊符号", "ab@12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePassword(tc.pw); !errors.Is(err, ErrPasswordInvalid) {
				t.Errorf("ValidatePassword(%q) error = %v, want ErrPasswordInvalid", tc.pw, err)
			}
		})
	}
}

// TestHashPasswordAndVerify 验证 bcrypt：哈希与明文不同、可校验、
// 错误明文不匹配、两次哈希不同（盐）。
func TestHashPasswordAndVerify(t *testing.T) {
	const pw = "Passw0rd"
	h1, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword error = %v, want nil", err)
	}
	if h1 == pw || !strings.HasPrefix(h1, "$2") {
		t.Errorf("哈希 = %q, 应为 bcrypt 前缀且不等于明文", h1)
	}
	if !VerifyPassword(h1, pw) {
		t.Error("VerifyPassword(h1, pw) = false, want true")
	}
	if VerifyPassword(h1, "wrong") {
		t.Error("VerifyPassword(h1, wrong) = true, want false")
	}
	h2, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword#2 error = %v, want nil", err)
	}
	if h1 == h2 {
		t.Error("两次哈希相同，bcrypt 盐未生效")
	}
	if VerifyPassword(h2, pw) != true {
		t.Error("VerifyPassword(h2, pw) = false, want true")
	}
}

// TestSettingsServiceApplyLocked 验证发牌开始后所有配置修改被拒绝：
// Meta.ExpectedPhase 非 PhaseLobby 一律 ErrSettingsLocked，且不产生写入/Effects。
func TestSettingsServiceApplyLocked(t *testing.T) {
	for _, phase := range []Phase{PhaseUnknown, PhaseDeal, PhaseNight, PhaseDaySpeech, PhaseDayVote, PhaseSettlement} {
		t.Run(phase.String(), func(t *testing.T) {
			repo := &fakeSettingsRepo{}
			svc := newTestSettings(t, repo)
			cmd := settingsCommand()
			cmd.Meta.ExpectedPhase = phase
			got, effects, err := svc.Apply(context.Background(), cmd)
			if !errors.Is(err, ErrSettingsLocked) {
				t.Fatalf("Apply(%v) error = %v, want ErrSettingsLocked", phase, err)
			}
			if !reflect.DeepEqual(got, RoomSettings{}) {
				t.Errorf("锁定拒绝后返回设置 = %+v, want 零值", got)
			}
			if len(effects) != 0 {
				t.Errorf("锁定拒绝后仍有 Effects: %v", effects)
			}
			if len(repo.saved) != 0 {
				t.Errorf("锁定拒绝后仍写入 Repository: %+v", repo.saved)
			}
		})
	}
}

// TestSettingsServiceApplyStoresHashNotPlaintext 验证成功路径：
// 明文仅用于 bcrypt，Repository 只收到哈希，且消息 Effect 参数不含明文。
func TestSettingsServiceApplyStoresHashNotPlaintext(t *testing.T) {
	repo := &fakeSettingsRepo{}
	svc := newTestSettings(t, repo)
	cmd := settingsCommand()
	pw := "Ab12cd"
	cmd.Password = &pw

	got, effects, err := svc.Apply(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("保存次数 = %d, want 1", len(repo.saved))
	}
	saved := repo.saved[0]
	if saved.hash == pw || !strings.HasPrefix(saved.hash, "$2") {
		t.Errorf("Repository 收到的密码 = %q, 应为 bcrypt 哈希而非明文", saved.hash)
	}
	if !VerifyPassword(saved.hash, pw) {
		t.Error("存库哈希无法校验明文")
	}
	if saved.settings != cmd.Settings {
		t.Errorf("保存的设置 = %+v, want %+v", saved.settings, cmd.Settings)
	}
	if !reflect.DeepEqual(got, cmd.Settings) {
		t.Errorf("Apply 返回设置 = %+v, want %+v", got, cmd.Settings)
	}
	requireSettingsUpdatedEffect(t, effects, "ABC123", true)
}

// TestSettingsServiceApplyClearPassword 验证显式清除密码：保存空哈希。
func TestSettingsServiceApplyClearPassword(t *testing.T) {
	repo := &fakeSettingsRepo{}
	svc := newTestSettings(t, repo)
	cmd := settingsCommand()
	pw := ""
	cmd.Password = &pw

	_, effects, err := svc.Apply(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].hash != "" {
		t.Fatalf("清除密码后保存 = %+v, want 空哈希", repo.saved)
	}
	requireSettingsUpdatedEffect(t, effects, "ABC123", false)
}

// TestSettingsServiceApplyKeepPassword 验证未携带密码意图时读取并保留现有哈希，
// 不因修改其他配置而静默清空密码。
func TestSettingsServiceApplyKeepPassword(t *testing.T) {
	existing, err := HashPassword("OldPass1")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	repo := &fakeSettingsRepo{hash: existing}
	svc := newTestSettings(t, repo)
	cmd := settingsCommand()

	_, effects, err := svc.Apply(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Apply error = %v, want nil", err)
	}
	if repo.loads != 1 {
		t.Errorf("LoadPasswordHash 调用次数 = %d, want 1", repo.loads)
	}
	if len(repo.saved) != 1 || repo.saved[0].hash != existing {
		t.Errorf("保留密码后保存 = %+v, want 原哈希继续保存", repo.saved)
	}
	requireSettingsUpdatedEffect(t, effects, "ABC123", true)
}

// TestSettingsServiceApplyRejectsInvalidPassword 验证非法明文密码在哈希前被拒绝，
// Repository 不产生任何写入。
func TestSettingsServiceApplyRejectsInvalidPassword(t *testing.T) {
	repo := &fakeSettingsRepo{}
	svc := newTestSettings(t, repo)
	cmd := settingsCommand()
	pw := "ab" // 过短
	cmd.Password = &pw

	_, _, err := svc.Apply(context.Background(), cmd)
	if !errors.Is(err, ErrPasswordInvalid) {
		t.Fatalf("Apply error = %v, want ErrPasswordInvalid", err)
	}
	if len(repo.saved) != 0 {
		t.Errorf("非法密码仍产生写入: %+v", repo.saved)
	}
}

// TestSettingsServiceApplyRejectsInvalidSettings 验证非法设置在写入前被拒绝。
func TestSettingsServiceApplyRejectsInvalidSettings(t *testing.T) {
	repo := &fakeSettingsRepo{}
	svc := newTestSettings(t, repo)
	cmd := settingsCommand()
	cmd.Settings.SpeechSeconds = -1

	_, _, err := svc.Apply(context.Background(), cmd)
	if err == nil {
		t.Fatal("Apply(非法设置) error = nil, want error")
	}
	if len(repo.saved) != 0 {
		t.Errorf("非法设置仍产生写入: %+v", repo.saved)
	}
}

// TestNewSettingsServiceRequiresRepo 验证 Repository 为硬约束。
func TestNewSettingsServiceRequiresRepo(t *testing.T) {
	if _, err := NewSettingsService(nil); err == nil {
		t.Error("NewSettingsService(nil) = nil error, want error")
	}
}

// TestMarshalSettingsRoundTrip 验证设置快照 JSON 序列化往返一致。
func TestMarshalSettingsRoundTrip(t *testing.T) {
	s := DefaultRoomSettings()
	s.SpeechMode = SpeechSoft
	s.FastMode = true
	s.Victory = VictorySide
	s.WitchSelfSaveFirstNight = false
	s.RevealRoleOnDeath = true
	s.WolfMustKill = false

	raw, err := MarshalSettings(s)
	if err != nil {
		t.Fatalf("MarshalSettings error = %v, want nil", err)
	}
	got, err := UnmarshalSettings(raw)
	if err != nil {
		t.Fatalf("UnmarshalSettings error = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Errorf("往返后设置 = %+v, want %+v", got, s)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("往返后 Validate() error = %v, want nil", err)
	}
	if _, err := UnmarshalSettings("not-json"); err == nil {
		t.Error("UnmarshalSettings(非法 JSON) = nil error, want error")
	}
}

// requireSettingsUpdatedEffect 断言设置保存成功产生房主确认消息 Effect。
func requireSettingsUpdatedEffect(t *testing.T, effects []Effect, roomID string, passwordSet bool) {
	t.Helper()
	var msg *MessageEffect
	for _, e := range effects {
		if v, ok := e.(MessageEffect); ok {
			msg = &v
		}
	}
	if msg == nil {
		t.Fatal("effects 缺少 MessageEffect")
	}
	if msg.Audience != AudienceHost {
		t.Errorf("MessageEffect.Audience = %v, want AudienceHost", msg.Audience)
	}
	if msg.Key != SettingsUpdatedMessageKey {
		t.Errorf("MessageEffect.Key = %q, want %q", msg.Key, SettingsUpdatedMessageKey)
	}
	if got, ok := msg.Params["room_code"].(string); !ok || got != roomID {
		t.Errorf("MessageEffect.Params[room_code] = %v, want %q", msg.Params["room_code"], roomID)
	}
	if got, ok := msg.Params["password_set"].(bool); !ok || got != passwordSet {
		t.Errorf("MessageEffect.Params[password_set] = %v, want %v（且绝不含明文密码）", msg.Params["password_set"], passwordSet)
	}
}
