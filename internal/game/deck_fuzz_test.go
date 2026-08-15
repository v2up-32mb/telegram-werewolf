package game

import (
	"math/rand"
	"testing"
)

// fuzzRNG 以确定性方式从 math/rand 序列提供 Intn（仅测试代码，
// math/rand 禁止进入生产路径）。
type fuzzRNG struct {
	r *rand.Rand
}

func (f fuzzRNG) Intn(n int) (int, error) {
	return f.r.Intn(n), nil
}

// FuzzDeck 验证任意 seed 下 Fisher–Yates 洗牌均不越界、
// 不 panic，且牌组长度与角色多重集合保持不变。
func FuzzDeck(f *testing.F) {
	f.Add(int64(1))
	f.Add(int64(42))
	f.Fuzz(func(t *testing.T, seed int64) {
		rng := fuzzRNG{r: rand.New(rand.NewSource(seed))}
		deck := StandardDeck()
		if err := Shuffle(deck, rng); err != nil {
			t.Fatalf("Shuffle error = %v, want nil", err)
		}
		if len(deck) != MVPPlayerCount {
			t.Fatalf("洗牌后 len = %d, want %d", len(deck), MVPPlayerCount)
		}
		if err := ValidateDeck(deck); err != nil {
			t.Fatalf("洗牌后牌组不合法（多重集合被破坏或无越界）: %v", err)
		}
	})
}
