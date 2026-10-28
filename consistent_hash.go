package httpx

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"sync"

	"github.com/oekit/httpx/loadbalance"
)

// defaultConsistentReplicas 每个节点在哈希环上的默认虚拟副本数。
// 值越大键分布越均匀，重建环与查找的开销越大；沿用 go-zero ConsistentHash
// 的同款默认值。
const defaultConsistentReplicas = 100

// consistentNode 哈希环节点：hash 排序后二分查找，idx 映射回节点下标。
type consistentNode struct {
	hash uint64
	idx  int
}

// ConsistentHashSelector 一致性哈希选择器：同一 key 稳定路由到同一节点，
// 节点增减仅引起相邻键的重新映射。适合有状态粘滞路由（同用户/同租户
// 粘住同实例）且实例列表动态变化的场景。
//
// key 取 SelectOption WithKey 值（httpx EndpointManager 默认传请求 PATH）；
// 节点权重按比例折算虚拟副本数（等权均值归一，clamp 到 [1, 4×base]），
// 注册中心权重配置照常生效。
//
// v0.4.0 起替代 ConsistentHashBalance（BalanceStrategy 形态已删除）。
type ConsistentHashSelector struct {
	replicas int
	hashFunc func([]byte) uint64

	mu    sync.RWMutex
	nodes []loadbalance.Node // 当前环对应的节点列表（变更检测）
	ring  []consistentNode   // 按 hash 升序
}

// ConsistentHashOption ConsistentHashSelector 的可选参数。
type ConsistentHashOption func(*ConsistentHashSelector)

// WithConsistentReplicas 设置每个节点的虚拟副本数（默认 100）。
// n <= 0 被忽略。
func WithConsistentReplicas(n int) ConsistentHashOption {
	return func(b *ConsistentHashSelector) {
		if n > 0 {
			b.replicas = n
		}
	}
}

// WithConsistentHashFunc 自定义哈希函数（默认 FNV-1a 64）。nil 被忽略。
func WithConsistentHashFunc(fn func([]byte) uint64) ConsistentHashOption {
	return func(b *ConsistentHashSelector) {
		if fn != nil {
			b.hashFunc = fn
		}
	}
}

// NewConsistentHashSelector 创建一致性哈希选择器。
func NewConsistentHashSelector(opts ...ConsistentHashOption) (*ConsistentHashSelector, error) {
	b := &ConsistentHashSelector{
		replicas: defaultConsistentReplicas,
		hashFunc: defaultConsistentHash,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b, nil
}

// Select 按一致性哈希选择节点。
func (b *ConsistentHashSelector) Select(ctx context.Context, nodes []loadbalance.Node, opts ...loadbalance.SelectOption) (loadbalance.Node, loadbalance.DoneFunc, error) {
	cand := loadbalance.Filtered(nodes, opts)
	if len(cand) == 0 {
		return nil, nil, loadbalance.ErrNoAvailable
	}
	if len(cand) == 1 {
		return cand[0], func(context.Context, loadbalance.DoneInfo) {}, nil
	}

	ring := b.ringFor(cand)
	h := b.hashFunc([]byte(loadbalance.Key(opts)))
	i := sort.Search(len(ring), func(i int) bool { return ring[i].hash >= h })
	if i == len(ring) {
		i = 0 // 环回绕：取环上最小 hash 的节点
	}
	selected := cand[ring[i].idx]
	return selected, func(ctx context.Context, di loadbalance.DoneInfo) {}, nil
}

// ringFor 返回当前节点列表对应的环；列表变化时重建（权重已折算在副本数中，
// 节点对象携带权重，列表不变则环复用）。
func (b *ConsistentHashSelector) ringFor(nodes []loadbalance.Node) []consistentNode {
	b.mu.RLock()
	ring, ok := b.ring, b.sameViewLocked(nodes)
	b.mu.RUnlock()
	if ok && ring != nil {
		return ring
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ring != nil && b.sameViewLocked(nodes) {
		return b.ring // 双重检查：并发重建已完成
	}
	ring = buildConsistentRing(nodes, b.replicas, b.hashFunc)
	b.nodes = append(b.nodes[:0], nodes...)
	b.ring = ring
	return ring
}

// sameViewLocked 节点地址列表与权重均未变化（调用方持有锁）。
//
// **权重也必须参与比较**（2026-10-28，#42）：权重折算出的虚拟副本数参与建环
// （buildConsistentRing 按 weight/mean 计算 replicas）⇒ 每轮刷新都会带来
// 携带新权重的节点对象，但若只比地址，地址不变的调权会被判成「同一视图」
// ⇒ 环被永久缓存 ⇒ **调权静默失效**（注册中心降权重想摘流，降不动）。
//
// 权重用精确比较：权重值来自注册中心的同一份实例数据，刷新之间逐字节稳定，
// 不存在浮点抖动导致的「每次刷新都重建」问题。
func (b *ConsistentHashSelector) sameViewLocked(nodes []loadbalance.Node) bool {
	if len(b.nodes) != len(nodes) {
		return false
	}
	for i := range nodes {
		if b.nodes[i].Address() != nodes[i].Address() {
			return false
		}
		if b.nodes[i].Weight() != nodes[i].Weight() {
			return false
		}
	}
	return true
}

// buildConsistentRing 构建哈希环：每节点 replicas 个虚拟副本（虚拟键
// "replica:addr"），按权重/均值折算副本数（clamp 到 [1, 4×base]）。
func buildConsistentRing(nodes []loadbalance.Node, baseReplicas int, hash func([]byte) uint64) []consistentNode {
	mean := 0.0
	for _, n := range nodes {
		mean += n.Weight()
	}
	mean /= float64(len(nodes))
	hasWeights := mean > 0

	ring := make([]consistentNode, 0, len(nodes)*baseReplicas)
	for i, n := range nodes {
		replicas := baseReplicas
		if hasWeights {
			w := n.Weight()
			if w <= 0 {
				w = 1 // 权重 <=0 按等权 1 处理
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
				hash: hash([]byte(strconv.Itoa(r) + ":" + n.Address())),
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
