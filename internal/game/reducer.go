package game

// reducer 是 Reducer 接口的实现：前置通用 validator + 阶段分派。
// 尚未实现的阶段返回 ErrNotImplemented 且不修改状态。
type reducer struct {
	rng RNG
}

// NewReducer 返回 Reducer 实例；生产随机源为 CryptoRNG。
func NewReducer() Reducer { return reducer{rng: CryptoRNG{}} }

// NewReducerWithRNG 返回使用注入随机源的 Reducer（发牌洗牌可复现，
// docs/技术选型.md §5.2）；rng 为空时回退 CryptoRNG。
func NewReducerWithRNG(rng RNG) Reducer {
	if rng == nil {
		rng = CryptoRNG{}
	}
	return reducer{rng: rng}
}

// Reduce 先执行通用拒绝规则（重复 ID、阶段、版本、在场、存活、目标），
// 全部通过后按阶段分派；任意拒绝都返回哨兵错误且不修改 State
// （docs/技术选型.md §13.1：非法命令不得部分修改状态）。
func (r reducer) Reduce(state State, cmd Command) (State, []Effect, error) {
	meta, err := commandMeta(cmd)
	if err != nil {
		return state, nil, err
	}
	if err := validate(state, cmd, meta); err != nil {
		return state, nil, err
	}
	// 跨阶段命令：自爆与游戏内退出可在多个主阶段受理，由各自 reducer
	// 校验自爆专用边界（白天 + 狼人）与游戏内退出边界（非大厅阶段）后
	// 再进入领域流程（explode.go / leave.go）。
	switch cmd := cmd.(type) {
	case ExplodeCommand:
		return r.explode(state, cmd)
	case LeaveGameCommand:
		return r.leaveGame(state, cmd)
	case GovernanceDissolveCommand:
		return r.governanceDissolve(state, cmd)
	case GovernanceDissolveVoteCommand:
		return r.governanceDissolveVote(state, cmd)
	case GovernanceKickCommand:
		return r.governanceKick(state, cmd)
	case GovernanceKickVoteCommand:
		return r.governanceKickVote(state, cmd)
	case HostDissolveCommand:
		return r.hostDissolve(state, cmd)
	}
	// 分派到阶段 reducer。已实现的阶段（Lobby 开始 / Deal 确认与超时）
	// 进入 deal.go 的领域流程；其余阶段返回明确错误且不修改状态。
	switch state.Phase {
	case PhaseLobby:
		if cmd, ok := cmd.(StartGameCommand); ok {
			return r.startGame(state, cmd)
		}
	case PhaseDeal:
		switch c := cmd.(type) {
		case ConfirmRoleCommand:
			return r.confirmRole(state, c)
		case TimeoutCommand:
			return r.timeoutDeal(state, c)
		}
	case PhaseDayVote:
		// 白天投票（Task 36）：选择/确认/锁定/超时弃票/遗言。
		switch c := cmd.(type) {
		case VoteCommand:
			return r.vote(state, c)
		case VoteConfirmCommand:
			return r.voteConfirm(state, c)
		case TimeoutCommand:
			// 收票窗口超时弃票；遗言窗口超时无正文结束白天；平票流程
			// 各阶段超时：加时发言推进缩圈、缩圈/无发言轮弃票、最终
			// 对决失权（tie_vote.go，docs §投票 4 与「超时与默认选择」）。
			if state.Vote.Stage == VoteStageLastWords {
				return r.lastWordsTimeout(state, c)
			}
			switch state.Vote.Tie {
			case TieSpeech:
				return r.tieSpeechTimeout(state, c)
			case TieRunoff, TieNoSpeech:
				return r.tieRoundTimeout(state, c)
			case TieFinal:
				return r.tieFinalTimeout(state, c)
			default:
				// 收票窗口超时（docs「超时与默认选择」）：先维护连续超时
				// 计数（未确认存活玩家 +1、已确认清零、达到阈值预警/
				// 强制移除），再把未确认者按弃权结算（docs 游戏流程设计.md
				// §恶意退出判定 ②：连续 3 次超时强制移除）。
				st1, fx1, err := r.advanceTimeoutStreaks(state, c.Meta.ReceivedAt, unconfirmedVoters(state))
				if err != nil {
					return state, nil, err
				}
				after, fx2, err := r.voteTimeout(st1, c)
				if err != nil {
					return state, nil, err
				}
				return after, append(fx1, fx2...), nil
			}
		case LastWordsCommand:
			return r.lastWords(state, c)
		}
	case PhaseNight:
		switch c := cmd.(type) {
		case WolfVoteCommand:
			return r.wolfVote(state, c)
		case WolfConfirmCommand:
			return r.wolfConfirm(state, c)
		case WitchSaveCommand:
			return r.witchSave(state, c)
		case WitchPoisonCommand:
			return r.witchPoisonSelect(state, c)
		case WitchConfirmCommand:
			return r.witchConfirm(state, c)
		case SeerCheckCommand:
			return r.seerCheck(state, c)
		case SeerConfirmCommand:
			return r.seerConfirm(state, c)
		case TimeoutCommand:
			// 狼人投票轮内超时弃刀；女巫窗口内超时不用任何药；
			// 预言家窗口内超时空验；全未开启时保持既有未实现语义
			// （reducer_test 契约）。
			if state.Night.WolfRound > 0 {
				return r.wolfTimeout(state, c)
			}
			if state.Night.WitchStage > WitchStageClosed {
				return r.witchTimeout(state, c)
			}
			if state.Night.SeerActive {
				return r.seerTimeout(state, c)
			}
		}
	case PhaseSettlement:
		// 结算阶段（Task 40）：房主「再来一局」回等待大厅。
		if cmd, ok := cmd.(RematchCommand); ok {
			return r.rematch(state, cmd)
		}
	}
	return state, nil, ErrNotImplemented
}

// commandMeta 提取命令携带的 Meta；未注册的命令类型返回 ErrUnknownCommand。
func commandMeta(cmd Command) (CommandMeta, error) {
	switch c := cmd.(type) {
	case CreateRoomCommand:
		return c.Meta, nil
	case JoinRoomCommand:
		return c.Meta, nil
	case StartGameCommand:
		return c.Meta, nil
	case ConfirmRoleCommand:
		return c.Meta, nil
	case WolfKillCommand:
		return c.Meta, nil
	case WolfVoteCommand:
		return c.Meta, nil
	case WolfConfirmCommand:
		return c.Meta, nil
	case WitchUseCommand:
		return c.Meta, nil
	case WitchSaveCommand:
		return c.Meta, nil
	case WitchPoisonCommand:
		return c.Meta, nil
	case WitchConfirmCommand:
		return c.Meta, nil
	case SeerCheckCommand:
		return c.Meta, nil
	case SeerConfirmCommand:
		return c.Meta, nil
	case SpeakCommand:
		return c.Meta, nil
	case VoteCommand:
		return c.Meta, nil
	case VoteConfirmCommand:
		return c.Meta, nil
	case LastWordsCommand:
		return c.Meta, nil
	case TimeoutCommand:
		return c.Meta, nil
	case ExplodeCommand:
		return c.Meta, nil
	case LeaveGameCommand:
		return c.Meta, nil
	case GovernanceDissolveCommand:
		return c.Meta, nil
	case GovernanceDissolveVoteCommand:
		return c.Meta, nil
	case GovernanceKickCommand:
		return c.Meta, nil
	case GovernanceKickVoteCommand:
		return c.Meta, nil
	case HostDissolveCommand:
		return c.Meta, nil
	case RematchCommand:
		return c.Meta, nil
	default:
		return CommandMeta{}, ErrUnknownCommand
	}
}

// validate 执行通用拒绝规则。
func validate(state State, cmd Command, meta CommandMeta) error {
	if state.Processed[meta.ID] {
		return ErrDuplicateCommand
	}
	if meta.ExpectedPhase != state.Phase {
		return ErrWrongPhase
	}
	if meta.PhaseVersion != state.PhaseVersion {
		return ErrStalePhaseVersion
	}
	switch cmd.(type) {
	case TimeoutCommand, LastWordsCommand:
		// 系统/特殊命令：仅校验阶段与版本（已通过），豁免 Actor 在场/
		// 存活校验（docs/技术选型.md §6.2 Timer 投递 Timeout Command；
		// LastWordsCommand 的 actor 是被票死者，已 Dead，由 vote.go
		// reducer 专门校验 Actor==被票死者）。
		return nil
	case CreateRoomCommand, JoinRoomCommand:
		// 创建/加入命令：Actor 在房间建立完成前尚未成为玩家。
		return nil
	case RematchCommand:
		// 「再来一局」是结算阶段的大厅控制操作：房主（房间所有者）即使
		// 本局已死亡也仍在房间内，可发起再来一局（docs §结算 5/6：房主
		// 控制；死亡只约束游戏内操作）。仅校验在房间内，存活校验由其他
		// 游戏内命令保留。
		if !actorInRoom(state, meta.Actor) {
			return ErrNotInRoom
		}
		return nil
	default:
		if err := validateActor(state, meta.Actor); err != nil {
			return err
		}
		return validateTarget(state, cmd)
	}
}

// actorInRoom 报告操作者是否为房间内的玩家（不要求存活）。
func actorInRoom(state State, actor UserID) bool {
	for _, p := range state.Players {
		if p.UserID == actor {
			return true
		}
	}
	return false
}

// validateActor 校验 Actor 是房间内的存活玩家。
func validateActor(state State, actor UserID) error {
	for _, p := range state.Players {
		if p.UserID == actor {
			if p.Dead {
				return ErrDeadPlayer
			}
			return nil
		}
	}
	return ErrNotInRoom
}

// validateTarget 按命令类型校验目标座位：必须在房间内；
// 对必须针对存活的命令（查验、非弃权投票），死亡目标同样拒绝。
func validateTarget(state State, cmd Command) error {
	inRoom := make(map[Seat]Player, len(state.Players))
	for _, p := range state.Players {
		if !p.Seat.Valid() {
			continue
		}
		inRoom[p.Seat] = p
	}
	switch c := cmd.(type) {
	case WolfKillCommand:
		return requireSeat(c.Target, inRoom, false)
	case WolfVoteCommand:
		// nil=主动空刀（仅 Settings.WolfMustKill=false 允许，由夜间
		// reducer 拦截）；非 nil 必须为房间内存活玩家（含自己与狼队友）。
		if c.Target == nil {
			return nil
		}
		return requireSeat(*c.Target, inRoom, true)
	case WitchUseCommand:
		return requireSeat(c.Target, inRoom, false)
	case WitchPoisonCommand:
		if c.Target == nil {
			return nil // 不使用毒药
		}
		return requireSeat(*c.Target, inRoom, true)
	case SeerCheckCommand:
		return requireSeat(c.Target, inRoom, true)
	case VoteCommand:
		if c.Target == nil {
			return nil // 弃权
		}
		return requireSeat(*c.Target, inRoom, true)
	case GovernanceKickCommand:
		// 投票踢人目标必须为房间内存活玩家（本人拒绝由治理 reducer
		// 单独校验 ErrGovernanceKickSelf）。
		return requireSeat(c.Target, inRoom, true)
	default:
		return nil
	}
}

// requireSeat 校验目标座位存在于房间；mustAlive 时目标必须存活。
func requireSeat(seat Seat, inRoom map[Seat]Player, mustAlive bool) error {
	p, ok := inRoom[seat]
	if !ok {
		return ErrInvalidTarget
	}
	if mustAlive && p.Dead {
		return ErrInvalidTarget
	}
	return nil
}
