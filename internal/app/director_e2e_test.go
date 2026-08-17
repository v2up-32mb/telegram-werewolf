package app

// B1-d 验收：完整一局经生产接线（Wiring）驱动——建房→满员→发牌确认→
// 夜间（狼刀/女巫/验人）→白天（死讯/麦序发言/投票）→遗言→第 2 夜→结算
// →再来一局。所有交互经真实文本处理/回调动作处理（同步驱动，绕开
// Long Polling 异步以保持确定性；Router.DispatchAction 已由单元测试覆盖）。
import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// e2eBoard 是导演完整局测试的驱动板。
type e2eBoard struct {
	t      *testing.T
	ctx    context.Context
	w      *Wiring
	db     *sql.DB
	seq    int64
	roomID game.RoomID
}

// st 返回房间当前状态（liveRegistry 快照）。
func (b *e2eBoard) st() game.State {
	lr, ok := b.w.reg.get(b.roomID)
	if !ok {
		b.t.Fatalf("房间 %s 不在 liveRegistry", b.roomID)
	}
	return lr.st
}

// text 发送一条文本消息（/newgame /join 等命令或发言/遗言）。
func (b *e2eBoard) text(user int64, messageID int, content string) {
	b.seq++
	u := telegram.Update{
		UpdateID: b.seq, ReceivedAt: time.Now(),
		Message: &telegram.IncomingMessage{MessageID: messageID, ChatID: user, UserID: user, Text: content},
	}
	if err := b.w.TextHandler().HandleText(b.ctx, u); err != nil {
		b.t.Fatalf("text(%s) 处理失败: %v", content, err)
	}
}

// click 发起一次回调动作（真实 callbackActionHandler：token → 命令/导演信号）。
func (b *e2eBoard) click(user int64, action, target string) {
	b.seq++
	st := b.st()
	tok, err := b.w.IssueButton(game.UserID(user), action, target, st.Phase, st.PhaseVersion)
	if err != nil {
		b.t.Fatalf("IssueButton(%s): %v", action, err)
	}
	act := telegram.CallbackAction{
		UpdateID: b.seq, Owner: game.UserID(user), Action: action, Target: target,
		ExpectedPhase: st.Phase, PhaseVersion: st.PhaseVersion, ReceivedAt: time.Now(),
	}
	_ = tok
	if err := b.w.ActionHandler().Handle(b.ctx, act); err != nil {
		b.t.Fatalf("click(%s) 处理失败: %v", action, err)
	}
}

// roleOf 返回指定座位角色。
func (b *e2eBoard) roleOf(seat game.Seat) game.Role {
	for _, p := range b.st().Players {
		if p.Seat == seat {
			return p.Role
		}
	}
	b.t.Fatalf("座位 %d 不存在", seat)
	return game.RoleUnknown
}

// userOf 返回指定座位用户 ID。
func (b *e2eBoard) userOf(seat game.Seat) int64 {
	for _, p := range b.st().Players {
		if p.Seat == seat {
			return int64(p.UserID)
		}
	}
	b.t.Fatalf("座位 %d 不存在", seat)
	return 0
}

// seatsOfRole 返回某角色全部存活座位。
func (b *e2eBoard) seatsOfRole(role game.Role) []game.Seat {
	var out []game.Seat
	for _, p := range b.st().Players {
		if p.Role == role && p.Seat.Valid() && !p.Dead {
			out = append(out, p.Seat)
		}
	}
	return out
}

// TestDirectorFullGameThroughWiring 是 B1-d 验收：完整一局打好过。
func TestDirectorFullGameThroughWiring(t *testing.T) {
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

	// ---- 建房 + 6 人满员（房主 5001 起点 1 号）----
	b.text(5001, 1, "/newgame GAME01")
	b.roomID = "GAME01"
	if got := b.st().Phase; got != game.PhaseLobby {
		t.Fatalf("建房后 phase = %v, want lobby", got)
	}
	for i := 2; i <= 6; i++ {
		b.text(5000+int64(i), 10+i, "/join GAME01")
	}
	if n := len(b.st().Players); n != 6 {
		t.Fatalf("玩家数 = %d, want 6", n)
	}

	// ---- 开始游戏：发牌确认 ----
	v0 := b.st().PhaseVersion
	b.click(5001, "start_game", "")
	st := b.st()
	if st.Phase != game.PhaseDeal {
		t.Fatalf("start 后 phase = %v, want deal", st.Phase)
	}
	if st.PhaseVersion != v0+1 {
		t.Fatalf("start 后 version = %d, want %d", st.PhaseVersion, v0+1)
	}
	// 全部确认身份（10 秒超时前）
	for i := 1; i <= 6; i++ {
		b.click(b.userOf(game.Seat(i)), "confirm_role", "")
	}
	st = b.st()
	if st.Phase != game.PhaseNight {
		t.Fatalf("全员确认后 phase = %v, want night", st.Phase)
	}
	if lr, ok := w.reg.get("GAME01"); ok {
		t.Logf("post-confirm: actor=%v wolfRound=%d version=%d wolfStarted=%v",
			lr.actor != nil, lr.st.Night.WolfRound, lr.st.PhaseVersion, w.director.room("GAME01").wolfStarted)
	}

	// ---- 第 1 夜 ----
	if st.Night.WolfRound != 1 {
		t.Fatalf("N1 WolfRound = %d, want 1（导演 BeginWolfPhase）", st.Night.WolfRound)
	}
	wolves := b.seatsOfRole(game.RoleWolf)
	if len(wolves) != 2 {
		t.Fatalf("狼人座位 = %v, want 2", wolves)
	}
	seer := b.seatsOfRole(game.RoleSeer)[0]
	witch := b.seatsOfRole(game.RoleWitch)[0]
	villagers := b.seatsOfRole(game.RoleVillager)
	target := villagers[0]

	// 狼刀村民 + 确认（两狼）
	for _, ws := range wolves {
		b.click(b.userOf(ws), "wolf_vote", fmt.Sprint(target))
		b.click(b.userOf(ws), "wolf_confirm", "")
	}
	st = b.st()
	if st.Phase != game.PhaseNight || st.Night.WolfRound != 0 {
		t.Fatalf("狼人完成后 stage = phase %v wolfRound %d, want night/0", st.Phase, st.Night.WolfRound)
	}
	if st.Night.WitchStage != game.WitchStageSave {
		t.Fatalf("女巫窗口未开启（导演 BeginWitchPhase），stage = %d", st.Night.WitchStage)
	}
	if !st.Night.WitchFirstNight {
		t.Fatalf("N1 女巫 firstNight = false, want true（首夜自救可配置项基于首夜判定）")
	}

	// 女巫：不救 → 不用毒
	b.click(b.userOf(witch), "witch_save", "no")
	b.click(b.userOf(witch), "witch_confirm", "")
	if s := b.st(); s.Night.WitchStage != game.WitchStagePoison {
		t.Fatalf("女巫确认不救后 stage = %d, want poison", s.Night.WitchStage)
	}
	b.click(b.userOf(witch), "witch_poison", "abstain")
	b.click(b.userOf(witch), "witch_confirm", "")
	if s := b.st(); s.Night.WitchStage != game.WitchStageClosed {
		t.Fatalf("女巫完成后 stage = %d, want closed", s.Night.WitchStage)
	}

	// 预言家：查验一个狼人
	b.click(b.userOf(seer), "seer_check", fmt.Sprint(wolves[0]))
	b.click(b.userOf(seer), "seer_confirm", "")
	st = b.st()
	if s := b.st(); s.Phase != game.PhaseDaySpeech {
		t.Fatalf("夜结算后 phase = %v, want day_speech", s.Phase)
	}
	// 首夜死讯：村民 target 死亡
	victimDead := false
	for _, p := range st.Players {
		if p.Seat == target && p.Dead {
			victimDead = true
		}
	}
	if !victimDead {
		t.Fatalf("村民 %d 首夜未死亡", target)
	}

	// ---- 白天发言麦序 ----
	order := st.Day.SpeechOrder
	if len(order) == 0 {
		t.Fatal("白天无麦序")
	}
	if st.Day.Speaker != order[0] {
		t.Fatalf("当前发言者 = %d, want %d", st.Day.Speaker, order[0])
	}
	// 每位存活者发言一次，然后结束发言
	for idx := 0; idx < len(order); idx++ {
		speaker := order[idx]
		if b.st().Day.Speaker != speaker {
			t.Fatalf("麦序第 %d 位应为 %d，实际 %d", idx, speaker, b.st().Day.Speaker)
		}
		b.text(b.userOf(speaker), 200+idx, fmt.Sprintf("我是%d号，请投狼。", speaker))
		if idx < len(order)-1 {
			b.click(b.userOf(speaker), "end_speech", "")
		}
	}
	// 最后一位结束发言 → BeginVote
	b.click(b.userOf(order[len(order)-1]), "end_speech", "")
	if s := b.st(); s.Phase != game.PhaseDayVote {
		t.Fatalf("发言结束 phase = %v, want day_vote", s.Phase)
	}

	// ---- 白天投票：好人投狼（4 票，狼反投好人）----
	alive := aliveSeatSlice(b.st())
	good := game.Seat(0)
	for _, seat := range alive {
		if b.roleOf(seat) != game.RoleWolf {
			good = seat
			break
		}
	}
	if good == 0 {
		t.Fatal("找不到存活的非狼玩家")
	}
	wolfTarget := game.Seat(0)
	for _, p := range st.Players {
		if p.Role == game.RoleWolf && !p.Dead {
			wolfTarget = p.Seat
		}
	}
	if wolfTarget == 0 {
		t.Fatal("找不到活狼")
	}
	for _, seat := range alive {
		user := b.userOf(seat)
		targetVote := wolfTarget
		// 狼投好人不投狼（避免狼票狼）
		if b.roleOf(seat) == game.RoleWolf {
			targetVote = good
		}
		b.click(user, "vote", fmt.Sprint(targetVote))
		b.click(user, "vote_confirm", "")
	}
	st = b.st()
	if st.Phase != game.PhaseDayVote {
		t.Fatalf("投票后 phase = %v，want day_vote（遗言或黑夜）", st.Phase)
	}
	if st.Vote.Exiled == nil || *st.Vote.Exiled != wolfTarget {
		t.Fatalf("放逐 = %v, want wolf %d", st.Vote.Exiled, wolfTarget)
	}
	// 不报身份模式：遗言窗口
	if st.Vote.Stage != game.VoteStageLastWords {
		t.Fatalf("放逐后 stage = %d, want last_words", st.Vote.Stage)
	}
	b.text(b.userOf(wolfTarget), 400, "我是狼，抱歉！")
	// 遗言提交 → 进入第 2 夜
	if s := b.st(); s.Phase != game.PhaseNight {
		t.Fatalf("遗言后 phase = %v, want night（第 2 夜）", s.Phase)
	}
	if s := b.st(); s.Night.WolfRound != 1 {
		t.Fatalf("N2 WolfRound = %d, want 1", s.Night.WolfRound)
	}

	// ---- 第 2 夜：狼刀预言家 + 女巫毒他（最后一狼）----
	wolves = b.seatsOfRole(game.RoleWolf)
	if len(wolves) != 1 {
		t.Fatalf("N2 活狼 = %v, want 1", wolves)
	}
	lastWolf := wolves[0]
	b.click(b.userOf(lastWolf), "wolf_vote", fmt.Sprint(seer))
	b.click(b.userOf(lastWolf), "wolf_confirm", "")
	b.click(b.userOf(witch), "witch_save", "no")
	b.click(b.userOf(witch), "witch_confirm", "")
	b.click(b.userOf(witch), "witch_poison", fmt.Sprint(lastWolf))
	b.click(b.userOf(witch), "witch_confirm", "")
	// 预言家窗口：空过（超时）——导演在 seer 阶段等待；用 Timeout 语义不适用，
	// 直接由女巫毒死后 ResolveNight 触发：先让 seer 查验一个任意存活目标。
	b.click(b.userOf(seer), "seer_check", fmt.Sprint(aliveSeatSlice(b.st())[0]))
	b.click(b.userOf(seer), "seer_confirm", "")

	// ---- 结算（狼全灭 → 好人胜）----
	st = b.st()
	if st.Phase != game.PhaseSettlement {
		t.Fatalf("N2 后 phase = %v, want settlement（预言家被刀+毒最后一狼）", st.Phase)
	}
	if st.Settled.Winner != game.CampGood {
		t.Fatalf("胜方 = %v, want good", st.Settled.Winner)
	}

	// ---- 结算持久化落库（I3）----
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM games`).Scan(&n); err != nil {
		t.Fatalf("games 计数: %v", err)
	}
	if n != 1 {
		t.Fatalf("games 行 = %d, want 1（结算落库）", n)
	}
	var winner string
	if err := db.QueryRowContext(ctx, `SELECT winner_camp FROM games LIMIT 1`).Scan(&winner); err != nil {
		t.Fatalf("winner_camp: %v", err)
	}
	if winner != "good" {
		t.Fatalf("winner_camp = %q, want good", winner)
	}
	var bp int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM battle_reports`).Scan(&bp); err != nil {
		t.Fatalf("battle_reports 计数: %v", err)
	}
	if bp != 1 {
		t.Fatalf("battle_reports 行 = %d, want 1", bp)
	}

	// ---- 再来一局（Rematch 回大厅，docs §结算 5/6）----
	if s := b.st(); s.Phase != game.PhaseSettlement {
		t.Fatalf("Rematch 前 phase = %v, want settlement", s.Phase)
	}
	b.click(5001, "rematch", "")
	if s := b.st(); s.Phase != game.PhaseLobby {
		t.Fatalf("Rematch 后 phase = %v, want lobby（回到等待大厅）", s.Phase)
	}
	if n := len(b.st().Players); n != 6 {
		t.Fatalf("Rematch 后玩家数 = %d, want 6（原班人马保留）", n)
	}
	// 回大厅后房主可再开局（退出窗口 15 秒后；此处仅验证状态）
	if got := b.st().Lobby.RematchReadyAt.IsZero(); got {
		t.Fatal("Rematch 后退出窗口未设置（RematchReadyAt 应为未来时刻）")
	}
}
