package game

// RoomID 唯一标识一个房间。
//
// 房间码为 6 位短码（去掉易混淆字符 0/O、1/I），也可由房主自定义，
// 但必须保证当前唯一（docs/游戏流程设计.md §房间）。
type RoomID string

// GameID 唯一标识一局游戏。
type GameID string

// UserID 唯一标识一个用户，对应 Telegram 用户 ID（int64）。
type UserID int64
