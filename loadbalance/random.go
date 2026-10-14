package loadbalance

import (
	"math/rand"

	"github.com/yi-nology/ekit-registry/registry"
)

// RandomBalancer 随机负载均衡器。
type RandomBalancer struct{}

// NewRandom 创建一个新的随机负载均衡器实例。
func NewRandom() *RandomBalancer {
	return &RandomBalancer{}
}

// Pick 从实例列表中随机选择一个实例，返回其索引。
func (b *RandomBalancer) Pick(instances []Instance) (int, error) {
	if len(instances) == 0 {
		return -1, registry.ErrNoInstances
	}

	return rand.Intn(len(instances)), nil
}
