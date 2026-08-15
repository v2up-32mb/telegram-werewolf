package game

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// RNG 是核心引擎依赖的可注入随机源（docs/技术选型.md §5.2）。
// Intn 返回 [0, n) 范围内无偏均匀的整数。
type RNG interface {
	Intn(n int) (int, error)
}

// maxUint32 是 32 位拒绝采样的模数上限。
const maxUint32 = 1<<32 - 1

// CryptoRNG 是基于 crypto/rand 的无偏整数采样实现。
// 生产环境统一使用本实现；测试使用固定或脚本化随机源。
type CryptoRNG struct{}

// Intn 返回 [0, n) 的无偏随机整数。
//
// 采用 32 位拒绝采样：仅接受小于最大 n 倍数下限的值，
// 消除模偏差；crypto/rand 读取失败或参数非法时返回明确错误。
func (CryptoRNG) Intn(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("game: Intn bound %d must be positive", n)
	}
	if uint64(n) > maxUint32 {
		return 0, fmt.Errorf("game: Intn bound %d exceeds uint32 sampling range", n)
	}
	limit := maxUint32 - maxUint32%uint32(n)
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("game: crypto/rand read: %w", err)
		}
		v := binary.BigEndian.Uint32(b[:])
		if v < limit {
			return int(v % uint32(n)), nil
		}
	}
}
