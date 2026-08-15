package telegram

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// mediaCacheFileTypePhoto 是 media_cache.file_type 的照片取值。
const mediaCacheFileTypePhoto = "photo"

// telegramCaptionMaxChars 是 sendPhoto Caption 的解析后字符上限
// （docs/阶段消息设计.md §6.1，Telegram 限制 1024）。身份卡 Caption 统一
// MarkdownV2，此处按可视化字符数（rune 数）近似「解析后字符」。
const telegramCaptionMaxChars = 1024

// ErrCaptionTooLong 表示 Caption 超过 Telegram 1024 上限，发送前拒绝。
var ErrCaptionTooLong = errors.New("telegram: caption exceeds 1024 characters")

// MediaCacheEntry 是 media_cache 表一行的领域 DTO（复用 Task 13 查询契约）。
type MediaCacheEntry struct {
	// FileID 是 Telegram 返回的媒体 file_id。
	FileID string
	// FileType 是缓存媒体类型（身份卡恒为 "photo"）。
	FileType string
}

// MediaCacheStore 是 media_cache 表的最小读写边界。
//
// 持久化实现由上层注入：产品侧适配 internal/storage/sqlc 的
// GetMediaCache/UpsertMediaCache（Task 13 已就位），测试使用真实 SQLite
// 验证查询契约（与 Task 20 CursorStore 的做法一致）。
type MediaCacheStore interface {
	// LoadMedia 按 cacheKey 读取缓存；无记录时 ok=false。
	LoadMedia(ctx context.Context, cacheKey string) (MediaCacheEntry, bool, error)
	// SaveMedia 写回（幂等 upsert）。
	SaveMedia(ctx context.Context, cacheKey string, entry MediaCacheEntry) error
}

// SendPhotoUploadParams 是「上传并发送新图片」的参数。
type SendPhotoUploadParams struct {
	ChatID    int64
	Image     []byte
	MimeType  string
	Caption   string
	ParseMode string
}

// PhotoUploader 在 file_id 未命中时上传图片并发送同一条消息，
// 返回新 file_id 供缓存回写。真实上传适配（不修改既有 Client 接口）
// 与首发的同消息发送一起由产品装配层注入；测试注入替身。
type PhotoUploader interface {
	// UploadAndSend 上传新图片并作为同一条 sendPhoto 发送，返回新 file_id。
	UploadAndSend(ctx context.Context, p SendPhotoUploadParams) (fileID string, msg *SentMessage, err error)
}

// PhotoSender 是命中缓存时的 file_id 直发边界（既有 Client 已满足）。
type PhotoSender interface {
	SendPhoto(ctx context.Context, p SendPhotoParams) (*SentMessage, error)
}

// MediaCache 按「Bot ID + 内容 SHA-256」缓存角色卡 file_id 并发送身份卡。
//
// 语义（docs/技术选型.md §10）：
//   - 缓存键 = Bot ID + SHA-256(图片内容)：换 Bot Token 或改图后旧缓存自动失效；
//   - 命中：用缓存 file_id 直发同一 sendPhoto，不重复上传；
//   - 未命中：上传并发送同一条消息（避免身份卡重复），回写缓存；
//   - 身份卡图片与完整 Caption 始终作为同一条 sendPhoto 发出；
//   - 发送前校验 Caption ≤ 1024（§6.1）。
type MediaCache struct {
	botID    int64
	store    MediaCacheStore
	uploader PhotoUploader
	sender   PhotoSender
}

// NewMediaCache 创建媒体发送与缓存服务。
func NewMediaCache(botID int64, store MediaCacheStore, uploader PhotoUploader, sender PhotoSender) *MediaCache {
	return &MediaCache{botID: botID, store: store, uploader: uploader, sender: sender}
}

// CacheKey 返回 cache_key = Bot ID + SHA-256(图片内容)（hex，`role-card:` 前缀）。
func (m *MediaCache) CacheKey(image []byte) string {
	sum := sha256.Sum256(image)
	return fmt.Sprintf("role-card:%d:%x", m.botID, sum[:])
}

// SendRoleCard 发送身份卡：图片 + 完整 Caption 作为同一条 sendPhoto。
//
// 命中缓存时用 file_id 直发；未命中时上传并发送同一条消息，随后回写缓存。
// Caption 超过 1024 字符返回 ErrCaptionTooLong，不发送、不写缓存。
func (m *MediaCache) SendRoleCard(ctx context.Context, chatID int64, image []byte, caption string) (*SentMessage, error) {
	if utf8.RuneCountInString(caption) > telegramCaptionMaxChars {
		return nil, ErrCaptionTooLong
	}
	key := m.CacheKey(image)
	entry, ok, err := m.store.LoadMedia(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("telegram: load media cache: %w", err)
	}
	if ok {
		return m.sender.SendPhoto(ctx, SendPhotoParams{
			ChatID:    chatID,
			FileID:    entry.FileID,
			Caption:   caption,
			ParseMode: markdownV2,
		})
	}
	mime := http.DetectContentType(image)
	fileID, msg, err := m.uploader.UploadAndSend(ctx, SendPhotoUploadParams{
		ChatID:    chatID,
		Image:     image,
		MimeType:  mime,
		Caption:   caption,
		ParseMode: markdownV2,
	})
	if err != nil {
		return nil, fmt.Errorf("telegram: upload role card: %w", err)
	}
	if err := m.store.SaveMedia(ctx, key, MediaCacheEntry{FileID: fileID, FileType: mediaCacheFileTypePhoto}); err != nil {
		return nil, fmt.Errorf("telegram: save media cache: %w", err)
	}
	return msg, nil
}
