package game

import "testing"

// TestInvariantsDeck 验证牌组构成不变量：6 人局必须为 2 狼、预言家、女巫、2 平民。
func TestInvariantsDeck(t *testing.T) {
	if err := ValidateDeck(validRoles()); err != nil {
		t.Errorf("标准牌组 ValidateDeck() error = %v, want nil", err)
	}
	bad := []Role{RoleWolf, RoleWolf, RoleWolf, RoleWitch, RoleVillager, RoleVillager}
	if err := ValidateDeck(bad); err == nil {
		t.Error("狼人过多的牌组 ValidateDeck() = nil, want error")
	}
	short := []Role{RoleWolf, RoleWolf, RoleSeer, RoleWitch, RoleVillager}
	if err := ValidateDeck(short); err == nil {
		t.Error("长度不足的牌组 ValidateDeck() = nil, want error")
	}
}

// TestInvariantsPlayerCount 验证 MVP 人数不变量：只允许 6 人。
func TestInvariantsPlayerCount(t *testing.T) {
	if err := ValidatePlayerCount(6); err != nil {
		t.Errorf("ValidatePlayerCount(6) error = %v, want nil", err)
	}
	for _, n := range []int{5, 7, 0, 8} {
		if err := ValidatePlayerCount(n); err == nil {
			t.Errorf("ValidatePlayerCount(%d) = nil, want error", n)
		}
	}
}

// TestInvariantsNoAI 验证 MVP 拒绝 AI 席位。
func TestInvariantsNoAI(t *testing.T) {
	if err := ValidateNoAI(false); err != nil {
		t.Errorf("ValidateNoAI(false) error = %v, want nil", err)
	}
	if err := ValidateNoAI(true); err == nil {
		t.Error("ValidateNoAI(true) = nil, want error")
	}
}

// TestInvariantsSeatRange 验证座位范围不变量：严格为 1～6。
func TestInvariantsSeatRange(t *testing.T) {
	for s := Seat(1); s <= 6; s++ {
		if !s.Valid() {
			t.Errorf("Seat(%d).Valid() = false, want true", s)
		}
	}
	for _, bad := range []Seat{0, 7} {
		if bad.Valid() {
			t.Errorf("Seat(%d).Valid() = true, want false", bad)
		}
	}
}

// TestInvariantsHostSeat 验证房主座位不变量：恒为 1。
func TestInvariantsHostSeat(t *testing.T) {
	if HostSeat != 1 {
		t.Errorf("HostSeat = %d, want 1", HostSeat)
	}
	if HostSeat != 1 || !Seat(1).Valid() {
		t.Errorf("房主座位不变量被破坏：HostSeat=%d, Seat(1).Valid()=%v", HostSeat, Seat(1).Valid())
	}
}

// TestRoleCampInvariant 验证角色到阵营的映射不变量。
func TestRoleCampInvariant(t *testing.T) {
	if RoleWolf.Camp() != CampWolf {
		t.Errorf("RoleWolf.Camp() = %v, want %v", RoleWolf.Camp(), CampWolf)
	}
	for _, r := range []Role{RoleSeer, RoleWitch, RoleVillager} {
		if r.Camp() != CampGood {
			t.Errorf("%s.Camp() = %v, want %v", r, r.Camp(), CampGood)
		}
	}
	if RoleUnknown.Camp() != CampUnknown {
		t.Errorf("RoleUnknown.Camp() = %v, want %v", RoleUnknown.Camp(), CampUnknown)
	}
}
