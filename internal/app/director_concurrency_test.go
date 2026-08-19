package app

// I3 回归：roomDirector.rooms 裸 map 被四类 goroutine 并发访问
// （Actor onApplied、Timer speechTimeout、Telegram update、SweepIdle）
// 会触发 Go runtime 的并发 map 读写 fatal panic；且 room() 在 release
// 后会重建条目（迟到的 speechTimeout 永久留下孤儿 dirRoom）。
import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

func newDirectorOnlyWiring(t *testing.T) *Wiring {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &bytes.Buffer{}))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 64)
	t.Cleanup(func() { _ = sched.Close(ctx) })
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return w
}

// TestDirectorConcurrentMapAccess 并发压 room/bind/release：
// 不得 panic（race 检测下暴露 map 读写竞争）。
func TestDirectorConcurrentMapAccess(t *testing.T) {
	w := newDirectorOnlyWiring(t)
	d := w.director

	roomIDs := []game.RoomID{"C001", "C002", "C003", "C004"}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 读侧压力（模拟 onApplied/speechTimeout 的 room() 调用）。
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range roomIDs {
					if dr := d.room(id); dr != nil {
						_ = dr.prevPhase
					}
				}
			}
		}()
	}

	// 写侧压力（bind/release 交替）。
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				for _, id := range roomIDs {
					d.bind(id, nil)
					d.release(id)
				}
			}
		}()
	}

	time.Sleep(80 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestDirectorRoomNoRecreateAfterRelease room() 在条目被 release 删除后
// 不得重新插入：迟到的 Timer 回调（speechTimeout）触达已解散房间时，
// 语义应为“房间已不存在”，重建会留下无法再 release 的孤儿条目。
func TestDirectorRoomNoRecreateAfterRelease(t *testing.T) {
	w := newDirectorOnlyWiring(t)
	d := w.director

	id := game.RoomID("C001")
	d.bind(id, nil)
	d.release(id)

	// 迟到回调路径：speechTimeout → room()。
	d.room(id)
	if _, ok := d.rooms[id]; ok {
		t.Fatal("room() 在 release 后重建了条目（孤儿 dirRoom 泄漏）")
	}
}
