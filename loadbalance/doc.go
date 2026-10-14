// Package loadbalance 提供与具体服务发现实现解耦的负载均衡策略。
//
// 本包只依赖一个轻量的 Instance 接口（仅需 GetWeight），不绑定任何特定的
// 服务实例结构（如 nns/metadata.Instance、httpx.NNSInstance），因此可被
// network、nns 等任意上层包复用，是 ekit 负载均衡的唯一算法实现。
//
// 支持的策略：
//   - 轮询（RoundRobin）
//   - 随机（Random）
//   - 加权随机（Weighted / WeightedRandom）
//
// 使用 New 按策略名创建，或直接调用具体的 NewXxx 构造器。
// 对于实现 Instance 接口的切片类型，用 ToInstances 转换后即可喂给 Balancer。
package loadbalance
