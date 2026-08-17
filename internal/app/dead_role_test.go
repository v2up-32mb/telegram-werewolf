package app

// I5 红测：死亡神职 2/3 假等待接线 + 不发送操作按钮
//（docs 游戏流程设计.md §夜间 6：角色在行动窗口开始前已死亡，不发送操作
// 按钮、不执行操作、也不算超时；但阶段仍按固定流程进入，等待原时长 2/3）。

import (
	"context"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestScaleDeadRoleTimers 验证死亡神职阶段计时器按 2/3 缩放。
func TestScaleDeadRoleTimers(t *testing.T) {
	fx := []game.Effect{
		game.MessageEffect{Audience: game.AudienceActor, Key: "witch.kill_reveal", Params: map[string]any{}},
		game.TimerEffect{Phase: game.PhaseNight, Duration: 15 * time.Second},
		game.TimerEffect{Phase: game.PhaseNight, Duration: 30 * time.Second},
	}
	// 对照：10 秒（15*2/3 向下取整）、20 秒（30*2/3）。
	if want := game.DeadRoleStageDuration(15 * time.Second); want != 10*time.Second {
		t.Fatalf("DeadRoleStageDuration(15s) = %v, want 10s", want)
	}
	out := scaleDeadRoleTimers(fx)
	var timers []time.Duration
	for _, e := range out {
		if te, ok := e.(game.TimerEffect); ok {
			timers = append(timers, te.Duration)
		}
	}
	if len(timers) != 2 || timers[0] != 10*time.Second || timers[1] != 20*time.Second {
		t.Fatalf("缩放后时长 = %v, want [10s 20s]（15s*2/3、30s*2/3 向下取整）", timers)
	}
}

// TestDeadViewerGetsNoButtons 验证死亡玩家不收到操作按钮（docs §夜间 6、
// §13.3 上帝视角只读行动记录）。
func TestDeadViewerGetsNoButtons(t *testing.T) {
	w, _, sched := newWiringSched(t, 8)
	defer func() { _ = sched.Close(context.Background()) }()

	st := game.State{
		RoomID: "DEAD01", Phase: game.PhaseNight, PhaseVersion: 3,
		Players: []game.Player{
			{UserID: 1, Seat: 1, Role: game.RoleWitch, Dead: true}, // 已死亡女巫
			{UserID: 2, Seat: 2, Role: game.RoleWolf},
		},
		Settings: game.DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
	v := viewerContext(st, 1) // 死者视角
	e := game.MessageEffect{
		Audience: game.AudienceActor, Key: "witch.save.prompt",
		Params: map[string]any{"save_used": false, "poison_used": false},
	}
	markup, err := w.buttonsFor(e, st, v)
	if err != nil {
		t.Fatalf("buttonsFor: %v", err)
	}
	if markup != nil {
		t.Fatal("死亡玩家仍收到操作按钮，want nil（docs §夜间 6 不发送操作按钮）")
	}

	// 存活玩家同一条消息有按钮（对照组）。
	lr := viewerContext(st, 2)
	markupAlive, err := w.buttonsFor(game.MessageEffect{Audience: game.AudienceWolf, Key: "wolf.vote", Params: map[string]any{"round": 1, "targets": []game.Seat{1, 2}}}, st, lr)
	if err != nil {
		t.Fatalf("buttonsFor(alive): %v", err)
	}
	if markupAlive == nil {
		t.Fatal("存活玩家未收到操作按钮，want 非 nil")
	}
}

// TestDeadRolePumpScalesTimerToTwoThirds 是 I5 红测：导演 pump 对死亡女巫
// 开启阶段时把计时器缩放到 2/3（防泄密假等待）。
func TestDeadRolePumpScalesTimerToTwoThirds(t *testing.T) {
	w, _, sched := newWiringSched(t, 8)
	defer func() { _ = sched.Close(context.Background()) }()
	d := newDirector(w)

	// 构造：狼阶段刚结束、女巫已死（被刀）、WolfRound=0。
	st := game.State{
		RoomID: "DEAD02", Phase: game.PhaseNight, PhaseVersion: 5,
		Players: []game.Player{
			{UserID: 1, Seat: 1, Role: game.RoleWolf},
			{UserID: 2, Seat: 2, Role: game.RoleWolf},
			{UserID: 3, Seat: 3, Role: game.RoleWitch, Dead: true},
			{UserID: 4, Seat: 4, Role: game.RoleSeer},
			{UserID: 5, Seat: 5, Role: game.RoleVillager},
			{UserID: 6, Seat: 6, Role: game.RoleVillager},
		},
		Night: game.NightState{WolfRound: 0, WitchStage: game.WitchStageClosed, SeerActive: false},
		Settings: game.DefaultRoomSettings(),
		Processed: map[string]bool{},
	}
	d.rooms["DEAD02"] = &dirRoom{wolfStarted: true}

	// 直接走 pumpkin（Need actor? pump 内部不依赖 actor；仅 onApplied 的 Adopt 需要）。
	next, fx, adv, err := d.pump("DEAD02", st)
	if err != nil {
		t.Fatalf("pump: %v", err)
	}
	if !adv {
		t.Fatal("pump 未推进（女巫阶段应开启）")
	}
	if next.Night.WitchStage != game.WitchStageSave {
		t.Fatalf("死亡女巫阶段未开启：stage = %d, want save（docs §夜间 6 阶段不可跳过）", next.Night.WitchStage)
	}
	// 计时器应为 2/3（默认其他角色 15 秒 → 10 秒）。
	var got time.Duration
	for _, e := range fx {
		if te, ok := e.(game.TimerEffect); ok && te.Phase == game.PhaseNight {
			got = te.Duration
		}
	}
	if got != 10*time.Second {
		t.Fatalf("死亡女巫阶段计时 = %v, want 10s（15s * 2/3，防泄密假等待）", got)
	}
}

// ensure storage import（编译占位）。
var _ = storage.ErrRoomNotFound