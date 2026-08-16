package telegram

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// newRoleTestRenderer 构建 MVP 唯一交付语言 zh-CN 的渲染器。
func newRoleTestRenderer(t *testing.T) *i18n.Renderer {
	t.Helper()
	r, err := i18n.NewRenderer("zh-CN")
	if err != nil {
		t.Fatalf("NewRenderer(zh-CN) error = %v", err)
	}
	return r
}

// fakeRoleImageProvider 是 RoleImageProvider 的最小替身：返回确定性
// 图片字节与 MIME；missing 中列出的角色名返回错误（模拟角色图缺失）。
type fakeRoleImageProvider struct {
	missing map[string]bool
}

func (f fakeRoleImageProvider) RoleCard(name string) ([]byte, string, error) {
	if f.missing[name] {
		return nil, "", errors.New("assets: role card missing")
	}
	return []byte("fake:" + name), "image/jpeg", nil
}

// TestRoleCardViewCaptionAndMedia 验证身份卡视图：图片字节/MIME/座位与
// MarkdownV2 Caption（经 i18n role_card.caption 渲染，含角色中文名）。
func TestRoleCardViewCaptionAndMedia(t *testing.T) {
	r := newRoleTestRenderer(t)
	cases := []struct {
		name    string
		role    game.Role
		imgName string
		display string
	}{
		{"wolf", game.RoleWolf, "werewolf", "狼人"},
		{"seer", game.RoleSeer, "seer", "预言家"},
		{"witch", game.RoleWitch, "witch", "女巫"},
		{"villager", game.RoleVillager, "villager", "平民"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := NewRoleCardView(r, fakeRoleImageProvider{}, tc.role, game.Seat(3))
			if err != nil {
				t.Fatalf("NewRoleCardView error = %v, want nil", err)
			}
			if v.Seat != 3 {
				t.Errorf("Seat = %v, want 3", v.Seat)
			}
			if v.MIME != "image/jpeg" {
				t.Errorf("MIME = %q, want image/jpeg", v.MIME)
			}
			if string(v.Image) != "fake:"+tc.imgName {
				t.Errorf("Image = %q, want fake:%s", v.Image, tc.imgName)
			}
			if !strings.Contains(v.Caption, tc.display) {
				t.Errorf("Caption = %q, want 包含角色中文名 %q", v.Caption, tc.display)
			}
			if !strings.HasPrefix(v.Caption, "你抽到了") || !strings.HasSuffix(v.Caption, "请确认你的身份。") {
				t.Errorf("Caption = %q, want role_card.caption 模板渲染结果", v.Caption)
			}
			if utf8.RuneCountInString(v.Caption) > telegramCaptionMaxChars {
				t.Errorf("Caption 长度 = %d, 超过 1024", utf8.RuneCountInString(v.Caption))
			}
		})
	}
}

// TestRoleCardViewMissingImage 验证角色图缺失时返回 ErrRoleImageNotAvailable，
// 视图为零值且不 panic。
func TestRoleCardViewMissingImage(t *testing.T) {
	fake := fakeRoleImageProvider{missing: map[string]bool{"werewolf": true}}
	v, err := NewRoleCardView(newRoleTestRenderer(t), fake, game.RoleWolf, 1)
	if !errors.Is(err, ErrRoleImageNotAvailable) {
		t.Fatalf("NewRoleCardView error = %v, want ErrRoleImageNotAvailable", err)
	}
	if v.Seat != 0 || len(v.Image) != 0 || v.MIME != "" || v.Caption != "" {
		t.Errorf("视图 = %+v, want 零值", v)
	}
}

// TestRoleCardViewUnsupportedRole 验证非法角色返回明确错误且不 panic。
func TestRoleCardViewUnsupportedRole(t *testing.T) {
	_, err := NewRoleCardView(newRoleTestRenderer(t), fakeRoleImageProvider{}, game.RoleUnknown, 1)
	if err == nil {
		t.Fatal("NewRoleCardView(RoleUnknown) error = nil, want 明确错误")
	}
}

// TestRoleCardCaptionBoundary 验证 Caption 超长（>1024）被拒绝，
// 1024 边界内通过。
func TestRoleCardCaptionBoundary(t *testing.T) {
	if err := ValidateRoleCardCaption(strings.Repeat("好", 1024)); err != nil {
		t.Errorf("ValidateRoleCardCaption(1024) error = %v, want nil", err)
	}
	if !errors.Is(ValidateRoleCardCaption(strings.Repeat("好", 1025)), ErrCaptionTooLong) {
		t.Errorf("ValidateRoleCardCaption(1025) error, want ErrCaptionTooLong")
	}
}

// TestRoleConfirmTexts 验证确认提示与已确认文案经 i18n 新增 key 渲染。
func TestRoleConfirmTexts(t *testing.T) {
	r := newRoleTestRenderer(t)

	prompt, err := RoleConfirmPrompt(r)
	if err != nil {
		t.Fatalf("RoleConfirmPrompt error = %v, want nil", err)
	}
	if !strings.Contains(prompt, "身份确认") || !strings.Contains(prompt, "尚未确认") {
		t.Errorf("确认提示 = %q, want 含「身份确认」与「尚未确认」", prompt)
	}

	done, err := RoleConfirmDone(r)
	if err != nil {
		t.Fatalf("RoleConfirmDone error = %v, want nil", err)
	}
	if !strings.Contains(done, "你已确认身份") {
		t.Errorf("已确认文案 = %q, want 含「你已确认身份」", done)
	}
}
