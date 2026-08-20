package app

// I1 红测：结算持久化失败时自动重试兜底，不静默丢分/战报。
// SQLite 单事务失败 = 未写，重试安全（不会双写积分）。

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// flakySettleRepo 包装真实 repo：前 failTimes 次 SettleGame 返回注入错误，
// 之后透传真实实现（模拟 DB 瞬时故障恢复）。
type flakySettleRepo struct {
	inner     *storage.SettlementRepository
	failTimes int
	attempts  int
}

func (f *flakySettleRepo) SettleGame(ctx context.Context, result storage.GameResult) error {
	f.attempts++
	if f.attempts <= f.failTimes {
		return errors.New("storage: simulated transient failure")
	}
	return f.inner.SettleGame(ctx, result)
}

// TestPersistSettlementRetries 是 I1 红测：persistSettlement 首次失败后
// 重试成功，积分/战报最终落库（不丢、不双写）。
func TestPersistSettlementRetries(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	urepo := storage.NewUserRepository(db)
	if err := urepo.Upsert(context.Background(), 9501, "狼人甲"); err != nil {
		t.Fatalf("upsert 9501: %v", err)
	}
	if err := urepo.Upsert(context.Background(), 9502, "好人乙"); err != nil {
		t.Fatalf("upsert 9502: %v", err)
	}

	flaky := &flakySettleRepo{
		inner:     storage.NewSettlementRepository(db),
		failTimes: 1,
	}

	// 直接构造最小 Wiring（不跑 Attach，注入 flaky repo）。
	w := &Wiring{settleStore: flaky, db: db, log: mustLogger(t, &bytes.Buffer{})}

	// 推送结算：狼人胜，2 名玩家。
	w.persistSettlement(game.Settlement{
		RoomID: "I1R01",
		Phase:  game.PhaseSettlement,
		Winner: game.CampWolf,
		Players: []game.PlayerResult{
			{UserID: 9501, Seat: 1, Role: game.RoleWolf, Camp: game.CampWolf},
			{UserID: 9502, Seat: 2, Role: game.RoleVillager, Camp: game.CampGood},
		},
	})

	if flaky.attempts < 2 {
		t.Fatalf("重试未发生: attempts = %d, want >= 2", flaky.attempts)
	}

	// 验证最终落库：狼人 +5 分，平民 0 分（重试不双写）。
	var p1, p2 int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT points FROM users WHERE telegram_id = ?`, int64(9501)).Scan(&p1); err != nil {
		t.Fatalf("read points 9501: %v", err)
	}
	if err := db.QueryRowContext(context.Background(),
		`SELECT points FROM users WHERE telegram_id = ?`, int64(9502)).Scan(&p2); err != nil {
		t.Fatalf("read points 9502: %v", err)
	}
	if p1 != 5 {
		t.Fatalf("狼人积分 = %d, want 5（胜利 +5）", p1)
	}
	if p2 != 0 {
		t.Fatalf("平民积分 = %d, want 0", p2)
	}
}

// TestPersistSettlementGivesUpAfterRetries 是 I1 配套：3 次都失败时
// 不 panic、显式 Error 日志（可观测不静默）。
func TestPersistSettlementGivesUpAfterRetries(t *testing.T) {
	db := openTestDB(t)
	if err := storage.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	flaky := &flakySettleRepo{inner: storage.NewSettlementRepository(db), failTimes: 99}
	w := &Wiring{settleStore: flaky, db: db, log: mustLogger(t, &bytes.Buffer{})}

	w.persistSettlement(game.Settlement{
		RoomID: "I1R02",
		Phase:  game.PhaseSettlement,
		Winner: game.CampGood,
		Players: []game.PlayerResult{
			{UserID: 9502, Seat: 2, Role: game.RoleVillager, Camp: game.CampGood},
		},
	})
	if flaky.attempts != 3 {
		t.Fatalf("attempts = %d, want 3（重试上限）", flaky.attempts)
	}
}
