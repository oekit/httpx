package loadbalance

import (
	"math/rand"

	"github.com/oekit/registry/registry"
)

// weightedPick 根据实例权重随机选择一个实例的索引（加权随机算法）。
//
// 权重 <= 0 的实例按权重 1 处理。所有实例权重之和为 0 时退化为等概率随机。
func weightedPick(instances []Instance) (int, error) {
	if len(instances) == 0 {
		return -1, registry.ErrNoInstances
	}

	var totalWeight float64
	for _, inst := range instances {
		w := inst.GetWeight()
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}

	if totalWeight == 0 {
		return rand.Intn(len(instances)), nil
	}

	r := rand.Float64() * totalWeight
	var cumulative float64
	for i, inst := range instances {
		w := inst.GetWeight()
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if r <= cumulative {
			return i, nil
		}
	}

	return len(instances) - 1, nil
}

// WeightedBalancer 加权随机负载均衡器。
// 每次 Pick 按权重概率随机选择实例，非确定性轮询。
// 如需确定性加权轮询（weight-3 的实例恰好处理 3/4 请求），需自行实现。
type WeightedBalancer struct{}

// NewWeighted 创建一个新的加权负载均衡器实例。
func NewWeighted() *WeightedBalancer {
	return &WeightedBalancer{}
}

// Pick 根据实例权重从实例列表中选择一个实例，返回其索引。
func (b *WeightedBalancer) Pick(instances []Instance) (int, error) {
	return weightedPick(instances)
}
