package i18n

import (
	"strings"
	"testing"
)

func newTestRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer("zh-CN")
	if err != nil {
		t.Fatalf("NewRenderer(zh-CN) error = %v, want nil", err)
	}
	return r
}

// TestRenderDefaultEscaping 验证模板参数默认全部转义。
func TestRenderDefaultEscaping(t *testing.T) {
	r := newTestRenderer(t)
	got, err := r.Render("notice.top", map[string]any{"Message": "玩家A (1号)!"})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	want := "📢 玩家A \\(1号\\)\\!"
	if got != want {
		t.Errorf("Render(notice.top) = %q, want %q", got, want)
	}
}

// TestRenderSafeMarkdown 验证 SafeMarkdown 显式绕过转义，而普通字符串仍转义。
func TestRenderSafeMarkdown(t *testing.T) {
	r := newTestRenderer(t)
	got, err := r.Render("notice.top", map[string]any{"Message": SafeMarkdown("**粗体**")})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got != "📢 **粗体**" {
		t.Errorf("SafeMarkdown 渲染 = %q, want %q", got, "📢 **粗体**")
	}

	got2, err := r.Render("notice.top", map[string]any{"Message": "**粗体**"})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got2 != "📢 \\*\\*粗体\\*\\*" {
		t.Errorf("普通字符串渲染 = %q, want %q", got2, "📢 \\*\\*粗体\\*\\*")
	}
}

// TestRenderMissingKey 验证缺失 message key 返回错误。
func TestRenderMissingKey(t *testing.T) {
	r := newTestRenderer(t)
	if _, err := r.Render("no.such.key", nil); err == nil {
		t.Fatal("Render(no.such.key) = nil error, want error")
	}
}

// TestRenderPrivateMarksNormalText 验证普通文字私密标记：Emoji + 明文。
func TestRenderPrivateMarksNormalText(t *testing.T) {
	r := newTestRenderer(t)
	cases := []struct {
		key  string
		want string
	}{
		{"mark.good_seer", "🟢 好人"},
		{"mark.wolf_seer", "🐺 狼人"},
		{"mark.wolfmate", "🐺 狼队友"},
		{"mark.role.villager", "👤 平民"},
		{"mark.role.werewolf", "🐺 狼人"},
		{"mark.role.seer", "🔮 预言家"},
		{"mark.role.witch", "💊 女巫"},
	}
	for _, c := range cases {
		got, err := r.Render(c.key, nil)
		if err != nil {
			t.Errorf("Render(%s) error = %v, want nil", c.key, err)
			continue
		}
		if got != c.want {
			t.Errorf("Render(%s) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestRenderSeatButtonShortMark 验证按钮短标记：座位号 + Emoji，紧贴无空格、无占位。
func TestRenderSeatButtonShortMark(t *testing.T) {
	r := newTestRenderer(t)
	got, err := r.Render("button.seat", map[string]any{"Seat": 2, "Mark": SafeMarkdown("🐺")})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got != "2号🐺" {
		t.Errorf("带标记按钮 = %q, want %q", got, "2号🐺")
	}

	got2, err := r.Render("button.seat", map[string]any{"Seat": 5, "Mark": SafeMarkdown("")})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got2 != "5号" {
		t.Errorf("无标记按钮 = %q, want %q（不添加占位字符）", got2, "5号")
	}
}

// TestRenderRoleCardCaption 验证身份卡 Caption 中玩家数据被转义。
func TestRenderRoleCardCaption(t *testing.T) {
	r := newTestRenderer(t)
	got, err := r.Render("role_card.caption", map[string]any{"RoleName": "预言_家"})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got != "你抽到了 预言\\_家！请确认你的身份。" {
		t.Errorf("身份卡 Caption = %q", got)
	}
}

// TestRenderPhaseMessages 验证时间段主消息、上帝视角与限时文案。
func TestRenderPhaseMessages(t *testing.T) {
	r := newTestRenderer(t)
	cases := []struct {
		name string
		key  string
		data map[string]any
		want string
	}{
		{"白天主消息", "phase.day.title", map[string]any{"PhaseNumber": 1}, "☀️ 第 1 天"},
		{"夜晚主消息", "phase.night.title", map[string]any{"PhaseNumber": 1}, "🌙 第 1 夜"},
		{"上帝视角头", "phase.god_view.header", nil, "【上帝视角记录】"},
		{"私密记录头", "phase.private_record.header", nil, "【我的私密记录】"},
		{"阶段限时", "phase.time_left", map[string]any{"Seconds": 30}, "本阶段限时：30 秒"},
	}
	for _, c := range cases {
		got, err := r.Render(c.key, c.data)
		if err != nil {
			t.Errorf("%s: Render(%s) error = %v, want nil", c.name, c.key, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: Render(%s) = %q, want %q", c.name, c.key, got, c.want)
		}
	}
}

// TestRenderTempActionPrompts 验证角色临时操作提示文案。
func TestRenderTempActionPrompts(t *testing.T) {
	r := newTestRenderer(t)
	cases := []struct{ key, want string }{
		{"action.wolf.kill.prompt", "请选择要击杀的玩家："},
		{"action.seer.check.prompt", "请选择要查验的玩家："},
		{"action.witch.save.prompt", "是否使用解药："},
		{"action.vote.prompt", "请投票给要放逐的玩家："},
	}
	for _, c := range cases {
		got, err := r.Render(c.key, nil)
		if err != nil {
			t.Errorf("Render(%s) error = %v, want nil", c.key, err)
			continue
		}
		if got != c.want {
			t.Errorf("Render(%s) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestRenderMenuAndHealth 验证主菜单与健康提示。
func TestRenderMenuAndHealth(t *testing.T) {
	r := newTestRenderer(t)
	menu, err := r.Render("menu.main", nil)
	if err != nil {
		t.Fatalf("Render(menu.main) error = %v, want nil", err)
	}
	if !strings.Contains(menu, "欢迎使用狼人杀 Bot") {
		t.Errorf("menu.main 缺少欢迎文案: %q", menu)
	}
	health, err := r.Render("health.alive", nil)
	if err != nil {
		t.Fatalf("Render(health.alive) error = %v, want nil", err)
	}
	if health != "服务运行正常" {
		t.Errorf("health.alive = %q, want 服务运行正常", health)
	}
}

// TestRenderErrorWithData 验证系统错误带参数转义。
func TestRenderErrorWithData(t *testing.T) {
	r := newTestRenderer(t)
	got, err := r.Render("error.invalid_input", map[string]any{"Detail": "房间码 (ABC)!"})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}
	if got != "输入无效：房间码 \\(ABC\\)\\!" {
		t.Errorf("error.invalid_input = %q", got)
	}
}

// TestNewRendererUnsupportedLocale 验证 MVP 仅支持 zh-CN。
func TestNewRendererUnsupportedLocale(t *testing.T) {
	if _, err := NewRenderer("en-US"); err == nil {
		t.Fatal("NewRenderer(en-US) = nil error, want error")
	}
}
