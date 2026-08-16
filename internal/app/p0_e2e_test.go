package app

// P0 端到端验收测试（实施计划 Task 27）：
// 在 Fake Telegram（假传输替身）+ 临时 SQLite 中跑通 1 房主 + 5 玩家的大厅闭环。
//
// 入口说明：本测试在适配层驱动生产组件（telegram 输入解析 + game 领域服务 +
// storage 临时 SQLite + outbox 调度/合并 + i18n.MarkdownV2 转义）。App 传输管线
// （UpdateSource→Router→CommandHandler→Transport）尚未在 production 接线，
// 属 Task 27 已知缺口（见 testharness_test.go 顶部注释）。
//
// 场景：/start（建房）→ 5 次 deep-link 加入（含一次指定昵称）→ 面板满员 →
// 退出/补位 → 过期（FakeClock 驱动 EvaluateIdle）。
// 断言：SQLite active 行、Outbox 顺序（per-Chat FIFO + 面板合并）、
// MarkdownV2 文本、无身份泄漏。
//
// 送达窗口约定：每步执行后调用 w.flush() 获得「本步截止游标」，
// 再以 sentSince(chat, 上一步游标) 断言本步新增消息（per-Chat FIFO 确定性）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
	"github.com/v2up-32mb/telegram-werewolf/internal/telegram"
)

func TestP0EndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newP0World(t)
	defer w.close()

	const (
		host      = game.UserID(101)
		amei      = game.UserID(102) // 指定昵称「阿明」
		playerB   = game.UserID(103)
		playerC   = game.UserID(104)
		playerD   = game.UserID(105)
		playerE   = game.UserID(106)
		latecomer = game.UserID(107) // 满员被拒、退出后可补位
		extra     = game.UserID(108) // 过期后加入被拒
	)
	const (
		roomCode = "P0TEST"
		room     = game.RoomID(roomCode)
	)

	cursor := 0

	// ---------- 1) /start 建房（适配层等价入口 /newgame；/start 属 App Router 缺口） ----------
	st, err := w.createRoom(host, "u1", roomCode)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if st.RoomID != room || st.Phase != game.PhaseLobby {
		t.Fatalf("create state = RoomID %s Phase %s, want %s/lobby", st.RoomID, st.Phase, room)
	}
	if len(st.Players) != 1 || st.Players[0].UserID != host || st.Players[0].Seat != game.HostSeat {
		t.Fatalf("create players = %+v, want 房主 1 号", st.Players)
	}
	cursor = w.flush()
	hostGot := w.sentSince(outbox.ChatID(host), 0)
	if len(hostGot) != 1 {
		t.Fatalf("建房后房主应收到 1 条消息，实际 %d 条: %+v", len(hostGot), hostGot)
	}
	if m := hostGot[0]; m.msg.Operation != telegram.OpSendText {
		t.Fatalf("建房消息 op = %q, want send_text", m.msg.Operation)
	} else if !strings.Contains(m.text, roomCode) || !strings.Contains(m.text, "1/6") {
		t.Fatalf("建房消息 MarkdownV2 文本缺少房间码/人数: %q", m.text)
	}
	// SQLite active 行：rooms 1 行 + room_players 房主 1 号
	if codes := w.activeCodes(); len(codes) != 1 || codes[0] != roomCode {
		t.Fatalf("active rooms = %v, want [%s]", codes, roomCode)
	}
	if n := w.roomPlayers(room); n != 1 {
		t.Fatalf("room_players count = %d, want 1（房主）", n)
	}

	// ---------- 2) 深链加入：阿明（指定昵称） ----------
	link := "https://t.me/werewolf_bot?start=" + roomCode
	res, err := w.join(amei, "u2", link, strPtr("阿明"))
	if err != nil {
		t.Fatalf("join 阿明: %v", err)
	}
	if res.Seat != 2 || res.Nickname != "阿明" {
		t.Fatalf("join 阿明 result = seat %d nick %q, want seat 2 nick 阿明", res.Seat, res.Nickname)
	}
	next := w.flush()
	// 加入确认只投递本人，含昵称与座位
	ameiGot := w.sentSince(outbox.ChatID(amei), cursor)
	if len(ameiGot) != 1 {
		t.Fatalf("阿明应收到 1 条确认，实际 %d: %+v", len(ameiGot), ameiGot)
	} else if !strings.Contains(ameiGot[0].text, "阿明") || !strings.Contains(ameiGot[0].text, "座位：2") {
		t.Fatalf("阿明确认消息缺少昵称/座位: %q", ameiGot[0].text)
	} else if ameiGot[0].msg.ChatID != outbox.ChatID(amei) {
		t.Fatalf("确认消息投递错 Chat: %d", ameiGot[0].msg.ChatID)
	}
	// 房主面板刷新：人数递增且含新昵称
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 {
		t.Fatalf("阿明加入后房主应收到 1 条面板，实际 %d: %+v", len(hostGot), hostGot)
	} else if !strings.Contains(hostGot[0].text, "2/6") || !strings.Contains(hostGot[0].text, "阿明") {
		t.Fatalf("房主面板缺少 2/6 或阿明: %q", hostGot[0].text)
	}
	cursor = next
	if n := w.roomPlayers(room); n != 2 {
		t.Fatalf("room_players count = %d, want 2", n)
	}

	// ---------- 3) 连续加入 B/C/D/E（随机昵称）直至满员 ----------
	players := []game.UserID{playerB, playerC, playerD, playerE}
	for i, p := range players {
		if _, err := w.join(p, fmt.Sprintf("u%d", 3+i), link, nil); err != nil {
			t.Fatalf("join player %d: %v", p, err)
		}
	}
	next = w.flush()
	// 每名玩家各收到 1 条自己的确认（不泄漏给他人）
	for _, p := range players {
		pGot := w.sentSince(outbox.ChatID(p), cursor)
		if len(pGot) != 1 {
			t.Fatalf("player %d 应收到 1 条确认，实际 %d: %+v", p, len(pGot), pGot)
		}
		if strings.Contains(pGot[0].text, "阿明") {
			t.Fatalf("player %d 的确认消息泄漏了他人的昵称: %q", p, pGot[0].text)
		}
	}
	// 面板合并：4 次加入只投递 1 条最终面板（6/6，含 5 名玩家昵称）
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 {
		t.Fatalf("B/C/D/E 加入后房主应收到 1 条合并面板，实际 %d 条: %+v", len(hostGot), hostGot)
	}
	if panel := hostGot[0].text; !strings.Contains(panel, "6/6") {
		t.Fatalf("合并面板应为 6/6: %q", panel)
	} else if !strings.Contains(panel, "阿明") {
		t.Fatalf("合并面板应含阿明昵称: %q", panel)
	}
	cursor = next
	if n := w.roomPlayers(room); n != 6 {
		t.Fatalf("room_players count = %d, want 6（满员）", n)
	}
	// 满员后再加入被明确拒绝
	if _, err := w.join(latecomer, "u8", link, nil); !errors.Is(err, game.ErrRoomFull) {
		t.Fatalf("满员加入 error = %v, want game.ErrRoomFull", err)
	}

	// ---------- 4) 退出 / 补位 ----------
	st, err = w.leave(amei, "u9")
	if err != nil {
		t.Fatalf("leave 阿明: %v", err)
	}
	if len(st.Players) != 5 {
		t.Fatalf("退出后 players = %d, want 5: %+v", len(st.Players), st.Players)
	}
	if n := w.roomPlayers(room); n != 5 {
		t.Fatalf("退出后 room_players count = %d, want 5", n)
	}
	next = w.flush()
	// 退出确认只投递本人；房主面板 5/6 且不再含阿明
	ameiGot = w.sentSince(outbox.ChatID(amei), cursor)
	if len(ameiGot) != 1 {
		t.Fatalf("退出后阿明应收到 1 条退出确认，实际 %d: %+v", len(ameiGot), ameiGot)
	} else if !strings.Contains(ameiGot[0].text, "已退出") {
		t.Fatalf("退出确认文案异常: %q", ameiGot[0].text)
	}
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 {
		t.Fatalf("退出后面板应为 1 条，实际 %d: %+v", len(hostGot), hostGot)
	} else if !strings.Contains(hostGot[0].text, "5/6") || strings.Contains(hostGot[0].text, "阿明") {
		t.Fatalf("退出后面板应 5/6 且不含阿明: %q", hostGot[0].text)
	}
	cursor = next
	// 补位：晚到者加入，复用阿明的空座位（2 号）
	res, err = w.join(latecomer, "u10", link, nil)
	if err != nil {
		t.Fatalf("补位加入: %v", err)
	}
	if res.Seat != 2 {
		t.Fatalf("补位座位 = %d, want 2（复用空位）", res.Seat)
	}
	next = w.flush()
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 || !strings.Contains(hostGot[0].text, "6/6") {
		t.Fatalf("补位后面板应 6/6 一条: %+v", hostGot)
	}
	cursor = next
	if n := w.roomPlayers(room); n != 6 {
		t.Fatalf("补位后 room_players count = %d, want 6", n)
	}

	// ---------- 5) 过期：50 分钟提醒 → 1 小时回收 ----------
	w.advance(50 * time.Minute)
	rem := w.evaluateIdle(ctx, room)
	if len(rem) != 1 {
		t.Fatalf("50 分钟提醒 effects = %d, want 1", len(rem))
	}
	next = w.flush()
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 {
		t.Fatalf("提醒后房主应收到 1 条，实际 %d: %+v", len(hostGot), hostGot)
	} else if !strings.Contains(hostGot[0].text, roomCode) {
		t.Fatalf("提醒消息缺少房间码: %q", hostGot[0].text)
	}
	cursor = next

	w.advance(10 * time.Minute)
	exp := w.evaluateIdle(ctx, room)
	if len(exp) != 1 {
		t.Fatalf("过期 effects = %d, want 1", len(exp))
	}
	next = w.flush()
	hostGot = w.sentSince(outbox.ChatID(host), cursor)
	if len(hostGot) != 1 {
		t.Fatalf("过期后房主应收到 1 条，实际 %d: %+v", len(hostGot), hostGot)
	} else if !strings.Contains(hostGot[0].text, "已过期") || !strings.Contains(hostGot[0].text, roomCode) {
		t.Fatalf("过期消息文案异常: %q", hostGot[0].text)
	}
	cursor = next
	if !w.expired(room) {
		t.Fatal("房间应标记为已过期")
	}
	// 过期后加入被拒
	if _, err := w.join(extra, "u12", link, nil); !errors.Is(err, game.ErrRoomExpired) {
		t.Fatalf("过期加入 error = %v, want game.ErrRoomExpired", err)
	}

	// ---------- 6) 无身份泄漏汇总断言 ----------
	// host 聊天只出现过 host 类消息 key（created/panel/reminder/expired）
	for _, m := range w.allAudited() {
		switch m.msg.ChatID {
		case outbox.ChatID(host):
			if m.key != game.CreateRoomMessageKey && m.key != game.LobbyPanelMessageKey &&
				m.key != game.IdleReminderMessageKey && m.key != game.RoomExpiredMessageKey {
				t.Fatalf("host 聊天出现非 host 类消息 %q: %+v", m.key, m.msg)
			}
		default:
			if m.key != game.JoinConfirmedMessageKey && m.key != game.LeaveConfirmedMessageKey {
				t.Fatalf("玩家聊天 %d 出现非本人消息 %q: %+v", m.msg.ChatID, m.key, m.msg)
			}
		}
		if m.text == "" {
			t.Fatalf("消息 %q 文本为空（渲染不得输出空文本）", m.key)
		}
	}
}
