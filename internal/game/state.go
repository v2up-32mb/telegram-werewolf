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

	Lobby      LobbyState
	Deal       DealState
	Night      NightState
	Day        DayState
	Vote       VoteState
	Settled    SettledState
	Governance GovernanceState // 游戏中治理（投票解散/投票踢人/房主强制解散，Task 39）

	// Settings 是建房后房主配置快照（docs「房间设置修改截止」：发牌后
	// 全部锁定）。由接线层填充；零值表示调用方未填充，领域测试必须
	// 显式设置 DefaultRoomSettings()。
	Settings RoomSettings

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

	// 狼人投票（Task 29，docs §夜间 2）：
	// WolfVotes 记录每只存活狼人的当前选择（nil=空刀，仅
	// Settings.WolfMustKill=false 时允许）；WolfLocked 记录已确认锁定
	//（确认后本轮不能修改）；WolfRound 为投票轮次（0=未开始/已结束，
	// 1=第一轮，2=第二轮平票重开）。
	WolfVotes  map[Seat]*Seat
	WolfLocked map[Seat]bool
	WolfRound  int

	// 女巫夜间（Task 30，docs §夜间 3、§8.2 女巫）：
	// WitchStage 是连续决策窗口（0=关闭/未开始，1=解药窗口，
	// 2=毒药窗口）；WitchFirstNight 记录是否首夜（自救仅首夜可选，
	// 由接线层调用 beginWitchPhase 时传入）；WitchUsedTonight 是本夜
	// 是否已用一瓶（beginWitchPhase 重置，reducer 保证一夜一瓶）；
	// WitchSaveChoice/WitchPoisonChoice/WitchPoisonSkip 是各窗口的
	// 待确认选择（确认前可修改，确认后锁定）。
	WitchStage        WitchStage
	WitchFirstNight   bool
	WitchUsedTonight  bool
	WitchSaveChoice   *bool // 解药窗口：true=使用解药，false=不使用
	WitchPoisonChoice *Seat // 毒药窗口：已选择的目标（nil=未选目标）
	WitchPoisonSkip   bool  // 毒药窗口：已选择「不使用毒药」

	// 预言家夜间（Task 31，docs §夜间 4、§8.3 预言家）：
	// SeerActive 表示查验窗口开启（false=未开始/已结束）；SeerPending
	// 是待确认查验目标（nil=未选择，确认前可反复修改）；SeerResults
	// 保存查验历史二分结果（CampWolf/CampGood，跨夜持续携带），
	// SeerChecked 同步记录已查验座位（docs §5 私密标记仅预言家可见）。
	SeerActive  bool
	SeerPending *Seat
	SeerResults map[Seat]Camp
}

// DayState 是白天发言阶段的最小状态（麦序模式）。
type DayState struct {
	Speaker     Seat
	SpeechOrder []Seat
}

// VoteStage 是白天投票阶段内的子阶段。
type VoteStage int

const (
	VoteStageClosed    VoteStage = iota
	VoteStageOpen                // 收票窗口：选择→确认，确认后锁定
	VoteStageLastWords           // 遗言窗口：仅被票死者（「不报身份」模式）
)

// Valid 报告投票子阶段是否为已知取值。
func (s VoteStage) Valid() bool {
	return s >= VoteStageClosed && s <= VoteStageLastWords
}

// VoteState 是白天投票阶段的最小状态（docs 游戏流程设计.md §投票、
// §结算 4 遗言、阶段消息设计.md §8.4、§8.5 平票）：
//   - Stage：收票/遗言/关闭子阶段；
//   - Tie：平票流程子阶段（TieNone=不在平票流程；Task 37）；
//   - TieRound：无发言投票轮计数（0=未开始/缩圈；1/2=第 N 轮无发言轮）；
//   - Candidates：当前平票候选座位（升序）；
//   - Excluded：最终对决被排除（随机/超时失权）的投票人，仅本轮失去
//     投票权、不死亡（docs §投票 4）；
//   - Ballots：确认后的票（投票人 → 目标座位；0=弃权）；
//   - Pending：待确认选择（键存在且 nil=弃权待确认，与 Night.WolfVotes
//     的 nil 键保留语义一致；键不存在=未选择）；
//   - Locked：已确认锁定（确认后不能修改/重复确认）；
//   - Exiled：放逐结果（nil=无人被放逐：全员弃权/零票；真实平票已走
//     平票流程，不在平票未落定时输出 nil 结果）；
//   - LastWords：遗言正文（空=未发表；仅「不报身份」模式启用）。
type VoteState struct {
	Stage      VoteStage
	Tie        TieStage
	TieRound   int
	Candidates []Seat
	Excluded   map[Seat]bool
	Ballots    map[Seat]Seat
	Pending    map[Seat]*Seat
	Locked     map[Seat]bool
	Exiled     *Seat
	LastWords  string
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

	c.Night.WolfVotes = make(map[Seat]*Seat, len(s.Night.WolfVotes))
	for seat, target := range s.Night.WolfVotes {
		if target != nil {
			v := *target
			c.Night.WolfVotes[seat] = &v
		} else {
			// nil=空刀投票，键必须保留（与「未投票」区分）。
			c.Night.WolfVotes[seat] = nil
		}
	}
	c.Night.WolfLocked = make(map[Seat]bool, len(s.Night.WolfLocked))
	for seat, locked := range s.Night.WolfLocked {
		c.Night.WolfLocked[seat] = locked
	}

	if s.Night.WitchSaveChoice != nil {
		v := *s.Night.WitchSaveChoice
		c.Night.WitchSaveChoice = &v
	}
	if s.Night.WitchPoisonChoice != nil {
		v := *s.Night.WitchPoisonChoice
		c.Night.WitchPoisonChoice = &v
	}

	if s.Night.SeerPending != nil {
		v := *s.Night.SeerPending
		c.Night.SeerPending = &v
	}
	c.Night.SeerResults = make(map[Seat]Camp, len(s.Night.SeerResults))
	for seat, camp := range s.Night.SeerResults {
		c.Night.SeerResults[seat] = camp
	}

	c.Day.SpeechOrder = append([]Seat(nil), s.Day.SpeechOrder...)

	c.Vote.Candidates = append([]Seat(nil), s.Vote.Candidates...)
	c.Vote.Excluded = make(map[Seat]bool, len(s.Vote.Excluded))
	for seat, excluded := range s.Vote.Excluded {
		c.Vote.Excluded[seat] = excluded
	}

	c.Vote.Ballots = make(map[Seat]Seat, len(s.Vote.Ballots))
	for from, to := range s.Vote.Ballots {
		c.Vote.Ballots[from] = to
	}

	c.Vote.Pending = make(map[Seat]*Seat, len(s.Vote.Pending))
	for seat, target := range s.Vote.Pending {
		if target != nil {
			v := *target
			c.Vote.Pending[seat] = &v
		} else {
			// nil=弃权待确认，键必须保留（与「未选择」区分）。
			c.Vote.Pending[seat] = nil
		}
	}
	c.Vote.Locked = make(map[Seat]bool, len(s.Vote.Locked))
	for seat, locked := range s.Vote.Locked {
		c.Vote.Locked[seat] = locked
	}
	if s.Vote.Exiled != nil {
		v := *s.Vote.Exiled
		c.Vote.Exiled = &v
	}

	c.Governance.PhaseVersion = s.Governance.PhaseVersion
	c.Governance.DissolveVotes = make(map[Seat]bool, len(s.Governance.DissolveVotes))
	for seat, yes := range s.Governance.DissolveVotes {
		c.Governance.DissolveVotes[seat] = yes
	}
	c.Governance.DissolveBy = make(map[Seat]bool, len(s.Governance.DissolveBy))
	for seat := range s.Governance.DissolveBy {
		c.Governance.DissolveBy[seat] = true
	}
	c.Governance.DissolveInitiated = s.Governance.DissolveInitiated
	c.Governance.KickVotes = make(map[Seat]bool, len(s.Governance.KickVotes))
	for seat, yes := range s.Governance.KickVotes {
		c.Governance.KickVotes[seat] = yes
	}
	c.Governance.KickBy = make(map[Seat]bool, len(s.Governance.KickBy))
	for seat := range s.Governance.KickBy {
		c.Governance.KickBy[seat] = true
	}
	c.Governance.KickInitiated = s.Governance.KickInitiated
	if s.Governance.KickTarget != nil {
		v := *s.Governance.KickTarget
		c.Governance.KickTarget = &v
	}
	c.Governance.HostDissolvePending = s.Governance.HostDissolvePending

	c.Processed = make(map[string]bool, len(s.Processed))
	for id := range s.Processed {
		c.Processed[id] = true
	}

	return c
}
