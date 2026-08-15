// Package app 承载应用级装配与依赖锁定测试。
package app

import (
	"os"
	"strings"
	"testing"

	// 以下 blank import 用于锁定 Task 2 已选定的库依赖：
	// go mod tidy 只会保留被（含测试文件）引用的 module，锁定测试通过真实引用
	// 使依赖保留在 go.mod 中，并验证当前工具链下这些版本可编译。
	_ "github.com/go-telegram/bot"            // Telegram Bot API 客户端
	_ "github.com/google/go-cmp/cmp"          // 测试比对
	_ "github.com/nicksnyder/go-i18n/v2/i18n" // 多语言文案
	_ "github.com/pressly/goose/v3"           // SQL migration
	_ "github.com/yeqown/go-qrcode/v2"        // 邀请二维码
	_ "go.yaml.in/yaml/v3"                    // YAML 配置解析
	_ "golang.org/x/crypto/bcrypt"            // 密码哈希（仅锁定模块，算法以后续任务实际需求为准）
	_ "golang.org/x/time/rate"                // 限速
	_ "modernc.org/sqlite"                    // 纯 Go SQLite 驱动
)

// TestLockedDependencies 断言 module 路径、Go 版本和关键依赖已在 go.mod 中锁定，
// 防止 CI 或本地构建使用浮动版本。
func TestLockedDependencies(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("读取 go.mod 失败: %v", err)
	}

	st := parseGoMod(string(data))

	if st.module != "github.com/v2up-32mb/telegram-werewolf" {
		t.Errorf("module 行 = %q，want %q", st.module, "github.com/v2up-32mb/telegram-werewolf")
	}
	// 真实状态：goose v3.27.3 的 go.mod 要求 go >= 1.25.7，
	// go get 已将 go 指令从 1.25.0 升级为 1.25.7（仍属 Go 1.25 基线）。
	if st.goVer != "1.25.7" {
		t.Errorf("go 指令 = %q，want %q", st.goVer, "1.25.7")
	}

	pinned := map[string]string{
		"github.com/go-telegram/bot":     "v1.23.0",
		"github.com/yeqown/go-qrcode/v2": "v2.2.5",
		"github.com/pressly/goose/v3":    "v3.27.3",
	}
	for mod, wantVer := range pinned {
		got, ok := st.require[mod]
		if !ok {
			t.Errorf("依赖未锁定：%s（缺少 require 条目）", mod)
			continue
		}
		if got != wantVer {
			t.Errorf("依赖版本 %s = %s，want %s", mod, got, wantVer)
		}
	}

	for _, mod := range []string{
		"modernc.org/sqlite",
		"github.com/nicksnyder/go-i18n/v2",
		"go.yaml.in/yaml/v3",
		"golang.org/x/time", // require 条目以模块路径为准（包路径为 golang.org/x/time/rate）
		"golang.org/x/crypto",
		"github.com/google/go-cmp",
	} {
		got, ok := st.require[mod]
		if !ok {
			t.Errorf("依赖未锁定：%s（缺少 require 条目）", mod)
			continue
		}
		if !strings.HasPrefix(got, "v") || !strings.Contains(got, ".") {
			t.Errorf("依赖 %s 的版本不是合法 semver：%q", mod, got)
		}
	}

	// tool 指令与对应 require 条目（命令路径 → module 路径）必须同时锁定。
	tools := []struct{ cmd, mod string }{
		{"github.com/sqlc-dev/sqlc/cmd/sqlc", "github.com/sqlc-dev/sqlc"},
		{"golang.org/x/vuln/cmd/govulncheck", "golang.org/x/vuln"},
		{"github.com/golangci/golangci-lint/v2/cmd/golangci-lint", "github.com/golangci/golangci-lint/v2"},
	}
	for _, tt := range tools {
		if !st.tools[tt.cmd] {
			t.Errorf("tool 指令未锁定：%s", tt.cmd)
		}
		if got, ok := st.require[tt.mod]; !ok {
			t.Errorf("tool 模块缺少 require 条目：%s", tt.mod)
		} else if !strings.HasPrefix(got, "v") || !strings.Contains(got, ".") {
			t.Errorf("tool 模块 %s 的版本不是合法 semver：%q", tt.mod, got)
		}
	}
}

// goMod 保存 go.mod 中需要核对的字段。
type goMod struct {
	module  string
	goVer   string
	require map[string]string
	tools   map[string]bool
}

// parseGoMod 用最小解析器提取 go.mod 的 module、go、require 与 tool 指令，
// 兼容单行 require/tool 与多行块（require (...)、tool (...)）两种写法。
func parseGoMod(data string) goMod {
	st := goMod{require: make(map[string]string), tools: make(map[string]bool)}
	inRequire, inTool := false, false
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !inRequire && !inTool && strings.HasPrefix(line, "module "):
			st.module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		case !inRequire && !inTool && strings.HasPrefix(line, "go "):
			st.goVer = strings.TrimSpace(strings.TrimPrefix(line, "go "))
		case !inRequire && !inTool && strings.HasPrefix(line, "require ("):
			inRequire = true
		case !inRequire && !inTool && strings.HasPrefix(line, "tool ("):
			inTool = true
		case inTool && line == ")":
			inTool = false
		case inRequire && line == ")":
			inRequire = false
		case inTool && trimmed != "" && !strings.HasPrefix(trimmed, "//"):
			st.tools[trimmed] = true
		case inRequire && trimmed != "" && !strings.HasPrefix(trimmed, "//"):
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				st.require[fields[0]] = fields[1]
			}
		case !inRequire && !inTool && strings.HasPrefix(line, "require "):
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "require ")))
			if len(fields) >= 2 {
				st.require[fields[0]] = fields[1]
			}
		case !inRequire && !inTool && strings.HasPrefix(line, "tool "):
			st.tools[strings.TrimSpace(strings.TrimPrefix(line, "tool "))] = true
		}
	}
	return st
}
