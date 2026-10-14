package httpx

import (
	"context"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/yi-nology/ekit-concurrency/async"
	"github.com/yi-nology/ekit-httpx/loadbalance"
	"github.com/yi-nology/ekit-logx"
	"github.com/yi-nology/ekit-registry/registry"
)

var httpLog = logx.Delegating("httpx")

// ServiceResolver 服务发现解析器接口。
type ServiceResolver interface {
	// Resolve 解析服务名称，返回可用的 endpoint 列表。
	Resolve(ctx context.Context) ([]string, error)
}

// WeightedEndpoint 带权重的 endpoint 视图。
// Weight 为实例注册权重（<=0 由负载均衡策略按 1 处理），0 值在
// 注册中心未提供权重时与等权语义等价。
type WeightedEndpoint struct {
	Addr   string
	Weight float64
}

// WeightedResolver 是 ServiceResolver 的可选增强接口：提供带实例权重的
// endpoint 视图。EndpointManager 检测到 resolver 实现本接口时，会把真实
// 权重喂给支持权重的负载均衡策略（见 WeightedBalanceStrategy）；
// 仅实现 ServiceResolver 的 resolver 保持等权语义（历史行为）。
type WeightedResolver interface {
	// ResolveWeighted 解析服务名称，返回带权重的 endpoint 列表。
	ResolveWeighted(ctx context.Context) ([]WeightedEndpoint, error)
}

// BalanceStrategy 负载均衡策略接口。
type BalanceStrategy interface {
	// Pick 从 endpoints 中选择一个。
	Pick(endpoints []string, req *http.Request) string
}

// HashBalance 哈希负载均衡（基于请求路径 CRC32）。
type HashBalance struct{}

// Pick 基于请求路径 CRC32 哈希选择 endpoint（同一路径总是路由到同一节点）。
func (b *HashBalance) Pick(endpoints []string, req *http.Request) string {
	if len(endpoints) == 0 {
		return ""
	}
	if len(endpoints) == 1 {
		return endpoints[0]
	}
	hash := crc32.ChecksumIEEE([]byte(req.URL.Path))
	return endpoints[int(hash)%len(endpoints)]
}

// lbAdapter 将 network/loadbalance.Balancer 适配为 httpx.BalanceStrategy。
// 消除 httpx 中独立实现的 RoundRobin/Random 负载均衡。
type lbAdapter struct {
	balancer  loadbalance.Balancer
	mu        sync.Mutex
	cached    int                    // 上次缓存的 endpoint 数量
	dirty     bool                   // 权重更新后置位，下次 Pick 重建实例视图
	weights   []float64              // 各 endpoint 的注册权重；nil = 等权
	instances []loadbalance.Instance // 缓存的实例 slice，避免每次 Pick 分配
}

// NewBalanceAdapter 创建适配器，将 loadbalance.Balancer 包装为 httpx.BalanceStrategy。
func NewBalanceAdapter(b loadbalance.Balancer) BalanceStrategy {
	return &lbAdapter{balancer: b}
}

// SetWeights 更新各 endpoint 的权重（与 endpoints 下标对齐，超出部分按等权 1）。
// 实现 WeightedBalanceStrategy；EndpointManager 在发现 resolver 提供实例
// 权重时调用。未调用或传 nil 时保持等权（历史行为）。
func (a *lbAdapter) SetWeights(weights []float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.weights = weights
	a.dirty = true
}

func (a *lbAdapter) Pick(endpoints []string, req *http.Request) string {
	if len(endpoints) == 0 {
		return ""
	}
	if len(endpoints) == 1 {
		return endpoints[0]
	}
	// 复用缓存的实例 slice，避免每次 Pick 分配新 slice；
	// endpoint 数量或权重变化时重建
	a.mu.Lock()
	if a.cached != len(endpoints) || a.dirty {
		a.instances = make([]loadbalance.Instance, len(endpoints))
		for i := range a.instances {
			w := 1.0
			if i < len(a.weights) {
				w = a.weights[i]
			}
			a.instances[i] = strInstance(w)
		}
		a.cached = len(endpoints)
		a.dirty = false
	}
	instances := a.instances
	a.mu.Unlock()

	idx, err := a.balancer.Pick(instances)
	if err != nil || idx < 0 || idx >= len(endpoints) {
		return endpoints[0]
	}
	return endpoints[idx]
}

// WeightedBalanceStrategy 是 BalanceStrategy 的可选增强接口：
// 接受实例权重分布的策略。仅对经 NewBalanceAdapter 适配
// network/loadbalance.Balancer 的策略生效；自定义 BalanceStrategy
// 实现不感知权重（等权语义）。
type WeightedBalanceStrategy interface {
	BalanceStrategy
	SetWeights(weights []float64)
}

// strInstance 实现 loadbalance.Instance 接口，用于字符串 endpoint 列表。
// 浮点数值即权重（默认 1 = 等权）。
type strInstance float64

func (s strInstance) GetWeight() float64 { return float64(s) }

// EndpointManager endpoint 管理器。
// 支持静态配置、动态刷新、缓存降级。
type EndpointManager struct {
	resolver        ServiceResolver
	balance         BalanceStrategy
	endpoints       []string
	cachedEndpoints []string
	mu              sync.RWMutex
	refreshTicker   *time.Ticker
	stopRefresh     chan struct{}
	stopOnce        sync.Once
	cancel          context.CancelFunc
}

// NewEndpointManager 创建 endpoint 管理器。
func NewEndpointManager(resolver ServiceResolver, balance BalanceStrategy, refreshInterval time.Duration) *EndpointManager {
	m := &EndpointManager{
		resolver:    resolver,
		balance:     balance,
		stopRefresh: make(chan struct{}),
	}

	if balance == nil {
		m.balance = NewBalanceAdapter(loadbalance.NewRoundRobin())
	}

	// 启动动态刷新
	if resolver != nil && refreshInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.refreshTicker = time.NewTicker(refreshInterval)
		async.GoSafe(func() {
			m.refreshLoop(ctx)
		})
	}

	return m
}

// refreshLoop 刷新循环。
func (m *EndpointManager) refreshLoop(ctx context.Context) {
	// 立即刷新一次
	m.refreshEndpoints(ctx)

	for {
		select {
		case <-m.stopRefresh:
			return
		case <-m.refreshTicker.C:
			m.refreshEndpoints(ctx)
		}
	}
}

// refreshEndpoints 刷新 endpoint 列表。
//
// resolver 实现 WeightedResolver 时走带权路径：真实实例权重贯通到
// 支持权重的负载均衡策略（WeightedBalanceStrategy），注册中心的权重
// 配置得以在 httpx 路径生效；否则保持等权（历史行为）。
func (m *EndpointManager) refreshEndpoints(ctx context.Context) {
	if m.resolver == nil {
		return
	}

	var endpoints []string
	var weights []float64
	if wr, ok := m.resolver.(WeightedResolver); ok {
		weighted, err := wr.ResolveWeighted(ctx)
		if err != nil {
			httpLog.Warn("refresh endpoints failed, using cached", "err", err)
			return
		}
		if len(weighted) == 0 {
			httpLog.Warn("refresh endpoints returned empty list, using cached")
			return
		}
		endpoints = make([]string, len(weighted))
		weights = make([]float64, len(weighted))
		for i, we := range weighted {
			endpoints[i] = we.Addr
			weights[i] = we.Weight
		}
	} else {
		var err error
		endpoints, err = m.resolver.Resolve(ctx)
		if err != nil {
			httpLog.Warn("refresh endpoints failed, using cached", "err", err)
			return // 使用缓存的 endpoint 列表
		}
		if len(endpoints) == 0 {
			httpLog.Warn("refresh endpoints returned empty list, using cached")
			return
		}
	}

	m.mu.Lock()
	m.endpoints = endpoints
	m.cachedEndpoints = endpoints // 更新缓存
	m.mu.Unlock()

	// 权重仅喂给支持权重的策略；自定义 BalanceStrategy 不感知（等权）
	if weights != nil {
		if ws, ok := m.balance.(WeightedBalanceStrategy); ok {
			ws.SetWeights(weights)
		} else {
			httpLog.Debug("resolver provides weights but balance strategy is not weight-aware, ignoring")
		}
	}

	httpLog.Debug("refreshed endpoints", "endpoints", endpoints)
}

// GetEndpoints 获取 endpoint 列表（优先使用最新，降级使用缓存）。
func (m *EndpointManager) GetEndpoints() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.endpoints) > 0 {
		return m.endpoints
	}
	return m.cachedEndpoints
}

// SetEndpoints 设置静态 endpoint 列表。
func (m *EndpointManager) SetEndpoints(endpoints []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = endpoints
	m.cachedEndpoints = endpoints
}

// Pick 选择一个 endpoint。
func (m *EndpointManager) Pick(req *http.Request) string {
	endpoints := m.GetEndpoints()
	if len(endpoints) == 0 {
		return ""
	}
	return m.balance.Pick(endpoints, req)
}

// Stop 停止刷新。
func (m *EndpointManager) Stop() {
	m.stopOnce.Do(func() {
		if m.refreshTicker != nil {
			m.refreshTicker.Stop()
		}
		if m.cancel != nil {
			m.cancel()
		}
		close(m.stopRefresh)
	})
}

// NNSClient NNS 客户端接口，与顶层 nns.NNSClient.GetInstances 签名一致。
type NNSClient interface {
	GetInstances(ctx context.Context, serviceName string) ([]registry.Instance, error)
}

// NNSResolver NNS 服务发现解析器。
type NNSResolver struct {
	serviceName string
	nnsClient   NNSClient
}

// NewNNSResolver 创建 NNS 解析器。
func NewNNSResolver(serviceName string, nnsClient NNSClient) *NNSResolver {
	return &NNSResolver{
		serviceName: serviceName,
		nnsClient:   nnsClient,
	}
}

// ResolveWeighted 解析服务名称，返回带实例权重的 endpoint 列表。
// 权重来自注册中心（registry.Instance.Weight），使加权 LB 策略在
// httpx 路径上生效；透传 context 到 NNS，保留超时和 trace_id 语义。
func (r *NNSResolver) ResolveWeighted(ctx context.Context) ([]WeightedEndpoint, error) {
	instances, err := r.nnsClient.GetInstances(ctx, r.serviceName)
	if err != nil {
		return nil, fmt.Errorf("nns resolve %s: %w", r.serviceName, err)
	}

	endpoints := make([]WeightedEndpoint, 0, len(instances))
	for _, inst := range instances {
		endpoints = append(endpoints, WeightedEndpoint{
			Addr:   "http://" + net.JoinHostPort(inst.IP, strconv.Itoa(inst.Port)),
			Weight: inst.Weight,
		})
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("nns resolve %s: no instances available", r.serviceName)
	}

	return endpoints, nil
}

// Resolve 解析服务名称，返回可用的 endpoint 列表（丢弃权重）。
func (r *NNSResolver) Resolve(ctx context.Context) ([]string, error) {
	weighted, err := r.ResolveWeighted(ctx)
	if err != nil {
		return nil, err
	}
	endpoints := make([]string, len(weighted))
	for i, we := range weighted {
		endpoints[i] = we.Addr
	}
	return endpoints, nil
}

// StaticResolver 静态 endpoint 解析器。
type StaticResolver struct {
	endpoints []string
}

// NewStaticResolver 创建静态解析器。
func NewStaticResolver(endpoints []string) *StaticResolver {
	return &StaticResolver{endpoints: endpoints}
}

// Resolve 返回静态配置的 endpoint 列表。
func (r *StaticResolver) Resolve(ctx context.Context) ([]string, error) {
	if len(r.endpoints) == 0 {
		return nil, fmt.Errorf("static resolver: no endpoints configured")
	}
	return r.endpoints, nil
}

// Priority 请求优先级。
type Priority int

const (
	PriorityLow    Priority = 0
	PriorityNormal Priority = 1
	PriorityHigh   Priority = 2
)

// PriorityQueue 分级准入控制队列。
//
// 注意语义：这是分级准入控制而非严格优先级队列——high/normal/low 三级
// 各自独立计数（同级 FIFO），不保证跨级抢占；高优先级请求不会抢占已获准的
// 低优先级请求，仅在自身级别容量内准入。未知优先级按 [PriorityNormal] 处理。
type PriorityQueue struct {
	high   chan struct{}
	normal chan struct{}
	low    chan struct{}
}

// NewPriorityQueue 创建优先级队列。
func NewPriorityQueue(capacity int) *PriorityQueue {
	return &PriorityQueue{
		high:   make(chan struct{}, capacity),
		normal: make(chan struct{}, capacity),
		low:    make(chan struct{}, capacity),
	}
}

// Acquire 获取许可（按优先级）。
func (q *PriorityQueue) Acquire(ctx context.Context, priority Priority) bool {
	switch priority {
	case PriorityHigh:
		select {
		case q.high <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	case PriorityNormal:
		select {
		case q.normal <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	case PriorityLow:
		select {
		case q.low <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	default:
		return q.Acquire(ctx, PriorityNormal)
	}
}

// Release 释放许可。
//
// priority 必须与 Acquire 时一致；未知优先级按 [PriorityNormal] 释放
// （与 Acquire 的 default 分支对齐），避免静默泄漏许可。
func (q *PriorityQueue) Release(priority Priority) {
	switch priority {
	case PriorityHigh:
		<-q.high
	case PriorityLow:
		<-q.low
	default:
		<-q.normal
	}
}

// Len 返回队列长度。
func (q *PriorityQueue) Len() int {
	return len(q.high) + len(q.normal) + len(q.low)
}
