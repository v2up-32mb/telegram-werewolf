package app

// 照片身份卡接线（Item 2；docs 技术选型.md §10、阶段消息设计.md §6.1）：
//   - 图片经 assets.RoleCards go:embed 内嵌（原生部署不依赖外部 assets）；
//   - 首次发送经 Client.UploadPhoto 上传并同消息发出，取回新 file_id 存
//     media_cache（缓存键 = Bot ID + SHA-256(图片内容)，换 Bot/改图自动失效）；
//   - 后续命中缓存用 file_id 直发同一 sendPhoto。
// 身份卡消息仍经 Outbox（OpSendRoleCard），限速/重试/脱敏统一。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/v2up-32mb/telegram-werewolf/assets"
	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// assetsRoleImageProvider 从内嵌 assets.RoleCards 提供角色卡图片。
type assetsRoleImageProvider struct{}

func (assetsRoleImageProvider) RoleCard(name string) ([]byte, string, error) {
	img, err := assets.RoleCards.ReadFile("role-cards/" + name + ".jpg")
	if err != nil {
		return nil, "", err
	}
	return img, "image/jpeg", nil
}

// clientPhotoUploader 经 Client.UploadPhoto 上传并同消息发送。
type clientPhotoUploader struct{ client telegram.Client }

func (u clientPhotoUploader) UploadAndSend(ctx context.Context, p telegram.SendPhotoUploadParams) (string, *telegram.SentMessage, error) {
	msg, err := u.client.UploadPhoto(ctx, telegram.UploadPhotoParams(p))
	if err != nil {
		return "", nil, err
	}
	if msg.PhotoFileID == "" {
		return "", nil, errors.New("app: upload photo returned no file_id")
	}
	return msg.PhotoFileID, msg, nil
}

// mediaCacheStore 是 media_cache 表的存储适配（sqlc media.sql 查询）。
type mediaCacheStore struct{ db *sql.DB }

func (s mediaCacheStore) LoadMedia(ctx context.Context, key string) (telegram.MediaCacheEntry, bool, error) {
	row, err := sqlc.New(s.db).GetMediaCache(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return telegram.MediaCacheEntry{}, false, nil
	}
	if err != nil {
		return telegram.MediaCacheEntry{}, false, err
	}
	return telegram.MediaCacheEntry{FileID: row.FileID, FileType: row.FileType}, true, nil
}

func (s mediaCacheStore) SaveMedia(ctx context.Context, key string, e telegram.MediaCacheEntry) error {
	return sqlc.New(s.db).UpsertMediaCache(ctx, sqlc.UpsertMediaCacheParams{
		CacheKey: key, FileID: e.FileID, FileType: e.FileType,
	})
}

// roleImageProvider 是生产角色卡图片提供器（内嵌 assets）。
func roleImageProvider() telegram.RoleImageProvider { return assetsRoleImageProvider{} }

// mediaCache 惰性构造 MediaCache：botID 经 client.GetMe 一次确定（缓存键
// 依据，docs 技术选型.md §10）。
func (w *Wiring) mediaCache(client telegram.Client) (*telegram.MediaCache, error) {
	w.botOnce.Do(func() {
		me, err := client.GetMe(context.Background())
		if err == nil {
			w.botID = me.ID
		} else {
			w.botErr = fmt.Errorf("app: get bot id for media cache: %w", err)
		}
	})
	if w.botErr != nil {
		return nil, w.botErr
	}
	return telegram.NewMediaCache(w.botID, mediaCacheStore{db: w.db}, clientPhotoUploader{client: client}, client), nil
}

// sendRoleCard 发送一名玩家的身份卡（sendPhoto 图片 + MarkdownV2 Caption，
// 首次上传并缓存 file_id，后续复用）。
func (w *Wiring) sendRoleCard(ctx context.Context, chatID int64, roleName string, seat int) error {
	role, err := roleFromName(roleName)
	if err != nil {
		return err
	}
	view, err := telegram.NewRoleCardView(w.renderer, roleImageProvider(), role, game.Seat(seat))
	if err != nil {
		return fmt.Errorf("app: role card view: %w", err)
	}
	client, err := w.client()
	if err != nil {
		return err
	}
	mc, err := w.mediaCache(client)
	if err != nil {
		return err
	}
	if _, err := mc.SendRoleCard(ctx, chatID, view.Image, view.Caption); err != nil {
		return classifyTelegramError(outboxMsgFor(chatID), err)
	}
	w.log.Info("app: role card sent", "chat", chatID, "role", roleName, "seat", seat)
	return nil
}

// roleFromName 把角色名主干（werewolf/seer/witch/villager）解析为领域角色。
func roleFromName(name string) (game.Role, error) {
	switch strings.ToLower(name) {
	case "wolf", "werewolf":
		return game.RoleWolf, nil
	case "seer":
		return game.RoleSeer, nil
	case "witch":
		return game.RoleWitch, nil
	case "villager":
		return game.RoleVillager, nil
	default:
		return game.RoleUnknown, fmt.Errorf("app: unsupported role card name %q", name)
	}
}

// outboxMsgFor 构造分类错误所需的最小消息（sendRoleCard 错误分类用）。
func outboxMsgFor(chatID int64) outbox.Message {
	return outbox.Message{ChatID: outbox.ChatID(chatID), Operation: telegram.OpSendRoleCard}
}
