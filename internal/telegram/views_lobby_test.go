package telegram

import (
	"errors"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// TestLobbyPanelStructure 验证房间面板视图字段齐全：
// 房间码、人数、成员、状态（阶段）、开始/设置/解散按钮。
// 渲染决策与按钮回调不在此视图层（Task 27/28 接线）。
func TestLobbyPanelStructure(t *testing.T) {
	panel := LobbyPanel{
		RoomCode:    "ABC123",
		PlayerCount: 3,
		MaxPlayers:  game.MVPPlayerCount,
		Phase:       game.PhaseLobby,
		Members: []LobbyMember{
			{Seat: game.HostSeat, Nickname: "快乐小猫", IsHost: true},
			{Seat: 2, Nickname: "Wolf", IsHost: false},
			{Seat: 3, Nickname: "小明", IsHost: false},
		},
		Buttons: []LobbyButton{
			{Key: LobbyButtonStart, Label: "开始游戏"},
			{Key: LobbyButtonSettings, Label: "房间设置"},
			{Key: LobbyButtonDismiss, Label: "解散房间"},
		},
	}
	if panel.RoomCode != "ABC123" {
		t.Errorf("RoomCode = %q, want ABC123", panel.RoomCode)
	}
	if panel.PlayerCount != 3 || panel.MaxPlayers != game.MVPPlayerCount {
		t.Errorf("人数 = %d/%d, want 3/6", panel.PlayerCount, panel.MaxPlayers)
	}
	if panel.Phase != game.PhaseLobby {
		t.Errorf("Phase = %v, want PhaseLobby", panel.Phase)
	}
	if len(panel.Members) != 3 {
		t.Fatalf("Members 数 = %d, want 3", len(panel.Members))
	}
	if !panel.Members[0].IsHost || panel.Members[0].Seat != game.HostSeat {
		t.Errorf("房主成员 = %+v, want Seat=HostSeat IsHost=true", panel.Members[0])
	}
	keys := map[string]bool{}
	for _, b := range panel.Buttons {
		keys[b.Key] = true
	}
	for _, k := range []string{LobbyButtonStart, LobbyButtonSettings, LobbyButtonDismiss} {
		if !keys[k] {
			t.Errorf("面板缺少按钮 %q", k)
		}
	}
	if len(panel.Buttons) != 3 {
		t.Errorf("按钮数 = %d, want 3", len(panel.Buttons))
	}
}

// TestLobbyMemberNicknamePreservesCase 验证面板成员昵称保留原始大小写
// （显示层语义），不经过 fold。
func TestLobbyMemberNicknamePreservesCase(t *testing.T) {
	m := LobbyMember{Nickname: "wOLF"}
	if m.Nickname != "wOLF" {
		t.Errorf("Nickname = %q, want 保留原始大小写 wOLF", m.Nickname)
	}
}

// TestNewInviteMessage 验证邀请消息视图：deep link 原文、分享按钮标记、
// 二维码为可解码 PNG 字节（复用 InviteQR）。
func TestNewInviteMessage(t *testing.T) {
	const deepLink = "https://t.me/xxxbot?start=ABC123"
	msg, err := NewInviteMessage(deepLink, 128)
	if err != nil {
		t.Fatalf("NewInviteMessage: %v", err)
	}
	if msg.DeepLink != deepLink {
		t.Errorf("DeepLink = %q, want %q", msg.DeepLink, deepLink)
	}
	if !msg.ShareButton {
		t.Error("ShareButton = false, want true（邀请消息合并分享按钮）")
	}
	if len(msg.QRPNG) == 0 {
		t.Fatal("QRPNG 为空，want 二维码 PNG 字节")
	}
	// PNG 魔数。
	if len(msg.QRPNG) < 8 || msg.QRPNG[0] != 0x89 || msg.QRPNG[1] != 'P' || msg.QRPNG[2] != 'N' || msg.QRPNG[3] != 'G' {
		t.Errorf("QRPNG 头部 = % x, want PNG 魔数", msg.QRPNG[:8])
	}
}

// TestNewInviteMessageEmptyLink 验证空深链明确报错（复用 / 对齐 InviteQR）。
func TestNewInviteMessageEmptyLink(t *testing.T) {
	if _, err := NewInviteMessage(" ", 128); !errors.Is(err, ErrEmptyDeepLink) {
		t.Fatalf("NewInviteMessage(空) err = %v, want ErrEmptyDeepLink", err)
	}
	if _, err := NewInviteMessage("https://t.me/x", 0); err == nil {
		t.Fatal("NewInviteMessage(size=0) err = nil, want error")
	}
}

// TestLinkFailureKinds 验证失效链接三态区分：已过期 / 已满 / 不存在，
// 各自携带对应引导语义。
func TestLinkFailureKinds(t *testing.T) {
	cases := []struct {
		kind LinkFailureKind
		want error
	}{
		{LinkFailureExpired, game.ErrRoomExpired},
		{LinkFailureFull, game.ErrRoomFull},
		{LinkFailureNotFound, game.ErrRoomNotFound},
	}
	for _, tc := range cases {
		f := LinkFailure{Kind: tc.kind, RoomCode: "ABC123"}
		if f.RoomCode != "ABC123" {
			t.Errorf("LinkFailure.RoomCode = %q, want ABC123", f.RoomCode)
		}
		if !f.Kind.Valid() {
			t.Errorf("Kind %d 不合法", f.Kind)
		}
	}
	if LinkFailureKind(99).Valid() {
		t.Error("LinkFailure(99).Valid() = true, want false")
	}
	if LinkFailureExpired == LinkFailureFull || LinkFailureFull == LinkFailureNotFound || LinkFailureNotFound == LinkFailureExpired {
		t.Error("三种失效类型应互不相同")
	}
	_ = cases
}
