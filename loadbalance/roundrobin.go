package loadbalance

import (
	"sync/atomic"

	"github.com/yi-nology/ekit-registry/registry"
)

// RoundRobinBalancer 轮询负载均衡器。
type RoundRobinBalancer struct {
	counter uint64
}

// NewRoundRobin 创建一个新的轮询负载均衡器实例。
func NewRoundRobin() *RoundRobinBalancer {
	return &RoundRobinBalancer{}
}

// Pick 以轮询方式从实例列表中选择一个实例，返回其索引。
func (b *RoundRobinBalancer) Pick(instances []Instance) (int, error) {
	if len(instances) == 0 {
		return -1, registry.ErrNoInstances
	}

	idx := atomic.AddUint64(&b.counter, 1)
	return int(idx % uint64(len(instances))), nil
}
