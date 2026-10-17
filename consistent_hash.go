package httpx

import (
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"sync"
)

// defaultConsistentReplicas 每个 endpoint 在哈希环上的默认虚拟副本数。
// 值越大键分布越均匀，重建环与查找的开销越大；沿用 go-zero ConsistentHash
// 的同款默认值。
const defaultConsistentReplicas = 100

// consistentNode 哈希环节点：hash 排序后二分查找，idx 映射回 endpoint 下标。
type consistentNode struct {
	hash uint64
	idx  int
}

// ConsistentHashBalance 一致性哈希负载均衡：同一 key 稳定路由到同一 endpoint，
// endpoint 增减仅引起相邻键的重新映射（对比 HashBalance 的全量重排）。
// 适合有状态粘滞路由（同用户/同租户粘住同实例）且实例列表动态变化的场景。
//
// key 缺省取请求路径（可用 WithConsistentKeyFunc 定制，如同租户取 header）；
// 实现了 WeightedBalanceStrategy——resolver 提供实例权重时按权重折算各节点
// 虚拟副本数，注册中心权重配置照常生效。
type ConsistentHashBalance struct {
	replicas int
	hashFunc func([]byte) uint64
	keyFunc  func(req *http.Request) string

	mu        sync.RWMutex
	endpoints []string         // 当前环对应的 endpoint 列表（变更检测）
	weights   []float64        // 当前环对应的权重（nil = 等权）
	ring      []consistentNode // 按 hash 升序
}

// ConsistentHashOption ConsistentHashBalance 的可选参数。
type ConsistentHashOption func(*ConsistentHashBalance)

// WithConsistentReplicas 设置每个 endpoint 的虚拟副本数（默认 100）。
// n <= 0 被忽略。
func WithConsistentReplicas(n int) ConsistentHashOption {
	return func(b *ConsistentHashBalance) {
		if n > 0 {
			b.replicas = n
		}
	}
}

// WithConsistentHashFunc 自定义哈希函数（默认 FNV-1a 64）。nil 被忽略。
func WithConsistentHashFunc(fn func([]byte) uint64) ConsistentHashOption {
	return func(b *ConsistentHashBalance) {
		if fn != nil {
			b.hashFunc = fn
		}
	}
}

// WithConsistentKeyFunc 自定义路由 key 提取（默认请求路径）。nil 被忽略。
// key 语义由业务保证（如同租户取 header 值）；恒为空的 key 会把全部请求
// 路由到同一节点。
func WithConsistentKeyFunc(fn func(req *http.Request) string) ConsistentHashOption {
	return func(b *ConsistentHashBalance) {
		if fn != nil {
			b.keyFunc = fn
		}
	}
}

// NewConsistentHashBalance 创建一致性哈希负载均衡策略。
func NewConsistentHashBalance(opts ...ConsistentHashOption) *ConsistentHashBalance {
	b := &ConsistentHashBalance{
		replicas: defaultConsistentReplicas,
		hashFunc: defaultConsistentHash,
		keyFunc:  defaultConsistentKey,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// SetWeights 更新实例权重（WeightedBalanceStrategy 实现）：权重按比例折算
// 各节点虚拟副本数（等权均值归一），下次 Pick 触发重建环。
func (b *ConsistentHashBalance) SetWeights(weights []float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.weights = weights
	b.ring = nil // 置空触发重建，endpoint 不变也重建（权重已变）
}

// Pick 按一致性哈希选择 endpoint。
func (b *ConsistentHashBalance) Pick(endpoints []string, req *http.Request) string {
	if len(endpoints) == 0 {
		return ""
	}
	if len(endpoints) == 1 {
		return endpoints[0]
	}

	ring := b.ringFor(endpoints)

	var key string
	if req != nil && b.keyFunc != nil {
		key = b.keyFunc(req)
	}
	h := b.hashFunc([]byte(key))
	i := sort.Search(len(ring), func(i int) bool { return ring[i].hash >= h })
	if i == len(ring) {
		i = 0 // 环回绕：取环上最小 hash 的节点
	}
	return endpoints[ring[i].idx]
}

// ringFor 返回当前 endpoint 列表（与权重）对应的环；列表或权重变化时重建。
func (b *ConsistentHashBalance) ringFor(endpoints []string) []consistentNode {
	b.mu.RLock()
	ring, ok := b.ring, b.sameViewLocked(endpoints)
	b.mu.RUnlock()
	if ok && ring != nil {
		return ring
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ring != nil && b.sameViewLocked(endpoints) {
		return b.ring // 双重检查：并发重建已完成
	}
	ring = buildConsistentRing(endpoints, b.weights, b.replicas, b.hashFunc)
	b.endpoints = append(b.endpoints[:0], endpoints...)
	b.ring = ring
	return ring
}

// sameViewLocked endpoint 与权重均未变化（调用方持有锁）。
func (b *ConsistentHashBalance) sameViewLocked(endpoints []string) bool {
	if len(b.endpoints) != len(endpoints) {
		return false
	}
	for i := range endpoints {
		if b.endpoints[i] != endpoints[i] {
			return false
		}
	}
	return true
}

// buildConsistentRing 构建哈希环：每节点 replicas 个虚拟副本（虚拟键
// "replica:addr"），有权重时按权重/均值折算副本数（clamp 到 [1, 4×base]）。
func buildConsistentRing(endpoints []string, weights []float64, baseReplicas int, hash func([]byte) uint64) []consistentNode {
	mean := 0.0
	hasWeights := len(weights) == len(endpoints)
	if hasWeights {
		for _, w := range weights {
			mean += w
		}
		mean /= float64(len(weights))
		if mean <= 0 {
			hasWeights = false // 全零/负权重退化为等权
		}
	}

	ring := make([]consistentNode, 0, len(endpoints)*baseReplicas)
	for i, ep := range endpoints {
		replicas := baseReplicas
		if hasWeights {
			w := weights[i]
			if w <= 0 {
				w = 1 // 权重 <=0 按等权 1 处理（与 WeightedBalancer 约定一致）
			}
			replicas = int(float64(baseReplicas) * w / mean)
			if replicas < 1 {
				replicas = 1
			}
			if replicas > 4*baseReplicas {
				replicas = 4 * baseReplicas
			}
		}
		for r := 0; r < replicas; r++ {
			ring = append(ring, consistentNode{
				hash: hash([]byte(strconv.Itoa(r) + ":" + ep)),
				idx:  i,
			})
		}
	}
	sort.Slice(ring, func(a, b int) bool { return ring[a].hash < ring[b].hash })
	return ring
}

// defaultConsistentHash FNV-1a 64（标准库，零依赖）。
func defaultConsistentHash(data []byte) uint64 {
	h := fnv.New64a()
	h.Write(data)
	return h.Sum64()
}

// defaultConsistentKey 缺省路由 key：请求路径。
func defaultConsistentKey(req *http.Request) string { return req.URL.Path }
