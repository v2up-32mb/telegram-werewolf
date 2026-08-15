package game

// Phase 是主阶段枚举，对应 docs/方案设计.md §3 游戏阶段状态机：
//
//	等待大厅 → 发牌确认 → 夜间 → 白天发言 → 白天投票 →（循环）→ 结算 → 等待大厅
type Phase int

const (
	PhaseUnknown    Phase = iota
	PhaseLobby            // 等待大厅
	PhaseDeal             // 发牌确认
	PhaseNight            // 夜间
	PhaseDaySpeech        // 白天发言
	PhaseDayVote          // 白天投票
	PhaseSettlement       // 结算
)

// Valid 报告阶段是否为 MVP 主阶段合法值。
func (p Phase) Valid() bool {
	return p >= PhaseLobby && p <= PhaseSettlement
}

// String 返回阶段的英文短名，供日志与错误消息使用。
func (p Phase) String() string {
	switch p {
	case PhaseLobby:
		return "lobby"
	case PhaseDeal:
		return "deal"
	case PhaseNight:
		return "night"
	case PhaseDaySpeech:
		return "day_speech"
	case PhaseDayVote:
		return "day_vote"
	case PhaseSettlement:
		return "settlement"
	default:
		return "unknown"
	}
}
