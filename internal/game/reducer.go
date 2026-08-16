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
		case TimeoutCommand:
			// 狼人投票轮内超时弃刀；女巫窗口内超时不用任何药；
			// 两者都未开启时保持既有未实现语义（reducer_test 契约）。
			if state.Night.WolfRound > 0 {
				return r.wolfTimeout(state, c)
			}
			if state.Night.WitchStage > WitchStageClosed {
				return r.witchTimeout(state, c)
			}
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
	case SpeakCommand:
		return c.Meta, nil
	case VoteCommand:
		return c.Meta, nil
	case TimeoutCommand:
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
	case TimeoutCommand:
		// 系统命令：仅校验阶段与版本（已通过），豁免 Actor 在场/存活
		// 校验（docs/技术选型.md §6.2 Timer 投递 Timeout Command）。
		return nil
	case CreateRoomCommand, JoinRoomCommand:
		// 创建/加入命令：Actor 在房间建立完成前尚未成为玩家。
		return nil
	default:
		if err := validateActor(state, meta.Actor); err != nil {
			return err
		}
		return validateTarget(state, cmd)
	}
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
