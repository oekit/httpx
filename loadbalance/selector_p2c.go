package loadbalance

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// p2c 默认参数（参照 go-kratos selector p2c 与 SRE 实践）。
const (
	// p2cAlpha EWMA 平滑系数：new = old*α + sample*(1-α)。越大越平滑。
	p2cAlpha = 0.8
	// p2cErrorPenalty 失败回写的惩罚时长（加到采样上，抬升故障节点 EWMA）。
	p2cErrorPenalty = 1 * time.Second
)

// p2cSelector 功力倒数第二（Power of Two Choices）自适应选择器（#6）：
// 随机取两个候选，选 EWMA 调用时长较低者。依赖 DoneFunc 回写结果建立
// 节点级统计（Err 计入 1s 惩罚），流量自动从慢/故障节点撬走。
//
// 统计口径：采样 = done 回调时刻与本次 Select 的间隔（近似调用耗时，含
// 调用方侧收尾）。未知节点 EWMA 视为 0（新节点优先获得流量试探）。
//
// 采样起点随 Select 返回的 done 闭包走（每次请求一个 time.Time，#108）：
// 修复前起点按「地址」存进共享 map——同址并发请求互相覆盖时间戳，先发请求
// 的 done 用「后一次 Select 时刻」算耗时，采样被压到接近 0，EWMA 永远学不到
// 真实延迟，慢节点永不摘流。候选只剩 1 个时同样记采样（修复前整段丢弃，
// 单节点场景 EWMA 永不更新）。
type p2cSelector struct {
	cfg         *selectorConfig
	mu          sync.Mutex
	ewma        map[string]float64 // address → EWMA(nanos)
	penaltyNano float64
}

func newP2CSelector(opts ...SelectorOption) *p2cSelector {
	cfg := &selectorConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &p2cSelector{
		cfg:         cfg,
		ewma:        make(map[string]float64),
		penaltyNano: float64(p2cErrorPenalty.Nanoseconds()),
	}
}

func (s *p2cSelector) Select(ctx context.Context, nodes []Node, opts ...SelectOption) (Node, DoneFunc, error) {
	o := applySelectOptions(opts)
	cand := candidates(nodes, o)
	if len(cand) == 0 {
		return nil, nil, ErrNoAvailable
	}

	s.mu.Lock()
	// 候选外地址的统计惰性清掉（节点下线不遗留）
	live := make(map[string]struct{}, len(cand))
	for _, n := range cand {
		live[n.Address()] = struct{}{}
	}
	for addr := range s.ewma {
		if _, ok := live[addr]; !ok {
			delete(s.ewma, addr)
		}
	}

	var chosen Node
	if len(cand) == 1 {
		chosen = cand[0]
	} else {
		// 随机取两个不同候选，选 EWMA 低者（未知节点 EWMA 0，等值随机）
		i := rand.Intn(len(cand))
		j := rand.Intn(len(cand) - 1)
		if j >= i {
			j++
		}
		a, b := cand[i], cand[j]
		ea, eb := s.ewma[a.Address()], s.ewma[b.Address()]
		switch {
		case ea < eb:
			chosen = a
		case eb < ea:
			chosen = b
		default:
			if rand.Intn(2) == 0 {
				chosen = a
			} else {
				chosen = b
			}
		}
	}
	s.mu.Unlock()

	// 采样起点随本次请求走：done 闭包捕获独立 time.Time，同址并发互不覆盖
	// （#108）；单候选场景同样建立采样。
	start := time.Now()
	return chosen, s.makeDone(chosen, start), nil
}

// makeDone 组装结果回写：以 done 调用时刻与本次 Select（start）的间隔为采样
// 更新 EWMA；Err 追加惩罚时长抬升故障节点。
func (s *p2cSelector) makeDone(n Node, start time.Time) DoneFunc {
	addr := n.Address()
	return func(ctx context.Context, di DoneInfo) {
		sample := float64(time.Since(start).Nanoseconds())
		if di.Err != nil {
			sample += s.penaltyNano
		}
		s.mu.Lock()
		old := s.ewma[addr]
		s.ewma[addr] = old*p2cAlpha + sample*(1-p2cAlpha)
		s.mu.Unlock()
		if s.cfg != nil && s.cfg.doneHook != nil {
			s.cfg.doneHook(ctx, n, di)
		}
	}
}
