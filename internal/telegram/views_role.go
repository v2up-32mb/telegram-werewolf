package telegram

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
)

// ErrRoleImageNotAvailable 表示角色卡图片缺失（Provider 无法提供该角色
// 图片）。自动化测试只验证 Provider 行为；正式图片已随 assets 内嵌。
var ErrRoleImageNotAvailable = errors.New("telegram: role image not available")

// RoleImageProvider 是角色卡图片数据源 seam（docs/阶段消息设计.md §6.1）：
// 生产实现可基于 assets.RoleCard 适配，测试注入假 Provider。
// 角色图缺失时返回明确错误，不落在核心流程。
type RoleImageProvider interface {
	// RoleCard 返回角色卡文件名主干（werewolf/seer/witch/villager/…）
	// 对应图片字节与 MIME 类型；缺失时返回错误。
	RoleCard(name string) ([]byte, string, error)
}

// RoleCardView 是身份卡消息的渲染产物（docs §6.1：sendPhoto 图片 +
// MarkdownV2 Caption 同一条消息）。本层只描述渲染输入，不执行
// Telegram 绘制；发送与缓存交由 MediaCache（Task 13）完成。
type RoleCardView struct {
	// Seat 是身份卡所属玩家座位。
	Seat game.Seat
	// Image 是角色卡图片字节。
	Image []byte
	// MIME 是图片 MIME 类型（身份卡统一 image/jpeg）。
	MIME string
	// Caption 是身份卡 MarkdownV2 Caption（经 i18n role_card.caption
	// 渲染，动态值默认转义）。
	Caption string
}

// roleCardCaptionMessageKey 是身份卡 Caption 文案 key（i18n）。
const roleCardCaptionMessageKey = "role_card.caption"

// NewRoleCardView 构造身份卡视图：
//  1. Provider 取图（缺图返回 ErrRoleImageNotAvailable，不 panic）；
//  2. 经 i18n 渲染角色中文名（mark.role.*）与 role_card.caption；
//  3. Caption 超过 Telegram 1024 上限（§6.1）拒绝；
//  4. 渲染/取图失败均显式报错，返回零值视图。
func NewRoleCardView(r *i18n.Renderer, p RoleImageProvider, role game.Role, seat game.Seat) (RoleCardView, error) {
	if p == nil {
		return RoleCardView{}, ErrRoleImageNotAvailable
	}
	name, err := roleImageName(role)
	if err != nil {
		return RoleCardView{}, err
	}
	img, mime, err := p.RoleCard(name)
	if err != nil {
		return RoleCardView{}, ErrRoleImageNotAvailable
	}
	displayKey, err := roleDisplayNameKey(role)
	if err != nil {
		return RoleCardView{}, err
	}
	roleName, err := r.Render(displayKey, nil)
	if err != nil {
		return RoleCardView{}, fmt.Errorf("telegram: render role name: %w", err)
	}
	caption, err := r.Render(roleCardCaptionMessageKey, map[string]any{"RoleName": roleName})
	if err != nil {
		return RoleCardView{}, fmt.Errorf("telegram: render role card caption: %w", err)
	}
	if err := ValidateRoleCardCaption(caption); err != nil {
		return RoleCardView{}, err
	}
	return RoleCardView{Seat: seat, Image: img, MIME: mime, Caption: caption}, nil
}

// roleImageName 映射领域角色到角色卡图片文件名主干（assets 约定）。
func roleImageName(role game.Role) (string, error) {
	switch role {
	case game.RoleWolf:
		return "werewolf", nil
	case game.RoleSeer:
		return "seer", nil
	case game.RoleWitch:
		return "witch", nil
	case game.RoleVillager:
		return "villager", nil
	default:
		return "", fmt.Errorf("telegram: unsupported role %v", role)
	}
}

// roleDisplayNameKey 映射领域角色到 i18n 角色名文案 key（mark.role.*）。
func roleDisplayNameKey(role game.Role) (string, error) {
	switch role {
	case game.RoleWolf:
		return "mark.role.werewolf", nil
	case game.RoleSeer:
		return "mark.role.seer", nil
	case game.RoleWitch:
		return "mark.role.witch", nil
	case game.RoleVillager:
		return "mark.role.villager", nil
	default:
		return "", fmt.Errorf("telegram: unsupported role %v", role)
	}
}

// ValidateRoleCardCaption 校验身份卡 Caption 不超过 Telegram 1024 字符
// 上限（§6.1，按 rune 数近似「解析后字符」），复用 media.go 的
// telegramCaptionMaxChars；超限返回 ErrCaptionTooLong，发送前拒绝。
func ValidateRoleCardCaption(caption string) error {
	if utf8.RuneCountInString(caption) > telegramCaptionMaxChars {
		return ErrCaptionTooLong
	}
	return nil
}

// RoleConfirmPrompt 渲染发牌确认提示文案（docs §6.2 临时确认消息初始态：
// 「尚未确认」）。消息销毁/编辑与按钮接线属适配层后续任务。
func RoleConfirmPrompt(r *i18n.Renderer) (string, error) {
	return r.Render("deal.confirm_prompt", nil)
}

// RoleConfirmDone 渲染玩家确认后的「已确认」文案（docs §6.2：
// 确认后编辑为「你已确认身份，请等待其他玩家」）。
func RoleConfirmDone(r *i18n.Renderer) (string, error) {
	return r.Render("deal.confirm_done", nil)
}
