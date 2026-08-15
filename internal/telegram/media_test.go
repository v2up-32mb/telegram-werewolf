package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/v2up-32mb/telegram-werewolf/internal/storage/sqlc"
)

// errBoom 是 fake 上传器的注入错误。
var errBoom = errors.New("boom: upload failed")

// tinyJPEG 在测试内生成最小合法 JPEG 字节（1x1 灰块），使本文件自包含。
func tinyJPEG(t *testing.T, shade uint8) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	img.SetGray(0, 0, color.Gray{Y: shade})
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture JPEG: %v", err)
	}
	return buf.Bytes()
}

// fakePhotoSender 记录 file_id 直发调用（命中路径）。
type fakePhotoSender struct {
	calls []SendPhotoParams
	err   error
}

func (f *fakePhotoSender) SendPhoto(_ context.Context, p SendPhotoParams) (*SentMessage, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return nil, f.err
	}
	return &SentMessage{MessageID: 1}, nil
}

// fakeUploader 记录「上传并发送」调用（未命中路径），可注入返回 file_id 或错误。
type fakeUploader struct {
	calls  []SendPhotoUploadParams
	fileID string
	err    error
}

func (f *fakeUploader) UploadAndSend(_ context.Context, p SendPhotoUploadParams) (string, *SentMessage, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return "", nil, f.err
	}
	return f.fileID, &SentMessage{MessageID: 2}, nil
}

// memMediaStore 是内存版 MediaCacheStore（非 SQLite 用例）。
type memMediaStore struct {
	m   map[string]MediaCacheEntry
	err error
}

func newMemMediaStore() *memMediaStore {
	return &memMediaStore{m: make(map[string]MediaCacheEntry)}
}

func (s *memMediaStore) LoadMedia(_ context.Context, cacheKey string) (MediaCacheEntry, bool, error) {
	if s.err != nil {
		return MediaCacheEntry{}, false, s.err
	}
	e, ok := s.m[cacheKey]
	return e, ok, nil
}

func (s *memMediaStore) SaveMedia(_ context.Context, cacheKey string, e MediaCacheEntry) error {
	if s.err != nil {
		return s.err
	}
	s.m[cacheKey] = e
	return nil
}

func TestMediaCacheKeyByBotAndContent(t *testing.T) {
	store := newMemMediaStore()
	uploader := &fakeUploader{fileID: "up-1"}
	sender := &fakePhotoSender{}
	imgA := tinyJPEG(t, 100)
	imgB := tinyJPEG(t, 200)

	m1 := NewMediaCache(42, store, uploader, sender)
	m2 := NewMediaCache(43, store, uploader, sender)

	if got := m1.CacheKey(imgA); got == "" {
		t.Fatal("CacheKey = empty, want non-empty")
	}
	if m1.CacheKey(imgA) != m1.CacheKey(imgA) {
		t.Fatal("同一 Bot ID + 同一图片内容 → 同一 cache_key")
	}
	if m2.CacheKey(imgA) == m1.CacheKey(imgA) {
		t.Fatal("不同 Bot ID → 不同 cache_key（换 Bot Token 后旧缓存自动失效）")
	}
	if m1.CacheKey(imgA) == m1.CacheKey(imgB) {
		t.Fatal("不同图片内容 → 不同 cache_key（改图后旧缓存自动失效）")
	}
}

func TestSendRoleCardHitUsesCachedFileID(t *testing.T) {
	store := newMemMediaStore()
	uploader := &fakeUploader{fileID: "up-1"}
	sender := &fakePhotoSender{}
	img := tinyJPEG(t, 100)
	caption := "🐺 狼人：你被选中了！（MarkdownV2 Caption）"

	m := NewMediaCache(42, store, uploader, sender)
	store.m[m.CacheKey(img)] = MediaCacheEntry{FileID: "cached-file-1", FileType: "photo"}

	msg, err := m.SendRoleCard(context.Background(), 1001, img, caption)
	if err != nil {
		t.Fatalf("SendRoleCard: %v", err)
	}
	if msg == nil || msg.MessageID != 1 {
		t.Fatalf("msg = %+v, want fake sender message id 1", msg)
	}
	if len(uploader.calls) != 0 {
		t.Fatalf("uploader calls = %d, want 0（命中时不得上传）", len(uploader.calls))
	}
	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1（身份卡同一 sendPhoto）", len(sender.calls))
	}
	got := sender.calls[0]
	if got.ChatID != 1001 || got.FileID != "cached-file-1" {
		t.Fatalf("SendPhoto params = %+v, want chat 1001 file_id cached-file-1", got)
	}
	if got.Caption != caption {
		t.Fatalf("caption = %q, want 完整 Caption 随同一 sendPhoto", got.Caption)
	}
	if got.ParseMode != markdownV2 {
		t.Fatalf("parse_mode = %q, want %q", got.ParseMode, markdownV2)
	}
}

func TestSendRoleCardMissUploadsWritesBackAndReuses(t *testing.T) {
	store := newMemMediaStore()
	uploader := &fakeUploader{fileID: "up-file-1"}
	sender := &fakePhotoSender{}
	img := tinyJPEG(t, 160)
	caption := "🔮 预言家：你被选中了！（MarkdownV2 Caption）"

	m := NewMediaCache(42, store, uploader, sender)

	// 第一次：未命中 → 上传并发送 → 回写缓存。
	msg, err := m.SendRoleCard(context.Background(), 1002, img, caption)
	if err != nil {
		t.Fatalf("SendRoleCard first: %v", err)
	}
	if msg == nil || msg.MessageID != 2 {
		t.Fatalf("msg = %+v, want uploader message id 2", msg)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("uploader calls = %d, want 1（未命中必须上传）", len(uploader.calls))
	}
	if len(sender.calls) != 0 {
		t.Fatalf("sender calls = %d, want 0（未命中路径上传即发送，避免身份卡重复）", len(sender.calls))
	}
	up := uploader.calls[0]
	if up.ChatID != 1002 || !bytes.Equal(up.Image, img) || up.Caption != caption || up.ParseMode != markdownV2 {
		t.Fatalf("upload params = %+v, want chat 1002 + 原图 + 完整 Caption + MarkdownV2", up)
	}
	if up.MimeType != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", up.MimeType)
	}
	key := m.CacheKey(img)
	entry, ok := store.m[key]
	if !ok || entry.FileID != "up-file-1" || entry.FileType != "photo" {
		t.Fatalf("cached entry = %+v ok=%v, want {up-file-1 photo}（上传后回写）", entry, ok)
	}

	// 第二次：同一图片命中缓存 → 直发缓存 file_id，不再上传。
	if _, err := m.SendRoleCard(context.Background(), 1002, img, caption); err != nil {
		t.Fatalf("SendRoleCard second: %v", err)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("uploader calls = %d after second send, want 1（命中后不再上传）", len(uploader.calls))
	}
	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d after second send, want 1", len(sender.calls))
	}
	if got := sender.calls[0]; got.FileID != "up-file-1" {
		t.Fatalf("second send file_id = %q, want 缓存回写的 up-file-1", got.FileID)
	}
}

func TestSendRoleCardUploadFailureDoesNotCacheOrSend(t *testing.T) {
	store := newMemMediaStore()
	uploader := &fakeUploader{err: errBoom}
	sender := &fakePhotoSender{}
	img := tinyJPEG(t, 100)

	m := NewMediaCache(42, store, uploader, sender)
	_, err := m.SendRoleCard(context.Background(), 1003, img, "caption")
	if err == nil {
		t.Fatal("SendRoleCard = nil error, want upload error 传播")
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("uploader calls = %d, want 1", len(uploader.calls))
	}
	if len(sender.calls) != 0 {
		t.Fatalf("sender calls = %d, want 0（上传失败不发送）", len(sender.calls))
	}
	if len(store.m) != 0 {
		t.Fatalf("cache written on upload failure: %v", store.m)
	}
}

func TestSendRoleCardCaptionBoundary(t *testing.T) {
	store := newMemMediaStore()
	uploader := &fakeUploader{fileID: "up-1"}
	sender := &fakePhotoSender{}
	img := tinyJPEG(t, 120)
	m := NewMediaCache(42, store, uploader, sender)
	// 预置缓存，边界用例走命中直发路径，避免与上传器计数耦合。
	store.m[m.CacheKey(img)] = MediaCacheEntry{FileID: "cached-file-1", FileType: "photo"}

	// 1024 字符（上限边界）→ 通过并发送。
	if _, err := m.SendRoleCard(context.Background(), 1004, img, strings.Repeat("好", 1024)); err != nil {
		t.Fatalf("caption=1024: %v, want pass", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("sender calls = %d, want 1（1024 字符应发送）", len(sender.calls))
	}
	if len(uploader.calls) != 0 {
		t.Fatalf("uploader calls = %d, want 0", len(uploader.calls))
	}

	// 1025 字符 → 明确错误，不发送、不缓存。
	if _, err := m.SendRoleCard(context.Background(), 1004, img, strings.Repeat("好", 1025)); !errors.Is(err, ErrCaptionTooLong) {
		t.Fatalf("caption=1025 error = %v, want ErrCaptionTooLong", err)
	}
	if len(sender.calls) != 1 || len(uploader.calls) != 0 {
		t.Fatalf("超长 Caption 不得触发发送/上传（sender=%d uploader=%d）", len(sender.calls), len(uploader.calls))
	}
}

// sqliteMediaStore 用 Task 13 的 sqlc 查询（GetMediaCache/UpsertMediaCache）适配 MediaCacheStore。
type sqliteMediaStore struct {
	db *sql.DB
}

func (s sqliteMediaStore) LoadMedia(ctx context.Context, cacheKey string) (MediaCacheEntry, bool, error) {
	row, err := sqlc.New(s.db).GetMediaCache(ctx, cacheKey)
	if err == sql.ErrNoRows {
		return MediaCacheEntry{}, false, nil
	}
	if err != nil {
		return MediaCacheEntry{}, false, err
	}
	return MediaCacheEntry{FileID: row.FileID, FileType: row.FileType}, true, nil
}

func (s sqliteMediaStore) SaveMedia(ctx context.Context, cacheKey string, e MediaCacheEntry) error {
	return sqlc.New(s.db).UpsertMediaCache(ctx, sqlc.UpsertMediaCacheParams{
		CacheKey: cacheKey,
		FileID:   e.FileID,
		FileType: e.FileType,
	})
}

// openMediaDB 建出与 migrations/000001_initial.sql 一致的 media_cache 表。
func openMediaDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `CREATE TABLE IF NOT EXISTS media_cache (
		cache_key  TEXT PRIMARY KEY,
		file_id    TEXT NOT NULL,
		file_type  TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatalf("create media_cache: %v", err)
	}
	return db
}

func TestSendRoleCardRealSqliteQueryContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	ctx := context.Background()
	db := openMediaDB(t, path)
	defer db.Close()

	store := sqliteMediaStore{db: db}
	uploader := &fakeUploader{fileID: "sqlite-up-1"}
	sender := &fakePhotoSender{}
	img := tinyJPEG(t, 200)
	caption := "💊 女巫：你被选中了！（MarkdownV2 Caption）"

	m := NewMediaCache(9, store, uploader, sender)

	// 未命中：上传 → sqlc.UpsertMediaCache 回写。
	if _, err := m.SendRoleCard(ctx, 2001, img, caption); err != nil {
		t.Fatalf("SendRoleCard miss: %v", err)
	}

	// 直接用 Task 13 sqlc 查询验证契约：GetMediaCache 可读回写行。
	key := m.CacheKey(img)
	row, err := sqlc.New(db).GetMediaCache(ctx, key)
	if err != nil {
		t.Fatalf("sqlc GetMediaCache after write: %v", err)
	}
	if row.FileID != "sqlite-up-1" || row.FileType != "photo" {
		t.Fatalf("sqlc row = %+v, want {file_id sqlite-up-1, file_type photo}", row)
	}
	if len(uploader.calls) != 1 || len(sender.calls) != 0 {
		t.Fatalf("uploader=%d sender=%d, want 1/0（首发送仅一条消息）", len(uploader.calls), len(sender.calls))
	}

	// 模拟重启：关闭后重开同一库文件，同一图片应命中持久化缓存，不再上传。
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	db = openMediaDB(t, path)
	defer db.Close()

	m2 := NewMediaCache(9, sqliteMediaStore{db: db}, &fakeUploader{fileID: "should-not-upload"}, sender)
	if _, err := m2.SendRoleCard(ctx, 2001, img, caption); err != nil {
		t.Fatalf("SendRoleCard after reopen: %v", err)
	}
	if len(uploader.calls) != 1 {
		t.Fatalf("uploader calls = %d after reopen, want 1（重启后命中缓存，不再上传）", len(uploader.calls))
	}
	if len(sender.calls) != 1 || sender.calls[0].FileID != "sqlite-up-1" {
		t.Fatalf("after reopen sender calls = %+v, want 1 × file_id sqlite-up-1", sender.calls)
	}
}
