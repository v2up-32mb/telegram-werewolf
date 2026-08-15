// Package storage 提供 SQLite 数据库打开、迁移与类型安全查询
// （docs/技术选型.md §8）。
package storage

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// 连接参数（docs/技术选型.md §8.4：启用 WAL、外键约束与合理 busy_timeout）。
const (
	// BusyTimeoutMS 是繁忙等待超时毫秒数。
	BusyTimeoutMS = 5000
	// DefaultMaxOpenConns 是连接池上限的保守默认值，后续按集成测试调整。
	DefaultMaxOpenConns = 4
)

// Open 使用 modernc.org/sqlite 打开数据库，并通过 DSN _pragma 参数保证
// 每条 database/sql 连接都启用 foreign_keys=ON、journal_mode=WAL 与
// busy_timeout（不依赖单条初始化连接上的手工 PRAGMA）。
func Open(path string, maxOpenConns int) (*sql.DB, error) {
	if maxOpenConns <= 0 {
		maxOpenConns = DefaultMaxOpenConns
	}
	// 仅转义会破坏 DSN query 解析的字符；路径分隔符必须原样保留。
	escaped := strings.NewReplacer("?", "%3F", "#", "%23", " ", "%20").Replace(path)
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)", escaped, BusyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: ping sqlite: %w", err)
	}
	return db, nil
}
