package game

import "fmt"

// VictoryMode 是胜负模式（docs/游戏流程设计.md §结算）。
type VictoryMode int

const (
	VictoryUnknown   VictoryMode = iota
	VictorySlaughter             // 屠城：好人（神职+村民）全灭即狼人胜；狼人全灭即好人胜
	VictorySide                  // 屠边：神职全灭或村民全灭即狼人胜；狼人全灭即好人胜
)

// Valid 报告胜负模式是否为合法值。
func (v VictoryMode) Valid() bool {
	return v >= VictorySlaughter && v <= VictorySide
}

// String 返回胜负模式的英文短名。
func (v VictoryMode) String() string {
	switch v {
	case VictorySlaughter:
		return "slaughter"
	case VictorySide:
		return "side"
	default:
		return "unknown"
	}
}

// MVP 边界常量（docs/游戏流程设计.md「6 人局（MVP）默认配置总表」）。
const (
	MVPPlayerCount   = 6
	MVPWolfCount     = 2
	MVPSeerCount     = 1
	MVPWitchCount    = 1
	MVPVillagerCount = 2
)

// GameConfig 是一局游戏的配置快照。
//
// MVP 仅支持 6 人局、无 AI 席位、固定标准牌组（2 狼、预言家、女巫、2 平民），
// 拒绝其他人数、AI 与非法牌组构成。
type GameConfig struct {
	PlayerCount int
	Roles       []Role
	UseAI       bool
	Victory     VictoryMode
}

// Validate 校验配置满足 MVP 全部边界，任一违反即返回错误。
func (c GameConfig) Validate() error {
	if err := ValidatePlayerCount(c.PlayerCount); err != nil {
		return err
	}
	if err := ValidateNoAI(c.UseAI); err != nil {
		return err
	}
	if err := ValidateDeck(c.Roles); err != nil {
		return err
	}
	if !c.Victory.Valid() {
		return fmt.Errorf("game: unsupported victory mode %v", c.Victory)
	}
	return nil
}

// ValidatePlayerCount 校验 MVP 人数不变量：只允许 6 人。
func ValidatePlayerCount(n int) error {
	if n != MVPPlayerCount {
		return fmt.Errorf("game: MVP supports %d players only, got %d", MVPPlayerCount, n)
	}
	return nil
}

// ValidateNoAI 校验 MVP 边界：拒绝 AI 席位。
func ValidateNoAI(useAI bool) error {
	if useAI {
		return fmt.Errorf("game: MVP does not support AI seats")
	}
	return nil
}

// ValidateDeck 校验牌组构成不变量：6 人局必须为
// 2 狼、预言家、女巫、2 平民，且不含非法角色。
func ValidateDeck(roles []Role) error {
	if len(roles) != MVPPlayerCount {
		return fmt.Errorf("game: deck has %d roles, want %d", len(roles), MVPPlayerCount)
	}
	counts := map[Role]int{}
	for _, r := range roles {
		if !r.Valid() {
			return fmt.Errorf("game: invalid role %v", r)
		}
		counts[r]++
	}
	want := map[Role]int{
		RoleWolf:     MVPWolfCount,
		RoleSeer:     MVPSeerCount,
		RoleWitch:    MVPWitchCount,
		RoleVillager: MVPVillagerCount,
	}
	for role, n := range want {
		if counts[role] != n {
			return fmt.Errorf("game: role %s count %d, want %d", role, counts[role], n)
		}
	}
	return nil
}
