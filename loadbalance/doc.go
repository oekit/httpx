// Package loadbalance 提供与具体服务发现实现解耦的节点选择抽象（Selector，
// 参照 go-kratos selector）。
//
// v0.4.0 起 Selector 全面替代 v0.3.x 的 Balancer：
//   - Node 携带完整实例信息（地址/权重/服务名/Metadata）；
//   - Select 支持 WithFilter 按 Metadata 过滤候选、WithKey 一致性选点键；
//   - 调用结束经 DoneFunc 回写结果（DoneInfo），配合 WithDoneHook 采集，
//     为 P2C/EWMA 等自适应算法预留数据入口。
//
// 支持的策略（NewSelector 按名创建，别名自动归一）：
//   - roundrobin（轮询）
//   - random（随机）
//   - weighted（加权随机）
//
// 本包不 import 任何实例类型或框架——registry.Instance 等由调用方经
// NodesFrom 泛型适配器（字段闭包映射）构造 Node。nns-endpoint 等上层包
// 与 httpx 根包复用本实现。
package loadbalance
