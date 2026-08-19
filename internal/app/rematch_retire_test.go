package app

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestRematchRetiresActor 是 B3 红测：结算后点「再来一局」回大厅时，原局
// Actor 必须退役（lr.actor == nil），房间重新交回 /newgame 周期——否则
// SweepIdle 因 actor != nil 永久跳过该房间，Actor goroutine/Timer 泄漏。
func TestRematchRetiresActor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var logBuf bytes.Buffer
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &logBuf))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	defer func() {
		if t.Failed() {
			t.Logf("---- wiring log ----\n%s", logBuf.String())
		}
	}()
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 64)
	defer func() { _ = sched.Close(ctx) }()
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	b := &e2eBoard{t: t, ctx: ctx, w: w, db: db}

	// 建房 + 满员 + 开局（同 e2e 主测试）
	b.text(5001, 1, "/newgame GAME01")
	b.roomID = "GAME01"
	for i := 2; i <= 6; i++ {
		b.text(5000+int64(i), 10+i, "/join GAME01")
	}
	b.click(5001, "start_game", "")

	// 快进到结算（复用 e2e 主流程推进）
	if s := b.st(); s.Phase != game.PhaseDeal {
		t.Fatalf("开局后 phase = %v, want deal", s.Phase)
	}
	fastForwardToSettlement(t, b)

	if s := b.st(); s.Phase != game.PhaseSettlement {
		t.Fatalf("快进后 phase = %v, want settlement", s.Phase)
	}

	// 结算时 Actor 仍应存活（局内推进需要）
	if lr, ok := w.reg.get(b.roomID); !ok || lr.actor == nil {
		t.Fatal("结算阶段 Actor 不应已退役")
	}

	// 再来一局：回大厅 → Actor 必须退役
	b.click(5001, "rematch", "")
	if s := b.st(); s.Phase != game.PhaseLobby {
		t.Fatalf("Rematch 后 phase = %v, want lobby", s.Phase)
	}
	lr, ok := w.reg.get(b.roomID)
	if !ok {
		t.Fatal("Rematch 后房间应保留在注册表")
	}
	if lr.actor != nil {
		t.Fatal("Rematch 回大厅后 Actor 仍在运行（孤儿 Actor 泄漏源）")
	}

	// 退役后房间仍可用：窗口内再开局被领域拒绝（ErrRematchWindowOpen
	// 是预期，证明新 Actor 路径畅通——若 Actor 未退役会走旧 Dispatch 路径）。
	// 窗口结束后可正常再开局；这里不等待 15 秒，只验证退役语义本身。
	if lr, ok := w.reg.get(b.roomID); !ok || lr.actor != nil {
		t.Fatal("Rematch 后 Actor 应已退役")
	}
}

// TestStartGameRejectedRetiresActor 是 B3 红测：开局被领域拒绝（人数不足/
// 退出窗口未过）时，刚绑定的 Actor 必须退役——否则大厅房间挂着
// actor != nil，SweepIdle 永久跳过它，Actor goroutine 泄漏。
func TestStartGameRejectedRetiresActor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var logBuf bytes.Buffer
	w, err := NewWiring(ctx, testConfig(), mustLogger(t, &logBuf))
	if err != nil {
		t.Fatalf("NewWiring: %v", err)
	}
	sched := outbox.NewScheduler(func(ctx context.Context, msg outbox.Message) error { return nil }, 64)
	defer func() { _ = sched.Close(ctx) }()
	if err := w.Attach(db, sched); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	b := &e2eBoard{t: t, ctx: ctx, w: w, db: db}
	b.text(5001, 1, "/newgame GAME01")
	b.roomID = "GAME01"
	// 只有 1 人（需 6 人）：start_game 必被 ErrRoomNotFull 拒绝
	b.click(5001, "start_game", "")

	lr, ok := w.reg.get(b.roomID)
	if !ok {
		t.Fatal("房间应在注册表")
	}
	if lr.actor != nil {
		t.Fatal("开局被拒后 Actor 仍在运行（泄漏：SweepIdle 将永久跳过该房间）")
	}
}

// fastForwardToSettlement 以主 e2e 相同的真实交互推进当前局到结算。
func fastForwardToSettlement(t *testing.T, b *e2eBoard) {
	t.Helper()
	// 发牌确认（全员 confirm_role → night）
	for i := 1; i <= 6; i++ {
		b.click(b.userOf(game.Seat(i)), "confirm_role", "")
	}
	for round := 0; round < 6 && b.st().Phase != game.PhaseSettlement; round++ {
		st := b.st()
		switch st.Phase {
		case game.PhaseNight:
			wolves := b.seatsOfRole(game.RoleWolf)
			villagers := aliveSeatsOfRole(b, game.RoleVillager)
			if len(villagers) == 0 {
				villagers = aliveSeatsOfRole(b, game.RoleSeer)
			}
			target := villagers[0]
			for _, ws := range wolves {
				b.click(b.userOf(ws), "wolf_vote", fmt.Sprint(target))
				b.click(b.userOf(ws), "wolf_confirm", "")
			}
			witch := b.seatsOfRole(game.RoleWitch)[0]
			b.click(b.userOf(witch), "witch_save", "no")
			b.click(b.userOf(witch), "witch_confirm", "")
			b.click(b.userOf(witch), "witch_poison", "abstain")
			b.click(b.userOf(witch), "witch_confirm", "")
			seer := b.seatsOfRole(game.RoleSeer)[0]
			b.click(b.userOf(seer), "seer_check", fmt.Sprint(wolves[0]))
			b.click(b.userOf(seer), "seer_confirm", "")
		case game.PhaseDaySpeech:
			order := st.Day.SpeechOrder
			for idx := 0; idx < len(order); idx++ {
				speaker := order[idx]
				b.text(b.userOf(speaker), 200+idx, fmt.Sprintf("我是%d号。", speaker))
				b.click(b.userOf(speaker), "end_speech", "")
			}
		case game.PhaseDayVote:
			// 投票放逐狼（好人多数）→ 遗言 → 进下一夜
			wolves := aliveSeatsOfRole(b, game.RoleWolf)
			if len(wolves) == 0 {
				t.Fatal("day_vote 无存活狼")
			}
			alive := aliveSeatSlice(st)
			var good game.Seat
			for _, s := range alive {
				if b.roleOf(s) != game.RoleWolf {
					good = s
					break
				}
			}
			exiled := wolves[0]
			for _, seat := range alive {
				target := exiled
				if b.roleOf(seat) == game.RoleWolf {
					target = good
				}
				b.click(b.userOf(seat), "vote", fmt.Sprint(target))
				b.click(b.userOf(seat), "vote_confirm", "")
			}
			// 遗言窗口（不报身份模式）：被放逐者发遗言后进夜
			b.text(b.userOf(exiled), 400, "我是被放逐的，再见！")
		default:
			// deal/settlement 等阶段无需手动推进
		}
	}
	if s := b.st(); s.Phase != game.PhaseSettlement {
		t.Fatalf("6 轮推进后仍未结算：phase = %v", s.Phase)
	}
}

func aliveSeatsOfRole(b *e2eBoard, role game.Role) []game.Seat {
	var out []game.Seat
	for _, p := range b.st().Players {
		if p.Role == role && !p.Dead {
			out = append(out, p.Seat)
		}
	}
	return out
}
