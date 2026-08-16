package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/v2up-32mb/telegram-werewolf/internal/game"
)

// 白天发言适配层（docs 游戏流程设计.md §白天 2-4、§发言限制、阶段消息
// 设计.md §10.1/10.2）：输入转换 + 判限（tokenizer/RoundCounter 注入
// seam）+ 效果输出。删除消息一律经 game.DelayEffect 由接线层定时执行，
// 绝不在 handler sleep。

// ErrNotSpeechTurn 表示发送者不是当前发言者（拒绝后不产生转播效果）。
var ErrNotSpeechTurn = errors.New("telegram: not your turn to speak")

// SpeechInput 是发言输入的归一化形态（文本消息与回调按钮统一落到本结构）。
type SpeechInput struct {
	// CommandID 是幂等键（update ID / 回调 token 语义）。
	CommandID string
	// Actor 是发言用户 ID。
	Actor game.UserID
	// ChatID 是来源 Chat。
	ChatID int64
	// MessageID 是玩家原始消息 ID（延迟自毁针对本条）。
	MessageID int
	// Text 是发言原文。
	Text string
}

// SpeechSpeaker 是「当前发言者」判定 seam（接线层注入房间/回合状态，
// 按 Actor 返回当前发言座位；ok=false 表示非发言者）。
type SpeechSpeaker interface {
	CurrentSpeaker(ctx context.Context, actor game.UserID) (game.Seat, bool, error)
}

// SpeechHandler 处理一条白天发言输入：判限并产出转播/拒绝反馈与
// 原消息延迟自毁效果。
type SpeechHandler struct {
	speaker SpeechSpeaker
	counter *game.RoundCounter
}

// NewSpeechHandler 构造发言适配器。
func NewSpeechHandler(speaker SpeechSpeaker, counter *game.RoundCounter) *SpeechHandler {
	return &SpeechHandler{speaker: speaker, counter: counter}
}

// Handle 处理一条发言输入：
//   - 非当前发言者 → ErrNotSpeechTurn（无效果）；
//   - 本回合已满 → 拒绝反馈 reason=SpeechRejectRoundFull + 原消息延迟自毁；
//   - 超长/ASCII token ≥21 → 拒绝反馈 reason=SpeechRejectTooLong +
//     原消息延迟自毁（计数不增加）；
//   - 通过 → 转播效果 speech.accepted + 原消息延迟自毁（计数 +1）。
//
// 所有删除均以 DelayEffect{3s, speech.self_delete} 表达，本函数不 sleep。
func (h *SpeechHandler) Handle(ctx context.Context, in SpeechInput) ([]game.Effect, error) {
	if h == nil || h.speaker == nil || h.counter == nil {
		return nil, fmt.Errorf("telegram: nil speech handler dependency")
	}
	seat, ok, err := h.speaker.CurrentSpeaker(ctx, in.Actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotSpeechTurn
	}

	selfDelete, err := game.SpeechSelfDelete(in.ChatID, in.MessageID)
	if err != nil {
		return nil, err
	}

	if !h.counter.CanSend() {
		rej, err := game.SpeechReject(seat, game.SpeechRejectRoundFull)
		if err != nil {
			return nil, err
		}
		return []game.Effect{rej, selfDelete}, nil
	}
	if _, accept := game.CheckSpeechAccept(in.Text); !accept {
		rej, err := game.SpeechReject(seat, game.SpeechRejectTooLong)
		if err != nil {
			return nil, err
		}
		return []game.Effect{rej, selfDelete}, nil
	}
	if err := h.counter.Count(); err != nil {
		return nil, err
	}
	acc, err := game.SpeechAccept(seat, in.Text)
	if err != nil {
		return nil, err
	}
	return []game.Effect{acc, selfDelete}, nil
}
