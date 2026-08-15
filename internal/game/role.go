package game

// Role 是 MVP 6 人局角色枚举（docs/角色卡片.md「MVP（6 人局）角色」）。
type Role int

const (
	RoleUnknown  Role = iota
	RoleWolf          // 狼人
	RoleSeer          // 预言家
	RoleWitch         // 女巫
	RoleVillager      // 平民
)

// Valid 报告角色是否属于 MVP 6 人局合法角色。
func (r Role) Valid() bool {
	return r >= RoleWolf && r <= RoleVillager
}

// String 返回角色的英文短名，供日志与错误消息使用。
func (r Role) String() string {
	switch r {
	case RoleWolf:
		return "wolf"
	case RoleSeer:
		return "seer"
	case RoleWitch:
		return "witch"
	case RoleVillager:
		return "villager"
	default:
		return "unknown"
	}
}

// Camp 是阵营枚举：狼人 / 好人（预言家查验结果为二分，docs/游戏流程设计.md §预言家）。
type Camp int

const (
	CampUnknown Camp = iota
	CampWolf         // 狼人阵营
	CampGood         // 好人阵营（神职 + 村民）
)

// Valid 报告阵营是否为合法值。
func (c Camp) Valid() bool {
	return c >= CampWolf && c <= CampGood
}

// String 返回阵营的英文短名。
func (c Camp) String() string {
	switch c {
	case CampWolf:
		return "wolf"
	case CampGood:
		return "good"
	default:
		return "unknown"
	}
}

// Camp 返回角色所属阵营：狼人属于狼人阵营，预言家/女巫/平民属于
// 好人阵营；非法角色返回 CampUnknown。
func (r Role) Camp() Camp {
	switch r {
	case RoleWolf:
		return CampWolf
	case RoleSeer, RoleWitch, RoleVillager:
		return CampGood
	default:
		return CampUnknown
	}
}
