package app

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/v2up-32mb/telegram-werewolf/internal/i18n"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

// TestTemplateRealSendSmoke 是全模板真实发送冒烟（Task 46 用户新指示）：
// 把 locales/active.zh-CN.yaml 中全部 i18n 键用生产同款 Renderer 渲染，
// 并经真实 Telegram Bot API（生产同款 MarkdownV2）逐条发送到测试账号，
// 验证每条模板在 MarkdownV2 下都能成功发送、无 400、无渲染错误。
//
// 触发方式（env 门控，缺省跳过，不影响常规测试/CI）：
//
//	TELEGRAM_SMOKE_CHAT_ID  测试账号 chat id（必填）
//	TELEGRAM_BOT_TOKEN      bot token（仅经 env 注入，绝不入库/日志/报告）
//	TELEGRAM_BOT_API_BASE_URL API 基址（可选，默认官方 api.telegram.org）
//	TELEGRAM_SMOKE_OUT      结果 TSV 输出路径（可选，用于报告汇总）
//
// 每条模板发送成功后立即删除消息，避免测试账号被模板消息刷屏；
// sendMessage 返回 messageID 即视为发送成功（无 400）。失败键汇总为
// 测试失败并附错误，测试结束时输出 键→成功/失败 清单。
func TestTemplateRealSendSmoke(t *testing.T) {
	chatIDStr := os.Getenv("TELEGRAM_SMOKE_CHAT_ID")
	if chatIDStr == "" {
		t.Skip("TELEGRAM_SMOKE_CHAT_ID 未设置：跳过真实发送冒烟（本地/CI 需 secret 注入）")
	}
	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil || chatID == 0 {
		t.Fatalf("TELEGRAM_SMOKE_CHAT_ID=%q 非法", chatIDStr)
	}
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		t.Fatal("TELEGRAM_BOT_TOKEN 未设置")
	}
	base := os.Getenv("TELEGRAM_BOT_API_BASE_URL")
	if base == "" {
		base = "https://api.telegram.org"
	}

	client, err := telegram.NewClient(token, telegram.WithServerURL(base))
	if err != nil {
		t.Fatalf("telegram.NewClient: %v", err)
	}
	renderer, err := i18n.NewRenderer("zh-CN")
	if err != nil {
		t.Fatalf("i18n.NewRenderer: %v", err)
	}

	templates := allLocaleTemplates(t)
	fmt.Fprintf(os.Stderr, "[template-smoke] 共 %d 个 i18n 模板键，发送目标 chat=%d\n", len(templates), chatID)

	out, closeOut := openSmokeOut(t)
	defer closeOut()

	var fails []string
	for _, tmpl := range templates {
		text, err := renderer.Render(tmpl.Key, sampleParamsFor(tmpl.Raw))
		if err != nil {
			msg := fmt.Sprintf("%s: 渲染失败: %v", tmpl.Key, err)
			fails = append(fails, msg)
			t.Errorf("键 %s 渲染失败: %v", tmpl.Key, err)
			writeSmokeRow(out, tmpl.Key, "FAIL", "", "", err.Error())
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		sent, sendErr := client.SendMessage(ctx, telegram.SendMessageParams{
			ChatID:    chatID,
			Text:      text,
			ParseMode: "MarkdownV2",
		})
		cancel()
		if sendErr != nil {
			msg := fmt.Sprintf("%s: %v", tmpl.Key, sendErr)
			fails = append(fails, msg)
			t.Errorf("键 %s 真实发送失败: %v", tmpl.Key, sendErr)
			writeSmokeRow(out, tmpl.Key, "FAIL", "", fmt.Sprint(len([]rune(text))), sendErr.Error())
		} else {
			t.Logf("OK 键=%-32s 字符=%d messageID=%d", tmpl.Key, len([]rune(text)), sent.MessageID)
			writeSmokeRow(out, tmpl.Key, "OK", strconv.Itoa(sent.MessageID), fmt.Sprint(len([]rune(text))), "")
			// 发送成功即已验证；删除消息防止测试账号被刷屏（删除失败不影响结论）。
			delCtx, delCancel := context.WithTimeout(context.Background(), 20*time.Second)
			if derr := client.DeleteMessage(delCtx, telegram.DeleteMessageParams{ChatID: chatID, MessageID: sent.MessageID}); derr != nil {
				t.Logf("警告 键 %s 删除成功消息失败（不影响结论）: %v", tmpl.Key, derr)
			}
			delCancel()
		}
		// 每 chat 约 1 msg/s 安全限速（config 默认 per_chat 2/s）。
		time.Sleep(1200 * time.Millisecond)
	}

	if len(fails) > 0 {
		t.Fatalf("全模板真实发送冒烟失败 %d/%d 条:\n%s", len(fails), len(templates), strings.Join(fails, "\n"))
	}
	t.Logf("全模板真实发送冒烟通过：%d/%d 键均成功发送（MarkdownV2，无 400/渲染错误）", len(templates), len(templates))
}

type localeTemplate struct {
	Key string
	Raw string
}

// allLocaleTemplates 从内嵌 locales/*.yaml 读取全部顶层消息键（保持文件顺序）。
func allLocaleTemplates(t *testing.T) []localeTemplate {
	t.Helper()
	data, err := fs.ReadFile(i18n.LocaleFS, "locales/active.zh-CN.yaml")
	if err != nil {
		t.Fatalf("读取内嵌 locale: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("解析 locale YAML: %v", err)
	}
	if len(doc.Content) == 0 {
		t.Fatal("locale YAML 顶层为空")
	}
	root := doc.Content[0]
	var out []localeTemplate
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		raw := root.Content[i+1].Value
		out = append(out, localeTemplate{Key: key, Raw: raw})
	}
	return out
}

var templateFieldRe = regexp.MustCompile(`\{\{\s*\.([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)

// sampleFields 是冒烟用样例参数；键值为模板 {{.Field}} 的占位名。
var sampleFields = map[string]string{
	"Buttons":       "开始游戏 房间设置 解散房间",
	"CampName":      "狼人阵营",
	"Candidates":    "3号、5号",
	"Choice":        "使用",
	"Count":         "1",
	"Deadline":      "23:59",
	"Detail":        "/join 0O1I",
	"Initiator":     "1",
	"Lines":         "3号 1票\n5号 1票",
	"Mark":          "（房主）",
	"Max":           "6",
	"Message":       "这是冒烟测试通知",
	"Nickname":      "冒烟玩家",
	"nickname":      "冒烟玩家",
	"Password":      "（未设置）",
	"PhaseNumber":   "1",
	"Points":        "5",
	"PoisonStatus":  "未使用",
	"Prompt":        "请选择目标",
	"Remaining":     "2",
	"RoleName":      "狼人",
	"RoomCode":      "WJ6MJT",
	"room_code":     "WJ6MJT",
	"Round":         "1",
	"SaveStatus":    "未使用",
	"Seat":          "3",
	"seat":          "3",
	"Seconds":       "30",
	"Streak":        "3",
	"Target":        "3号",
	"Targets":       "3号、5号",
	"Text":          "这是我的遗言",
	"Winner":        "好人",
	"WindowSeconds": "60",
	"WolfMates":     "3号、7号",
}

// sampleParamsFor 从模板原文提取 {{.Field}} 并生成样例参数：
// 已知字段用可读样例值，未收录字段用 "[Field]" 占位（渲染不报错即可）。
func sampleParamsFor(raw string) map[string]any {
	params := make(map[string]any)
	for _, m := range templateFieldRe.FindAllStringSubmatch(raw, -1) {
		field := m[1]
		if v, ok := sampleFields[field]; ok {
			params[field] = v
		} else {
			params[field] = "[" + field + "]"
		}
	}
	return params
}

// openSmokeOut 按 TELEGRAM_SMOKE_OUT 打开结果 TSV；未设置时返回丢弃输出。
func openSmokeOut(t *testing.T) (io.Writer, func()) {
	t.Helper()
	path := os.Getenv("TELEGRAM_SMOKE_OUT")
	if path == "" {
		return io.Discard, func() {}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建冒烟结果文件: %v", err)
	}
	return f, func() { _ = f.Close() }
}

// writeSmokeRow 写一行 key/status/messageID/chars/note（制表符分隔）。
func writeSmokeRow(w io.Writer, key, status, msgID, chars, note string) {
	note = strings.ReplaceAll(strings.ReplaceAll(note, "\t", " "), "\n", " ")
	_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", key, status, msgID, chars, note)
}
