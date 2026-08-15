package game

// reducer 是 Reducer 接口的骨架实现：前置通用 validator + 阶段分派。
// 具体角色规则由后续 Task 在各阶段 reducer 中实现。
type reducer struct{}

// NewReducer 返回 Reducer 骨架实例。
func NewReducer() Reducer { return reducer{} }

// Reduce 先执行通用拒绝规则（重复 ID、阶段、版本、在场、存活、目标），
// 全部通过后按阶段分派；任意拒绝都返回哨兵错误且不修改 State
// （docs/技术选型.md §13.1：非法命令不得部分修改状态）。
func (reducer) Reduce(state State, cmd Command) (State, []Effect, error) {
	meta, err := commandMeta(cmd)
	if err != nil {
		return state, nil, err
	}
	if err := validate(state, cmd, meta); err != nil {
		return state, nil, err
	}
	// 分派到阶段 reducer。骨架阶段尚未实现任何对局逻辑，
	// 返回明确错误且不修改状态。
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
	case WitchUseCommand:
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
	case WitchUseCommand:
		return requireSeat(c.Target, inRoom, false)
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
