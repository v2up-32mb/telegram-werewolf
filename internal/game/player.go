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
}
