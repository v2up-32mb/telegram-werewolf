// Package assets 持有 go:embed 静态资源（角色卡图片）。
package assets

import (
	"embed"
	"fmt"
)

// RoleCards 内嵌角色卡图片。
//
// 磁盘实况为 6 张真实 JFIF .jpg（提交 9ba1fe0），因此 embed 模式使用
// role-cards/*.jpg；实施计划中「werewolf.png」等 .png 命名与磁盘不符，
// 以磁盘为准（docs/角色卡片.md 的目录树同为 .jpg）。README.md 不在
// go:embed 模式内。hunter/guard 图片已随资源提交，一并内嵌供后续阶段使用。
//
//go:embed role-cards/*.jpg
var RoleCards embed.FS

// RoleCard 返回指定角色卡图片字节与 MIME 类型。
//
// name 为角色卡文件名主干：werewolf（狼人）、seer（预言家）、witch（女巫）、
// villager（平民）、hunter（猎人）、guard（守卫），对应
// docs/角色卡片.md 的角色命名约定。
func RoleCard(name string) ([]byte, string, error) {
	b, err := RoleCards.ReadFile("role-cards/" + name + ".jpg")
	if err != nil {
		return nil, "", fmt.Errorf("assets: read role card %q: %w", name, err)
	}
	return b, "image/jpeg", nil
}
