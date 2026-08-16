package storage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage"
)

// TestRoomSettingsPersistsHashNotPlaintext 契约测试：settings 往返一致，
// password_hash 列持久化 bcrypt 哈希而非明文，且写读方法语义正确
// （docs/游戏流程设计.md §密码：明文不得入库）。
func TestRoomSettingsPersistsHashNotPlaintext(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	mustUser(t, db, 201, "host")
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("ABC123"), 201, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	settings := game.DefaultRoomSettings()
	settings.FastMode = true
	settings.WolfMustKill = false
	settingsJSON, err := game.MarshalSettings(settings)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}

	const plaintext = "Passw0rd"
	hash, err := game.HashPassword(plaintext)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, plaintext) {
		t.Fatal("bcrypt 哈希包含明文子串")
	}

	if err := repo.UpdateRoomSettings(ctx, game.RoomID("ABC123"), settingsJSON, hash); err != nil {
		t.Fatalf("UpdateRoomSettings: %v", err)
	}

	var storedSettings, storedHash string
	if err := db.QueryRow(`SELECT settings, password_hash FROM room_settings WHERE room_code='ABC123'`).Scan(&storedSettings, &storedHash); err != nil {
		t.Fatalf("scan rooms row: %v", err)
	}
	if storedHash != hash || strings.Contains(storedHash, plaintext) {
		t.Fatalf("库中 password_hash = %q, 应为 bcrypt 哈希且不含明文", storedHash)
	}
	if storedSettings != settingsJSON {
		t.Fatalf("库中 settings = %q, want %q", storedSettings, settingsJSON)
	}
	if !game.VerifyPassword(storedHash, plaintext) {
		t.Error("VerifyPassword(存库哈希, 明文) = false, want true")
	}

	gotHash, err := repo.RoomPasswordHash(ctx, game.RoomID("ABC123"))
	if err != nil || gotHash != hash {
		t.Fatalf("RoomPasswordHash = %q/%v, want %q/nil", gotHash, err, hash)
	}

	// 清除密码：空哈希覆盖；设置仍保留。
	if err := repo.UpdateRoomSettings(ctx, game.RoomID("ABC123"), settingsJSON, ""); err != nil {
		t.Fatalf("UpdateRoomSettings(clear): %v", err)
	}
	if gotHash, err := repo.RoomPasswordHash(ctx, game.RoomID("ABC123")); err != nil || gotHash != "" {
		t.Fatalf("清除后 RoomPasswordHash = %q/%v, want 空/nil", gotHash, err)
	}
	if gotSettings, err := repo.RoomSettings(ctx, game.RoomID("ABC123")); err != nil || gotSettings != settingsJSON {
		t.Fatalf("清除密码后 RoomSettings = %q/%v, want %q/nil", gotSettings, err, settingsJSON)
	}
}

// TestRoomSettingsRefusesPlaintext 防御契约：存储层拒绝明文与伪 bcrypt 前缀
// 的密码值（bcrypt.Cost 严格校验），防止明文/垃圾值误入库
// （数据库不得保存明文）。
func TestRoomSettingsRefusesPlaintext(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	mustUser(t, db, 202, "host")
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("DEF456"), 202, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, bad := range []string{"Passw0rd", "$2not-a-real-bcrypt-hash"} {
		err := repo.UpdateRoomSettings(ctx, game.RoomID("DEF456"), "{}", bad)
		if err == nil || !strings.Contains(err.Error(), "bcrypt") {
			t.Errorf("UpdateRoomSettings(%q) err = %v, want 含 bcrypt 拒绝", bad, err)
		}
	}
}

// TestRoomSettingsEmptyStateForRoomWithoutSettings 验证房间存在但从未保存过
// 设置时读写读均为空状态（空哈希/空设置），而非 ErrRoomNotFound——
// 保证首次保存（保留原密码语义）不误报房间缺失。
func TestRoomSettingsEmptyStateForRoomWithoutSettings(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	mustUser(t, db, 203, "host")
	repo := storage.NewRoomRepository(db)
	if err := repo.Create(ctx, game.RoomID("GHI789"), 203, "lobby"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got, err := repo.RoomPasswordHash(ctx, game.RoomID("GHI789")); err != nil || got != "" {
		t.Fatalf("RoomPasswordHash(未保存设置) = %q/%v, want 空/nil", got, err)
	}
	if got, err := repo.RoomSettings(ctx, game.RoomID("GHI789")); err != nil || got != "" {
		t.Fatalf("RoomSettings(未保存设置) = %q/%v, want 空/nil", got, err)
	}

	// 空状态下首次保存（保留密码意图，nil=读现有哈希）模拟领域调用：应成功且为空哈希。
	settings := game.DefaultRoomSettings()
	raw, err := game.MarshalSettings(settings)
	if err != nil {
		t.Fatalf("MarshalSettings: %v", err)
	}
	hash, err := repo.RoomPasswordHash(ctx, game.RoomID("GHI789"))
	if err != nil {
		t.Fatalf("RoomPasswordHash before save: %v", err)
	}
	if err := repo.UpdateRoomSettings(ctx, game.RoomID("GHI789"), raw, hash); err != nil {
		t.Fatalf("首次保存(空哈希): %v", err)
	}
	if got, err := repo.RoomSettings(ctx, game.RoomID("GHI789")); err != nil || got != raw {
		t.Fatalf("保存后 RoomSettings = %q/%v, want %q/nil", got, err, raw)
	}
}

// TestRoomSettingsMissingRoom 验证房间不存在时写读均返回明确错误，
// 不留任何部分状态。
func TestRoomSettingsMissingRoom(t *testing.T) {
	db := openTestDB(t)
	runUp(t, db)
	ctx := context.Background()
	repo := storage.NewRoomRepository(db)

	hash, err := game.HashPassword("A1b2c3")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := repo.UpdateRoomSettings(ctx, game.RoomID("NOPE99"), "{}", hash); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("UpdateRoomSettings(不存在) err = %v, want ErrRoomNotFound", err)
	}
	if _, err := repo.RoomPasswordHash(ctx, game.RoomID("NOPE99")); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("RoomPasswordHash(不存在) err = %v, want ErrRoomNotFound", err)
	}
	if _, err := repo.RoomSettings(ctx, game.RoomID("NOPE99")); !errors.Is(err, storage.ErrRoomNotFound) {
		t.Fatalf("RoomSettings(不存在) err = %v, want ErrRoomNotFound", err)
	}
}
