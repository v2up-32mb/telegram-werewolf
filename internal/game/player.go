package game

// Seat 是玩家座位号。MVP 6 人局有效座位严格为 1～6（0 与 7 均非法）。
type Seat int8

// HostSeat 是房主座位：固定为 1（docs/游戏流程设计.md §结算）。
const HostSeat Seat = 1

// Valid 报告座位是否在 MVP 有效范围 1～6 内。
func (s Seat) Valid() bool {
	return s >= 1 && s <= 6
}

// Player 表示一局游戏中的一名玩家。
type Player struct {
	UserID UserID
	Seat   Seat
	Role   Role
	Dead   bool // 是否已死亡；零值 false 表示存活

	// Left 表示是否已离开本局（主动退出/强制移除；退出玩家不能重入
	// 同一局，docs 游戏流程设计.md §退出约束，接线层经 PersistGameLeave
	// 写入 JoinStore.HasLeft）。狼人自爆（ExplodeCommand）不是退出，
	// Left 不置位。
	Left bool
	// TimeoutStreak 是整局累计连续超时次数（docs §恶意退出判定 ②）：
	// 中间有被受理的操作清零；达到 2 私聊预警、达到 3 系统强制移除。
	TimeoutStreak int
}
