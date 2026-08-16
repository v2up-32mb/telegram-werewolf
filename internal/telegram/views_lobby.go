package telegram

import (
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// 房间面板与邀请消息的视图结构（docs/游戏流程设计.md §一.9 房间面板、
// §二.1 加入入口、§邀请链接失效提示）。本层只描述渲染输入，
// 不执行 Telegram 绘制；MarkdownV2 转义与按钮回调接线属后续任务。

// LobbyMember 是房间面板成员列表的一行。
type LobbyMember struct {
	// Seat 是座位号。
	Seat game.Seat
	// Nickname 是游戏昵称（保留玩家原始大小写，不做 fold）。
	Nickname string
	// IsHost 标记房主。
	IsHost bool
}

// LobbyButton 是房间面板的操作按钮。
type LobbyButton struct {
	// Key 是按钮稳定标识（后续接线用于回调 token）。
	Key string
	// Label 是展示文案。
	Label string
}

// 房间面板按钮 key（docs §一.9：开始游戏/房间设置/解散房间）。
const (
	LobbyButtonStart    = "lobby.start"
	LobbyButtonSettings = "lobby.settings"
	LobbyButtonDismiss  = "lobby.dismiss"
)

// LobbyPanel 是房主房间面板的渲染输入：房间状态（房间码/人数/阶段）、
// 成员列表与三枚操作按钮。
type LobbyPanel struct {
	// RoomCode 是房间码（统一大写）。
	RoomCode string
	// PlayerCount 是当前人数。
	PlayerCount int
	// MaxPlayers 是房间容量（MVP 6 席）。
	MaxPlayers int
	// Phase 是当前阶段（等待大厅/进行中）。
	Phase game.Phase
	// Members 是成员列表（房主 1 号在前）。
	Members []LobbyMember
	// Buttons 是操作按钮（开始/设置/解散）。
	Buttons []LobbyButton
}

// InviteMessage 是邀请消息的渲染输入（docs §一.4 邀请链接）：
// deep link、分享按钮与二维码合并成一条消息。
type InviteMessage struct {
	// DeepLink 是邀请深链原文（二维码内容与「分享」目标一致）。
	DeepLink string
	// ShareButton 表示是否合并分享按钮。
	ShareButton bool
	// QRPNG 是邀请二维码 PNG 字节（复用 internal/telegram/qrcode.go）。
	QRPNG []byte
}

// NewInviteMessage 生成邀请消息视图：deep link 原文、分享按钮标记
// 与二维码 PNG（经 InviteQR 渲染，空链接返回 ErrEmptyDeepLink）。
func NewInviteMessage(deepLink string, qrSize int) (InviteMessage, error) {
	png, err := InviteQR(deepLink, qrSize)
	if err != nil {
		return InviteMessage{}, err
	}
	return InviteMessage{
		DeepLink:    deepLink,
		ShareButton: true,
		QRPNG:       png,
	}, nil
}

// LinkFailureKind 是邀请链接失效原因（docs「邀请链接失效提示」）：
// 必须区分房间已过期 / 已满 / 不存在，并给出对应引导。
type LinkFailureKind int

const (
	// LinkFailureUnknown 是未分类失效。
	LinkFailureUnknown LinkFailureKind = iota
	// LinkFailureExpired 表示房间已过期/被回收。
	LinkFailureExpired
	// LinkFailureFull 表示房间已满员。
	LinkFailureFull
	// LinkFailureNotFound 表示房间不存在。
	LinkFailureNotFound
)

// Valid 报告失效分类是否为已知枚举。
func (k LinkFailureKind) Valid() bool {
	return k >= LinkFailureExpired && k <= LinkFailureNotFound
}

// LinkFailure 是一次失效链接的渲染输入：原因分类 + 房间码，
// 渲染层按分类产出对应引导（如联系房主重新要链接）。
type LinkFailure struct {
	// Kind 是失效原因分类。
	Kind LinkFailureKind
	// RoomCode 是目标房间码。
	RoomCode string
}
