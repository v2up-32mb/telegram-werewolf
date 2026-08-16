package telegram

import (
	"errors"
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
)

func dayViewRenderer(t *testing.T) *i18n.Renderer {
	t.Helper()
	r, err := i18n.NewRenderer("zh-CN")
	if err != nil {
		t.Fatalf("NewRenderer(zh-CN) error = %v", err)
	}
	return r
}

// dayViewState 构造 PhaseDaySpeech 的 6 人状态：3/5 死亡，5 是女巫、
// 4 是预言家、1/6 是狼人、2 是村民；夜间素材（查验/用药）有值。
func dayViewState(t *testing.T) game.State {
	t.Helper()
	st := game.State{
		RoomID:       game.RoomID("VIEW34"),
		Phase:        game.PhaseDaySpeech,
		PhaseVersion: 5,
		Settings:     game.DefaultRoomSettings(),
		Night: game.NightState{
			WitchUsedTonight:  true,
			WitchSaveUsed:     true,
			WitchPoisonUsed:   true,
			WitchPoisonTarget: daySeatPtr(5, t),
			SeerChecked:       map[game.Seat]bool{6: true},
			SeerResults:       map[game.Seat]game.Camp{6: game.CampWolf},
		},
	}
	roles := []game.Role{game.RoleVillager, game.RoleVillager, game.RoleWolf, game.RoleSeer, game.RoleWitch, game.RoleWolf}
	for i, role := range roles {
		s := game.Seat(i + 1)
		st.Players = append(st.Players, game.Player{
			UserID: game.UserID(301 + i),
			Seat:   s,
			Role:   role,
			Dead:   s == 3 || s == 5,
		})
	}
	return st
}

func daySeatPtr(s game.Seat, t *testing.T) *game.Seat {
	t.Helper()
	p := s
	return &p
}

// TestViewerAppendBasic 验证页引用创建与长度累计。
func TestViewerAppendBasic(t *testing.T) {
	v := NewViewer()
	period := Period{Kind: PeriodDay, Number: 2}
	ref, created, err := v.Append(1, period, "你好")
	if err != nil {
		t.Fatalf("Append error = %v", err)
	}
	if !created {
		t.Error("首次 Append 应创建页")
	}
	if ref.Page != 1 || ref.Length != 2 || ref.Full {
		t.Errorf("ref = %+v, want page=1 len=2 full=false", ref)
	}
	cur := v.Current(1, period)
	if cur == nil || cur.Page != 1 {
		t.Fatalf("Current = %+v, want page 1", cur)
	}
}

// TestViewerAppendContinueAfterFull 验证跨过 3000 字符的编辑照常落在当前页
// 并标记已满（docs §主消息形态 2），下一次更新创建顺序编号续页且文本落在
// 续页上。
func TestViewerAppendContinueAfterFull(t *testing.T) {
	v := NewViewer()
	period := Period{Kind: PeriodNight, Number: 1}
	big := strings.Repeat("字", 2999)
	if _, _, err := v.Append(1, period, big); err != nil {
		t.Fatalf("Append big error = %v", err)
	}
	// 跨过 3000 的编辑照常发送：落在当前页并标记已满。
	ref2, created2, err := v.Append(1, period, "ab")
	if err != nil {
		t.Fatalf("Append ab error = %v", err)
	}
	if created2 {
		t.Error("跨过 3000 的编辑不应创建续页")
	}
	if ref2.Page != 1 || ref2.Length != 3001 || !ref2.Full {
		t.Errorf("跨限页 = %+v, want page=1 len=3001 full=true", ref2)
	}
	// 下一次更新创建续页，文本落在续页。
	ref3, created3, err := v.Append(1, period, "丙")
	if err != nil {
		t.Fatalf("Append 丙 error = %v", err)
	}
	if !created3 {
		t.Error("下一次 Append 应创建续页")
	}
	if ref3.Page != 2 || ref3.Length != 1 || ref3.Full {
		t.Errorf("续页 = %+v, want page=2 len=1 full=false", ref3)
	}
	cur := v.Current(1, Period{Kind: PeriodNight, Number: 1})
	if cur == nil || cur.Page != 2 {
		t.Fatalf("Current = %+v, want 最新页 page 2", cur)
	}
}

// TestViewerFinalize 验证时间段定稿后拒绝编辑。
func TestViewerFinalize(t *testing.T) {
	v := NewViewer()
	period := Period{Kind: PeriodDay, Number: 3}
	if _, _, err := v.Append(2, period, "x"); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	v.Finalize(2, period)
	if _, _, err := v.Append(2, period, "y"); !errors.Is(err, ErrPeriodFinalized) {
		t.Fatalf("定稿后 Append err = %v, want ErrPeriodFinalized", err)
	}
	// 其他时间段不受影响。
	if _, _, err := v.Append(2, Period{Kind: PeriodNight, Number: 3}, "z"); err != nil {
		t.Fatalf("其他时间段 Append err = %v", err)
	}
}

// TestViewerPeriodIsolation 验证不同 Chat / 不同时间段互不干扰。
func TestViewerPeriodIsolation(t *testing.T) {
	v := NewViewer()
	if _, _, err := v.Append(9, Period{Kind: PeriodNight, Number: 1}, "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Append(9, Period{Kind: PeriodDay, Number: 1}, "b"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Append(10, Period{Kind: PeriodNight, Number: 1}, "c"); err != nil {
		t.Fatal(err)
	}
	cur := v.Current(9, Period{Kind: PeriodNight, Number: 1})
	if cur == nil || cur.Length != 1 {
		t.Errorf("chat9 night.1 = %+v, want 单独页", cur)
	}
}

// TestRenderDayAliveNoLeak 验证存活玩家视图：只含公共死讯（死亡名单），
// 不泄漏身份/死因/夜间行动；私密记录为「无」；无上帝视角段。
func TestRenderDayAliveNoLeak(t *testing.T) {
	r := dayViewRenderer(t)
	st := dayViewState(t)
	out := game.DayOutcome{
		Victims: []game.Seat{3, 5},
		Cause:   map[game.Seat]game.DeathCause{3: game.CauseWolfKill, 5: game.CauseWitchPoison},
	}
	view, err := RenderDayMain(r, st, 1, 2, out, nil)
	if err != nil {
		t.Fatalf("RenderDayMain error = %v", err)
	}
	if view.Progress == "" || view.Timeline == "" {
		t.Errorf("存活玩家主消息缺少进度/累计进程段: %+v", view)
	}
	if !strings.Contains(view.Private, "无") {
		t.Errorf("存活玩家私密记录应显式「无」: %q", view.Private)
	}
	if view.GodView != "" || view.Actions != "" {
		t.Errorf("存活玩家不得有上帝视角/只读行动段")
	}
	total := view.Title + view.Progress + view.Timeline + view.Private
	if !strings.Contains(total, "3号") || !strings.Contains(total, "5号") {
		t.Errorf("死讯应含死亡名单: %q", total)
	}
	for _, secret := range []string{"wolf_kill", "witch_poison", "狼人", "女巫", "预言家", "查验", "毒药"} {
		if strings.Contains(total, secret) {
			t.Errorf("存活视图泄漏敏感内容 %q: %q", secret, total)
		}
	}
}

// TestRenderDayAliveRevealRole 验证报身份开启后存活玩家死讯附带死者身份，
// 但依然无死因、且不泄漏未公开的存活者身份。
func TestRenderDayAliveRevealRole(t *testing.T) {
	r := dayViewRenderer(t)
	st := dayViewState(t)
	st.Settings.RevealRoleOnDeath = true
	out := game.DayOutcome{Victims: []game.Seat{3, 5}, Cause: map[game.Seat]game.DeathCause{3: game.CauseWolfKill}}
	view, err := RenderDayMain(r, st, 1, 2, out, nil)
	if err != nil {
		t.Fatalf("RenderDayMain error = %v", err)
	}
	total := view.Title + view.Progress + view.Timeline + view.Private
	if !strings.Contains(total, "狼人") || !strings.Contains(total, "女巫") {
		t.Errorf("报身份开启后死讯应含死者身份: %q", total)
	}
	if strings.Contains(total, "wolf_kill") || strings.Contains(total, "witch_poison") {
		t.Errorf("公共死讯不得含死因: %q", total)
	}
	if strings.Contains(total, "预言家") {
		t.Errorf("存活视图不得泄漏存活者（4号预言家）身份: %q", total)
	}
}

// TestRenderDayDeadGodView 验证死亡玩家视图：统一上帝视角第三段（全员身份、
// 用药/查验/死讯素材）+ 本人真实身份/死因说明 + 只读行动记录。
func TestRenderDayDeadGodView(t *testing.T) {
	r := dayViewRenderer(t)
	st := dayViewState(t)
	out := game.DayOutcome{
		Victims: []game.Seat{3, 5},
		Cause:   map[game.Seat]game.DeathCause{3: game.CauseWolfKill, 5: game.CauseWitchPoison},
	}
	view, err := RenderDayMain(r, st, 5, 2, out, map[game.Seat]string{5: "毒药女巫"})
	if err != nil {
		t.Fatalf("RenderDayMain error = %v", err)
	}
	if view.GodView == "" {
		t.Fatal("死亡玩家应有上帝视角记录段")
	}
	for _, want := range []string{"狼人", "预言家", "女巫", "查验", "毒药", "3号", i18n.EscapeMarkdownV2("witch_poison")} {
		if !strings.Contains(view.GodView, want) {
			t.Errorf("上帝视角缺少 %q: %q", want, view.GodView)
		}
	}
	if !strings.Contains(view.Actions, "只读") {
		t.Errorf("死亡玩家应有只读行动记录素材: %q", view.Actions)
	}
	if view.Private != "" {
		t.Errorf("死亡玩家不适用存活私密记录段: %q", view.Private)
	}
}

// TestRenderDayPeace 验证平安夜视图不出现死讯名单。
func TestRenderDayPeace(t *testing.T) {
	r := dayViewRenderer(t)
	st := dayViewState(t)
	view, err := RenderDayMain(r, st, 1, 1, game.DayOutcome{}, nil)
	if err != nil {
		t.Fatalf("RenderDayMain error = %v", err)
	}
	total := view.Title + view.Progress + view.Timeline + view.Private
	if !strings.Contains(total, "平安") {
		t.Errorf("平安夜视图应含平安播报: %q", total)
	}
	if strings.Contains(total, "死亡") || strings.Contains(total, "3号") {
		t.Errorf("平安夜视图不得出现死讯: %q", total)
	}
}

// TestRenderDayEscapesDynamic 验证动态值统一 MarkdownV2 转义（昵称含
// 通配符/下划线/方括号时不得原样进入输出）。
func TestRenderDayEscapesDynamic(t *testing.T) {
	r := dayViewRenderer(t)
	st := dayViewState(t)
	out := game.DayOutcome{Victims: []game.Seat{3, 5}}
	view, err := RenderDayMain(r, st, 1, 2, out, map[game.Seat]string{3: "a*b_c[d]"})
	if err != nil {
		t.Fatalf("RenderDayMain error = %v", err)
	}
	if strings.Contains(view.Progress, "a*b_c[d]") {
		t.Errorf("昵称未转义: %q", view.Progress)
	}
	if !strings.Contains(view.Progress, i18n.EscapeMarkdownV2("a*b_c[d]")) {
		t.Errorf("昵称应以转义形式出现: %q", view.Progress)
	}
}

// TestViewerSetMessageID 验证接线层回填 MessageID 到最新页。
func TestViewerSetMessageID(t *testing.T) {
	v := NewViewer()
	period := Period{Kind: PeriodDay, Number: 2}
	if _, _, err := v.Append(outbox.ChatID(7), period, "x"); err != nil {
		t.Fatal(err)
	}
	v.SetMessageID(outbox.ChatID(7), period, 424242)
	cur := v.Current(outbox.ChatID(7), period)
	if cur == nil || cur.MessageID != 424242 {
		t.Errorf("MessageID 未回填: %+v", cur)
	}
}
