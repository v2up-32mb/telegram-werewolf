package storage_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
	_ "modernc.org/sqlite"
)

// pointsOf 读取用户最新积分。
func pointsOf(t *testing.T, db *sql.DB, user int64) int64 {
	t.Helper()
	var p int64
	if err := db.QueryRow(`SELECT points FROM users WHERE telegram_id = ?`, user).Scan(&p); err != nil {
		t.Fatalf("points of %d: %v", user, err)
	}
	return p
}

// TestSettlementPointRules 验证四种积分口径：胜利 +5、死亡躺赢 +2、
// 失败 0、恶意退出后阵营胜利 0 / 阵营失败 -5（docs/游戏流程设计.md §积分系统）。
func TestSettlementPointRules(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewSettlementRepository(db)

	for _, tc := range []struct {
		name       string
		user       int64
		winnerCamp game.Camp
		playerCamp game.Camp
		died       bool
		malicious  bool
		wantPoints int64
	}{
		{name: "胜利+5", user: 1001, winnerCamp: game.CampWolf, playerCamp: game.CampWolf, wantPoints: 5},
		{name: "死亡躺赢+2", user: 1002, winnerCamp: game.CampGood, playerCamp: game.CampGood, died: true, wantPoints: 2},
		{name: "失败0", user: 1003, winnerCamp: game.CampWolf, playerCamp: game.CampGood, wantPoints: 0},
		{name: "恶意退出阵营胜利0", user: 1004, winnerCamp: game.CampWolf, playerCamp: game.CampWolf, malicious: true, wantPoints: 0},
		{name: "恶意退出阵营失败-5", user: 1005, winnerCamp: game.CampGood, playerCamp: game.CampWolf, malicious: true, wantPoints: -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustUser(t, db, tc.user, "u")
			role := game.RoleVillager
			if tc.playerCamp == game.CampWolf {
				role = game.RoleWolf
			}
			result := storage.GameResult{
				RoomCode:   game.RoomID("ABCDEF"),
				Phase:      game.PhaseSettlement,
				WinnerCamp: tc.winnerCamp,
				Players: []storage.PlayerResult{{
					UserID:        game.UserID(tc.user),
					Seat:          1,
					Role:          role,
					Camp:          tc.playerCamp,
					Died:          tc.died,
					MaliciousExit: tc.malicious,
				}},
				Report: "report",
			}
			if err := repo.SettleGame(ctx, result); err != nil {
				t.Fatalf("SettleGame: %v", err)
			}
			if got := pointsOf(t, db, tc.user); got != tc.wantPoints {
				t.Errorf("积分 = %d, want %d", got, tc.wantPoints)
			}
		})
	}
}

// TestSettlementWritesStatsAndReport 验证单事务写入 games、game_players
// （is_winner 按阵营推导）、role_stats 累加与 battle_reports。
func TestSettlementWritesStatsAndReport(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewSettlementRepository(db)
	for id := int64(101); id <= 102; id++ {
		mustUser(t, db, id, "u")
	}
	rooms := storage.NewRoomRepository(db)
	if err := rooms.Create(ctx, game.RoomID("ABCDEF"), 101, "lobby"); err != nil {
		t.Fatalf("create room: %v", err)
	}
	if _, err := rooms.Join(ctx, game.RoomID("ABCDEF"), 102); err != nil {
		t.Fatalf("join room: %v", err)
	}
	result := storage.GameResult{
		RoomCode:   game.RoomID("ABCDEF"),
		Phase:      game.PhaseSettlement,
		WinnerCamp: game.CampWolf,
		Players: []storage.PlayerResult{
			{UserID: 101, Seat: 1, Role: game.RoleWolf, Camp: game.CampWolf},
			{UserID: 102, Seat: 2, Role: game.RoleVillager, Camp: game.CampGood},
		},
		Report: "胜方：狼人；翻牌：1号狼人、2号平民",
	}
	if err := repo.SettleGame(ctx, result); err != nil {
		t.Fatalf("SettleGame: %v", err)
	}

	var gameID, aborted int64
	var winnerCamp, phase string
	if err := db.QueryRow(`SELECT id, winner_camp, phase, aborted FROM games WHERE room_code='ABCDEF'`).Scan(&gameID, &winnerCamp, &phase, &aborted); err != nil {
		t.Fatalf("games row: %v", err)
	}
	if winnerCamp != "wolf" || phase != "settlement" || aborted != 0 {
		t.Errorf("games 字段 = winner=%s phase=%s aborted=%d, want wolf/settlement/0", winnerCamp, phase, aborted)
	}
	var winnerRows, totalRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_players WHERE game_id=? AND is_winner=1`, gameID).Scan(&winnerRows); err != nil {
		t.Fatalf("winner rows: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM game_players WHERE game_id=?`, gameID).Scan(&totalRows); err != nil {
		t.Fatalf("total rows: %v", err)
	}
	if winnerRows != 1 || totalRows != 2 {
		t.Errorf("game_players winner=%d total=%d, want 1/2", winnerRows, totalRows)
	}
	// role_stats：胜者 wolf 1胜0败1场；败者 villager 0胜1败1场。
	var wins, losses, plays int64
	if err := db.QueryRow(`SELECT wins, losses, plays FROM role_stats WHERE user_id=101 AND role='wolf'`).Scan(&wins, &losses, &plays); err != nil {
		t.Fatalf("role_stats wolf: %v", err)
	}
	if wins != 1 || losses != 0 || plays != 1 {
		t.Errorf("wolf 战绩 = %d/%d/%d, want 1/0/1", wins, losses, plays)
	}
	if err := db.QueryRow(`SELECT wins, losses, plays FROM role_stats WHERE user_id=102 AND role='villager'`).Scan(&wins, &losses, &plays); err != nil {
		t.Fatalf("role_stats villager: %v", err)
	}
	if wins != 0 || losses != 1 || plays != 1 {
		t.Errorf("villager 战绩 = %d/%d/%d, want 0/1/1", wins, losses, plays)
	}
	var report string
	if err := db.QueryRow(`SELECT content FROM battle_reports WHERE game_id=?`, gameID).Scan(&report); err != nil {
		t.Fatalf("battle_reports: %v", err)
	}
	if report != result.Report {
		t.Errorf("战报 = %q, want %q", report, result.Report)
	}
	// 正常结算清除 active 标记（docs/技术选型.md §8.3）。
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ABCDEF'`).Scan(&active); err != nil {
		t.Fatalf("count rooms: %v", err)
	}
	if active != 0 {
		t.Errorf("结算后 rooms = %d, want 0（active 标记已清除）", active)
	}
	var players int
	if err := db.QueryRow(`SELECT COUNT(*) FROM room_players WHERE room_code='ABCDEF'`).Scan(&players); err != nil {
		t.Fatalf("count room_players: %v", err)
	}
	if players != 0 {
		t.Errorf("结算后 room_players = %d, want 0", players)
	}
}

// TestSettlementAtomicRollback 验证事务任一步失败时全部回滚：用触发器
// 强制 battle_reports 写入失败，games/game_players/role_stats/users.points
// 必须全部无残留。
func TestSettlementAtomicRollback(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewSettlementRepository(db)
	mustUser(t, db, 201, "u")
	mustUser(t, db, 202, "u")
	if err := storage.NewRoomRepository(db).Create(ctx, game.RoomID("ABCDEF"), 201, "lobby"); err != nil {
		t.Fatalf("create room: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER zz_fail_report
		BEFORE INSERT ON battle_reports
		BEGIN
			SELECT RAISE(ABORT, 'forced report failure');
		END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	result := storage.GameResult{
		RoomCode:   game.RoomID("ABCDEF"),
		Phase:      game.PhaseSettlement,
		WinnerCamp: game.CampWolf,
		Players: []storage.PlayerResult{
			{UserID: 201, Seat: 1, Role: game.RoleWolf, Camp: game.CampWolf},
			{UserID: 202, Seat: 2, Role: game.RoleVillager, Camp: game.CampGood},
		},
		Report: "report",
	}
	if err := repo.SettleGame(ctx, result); err == nil {
		t.Fatal("战报写入失败时 SettleGame 应返回错误")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM games WHERE room_code='ABCDEF'`,
		`SELECT COUNT(*) FROM game_players WHERE user_id IN (201, 202)`,
		`SELECT COUNT(*) FROM role_stats WHERE user_id IN (201, 202)`,
		`SELECT COUNT(*) FROM battle_reports`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q, err)
		}
		if n != 0 {
			t.Errorf("回滚后 %s = %d, want 0", q, n)
		}
	}
	if got := pointsOf(t, db, 201); got != 0 {
		t.Errorf("回滚后 201 积分 = %d, want 0", got)
	}
	if got := pointsOf(t, db, 202); got != 0 {
		t.Errorf("回滚后 202 积分 = %d, want 0", got)
	}
	// 活跃房间的清除也属于同一事务：失败后 rooms 标记必须保留。
	var rooms int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rooms WHERE room_code='ABCDEF'`).Scan(&rooms); err != nil {
		t.Fatalf("count rooms: %v", err)
	}
	if rooms != 1 {
		t.Errorf("回滚后 rooms = %d, want 1（删除已回滚）", rooms)
	}
}

// TestSettlementRejectsInvalidInput 验证非法输入返回错误且不落库。
func TestSettlementRejectsInvalidInput(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewSettlementRepository(db)
	mustUser(t, db, 301, "u")

	base := storage.GameResult{
		RoomCode:   game.RoomID("ABCDEF"),
		Phase:      game.PhaseSettlement,
		WinnerCamp: game.CampWolf,
		Players: []storage.PlayerResult{
			{UserID: 301, Seat: 1, Role: game.RoleWolf, Camp: game.CampWolf},
		},
		Report: "report",
	}
	for _, tc := range []struct {
		name string
		mut  func(*storage.GameResult)
	}{
		{name: "胜方未知", mut: func(r *storage.GameResult) { r.WinnerCamp = game.CampUnknown }},
		{name: "无玩家", mut: func(r *storage.GameResult) { r.Players = nil }},
		{name: "角色非法", mut: func(r *storage.GameResult) { r.Players[0].Role = game.RoleUnknown }},
		{name: "阵营非法", mut: func(r *storage.GameResult) { r.Players[0].Camp = game.CampUnknown }},
		{name: "座位非法", mut: func(r *storage.GameResult) { r.Players[0].Seat = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Players = append([]storage.PlayerResult(nil), base.Players...)
			tc.mut(&r)
			if err := repo.SettleGame(ctx, r); err == nil {
				t.Fatal("非法输入应返回错误")
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM games WHERE room_code='ABCDEF'`).Scan(&n); err != nil {
				t.Fatalf("count games: %v", err)
			}
			if n != 0 {
				t.Errorf("非法输入落库 games = %d, want 0", n)
			}
		})
	}
}
