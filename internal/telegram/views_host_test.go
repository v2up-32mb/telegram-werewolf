package telegram

import (
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// 房主控制面板视图测试（docs 游戏流程设计.md §房主控制面板 97-98）：
// 房主额外拥有一组管理按钮（投票踢人/强制解散/投票解散），与普通游戏
// 操作按钮分开呈现；本层只描述渲染输入，不执行 Telegram 绘制。

// TestHostGovernancePanelStructure 验证房主控制面板字段齐全：
// 当前阶段、存活人数与三枚管理按钮（key + label）。
func TestHostGovernancePanelStructure(t *testing.T) {
	panel := HostGovernancePanel{
		Phase:      game.PhaseNight,
		AliveCount: 5,
		Buttons: []HostGovernanceButton{
			{Key: HostButtonDissolveVote, Label: "投票解散"},
			{Key: HostButtonKickVote, Label: "投票踢人"},
			{Key: HostButtonForceDissolve, Label: "强制解散"},
		},
	}
	if panel.Phase != game.PhaseNight {
		t.Errorf("Phase = %v, want PhaseNight", panel.Phase)
	}
	if panel.AliveCount != 5 {
		t.Errorf("AliveCount = %d, want 5", panel.AliveCount)
	}
	if len(panel.Buttons) != 3 {
		t.Fatalf("管理按钮数 = %d, want 3", len(panel.Buttons))
	}
	keys := map[string]string{}
	for _, b := range panel.Buttons {
		keys[b.Key] = b.Label
	}
	for _, want := range []struct {
		key   string
		label string
	}{
		{HostButtonDissolveVote, "投票解散"},
		{HostButtonKickVote, "投票踢人"},
		{HostButtonForceDissolve, "强制解散"},
	} {
		if keys[want.key] != want.label {
			t.Errorf("管理按钮 %q = %q, want %q", want.key, keys[want.key], want.label)
		}
	}
}

// TestHostGovernanceButtonsSeparateFromNormal 验证房主管理按钮与普通
// 游戏/大厅操作按钮分开展示：管理按钮 key 相互独立，且不与既有普通
// 按钮 key（lobby.start/settings/dismiss）冲突。
func TestHostGovernanceButtonsSeparateFromNormal(t *testing.T) {
	host := []string{HostButtonDissolveVote, HostButtonKickVote, HostButtonForceDissolve}
	seen := map[string]bool{}
	for _, k := range host {
		if seen[k] {
			t.Fatalf("管理按钮 key 重复：%q", k)
		}
		seen[k] = true
	}

	normal := []string{LobbyButtonStart, LobbyButtonSettings, LobbyButtonDismiss}
	for _, k := range host {
		for _, n := range normal {
			if k == n {
				t.Fatalf("管理按钮 %q 与普通按钮冲突", k)
			}
		}
	}
}
