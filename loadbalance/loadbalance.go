package loadbalance

import "fmt"

// 负载均衡策略名称常量。
// 这些值与 nns/metadata 中定义的策略字符串保持一致，便于按配置名创建。
const (
	StrategyRoundRobin     = "roundrobin"
	StrategyRandom         = "random"
	StrategyWeighted       = "weighted"
	StrategyWeightedRandom = "weighted_random"
)

// Instance 负载均衡所需的最小实例抽象。
//
// 实现者只需提供 GetWeight 即可参与负载均衡选择。
// 权重 <= 0 的实例会被策略按权重 1 处理。
type Instance interface {
	// GetWeight 返回实例权重，用于加权策略。
	GetWeight() float64
}

// Balancer 从一组实例中选择一个，返回其索引。
type Balancer interface {
	Pick(instances []Instance) (int, error)
}

// ToInstances 将任意实现 Instance 的具体切片转换为 []Instance。
// 用于把 []registry.Instance 等具体类型切片喂给 Balancer。
func ToInstances[T Instance](items []T) []Instance {
	out := make([]Instance, len(items))
	for i := range items {
		out[i] = items[i]
	}
	return out
}

// NormalizeStrategy 将各种别名统一为规范策略名。
// 规范名以 network/loadbalance 常量为准：
//
//	"round_robin"           → "roundrobin"
//	"weighted_round_robin"  → "weighted"
//	其他未知名称原样返回
func NormalizeStrategy(name string) string {
	switch name {
	case "round_robin":
		return StrategyRoundRobin
	case "weighted_round_robin":
		return StrategyWeighted
	default:
		return name
	}
}

// New 根据策略名称创建负载均衡器，空字符串或未知策略回退为轮询。
// 接受规范名或别名（自动标准化）。
func New(strategy string) (Balancer, error) {
	switch NormalizeStrategy(strategy) {
	case StrategyRoundRobin, "":
		return NewRoundRobin(), nil
	case StrategyRandom:
		return NewRandom(), nil
	case StrategyWeighted, StrategyWeightedRandom:
		return NewWeighted(), nil
	default:
		return nil, fmt.Errorf("unsupported load balance strategy: %s", strategy)
	}
}
