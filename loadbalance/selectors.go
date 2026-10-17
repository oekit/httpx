package loadbalance

import (
	"context"
	"math/rand"
	"sync/atomic"
)

// roundRobinSelector 轮询选择器：per-Selector 原子游标取模。
type roundRobinSelector struct {
	cfg    *selectorConfig
	cursor atomic.Uint64
}

func newRoundRobinSelector(opts ...SelectorOption) *roundRobinSelector {
	cfg := &selectorConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &roundRobinSelector{cfg: cfg}
}

func (s *roundRobinSelector) Select(ctx context.Context, nodes []Node, opts ...SelectOption) (Node, DoneFunc, error) {
	o := applySelectOptions(opts)
	cand := candidates(nodes, o)
	if len(cand) == 0 {
		return nil, nil, ErrNoAvailable
	}
	n := cand[s.cursor.Add(1)%uint64(len(cand))]
	return n, makeDoneFunc(s.cfg, n), nil
}

// randomSelector 等概率随机选择器。
type randomSelector struct {
	cfg *selectorConfig
}

func newRandomSelector(opts ...SelectorOption) *randomSelector {
	cfg := &selectorConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &randomSelector{cfg: cfg}
}

func (s *randomSelector) Select(ctx context.Context, nodes []Node, opts ...SelectOption) (Node, DoneFunc, error) {
	o := applySelectOptions(opts)
	cand := candidates(nodes, o)
	if len(cand) == 0 {
		return nil, nil, ErrNoAvailable
	}
	n := cand[rand.Intn(len(cand))]
	return n, makeDoneFunc(s.cfg, n), nil
}

// weightedSelector 加权随机选择器：权重 <=0 按 1 处理；总权重为 0 时退化为等概率。
// 内核自 v0.3.x weightedPick 迁移。
type weightedSelector struct {
	cfg *selectorConfig
}

func newWeightedSelector(opts ...SelectorOption) *weightedSelector {
	cfg := &selectorConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return &weightedSelector{cfg: cfg}
}

func (s *weightedSelector) Select(ctx context.Context, nodes []Node, opts ...SelectOption) (Node, DoneFunc, error) {
	o := applySelectOptions(opts)
	cand := candidates(nodes, o)
	if len(cand) == 0 {
		return nil, nil, ErrNoAvailable
	}

	var totalWeight float64
	weights := make([]float64, len(cand))
	for i, n := range cand {
		w := n.Weight()
		if w <= 0 {
			w = 1
		}
		weights[i] = w
		totalWeight += w
	}

	var idx int
	if totalWeight == 0 {
		idx = rand.Intn(len(cand))
	} else {
		r := rand.Float64() * totalWeight
		var cumulative float64
		for i, w := range weights {
			cumulative += w
			if r <= cumulative {
				idx = i
				break
			}
			idx = len(cand) - 1
		}
	}

	n := cand[idx]
	return n, makeDoneFunc(s.cfg, n), nil
}
