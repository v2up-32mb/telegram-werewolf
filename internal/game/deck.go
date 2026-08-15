package game

import "fmt"

// StandardDeck 返回未洗牌的标准 6 人牌组：
// 2 狼、1 预言家、1 女巫、2 平民（docs/游戏流程设计.md「6 人局默认配置」）。
func StandardDeck() []Role {
	return []Role{
		RoleWolf, RoleWolf,
		RoleSeer,
		RoleWitch,
		RoleVillager, RoleVillager,
	}
}

// Shuffle 使用无偏 Fisher–Yates 算法就地洗牌 roles
// （docs/技术选型.md §5.2：发牌采用无偏 Fisher–Yates 洗牌）。
//
// 随机源错误向上传播并保留已完成的交换；空牌组与单元素牌组
// 不做任何随机源调用。生产随机源禁止使用 math/rand。
func Shuffle(roles []Role, rng RNG) error {
	for i := len(roles) - 1; i > 0; i-- {
		j, err := rng.Intn(i + 1)
		if err != nil {
			return fmt.Errorf("game: shuffle rng failure: %w", err)
		}
		roles[i], roles[j] = roles[j], roles[i]
	}
	return nil
}
