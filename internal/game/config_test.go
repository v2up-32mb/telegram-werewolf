package game

import "testing"

// validRoles 返回 6 人局标准牌组：2 狼、预言家、女巫、2 平民。
func validRoles() []Role {
	return []Role{RoleWolf, RoleWolf, RoleSeer, RoleWitch, RoleVillager, RoleVillager}
}

// TestConfigValid 验证 6 人、无 AI、标准牌组、合法胜利模式的配置通过校验。
func TestConfigValid(t *testing.T) {
	c := GameConfig{
		PlayerCount: 6,
		Roles:       validRoles(),
		Victory:     VictorySlaughter,
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("标准配置 Validate() error = %v, want nil", err)
	}
}

// TestConfigRejectsWrongDeck 验证牌组构成错误必须被拒绝。
func TestConfigRejectsWrongDeck(t *testing.T) {
	cases := []struct {
		name  string
		roles []Role
	}{
		{"狼人过多", []Role{RoleWolf, RoleWolf, RoleWolf, RoleWitch, RoleVillager, RoleVillager}},
		{"缺少预言家", []Role{RoleWolf, RoleWolf, RoleWitch, RoleWitch, RoleVillager, RoleVillager}},
		{"缺少女巫", []Role{RoleWolf, RoleWolf, RoleSeer, RoleVillager, RoleVillager, RoleVillager}},
		{"平民数错误", []Role{RoleWolf, RoleWolf, RoleSeer, RoleWitch, RoleVillager, RoleSeer}},
		{"牌组长度错误", []Role{RoleWolf, RoleWolf, RoleSeer, RoleWitch, RoleVillager}},
	}
	for _, tc := range cases {
		c := GameConfig{PlayerCount: 6, Roles: tc.roles}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", tc.name)
		}
	}
}

// TestConfigRejectsBadPlayerCount 验证 MVP 拒绝 6 人以外的其他人数。
func TestConfigRejectsBadPlayerCount(t *testing.T) {
	for _, n := range []int{5, 7, 0} {
		c := GameConfig{PlayerCount: n}
		if err := c.Validate(); err == nil {
			t.Errorf("PlayerCount=%d: Validate() = nil, want error", n)
		}
	}
}

// TestConfigRejectsAI 验证 MVP 拒绝 AI 席位。
func TestConfigRejectsAI(t *testing.T) {
	c := GameConfig{PlayerCount: 6, Roles: validRoles(), UseAI: true}
	if err := c.Validate(); err == nil {
		t.Error("UseAI=true: Validate() = nil, want error")
	}
}

// TestHostSeatIsOne 验证房主座位固定为 1。
func TestHostSeatIsOne(t *testing.T) {
	if HostSeat != 1 {
		t.Errorf("HostSeat = %d, want 1", HostSeat)
	}
	if !Seat(1).Valid() {
		t.Error("Seat(1).Valid() = false, want true")
	}
}

// TestSeatBounds 验证有效座位严格为 1～6，0 与 7 均非法。
func TestSeatBounds(t *testing.T) {
	for s := Seat(1); s <= 6; s++ {
		if !s.Valid() {
			t.Errorf("Seat(%d).Valid() = false, want true", s)
		}
	}
	for _, bad := range []Seat{0, 7, -1} {
		if bad.Valid() {
			t.Errorf("Seat(%d).Valid() = true, want false", bad)
		}
	}
}
