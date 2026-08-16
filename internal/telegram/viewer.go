package telegram

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/v2up-32mb/telegram-werewolf/internal/outbox"
)

// 查看者分级视图（docs 游戏流程设计.md §死亡玩家权限、§主消息形态 2、
// 阶段消息设计.md §3）。本层只维护主消息页引用与渲染输入，不执行
// Telegram 绘制；消息发送/编辑/删除由接线层与 transport 负责
//（后续任务）。

// ErrPeriodFinalized 表示时间段已定稿冻结，拒绝再编辑该时间段主消息。
var ErrPeriodFinalized = errors.New("telegram: period already finalized")

// maxMainMessageRunes 是主消息单页字符上限（docs §主消息形态 2：超过
// 3000 字符的最后一次合法编辑照常发送并将该页标记为已满，下一次更新
// 创建顺序编号续页）。
const maxMainMessageRunes = 3000

// PeriodKind 是主消息时间段类型（白天/夜晚）。
type PeriodKind int

const (
	PeriodUnknown PeriodKind = iota
	PeriodNight              // 夜晚
	PeriodDay                // 白天
)

// String 返回时间段类型英文短名。
func (k PeriodKind) String() string {
	switch k {
	case PeriodNight:
		return "night"
	case PeriodDay:
		return "day"
	default:
		return "unknown"
	}
}

// Period 是主消息时间段标识（类型 × 序号；自然时间线第 1 夜 → 第 2 天
// 白天 → 第 2 夜 → …，序号与夜间/白天计数对应）。
type Period struct {
	Kind   PeriodKind
	Number int
}

// Valid 报告时间段是否合法（类型已知且序号 >= 1）。
func (p Period) Valid() bool {
	return (p.Kind == PeriodNight || p.Kind == PeriodDay) && p.Number >= 1
}

// String 返回时间段短名（如 day.2 / night.1），用于日志与页引用。
func (p Period) String() string {
	return fmt.Sprintf("%s.%d", p.Kind, p.Number)
}

// PageRef 是某 Chat 某时间段主消息的一个页引用。
type PageRef struct {
	Period    Period
	Page      int   // 顺序编号，从 1 开始
	MessageID int64 // 接线层回填真实 Telegram MessageID
	Length    int   // 当前页累计 rune 长度
	Full      bool  // 超过 3000 字符标记已满，下一次更新创建续页
}

// Viewer 维护每个 Chat 的时间段主消息页（MVP 纯内存，不持久化）。
// 同一时间段内角色子阶段只编辑该时间段主消息页；时间段结束由接线层
// Finalize 定稿冻结。
//
// 非并发安全：按 docs 技术选型.md §6.1「同房间严格有序」，Viewer 归
// 每个房间的 Actor 单 goroutine 持有，外部不得并发调用。
type Viewer struct {
	pages map[outbox.ChatID]map[Period][]PageRef
	final map[outbox.ChatID]map[Period]bool
}

// NewViewer 创建空 Viewer。
func NewViewer() *Viewer {
	return &Viewer{
		pages: make(map[outbox.ChatID]map[Period][]PageRef),
		final: make(map[outbox.ChatID]map[Period]bool),
	}
}

// Current 返回指定 Chat 当前时间段的最后页引用；不存在返回 nil。
func (v *Viewer) Current(chat outbox.ChatID, p Period) *PageRef {
	list := v.pages[chat][p]
	if len(list) == 0 {
		return nil
	}
	last := list[len(list)-1]
	return &last
}

// Append 把一段文本追加到指定 Chat 当前时间段的主消息页：
//   - 无页时创建第 1 页（created=true）；
//   - 当前页已满（Full）时创建续页（created=true），文本落在续页；
//   - 累计长度超过 3000 字符的成功编辑照常落在当前页并将该页标记 Full；
//   - 时间段已定稿返回 ErrPeriodFinalized。
//
// 返回最终页引用与是否新建了页（created）供接线层决定 send/edit。
func (v *Viewer) Append(chat outbox.ChatID, p Period, text string) (PageRef, bool, error) {
	if !p.Valid() {
		return PageRef{}, false, fmt.Errorf("telegram: invalid period %v", p)
	}
	if v.final[chat][p] {
		return PageRef{}, false, ErrPeriodFinalized
	}
	created := false
	cur := v.Current(chat, p)
	if cur == nil {
		cur = &PageRef{Period: p, Page: 1}
		created = true
	} else if cur.Full {
		cur = &PageRef{Period: p, Page: cur.Page + 1}
		created = true
	} else {
		cp := *cur
		cur = &cp
	}
	cur.Length += utf8.RuneCountInString(text)
	if cur.Length > maxMainMessageRunes {
		cur.Full = true
	}
	v.store(chat, p, *cur, created)
	return *cur, created, nil
}

func (v *Viewer) store(chat outbox.ChatID, p Period, ref PageRef, created bool) {
	if v.pages[chat] == nil {
		v.pages[chat] = make(map[Period][]PageRef)
	}
	list := v.pages[chat][p]
	if created {
		v.pages[chat][p] = append(list, ref)
		return
	}
	if len(list) == 0 {
		v.pages[chat][p] = []PageRef{ref}
		return
	}
	list[len(list)-1] = ref
}

// Finalize 定稿指定 Chat 的时间段：此后对该时间段的 Append 一律
// ErrPeriodFinalized（时间段结束定稿冻结，docs §3.1）。
func (v *Viewer) Finalize(chat outbox.ChatID, p Period) {
	if v.final[chat] == nil {
		v.final[chat] = make(map[Period]bool)
	}
	v.final[chat][p] = true
}

// SetMessageID 由接线层回填最近一次发送/编辑的 Telegram MessageID。
func (v *Viewer) SetMessageID(chat outbox.ChatID, p Period, id int64) {
	list := v.pages[chat][p]
	if len(list) == 0 {
		return
	}
	list[len(list)-1].MessageID = id
}
