package outbox

import (
	"testing"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

const periodKey = "period-main"

func msgPeriod(chat ChatID, version string) Message {
	return Message{
		CorrelationID: version,
		ChatID:        chat,
		RoomID:        game.RoomID("ABC123"),
		Operation:     "edit_message",
		Priority:      PriorityLow,
		CoalesceKey:   periodKey,
	}
}

func TestCoalesceKeepsOnlyLatestPerKey(t *testing.T) {
	c := NewCoalescer()
	if replaced := c.Submit(msgPeriod(ChatID(1), "v1")); replaced {
		t.Fatal("first Submit should not replace")
	}
	if replaced := c.Submit(msgPeriod(ChatID(1), "v2")); !replaced {
		t.Fatal("second Submit of same key should replace")
	}
	if replaced := c.Submit(msgPeriod(ChatID(1), "v3")); !replaced {
		t.Fatal("third Submit of same key should replace")
	}
	if got := c.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1（同 key 只保留最新待发版本）", got)
	}
	msgs := c.Flush()
	if len(msgs) != 1 || msgs[0].CorrelationID != "v3" {
		t.Fatalf("Flush = %+v, want 仅最新版本 v3", msgs)
	}
}

func TestCoalesceKeepsLatestPerChatPerKey(t *testing.T) {
	c := NewCoalescer()
	c.Submit(msgPeriod(ChatID(1), "chat1-v1"))
	c.Submit(msgPeriod(ChatID(1), "chat1-v2"))
	c.Submit(msgPeriod(ChatID(2), "chat2-v1"))
	msgs := c.Flush()
	if len(msgs) != 2 {
		t.Fatalf("Flush len = %d, want 2（各 Chat 独立合并）", len(msgs))
	}
	byChat := map[ChatID]string{}
	for _, m := range msgs {
		byChat[m.ChatID] = m.CorrelationID
	}
	if byChat[ChatID(1)] != "chat1-v2" || byChat[ChatID(2)] != "chat2-v1" {
		t.Fatalf("Flush = %v, want chat1 保留 v2、chat2 保留 v1", byChat)
	}
}

func TestCoalesceNonMergeablePreserved(t *testing.T) {
	c := NewCoalescer()
	identityCard := Message{CorrelationID: "card", ChatID: ChatID(1), Operation: "send_photo", Priority: PriorityHigh, CoalesceKey: ""}
	// 上帝视角实时行动记录：每次行动更新都必须送达，模型上不参与待发合并（空 key）。
	godView := Message{CorrelationID: "god-view", ChatID: ChatID(1), Operation: "edit_message", Priority: PriorityNormal, CoalesceKey: ""}
	report := Message{CorrelationID: "report", ChatID: ChatID(1), Operation: "send_message", Priority: PriorityCritical, CoalesceKey: "settlement"}

	for _, m := range []Message{identityCard, godView, report} {
		if c.Submit(m) {
			t.Fatalf("Submit(%s) unexpectedly replaced", m.CorrelationID)
		}
	}
	// 身份卡（空 key）、上帝视角行动 key、结算战报（关键优先级）全部保留。
	if got := c.Pending(); got != 3 {
		t.Fatalf("Pending = %d, want 3（不可合并消息必须保留）", got)
	}
}

func TestCoalescePaginationKeepsSeparatePages(t *testing.T) {
	c := NewCoalescer()
	page1 := Message{CorrelationID: "page1-v1", ChatID: ChatID(5), Priority: PriorityLow, CoalesceKey: "period-main:page1"}
	page1New := Message{CorrelationID: "page1-v2", ChatID: ChatID(5), Priority: PriorityLow, CoalesceKey: "period-main:page1"}
	page2 := Message{CorrelationID: "page2-v1", ChatID: ChatID(5), Priority: PriorityLow, CoalesceKey: "period-main:page2"}

	c.Submit(page1)
	c.Submit(page1New) // 同页被合并
	c.Submit(page2)    // 3000 冻结后的顺序编号续页：新 key，不得合并

	msgs := c.Flush()
	if len(msgs) != 2 {
		t.Fatalf("Flush len = %d, want 2（续页不互相合并，两页都保留）", len(msgs))
	}
	if msgs[0].CorrelationID != "page1-v2" || msgs[1].CorrelationID != "page2-v1" {
		t.Fatalf("Flush order = %v, want [page1-v2 page2-v1]", msgs)
	}
}

func TestCoalesceFIFOOrderForOtherKeys(t *testing.T) {
	c := NewCoalescer()
	c.Submit(Message{CorrelationID: "first", ChatID: ChatID(9), Priority: PriorityLow, CoalesceKey: "k1"})
	c.Submit(Message{CorrelationID: "second", ChatID: ChatID(9), Priority: PriorityLow, CoalesceKey: "k2"})
	c.Submit(Message{CorrelationID: "first-v2", ChatID: ChatID(9), Priority: PriorityLow, CoalesceKey: "k1"})
	msgs := c.Flush()
	if len(msgs) != 2 {
		t.Fatalf("Flush len = %d, want 2", len(msgs))
	}
	// k1 的替换保留原队首位置；k2 保持原相对顺序。
	if msgs[0].CorrelationID != "first-v2" || msgs[1].CorrelationID != "second" {
		t.Fatalf("Flush = %v, want [first-v2 second]", msgs)
	}
}

func TestCoalesceMajorEventsGoIntoPeriodMessage(t *testing.T) {
	c := NewCoalescer()
	// 重大事件（狼人自爆、猎人开枪、恶意退出等）只作为当前时间段主消息的
	// 更新内容（docs/阶段消息设计.md §14），不单独产生独立永久事件消息。
	base := msgPeriod(ChatID(7), "base")
	event := Message{CorrelationID: "event-into-period", ChatID: ChatID(7), Operation: "edit_message", Priority: PriorityLow, CoalesceKey: periodKey}
	c.Submit(base)
	c.Submit(event)
	msgs := c.Flush()
	if len(msgs) != 1 {
		t.Fatalf("Flush len = %d, want 1（重大事件并入主消息，不另发独立事件消息）", len(msgs))
	}
	if msgs[0].CorrelationID != "event-into-period" {
		t.Fatalf("Flush = %+v, want 仅主消息的最新版本", msgs)
	}
}
