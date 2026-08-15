package game

import (
	"errors"
	"fmt"
	"testing"
)

// countRoles 统计牌组中各角色数量。
func countRoles(roles []Role) map[Role]int {
	counts := map[Role]int{}
	for _, r := range roles {
		counts[r]++
	}
	return counts
}

// seqRNG 返回固定整数序列并记录每次 Intn 的上限，用于确定性复现
// 与 Fisher–Yates 无越界断言；序列耗尽后返回错误。
type seqRNG struct {
	seq    []int
	calls  int
	bounds []int
}

func (r *seqRNG) Intn(n int) (int, error) {
	if r.calls >= len(r.seq) {
		return 0, fmt.Errorf("seq rng exhausted")
	}
	v := r.seq[r.calls] % n
	r.bounds = append(r.bounds, n)
	r.calls++
	return v, nil
}

// errRNG 恒返回错误，用于验证错误传播。
type errRNG struct{}

func (errRNG) Intn(int) (int, error) {
	return 0, errors.New("rng down")
}

// TestDeckStandardComposition 验证标准牌组构成与数量。
func TestDeckStandardComposition(t *testing.T) {
	deck := StandardDeck()
	if len(deck) != MVPPlayerCount {
		t.Fatalf("len(StandardDeck()) = %d, want %d", len(deck), MVPPlayerCount)
	}
	counts := countRoles(deck)
	want := map[Role]int{
		RoleWolf:     MVPWolfCount,
		RoleSeer:     MVPSeerCount,
		RoleWitch:    MVPWitchCount,
		RoleVillager: MVPVillagerCount,
	}
	for role, n := range want {
		if counts[role] != n {
			t.Errorf("角色 %s 数量 = %d, want %d", role, counts[role], n)
		}
	}
	if err := ValidateDeck(deck); err != nil {
		t.Errorf("StandardDeck() 未通过 ValidateDeck: %v", err)
	}
}

// TestDeckOnePerPlayer 验证洗牌后每人恰好一张（长度与多重集合不变）。
func TestDeckOnePerPlayer(t *testing.T) {
	deck := StandardDeck()
	if err := Shuffle(deck, &seqRNG{seq: []int{3, 1, 0, 2, 4}}); err != nil {
		t.Fatalf("Shuffle error = %v, want nil", err)
	}
	if len(deck) != MVPPlayerCount {
		t.Fatalf("洗牌后 len = %d, want %d", len(deck), MVPPlayerCount)
	}
	counts := countRoles(deck)
	want := map[Role]int{
		RoleWolf:     MVPWolfCount,
		RoleSeer:     MVPSeerCount,
		RoleWitch:    MVPWitchCount,
		RoleVillager: MVPVillagerCount,
	}
	for role, n := range want {
		if counts[role] != n {
			t.Errorf("洗牌后角色 %s 数量 = %d, want %d", role, counts[role], n)
		}
	}
}

// TestDeckReproducibleWithSameFixture 验证同一确定性随机源序列可复现。
func TestDeckReproducibleWithSameFixture(t *testing.T) {
	seq := []int{3, 1, 0, 2, 4}
	shuffleOnce := func() []Role {
		deck := StandardDeck()
		if err := Shuffle(deck, &seqRNG{seq: seq}); err != nil {
			t.Fatalf("Shuffle error = %v, want nil", err)
		}
		return deck
	}
	first := shuffleOnce()
	second := shuffleOnce()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("同 seed 序列不可复现：第 %d 张 %v vs %v", i, first[i], second[i])
		}
	}
}

// TestDeckFisherYatesWithinBounds 验证 Fisher–Yates 调用上限为
// 6,5,4,3,2（无越界），且空/单元素牌组不调用随机源。
func TestDeckFisherYatesWithinBounds(t *testing.T) {
	rng := &seqRNG{seq: []int{1, 2, 3, 4, 5}}
	deck := StandardDeck()
	if err := Shuffle(deck, rng); err != nil {
		t.Fatalf("Shuffle error = %v, want nil", err)
	}
	wantBounds := []int{6, 5, 4, 3, 2}
	if len(rng.bounds) != len(wantBounds) {
		t.Fatalf("Intn 调用次数 = %d, want %d", len(rng.bounds), len(wantBounds))
	}
	for i, b := range rng.bounds {
		if b != wantBounds[i] {
			t.Errorf("第 %d 次 Intn 上限 = %d, want %d", i, b, wantBounds[i])
		}
	}

	r2 := &seqRNG{seq: []int{0}}
	if err := Shuffle(nil, r2); err != nil {
		t.Errorf("空牌组 Shuffle error = %v, want nil", err)
	}
	if r2.calls != 0 {
		t.Errorf("空牌组触发了 %d 次随机源调用, want 0", r2.calls)
	}
	if err := Shuffle([]Role{RoleWolf}, r2); err != nil {
		t.Errorf("单元素牌组 Shuffle error = %v, want nil", err)
	}
	if r2.calls != 0 {
		t.Errorf("单元素牌组触发了 %d 次随机源调用, want 0", r2.calls)
	}
}

// TestDeckRNGErrorPropagates 验证随机源错误向上传播且不 panic。
func TestDeckRNGErrorPropagates(t *testing.T) {
	deck := StandardDeck()
	if err := Shuffle(deck, errRNG{}); err == nil {
		t.Error("Shuffle with failing RNG = nil error, want error")
	}
}

// TestDeckCryptoRNG 验证生产随机源采样落在 [0, n) 内且拒绝非法参数。
func TestDeckCryptoRNG(t *testing.T) {
	var rng CryptoRNG
	for i := 0; i < 200; i++ {
		for _, n := range []int{2, 6, 31} {
			v, err := rng.Intn(n)
			if err != nil {
				t.Fatalf("CryptoRNG.Intn(%d) error = %v, want nil", n, err)
			}
			if v < 0 || v >= n {
				t.Fatalf("CryptoRNG.Intn(%d) = %d, want [0,%d)", n, v, n)
			}
		}
	}
	if _, err := rng.Intn(0); err == nil {
		t.Error("CryptoRNG.Intn(0) = nil error, want error")
	}
	if _, err := rng.Intn(-1); err == nil {
		t.Error("CryptoRNG.Intn(-1) = nil error, want error")
	}
	if _, err := rng.Intn(1<<32 + 1); err == nil {
		t.Error("CryptoRNG.Intn(1<<32+1) = nil error, want error（超过 uint32 采样上限）")
	}
}
