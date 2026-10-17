// Package loadbalance 提供节点选择抽象（Selector，参照 go-kratos selector）。
//
// 相比 v0.3.x 的 Balancer（仅地址+权重的索引选择），Selector 携带完整节点
// 元数据（Version 经 Metadata）、支持按 Metadata 过滤候选、并在调用结束后
// 经 DoneFunc 回写结果（P2C/EWMA 等自适应算法的数据入口）。
//
// 与具体注册中心解耦：Node 经 NodesFrom 泛型适配器从任意实例类型构造，
// 本包不 import 任何 provider 或框架。
package loadbalance

import (
	"context"
	"errors"
	"fmt"
)

// 负载均衡策略名称常量（与 nns/metadata 中配置字符串保持一致）。
const (
	StrategyRoundRobin = "roundrobin"
	StrategyRandom     = "random"
	StrategyWeighted   = "weighted"
	// StrategyP2C 是自适应选择策略（Power of Two Choices，#6）：
	// 依据 DoneFunc 回写的调用时长 EWMA，把流量从慢/故障节点撬走。
	StrategyP2C = "p2c"
)

// ErrNoAvailable 表示无可选节点（候选为空或全部被过滤）。
var ErrNoAvailable = errors.New("loadbalance: no available node")

// Node 是参与选择的节点，携带完整实例信息。
type Node interface {
	// Address 是节点地址（host:port 或 scheme://host:port）。
	Address() string
	// Weight 是调度权重（<=0 视为 1）。
	Weight() float64
	// ServiceName 是节点所属服务名。
	ServiceName() string
	// Metadata 是节点元数据（版本、区域、集群等）。
	Metadata() map[string]string
}

// DoneInfo 是调用结束时的回写信息。
type DoneInfo struct {
	// Err 是本次调用的最终错误（nil = 成功）。
	Err error
	// BytesSent/BytesReceived 标记是否发生了发送/接收（保留字段）。
	BytesSent     bool
	BytesReceived bool
}

// DoneFunc 在本次调用的最终结果确定后回调（含重试、熔断等出口包装）；
// 未回写等价于 DoneInfo 零值。
type DoneFunc func(ctx context.Context, di DoneInfo)

// SelectOption 配置单次 Select。
type SelectOption func(*selectOptions)

type selectOptions struct {
	filter func(Node) bool
	key    string
}

// WithFilter 过滤候选节点（如同 Group/Cluster 优先）；全部被过滤时返回
// ErrNoAvailable。
func WithFilter(fn func(Node) bool) SelectOption {
	return func(o *selectOptions) { o.filter = fn }
}

// WithKey 提供一致性选点键（一致性哈希类 Selector 使用；roundrobin 等忽略）。
func WithKey(key string) SelectOption {
	return func(o *selectOptions) { o.key = key }
}

// Selector 是可回写调用结果的节点选择器。
type Selector interface {
	// Select 从候选节点中选择一个。候选为空或全被过滤返回 ErrNoAvailable；
	// 成功时 selected 与 done 均非 nil。
	Select(ctx context.Context, nodes []Node, opts ...SelectOption) (selected Node, done DoneFunc, err error)
}

// SelectorOption 配置 Selector 实例级行为。
type SelectorOption func(*selectorConfig)

type selectorConfig struct {
	doneHook func(ctx context.Context, n Node, di DoneInfo)
}

// WithDoneHook 订阅每次调用的结果回写（采集入口；本期只采集不参与决策，
// 为 P2C/EWMA 预留）。
func WithDoneHook(fn func(ctx context.Context, n Node, di DoneInfo)) SelectorOption {
	return func(c *selectorConfig) { c.doneHook = fn }
}

// NormalizeSelectorName 将历史别名归一为规范策略名：
//
//	"round_robin"          → "roundrobin"
//	"weighted_round_robin" → "weighted"
//	其他名称原样返回
func NormalizeSelectorName(name string) string {
	switch name {
	case "round_robin":
		return StrategyRoundRobin
	case "weighted_round_robin", "weighted_random":
		return StrategyWeighted
	default:
		return name
	}
}

// NewSelector 按策略名创建 Selector。接受规范名或别名（自动归一）；
// 空名称回退为 roundrobin；未知名称报错。
func NewSelector(name string, opts ...SelectorOption) (Selector, error) {
	switch NormalizeSelectorName(name) {
	case StrategyRoundRobin, "":
		return newRoundRobinSelector(opts...), nil
	case StrategyRandom:
		return newRandomSelector(opts...), nil
	case StrategyWeighted:
		return newWeightedSelector(opts...), nil
	case StrategyP2C:
		return newP2CSelector(opts...), nil
	default:
		return nil, fmt.Errorf("unsupported load balance strategy: %s", name)
	}
}

// doneFunc 组装统一的 DoneFunc：把结果喂给实例级 doneHook。
// 多个 doneHook 场景目前不存在（SelectorOption 单钩子），实现保持简单。
func makeDoneFunc(cfg *selectorConfig, n Node) DoneFunc {
	return func(ctx context.Context, di DoneInfo) {
		if cfg != nil && cfg.doneHook != nil {
			cfg.doneHook(ctx, n, di)
		}
	}
}

// selectOptions 应用单次选项。
func applySelectOptions(opts []SelectOption) *selectOptions {
	o := &selectOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// candidates 返回过滤后的候选节点。
func candidates(nodes []Node, o *selectOptions) []Node {
	if o.filter == nil {
		return nodes
	}
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if o.filter(n) {
			out = append(out, n)
		}
	}
	return out
}

// node 是 Node 的默认实现。
type node struct {
	address  string
	weight   float64
	service  string
	metadata map[string]string
}

func (n *node) Address() string             { return n.address }
func (n *node) Weight() float64             { return n.weight }
func (n *node) ServiceName() string         { return n.service }
func (n *node) Metadata() map[string]string { return n.metadata }

// NodesFrom 从任意实例类型切片构造 Node 切片（字段经闭包映射）。
// 任一闭包为 nil 时使用对应零值。本包不 import 任何实例类型——
// registry.Instance 等由调用方（如 httpx）传闭包适配。
func NodesFrom[T any](items []T, addr func(T) string, weight func(T) float64,
	meta func(T) map[string]string, svc func(T) string) []Node {
	out := make([]Node, len(items))
	for i, it := range items {
		n := &node{}
		if addr != nil {
			n.address = addr(it)
		}
		if weight != nil {
			n.weight = weight(it)
		}
		if svc != nil {
			n.service = svc(it)
		}
		if meta != nil {
			n.metadata = meta(it)
		}
		out[i] = n
	}
	return out
}

// Key 提取 SelectOption 列表中的选点键（未设置返回空串）。
// 供自定义 Selector 实现（如一致性哈希）读取。
func Key(opts []SelectOption) string {
	return applySelectOptions(opts).key
}

// FilterOf 提取过滤函数（未设置返回 nil）。
func FilterOf(opts []SelectOption) func(Node) bool {
	return applySelectOptions(opts).filter
}

// Filtered 返回经选项过滤后的候选节点。
func Filtered(nodes []Node, opts []SelectOption) []Node {
	return candidates(nodes, applySelectOptions(opts))
}
