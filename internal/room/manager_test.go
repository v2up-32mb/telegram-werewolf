package room

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// stubRegistry 模拟 storage unique constraint（Task 12 起由 storage 实现）：
// 可配置部分码被占用（碰撞），记录每次 Reserve 调用。
type stubRegistry struct {
	mu     sync.Mutex
	taken  map[game.RoomID]bool
	seen   []game.RoomID
	closed bool
}

func (s *stubRegistry) Reserve(_ context.Context, code game.RoomID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, code)
	if s.taken[code] {
		return false, nil
	}
	s.taken[code] = true
	return true, nil
}

// TestManagerHostOnlyOneActiveRoom 验证唯一约束：一个用户（房主）只能
// 在一个进行中房间；移除后约束释放（docs/游戏流程设计.md §一.7）。
func TestManagerHostOnlyOneActiveRoom(t *testing.T) {
	fc := newFakeClock()
	m := NewManager(ManagerOptions{
		RNG:      newScriptedRNG(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11),
		Registry: &stubRegistry{taken: map[game.RoomID]bool{}},
	})
	red := &fakeReducer{}

	r1, err := m.CreateRoom(context.Background(), 1001, fc, red)
	if err != nil {
		t.Fatalf("首次 CreateRoom error = %v, want nil", err)
	}
	if _, err := m.CreateRoom(context.Background(), 1001, fc, red); !errors.Is(err, ErrUserInRoom) {
		t.Fatalf("同 host 再次 CreateRoom err = %v, want ErrUserInRoom", err)
	}
	if err := m.Remove(r1.ID); err != nil {
		t.Fatalf("Remove error = %v, want nil", err)
	}
	if _, err := m.CreateRoom(context.Background(), 1001, fc, red); err != nil {
		t.Fatalf("移除后同 host 再创建 error = %v, want nil（唯一约束已释放）", err)
	}
}

// TestManagerHostCanCreateOnlyOne 显式验证“房主只能创建一个进行中房间”。
func TestManagerHostCanCreateOnlyOne(t *testing.T) {
	fc := newFakeClock()
	m := NewManager(ManagerOptions{
		RNG:      newScriptedRNG(repeatVals(0, 24)...),
		Registry: &stubRegistry{taken: map[game.RoomID]bool{}},
	})
	if _, err := m.CreateRoom(context.Background(), 7, fc, &fakeReducer{}); err != nil {
		t.Fatalf("首次创建 error = %v, want nil", err)
	}
	if _, err := m.CreateRoom(context.Background(), 7, fc, &fakeReducer{}); !errors.Is(err, ErrUserInRoom) {
		t.Fatalf("房主第二房间 err = %v, want ErrUserInRoom", err)
	}
}

// TestManagerCodeRetryOnReservationCollision 验证房间码与 storage 唯一约束
// 碰撞时重新生成并重试，最终得到唯一码（docs/技术选型.md §5.2）。
func TestManagerCodeRetryOnReservationCollision(t *testing.T) {
	fc := newFakeClock()
	rng := newScriptedRNG(0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1)
	reg := &stubRegistry{taken: map[game.RoomID]bool{"AAAAAA": true}}
	m := NewManager(ManagerOptions{RNG: rng, Registry: reg})
	red := &fakeReducer{}

	r, err := m.CreateRoom(context.Background(), 42, fc, red)
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	if r.ID != "BBBBBB" {
		t.Errorf("房间码 = %q, want BBBBBB（首次被占用后应重试生成）", r.ID)
	}
	if len(reg.seen) < 2 {
		t.Errorf("Reserve 调用次数 = %d, want >= 2（应发生碰撞重试）", len(reg.seen))
	}
	// 生成的码不得与已占用码重复。
	if reg.taken[r.ID] != true {
		t.Errorf("最终码 %q 未在 registry 登记", r.ID)
	}
	// RNG 序列耗尽：错误应向上传播。
	if _, err := m.CreateRoom(context.Background(), 43, fc, red); err == nil {
		t.Error("RNG 耗尽时 CreateRoom 应返回错误")
	}
}

// TestManagerCodeExhausted 验证重试耗尽返回明确错误且不登记房间。
func TestManagerCodeExhausted(t *testing.T) {
	fc := newFakeClock()
	reg := &stubRegistry{taken: map[game.RoomID]bool{"AAAAAA": true}}
	m := NewManager(ManagerOptions{
		RNG:          newScriptedRNG(repeatVals(0, 3*6)...),
		Registry:     reg,
		MaxCodeTries: 3,
	})
	if _, err := m.CreateRoom(context.Background(), 9, fc, &fakeReducer{}); !errors.Is(err, ErrCodeExhausted) {
		t.Fatalf("CreateRoom err = %v, want ErrCodeExhausted", err)
	}
}

// TestManagerConcurrentCreateUniqueCodes 验证并发创建不重复：不同用户
// 并发创建的房间码集合全部唯一。
func TestManagerConcurrentCreateUniqueCodes(t *testing.T) {
	fc := newFakeClock()
	m := NewManager(ManagerOptions{
		RNG:      newPseudoRNG(1),
		Registry: &stubRegistry{taken: map[game.RoomID]bool{}},
	})
	red := &fakeReducer{}
	const n = 50

	var wg sync.WaitGroup
	codes := make([]game.RoomID, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := m.CreateRoom(context.Background(), game.UserID(1000+i), fc, red)
			if err != nil {
				errs[i] = err
				return
			}
			codes[i] = r.ID
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("CreateRoom#%d error = %v, want nil", i, errs[i])
		}
	}
	seen := map[game.RoomID]bool{}
	for _, c := range codes {
		if c == "" {
			t.Fatal("房间码为空")
		}
		if seen[c] {
			t.Fatalf("并发创建房间码重复: %q", c)
		}
		seen[c] = true
	}
	if len(seen) != n {
		t.Errorf("唯一房间码数量 = %d, want %d", len(seen), n)
	}
}

// TestManagerGetRemoveClose 验证查询、移除与整体关闭的生命周期语义。
func TestManagerGetRemoveClose(t *testing.T) {
	fc := newFakeClock()
	vals := append(repeatVals(0, 6), repeatVals(1, 6)...)
	m := NewManager(ManagerOptions{
		RNG:      newScriptedRNG(vals...),
		Registry: &stubRegistry{taken: map[game.RoomID]bool{}},
	})
	red := &fakeReducer{}

	r1, err := m.CreateRoom(context.Background(), 1, fc, red)
	if err != nil {
		t.Fatalf("CreateRoom#1 error = %v, want nil", err)
	}
	r2, err := m.CreateRoom(context.Background(), 2, fc, red)
	if err != nil {
		t.Fatalf("CreateRoom#2 error = %v, want nil", err)
	}
	if got, err := m.Get(r1.ID); err != nil || got != r1 {
		t.Fatalf("Get(%q) = %v/%v, want 原房间/nil", r1.ID, got, err)
	}
	if _, err := m.Get("ZZZZZZ"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("Get(不存在) err = %v, want ErrRoomNotFound", err)
	}

	if err := m.Remove(r1.ID); err != nil {
		t.Fatalf("Remove error = %v, want nil", err)
	}
	if _, err := m.Get(r1.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("移除后 Get err = %v, want ErrRoomNotFound", err)
	}
	// 移除停止 Actor：后续 Dispatch 返回 ErrClosed。
	if _, err := r1.Dispatch(context.Background(), plainCmd(99, fc.Now())); !errors.Is(err, ErrClosed) {
		t.Fatalf("已移除房间 Dispatch err = %v, want ErrClosed", err)
	}
	// 重复 Remove 返回明确错误。
	if err := m.Remove(r1.ID); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("重复 Remove err = %v, want ErrRoomNotFound", err)
	}

	m.Close()
	if _, err := m.CreateRoom(context.Background(), 3, fc, red); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 后 CreateRoom err = %v, want ErrClosed", err)
	}
	if _, err := m.Get(r2.ID); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 后 Get err = %v, want ErrClosed", err)
	}
}

// TestManagerCloseStopsAllActors 验证 Close 停止所有房间 Actor。
func TestManagerCloseStopsAllActors(t *testing.T) {
	fc := newFakeClock()
	vals := append(repeatVals(0, 6), repeatVals(1, 6)...)
	m := NewManager(ManagerOptions{
		RNG:      newScriptedRNG(vals...),
		Registry: &stubRegistry{taken: map[game.RoomID]bool{}},
	})
	red := &fakeReducer{}
	r1, err := m.CreateRoom(context.Background(), 1, fc, red)
	if err != nil {
		t.Fatalf("CreateRoom error = %v, want nil", err)
	}
	m.Close()
	if _, err := r1.Dispatch(context.Background(), plainCmd(1, fc.Now())); !errors.Is(err, ErrClosed) {
		t.Fatalf("Close 后房间 Dispatch err = %v, want ErrClosed", err)
	}
}
