// Package migrations 通过 go:embed 暴露 SQLite 迁移 SQL 文件。
// 迁移为可审查的纯 SQL 文件，使用 Goose SQL annotations 编写
// （docs/技术选型.md §8.1：迁移保留为可审查的纯 SQL migration 文件）。
package migrations

import "embed"

// FS 嵌入全部 migration SQL，供测试与运行时迁移执行使用。
//
//go:embed 000001_initial.sql
var FS embed.FS
