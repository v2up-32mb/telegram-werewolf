package telegram

import (
	"reflect"
	"strings"
	"testing"
)

// 结算战报与大厅控制消息视图测试（docs/阶段消息设计.md §15/§16）：
// 永久结算战报不含任何按钮；大厅控制为独立临时消息，含「再来一局」
//（仅房主）/「退出房间」/「查看房间面板」三枚按钮；控制按钮与既有
// 普通按钮/房主管理按钮 key 无冲突；非房主不渲染「再来一局」。

// TestResultReportHasNoButtons 验证永久战报不挂任何按钮（docs §15：
// 不把大厅操作按钮挂在永久战报上）。
func TestResultReportHasNoButtons(t *testing.T) {
	rt := reflect.TypeOf(ResultReport{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		if strings.Contains(name, "button") {
			t.Errorf("ResultReport 含按钮字段 %s（永久战报不得挂按钮）", rt.Field(i).Name)
		}
	}
}

// TestLobbyControlButtons 验证独立临时大厅控制消息：房主含三枚按钮
// （再来一局/退出房间/查看房间面板），非房主不含「再来一局」。
func TestLobbyControlButtons(t *testing.T) {
	host := NewLobbyControl(true)
	var keys []string
	for _, b := range host.Buttons {
		keys = append(keys, b.Key)
		if b.Label == "" {
			t.Errorf("按钮 %q 缺文案", b.Key)
		}
	}
	want := []string{ResultButtonRematch, ResultButtonExit, ResultButtonPanel}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("房主控制按钮 = %v, want %v", keys, want)
	}

	guest := NewLobbyControl(false)
	if len(guest.Buttons) != 2 {
		t.Fatalf("非房主控制按钮数 = %d, want 2", len(guest.Buttons))
	}
	if guest.Buttons[0].Key != ResultButtonExit || guest.Buttons[1].Key != ResultButtonPanel {
		t.Errorf("非房主控制按钮 = %+v, want 退出房间/查看房间面板", guest.Buttons)
	}
}

// TestLobbyControlKeysDisjointFromExistingButtons 验证控制消息按钮 key
// 与既有普通按钮（lobby.*）及房主管理按钮（host.*）无冲突。
func TestLobbyControlKeysDisjointFromExistingButtons(t *testing.T) {
	existing := map[string]bool{
		LobbyButtonStart:        true,
		LobbyButtonSettings:     true,
		LobbyButtonDismiss:      true,
		HostButtonDissolveVote:  true,
		HostButtonKickVote:      true,
		HostButtonForceDissolve: true,
	}
	for _, k := range []string{ResultButtonRematch, ResultButtonExit, ResultButtonPanel} {
		if existing[k] {
			t.Errorf("控制消息按钮 key %q 与既有按钮冲突", k)
		}
	}
}
