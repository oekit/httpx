package httpx

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/httpx/loadbalance"
	"github.com/oekit/logx"
	"github.com/oekit/registry"
)

var httpLog = logx.Delegating("httpx")

// ServiceResolver 服务发现解析器接口。
type ServiceResolver interface {
	// Resolve 解析服务名称，返回可用的 endpoint 列表。
	Resolve(ctx context.Context) ([]string, error)
}

// InstanceResolver 是 ServiceResolver 的可选增强接口：提供完整实例视图
// （含权重与元数据）。EndpointManager 检测到 resolver 实现本接口时，
// 实例的权重与 Metadata 贯通到 Selector（加权策略与 WithFilter 生效）；
// 仅实现 ServiceResolver 的 resolver 等权合成（权重 1、无元数据）。
type InstanceResolver interface {
	// ResolveInstances 解析服务名称，返回实例列表。
	ResolveInstances(ctx context.Context) ([]registry.Instance, error)
}

// SelectorNodes 把注册中心实例切片适配为 Selector 节点（字段闭包映射，
// 地址补 "http://" 前缀，与历史 endpoint 形态一致）。
func SelectorNodes(insts []registry.Instance) []loadbalance.Node {
	return loadbalance.NodesFrom(insts,
		func(i registry.Instance) string { return "http://" + i.Addr() },
		func(i registry.Instance) float64 { return i.Weight },
		func(i registry.Instance) map[string]string { return i.Metadata },
		func(i registry.Instance) string { return i.Service },
	)
}

// EndpointManager endpoint 管理器：动态刷新节点快照 + 缓存降级，
// 选择经 loadbalance.Selector 完成。
type EndpointManager struct {
	resolver      ServiceResolver
	selector      loadbalance.Selector
	nodes         []loadbalance.Node
	cachedNodes   []loadbalance.Node
	mu            sync.RWMutex
	refreshTicker *time.Ticker
	stopRefresh   chan struct{}
	stopOnce      sync.Once
	cancel        context.CancelFunc
}

// NewEndpointManager 创建 endpoint 管理器。
// selector 为 nil 时回退 roundrobin；refreshInterval > 0 且 resolver 非空时
// 启动刷新循环。
func NewEndpointManager(resolver ServiceResolver, selector loadbalance.Selector, refreshInterval time.Duration) *EndpointManager {
	if selector == nil {
		var err error
		selector, err = loadbalance.NewSelector("")
		if err != nil {
			panic("httpx: default selector creation failed: " + err.Error())
		}
	}
	m := &EndpointManager{
		resolver:    resolver,
		selector:    selector,
		stopRefresh: make(chan struct{}),
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
	m.refreshNodes(ctx)

	for {
		select {
		case <-m.stopRefresh:
			return
		case <-m.refreshTicker.C:
			m.refreshNodes(ctx)
		}
	}
}

// refreshNodes 刷新节点快照：优先走实例视图（权重/元数据贯通），
// 否则字符串端点等权合成；失败时保留现有快照（缓存降级）。
func (m *EndpointManager) refreshNodes(ctx context.Context) {
	if m.resolver == nil {
		return
	}

	var nodes []loadbalance.Node
	if ir, ok := m.resolver.(InstanceResolver); ok {
		insts, err := ir.ResolveInstances(ctx)
		if err != nil {
			httpLog.Warn("refresh instances failed, using cached", "err", err)
			return
		}
		if len(insts) == 0 {
			httpLog.Warn("refresh instances returned empty list, using cached")
			return
		}
		nodes = SelectorNodes(insts)
	} else {
		endpoints, err := m.resolver.Resolve(ctx)
		if err != nil {
			httpLog.Warn("refresh endpoints failed, using cached", "err", err)
			return
		}
		if len(endpoints) == 0 {
			httpLog.Warn("refresh endpoints returned empty list, using cached")
			return
		}
		nodes = NodesFromEndpoints(endpoints)
	}

	m.mu.Lock()
	m.nodes = nodes
	m.cachedNodes = nodes
	m.mu.Unlock()

	httpLog.Debug("refreshed nodes", "count", len(nodes))
}

// NodesFromEndpoints 把字符串端点列表合成为等权节点（权重 1、无元数据）。
func NodesFromEndpoints(endpoints []string) []loadbalance.Node {
	return loadbalance.NodesFrom(endpoints,
		func(s string) string { return s },
		func(string) float64 { return 1 },
		nil,
		nil,
	)
}

// GetNodes 获取节点快照（优先最新，降级缓存）。
func (m *EndpointManager) GetNodes() []loadbalance.Node {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.nodes) > 0 {
		return m.nodes
	}
	return m.cachedNodes
}

// SetEndpoints 设置静态端点列表（等权合成，停止动态刷新语义不变）。
func (m *EndpointManager) SetEndpoints(endpoints []string) {
	m.SetNodes(NodesFromEndpoints(endpoints))
}

// SetNodes 直接设置节点快照。
func (m *EndpointManager) SetNodes(nodes []loadbalance.Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = nodes
	m.cachedNodes = nodes
}

// Pick 选择一个节点地址，返回地址与结果回写句柄（调用结束后以最终
// 错误调用 done；done 为 nil 表示无需回写）。
// key 用于一致性选点类 Selector（httpx 以请求 PATH 为默认键）。
func (m *EndpointManager) Pick(ctx context.Context, key string) (string, loadbalance.DoneFunc, error) {
	nodes := m.GetNodes()
	if len(nodes) == 0 {
		return "", nil, loadbalance.ErrNoAvailable
	}
	n, done, err := m.selector.Select(ctx, nodes, loadbalance.WithKey(key))
	if err != nil {
		return "", nil, err
	}
	return n.Address(), done, nil
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

// NNSResolver NNS 服务发现解析器（实现 ServiceResolver 与 InstanceResolver）。
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

// ResolveInstances 解析服务名称，返回实例列表（地址合成 "http://ip:port"）。
// 透传 context 到 NNS，保留超时和 trace_id 语义。
func (r *NNSResolver) ResolveInstances(ctx context.Context) ([]registry.Instance, error) {
	instances, err := r.nnsClient.GetInstances(ctx, r.serviceName)
	if err != nil {
		return nil, fmt.Errorf("nns resolve %s: %w", r.serviceName, err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("nns resolve %s: no instances available", r.serviceName)
	}
	return instances, nil
}

// Resolve 解析服务名称，返回地址列表（丢弃权重与元数据）。
func (r *NNSResolver) Resolve(ctx context.Context) ([]string, error) {
	instances, err := r.ResolveInstances(ctx)
	if err != nil {
		return nil, err
	}
	endpoints := make([]string, len(instances))
	for i, inst := range instances {
		endpoints[i] = "http://" + net.JoinHostPort(inst.IP, strconv.Itoa(inst.Port))
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
