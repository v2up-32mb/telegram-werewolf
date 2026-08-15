package game

// State 是一局游戏的不可变快照，由 Reducer 以值语义驱动：
// 新状态 + Effects = Reduce(旧状态, Command)。
//
// MVP 仅覆盖 Lobby/Deal/Night/Day/Vote/Settled 六个主阶段的
// 最小字段，不引入猎人、守卫等后续角色字段。
type State struct {
	RoomID       RoomID
	GameID       GameID
	Phase        Phase
	PhaseVersion uint64
	Players      []Player

	Lobby   LobbyState
	Deal    DealState
	Night   NightState
	Day     DayState
	Vote    VoteState
	Settled SettledState

	// Processed 记录已受理的 Command ID，用于拒绝重复 Command
	// （防止重复结算，docs/技术选型.md §13.2）。
	Processed map[string]bool
}

// LobbyState 是等待大厅阶段的配置快照。
type LobbyState struct {
	Owner  UserID
	Config GameConfig
}

// DealState 是发牌确认阶段的状态：记录已确认身份的座位。
type DealState struct {
	Confirmed []Seat
}

// NightState 是夜间阶段的最小状态：狼人刀人、女巫用药、预言家查验。
type NightState struct {
	WolfKillTarget    *Seat         // 狼人刀人目标（nil 表示尚未出刀）
	WitchSaveUsed     bool          // 解药是否已用
	WitchPoisonUsed   bool          // 毒药是否已用
	WitchPoisonTarget *Seat         // 毒药目标（nil 表示未用）
	SeerChecked       map[Seat]bool // 预言家已查验的座位
}

// DayState 是白天发言阶段的最小状态（麦序模式）。
type DayState struct {
	Speaker     Seat
	SpeechOrder []Seat
}

// VoteState 是白天投票阶段的最小状态：投票人 → 目标座位。
type VoteState struct {
	Ballots map[Seat]Seat
}

// SettledState 是结算阶段的胜利快照。
type SettledState struct {
	Winner Camp
}

// Copy 返回 State 的深拷贝：slice、map 与指针字段均复制，
// 保证纯值语义——Reducers 可安全修改副本而不影响旧状态。
func (s State) Copy() State {
	c := s

	c.Players = append([]Player(nil), s.Players...)

	c.Lobby.Config.Roles = append([]Role(nil), s.Lobby.Config.Roles...)

	c.Deal.Confirmed = append([]Seat(nil), s.Deal.Confirmed...)

	if s.Night.WolfKillTarget != nil {
		seat := *s.Night.WolfKillTarget
		c.Night.WolfKillTarget = &seat
	}
	if s.Night.WitchPoisonTarget != nil {
		seat := *s.Night.WitchPoisonTarget
		c.Night.WitchPoisonTarget = &seat
	}
	c.Night.SeerChecked = make(map[Seat]bool, len(s.Night.SeerChecked))
	for seat, checked := range s.Night.SeerChecked {
		c.Night.SeerChecked[seat] = checked
	}

	c.Day.SpeechOrder = append([]Seat(nil), s.Day.SpeechOrder...)

	c.Vote.Ballots = make(map[Seat]Seat, len(s.Vote.Ballots))
	for from, to := range s.Vote.Ballots {
		c.Vote.Ballots[from] = to
	}

	c.Processed = make(map[string]bool, len(s.Processed))
	for id := range s.Processed {
		c.Processed[id] = true
	}

	return c
}
