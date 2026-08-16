package game

import (
	"fmt"
	"sort"
	"time"
)

// 白天麦序与发言效果原语（docs 游戏流程设计.md §白天 2-4、§发言限制、
// 阶段消息设计.md §10.1/10.2）：
//   - 麦序：首夜有死者从死者下一位开始，平安夜从 1 号开始；白天固定
//     一轮、每人一轮一次，结束发言即移交给下一位；
//   - 效果原语：发言控制（speech.turn）、转播（speech.accepted）、
//     拒绝反馈（speech.rejected）、主动超时提示（speech.time_left）、
//     延迟自毁（speech.self_delete 经 DelayEffect）；
//   - 删除消息一律经 DelayEffect 由接线层定时执行，绝不在 handler sleep。
//
// 已知缺口（如实记录，本任务不写 production 接线）：
//  1. 固定限时超时→移交下一位、软限时语义、以及真实删除消息的定时执行
//     属接线层/房间 Actor 任务（State 与 reducer 本任务不修改）；
//  2. 已接受正文累计进主消息由渲染层在接线任务中完成（本文件只产出
//     speech.accepted 效果）。

// 发言消息 key（docs 阶段消息设计.md §10）。
const (
	// SpeechTurnMessageKey 是发言控制临时消息（AudienceActor）：params
	// speaker/deadline/sent/total（docs §10.2 控制消息要素）。
	SpeechTurnMessageKey = "speech.turn"
	// SpeechAcceptedMessageKey 是成功转播（AudiencePublic）：params
	// seat/text（署名由渲染层加，原消息删除不影响已生成的转播记录）。
	SpeechAcceptedMessageKey = "speech.accepted"
	// SpeechRejectedMessageKey 是拒绝反馈（AudienceActor）：params reason。
	SpeechRejectedMessageKey = "speech.rejected"
	// SpeechTimeLeftMessageKey 是主动超时/剩余提示（AudienceActor）。
	SpeechTimeLeftMessageKey = "speech.time_left"
	// SpeechSelfDeleteMessageKey 是原消息/临时消息延迟自毁（AudienceActor）：
	// params chat_id/message_id（Telegram 标识由接线层填入）。
	SpeechSelfDeleteMessageKey = "speech.self_delete"
)

// 拒绝反馈文案（docs §发言限制 4）。
const (
	SpeechRejectTooLong   = "发言过长，已拒绝转播"
	SpeechRejectRoundFull = "本回合已发满 5 条，请点结束发言"
)

// SpeechSelfDeleteAfter 是原消息/错误提示/超时提示自动删除延迟
// （docs §发言限制 2：约 3 秒后自动删除）。
const SpeechSelfDeleteAfter = 3 * time.Second

// DelayEffect 是延迟效果原语：接线层在 After 之后投递 Inner
// （删除消息等副作用定时执行，不在 handler sleep）。
type DelayEffect struct {
	After time.Duration
	Inner Effect
}

func (DelayEffect) effect() {}

// DayStartSeat 计算白天麦序起点（docs §白天 3）：
//   - 首夜有死者：取最小死者座位之后的第一位存活座位（按座位升序环绕）；
//   - 首夜平安夜（无死者）：从 1 号开始。
//
// alive 必须非空；本函数只使用传入集合，不做状态读取。
func DayStartSeat(victims []Seat, alive []Seat) Seat {
	seats := sortedCopy(alive)
	if len(seats) == 0 {
		return 0
	}
	if len(victims) == 0 {
		return 1
	}
	minVictim := victims[0]
	for _, v := range victims[1:] {
		if v < minVictim {
			minVictim = v
		}
	}
	for _, s := range seats {
		if s > minVictim {
			return s
		}
	}
	return seats[0] // 环绕：死者之后无更大座位，从最小存活座位开始
}

// BuildSpeechOrder 生成白天固定一轮麦序：存活玩家按座位升序，从
// DayStartSeat 起点开始环绕（docs §白天 2-3）。
func BuildSpeechOrder(victims []Seat, players []Player) []Seat {
	alive := make([]Seat, 0, len(players))
	for _, p := range players {
		if !p.Dead && p.Seat.Valid() {
			alive = append(alive, p.Seat)
		}
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i] < alive[j] })
	if len(alive) == 0 {
		return nil
	}
	start := DayStartSeat(victims, alive)
	idx := 0
	for i, s := range alive {
		if s == start {
			idx = i
			break
		}
	}
	order := make([]Seat, 0, len(alive))
	order = append(order, alive[idx:]...)
	order = append(order, alive[:idx]...)
	return order
}

// NextSpeech 返回当前发言者之后的下一位（按轮序移交，docs §白天 4）；
// 越过本轮最后一位时 ok=false（本轮结束，不再补充）。
func NextSpeech(current Seat, order []Seat) (Seat, bool) {
	for i, s := range order {
		if s == current {
			if i+1 < len(order) {
				return order[i+1], true
			}
			return 0, false
		}
	}
	return 0, false
}

// SpeechControl 生成发言控制效果与阶段计时（docs 阶段消息设计.md §10.2）：
//   - speech.turn（AudienceActor：speaker/deadline/sent/total）；
//   - TimerEffect（PhaseDaySpeech，时长来自 Settings.EffectiveDurations
//     发言项：标准 60s，快速模式减半取整且 ≥5s）。
func SpeechControl(st State, speaker Seat, sent, total int, at time.Time) ([]Effect, error) {
	speechSec, _, _ := st.Settings.EffectiveDurations()
	if speechSec <= 0 {
		return nil, fmt.Errorf("game: invalid speech duration %d", speechSec)
	}
	deadline := at.Add(time.Duration(speechSec) * time.Second)
	turn, err := NewMessageEffect(AudienceActor, SpeechTurnMessageKey, map[string]any{
		"speaker":  speaker,
		"deadline": deadline,
		"sent":     sent,
		"total":    total,
	})
	if err != nil {
		return nil, err
	}
	return []Effect{
		turn,
		TimerEffect{Phase: PhaseDaySpeech, Duration: time.Duration(speechSec) * time.Second},
	}, nil
}

// SpeechAccept 生成成功转播效果（AudiencePublic；署名由渲染层按
// seat+text 添加，docs §10.1）。
func SpeechAccept(seat Seat, text string) (Effect, error) {
	me, err := NewMessageEffect(AudiencePublic, SpeechAcceptedMessageKey, map[string]any{
		"seat": seat,
		"text": text,
	})
	if err != nil {
		return nil, err
	}
	return me, nil
}

// SpeechReject 生成拒绝反馈效果（AudienceActor；reason 为
// SpeechRejectTooLong / SpeechRejectRoundFull）。
func SpeechReject(seat Seat, reason string) (Effect, error) {
	me, err := NewMessageEffect(AudienceActor, SpeechRejectedMessageKey, map[string]any{
		"reason": reason,
	})
	if err != nil {
		return nil, err
	}
	_ = seat // 反馈只发当前发言者本人；seat 供渲染层署名
	return me, nil
}

// SpeechTimeLeft 生成主动超时/剩余提示效果（AudienceActor）。
func SpeechTimeLeft(seat Seat) (Effect, error) {
	me, err := NewMessageEffect(AudienceActor, SpeechTimeLeftMessageKey, map[string]any{
		"speaker": seat,
	})
	if err != nil {
		return nil, err
	}
	return me, nil
}

// SpeechSelfDelete 生成原消息/错误提示/超时提示的延迟自毁效果：
// DelayEffect{SpeechSelfDeleteAfter, speech.self_delete}，params 携带
// Telegram ChatID 与 MessageID（由接线层在发送/编辑时回填）。
func SpeechSelfDelete(chatID int64, messageID int) (Effect, error) {
	me, err := NewMessageEffect(AudienceActor, SpeechSelfDeleteMessageKey, map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	})
	if err != nil {
		return nil, err
	}
	return DelayEffect{After: SpeechSelfDeleteAfter, Inner: me}, nil
}

func sortedCopy(seats []Seat) []Seat {
	out := append([]Seat(nil), seats...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
