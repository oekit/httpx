package loadbalance

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func nodesOf(addrs ...string) []Node {
	return NodesFrom(addrs,
		func(s string) string { return s },
		func(string) float64 { return 1 },
		func(s string) map[string]string { return map[string]string{"v": s} },
		func(string) string { return "svc" },
	)
}

func TestSelectorRoundRobinDistribution(t *testing.T) {
	sel, err := NewSelector("roundrobin")
	if err != nil {
		t.Fatal(err)
	}
	nodes := nodesOf("a", "b", "c")
	picked := map[string]int{}
	for i := 0; i < 6; i++ {
		n, done, err := sel.Select(context.Background(), nodes)
		if err != nil || n == nil || done == nil {
			t.Fatalf("select: node=%v done=%v err=%v", n, done, err)
		}
		picked[n.Address()]++
		done(context.Background(), DoneInfo{})
	}
	for addr, cnt := range picked {
		if cnt != 2 {
			t.Fatalf("addr %s picked %d times, want 2", addr, cnt)
		}
	}
}

func TestSelectorRoundRobinCursorResetsOnSize(t *testing.T) {
	// 游标取模对候选数变化鲁棒（不 panic、仍在候选集合内）
	sel, _ := NewSelector("")
	nodes := nodesOf("a", "b")
	for i := 0; i < 5; i++ {
		n, _, err := sel.Select(context.Background(), nodes)
		if err != nil {
			t.Fatal(err)
		}
		if n.Address() != "a" && n.Address() != "b" {
			t.Fatalf("unexpected node %s", n.Address())
		}
	}
}

func TestSelectorEmptyNodes(t *testing.T) {
	for _, name := range []string{"roundrobin", "random", "weighted"} {
		sel, _ := NewSelector(name)
		if _, _, err := sel.Select(context.Background(), nil); !errors.Is(err, ErrNoAvailable) {
			t.Fatalf("%s: err = %v, want ErrNoAvailable", name, err)
		}
	}
}

func TestSelectorFilter(t *testing.T) {
	sel, _ := NewSelector("roundrobin")
	nodes := nodesOf("a", "b")
	for i := 0; i < 4; i++ {
		n, _, err := sel.Select(context.Background(), nodes, WithFilter(func(n Node) bool {
			return n.Metadata()["v"] == "a"
		}))
		if err != nil || n.Address() != "a" {
			t.Fatalf("got %v %v", n, err)
		}
	}
	if _, _, err := sel.Select(context.Background(), nodes, WithFilter(func(Node) bool {
		return false
	})); !errors.Is(err, ErrNoAvailable) {
		t.Fatalf("all-filtered err = %v", err)
	}
}

func TestSelectorWeightedDistribution(t *testing.T) {
	sel, _ := NewSelector("weighted")
	nodes := NodesFrom([]string{"light", "heavy"},
		func(s string) string { return s },
		func(s string) float64 {
			if s == "heavy" {
				return 9
			}
			return 1
		},
		nil,
		func(string) string { return "svc" },
	)
	heavy := 0
	const total = 1000
	for i := 0; i < total; i++ {
		n, _, err := sel.Select(context.Background(), nodes)
		if err != nil {
			t.Fatal(err)
		}
		if n.Address() == "heavy" {
			heavy++
		}
	}
	// 期望 ~90%，放宽到 [80%, 99%]
	if heavy < total*8/10 || heavy > total*99/100 {
		t.Fatalf("heavy ratio out of band: %d/%d", heavy, total)
	}
}

func TestSelectorWeightedZeroWeights(t *testing.T) {
	sel, _ := NewSelector("weighted")
	nodes := NodesFrom([]string{"a", "b"},
		func(s string) string { return s },
		func(string) float64 { return 0 }, // 全零权重 → 等概率
		nil, func(string) string { return "svc" },
	)
	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		n, _, err := sel.Select(context.Background(), nodes)
		if err != nil {
			t.Fatal(err)
		}
		seen[n.Address()]++
	}
	if len(seen) != 2 {
		t.Fatalf("zero-weight should degrade to equal-probability: %v", seen)
	}
}

func TestDoneHook(t *testing.T) {
	var gotNode Node
	var gotDI DoneInfo
	sel, _ := NewSelector("roundrobin", WithDoneHook(func(_ context.Context, n Node, di DoneInfo) {
		gotNode, gotDI = n, di
	}))
	nodes := nodesOf("a")
	n, done, err := sel.Select(context.Background(), nodes)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("x")
	done(context.Background(), DoneInfo{Err: wantErr})
	if gotNode == nil || gotNode.Address() != n.Address() || !errors.Is(gotDI.Err, wantErr) {
		t.Fatalf("done hook: node=%v di=%+v", gotNode, gotDI)
	}
}

func TestNormalizeSelectorName(t *testing.T) {
	cases := map[string]string{
		"round_robin":          "roundrobin",
		"weighted_round_robin": "weighted",
		"weighted_random":      "weighted",
		"random":               "random",
		"roundrobin":           "roundrobin",
		"":                     "",
	}
	for in, want := range cases {
		if got := NormalizeSelectorName(in); got != want {
			t.Fatalf("NormalizeSelectorName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewSelectorUnknownStrategy(t *testing.T) {
	if _, err := NewSelector("p2cx"); err == nil {
		t.Fatal("unknown strategy should error")
	}
}

func TestNodesFromNilClosures(t *testing.T) {
	nodes := NodesFrom([]int{1, 2}, nil, nil, nil, nil)
	if len(nodes) != 2 || nodes[0].Address() != "" || nodes[0].Weight() != 0 {
		t.Fatalf("NodesFrom nil closures broken: %+v", nodes)
	}
	if got := nodes[1].Metadata(); got != nil {
		t.Fatalf("metadata should be nil, got %v", got)
	}
	// Node 切片顺序保持
	addrs := make([]string, 0, 2)
	for _, n := range nodesOf("x", "y") {
		addrs = append(addrs, n.Address())
	}
	if !slices.Equal(addrs, []string{"x", "y"}) {
		t.Fatalf("order broken: %v", addrs)
	}
}

// TestSelectorP2CSkewsToFastNode 钉住 #6：P2C 依据 DoneFunc 回写的调用时长
// 建立 EWMA，把流量从慢节点撬到快节点。
func TestSelectorP2CSkewsToFastNode(t *testing.T) {
	sel, err := NewSelector("p2c")
	if err != nil {
		t.Fatal(err)
	}
	nodes := NodesFrom([]string{"fast", "slow"},
		func(s string) string { return s }, func(string) float64 { return 1 },
		nil, func(string) string { return "svc" })

	ctx := context.Background()
	slow, fast := 0, 0
	const rounds = 40
	for i := 0; i < rounds; i++ {
		n, done, err := sel.Select(ctx, nodes)
		if err != nil {
			t.Fatal(err)
		}
		isSlow := n.Address() == "slow"
		// 模拟慢节点耗时（EWMA 经 done 回写采样；fast 立即 done）
		if isSlow {
			time.Sleep(20 * time.Millisecond)
		}
		done(ctx, DoneInfo{})
		if isSlow {
			slow++
		} else {
			fast++
		}
	}
	// warmup 后流量应显著偏向 fast（各 50% 起步 → fast 应 > 70%）
	if fast <= slow {
		t.Fatalf("p2c did not skew to fast node: fast=%d slow=%d", fast, slow)
	}
}

// TestSelectorP2CErrorPenalty 钉住错误惩罚：持续报错的节点 EWMA 被抬升，
// 后续选择偏向健康节点。
func TestSelectorP2CErrorPenalty(t *testing.T) {
	sel, _ := NewSelector("p2c")
	nodes := NodesFrom([]string{"healthy", "flaky"},
		func(s string) string { return s }, func(string) float64 { return 1 },
		nil, func(string) string { return "svc" })

	ctx := context.Background()
	healthyPicks := 0
	const rounds = 30
	for i := 0; i < rounds; i++ {
		n, done, _ := sel.Select(ctx, nodes)
		isFlaky := n.Address() == "flaky"
		if isFlaky {
			time.Sleep(15 * time.Millisecond)
			done(ctx, DoneInfo{Err: errors.New("flaky fail")})
		} else {
			done(ctx, DoneInfo{})
			healthyPicks++
		}
	}
	if healthyPicks < rounds*7/10 {
		t.Fatalf("error penalty not effective: healthy=%d/%d", healthyPicks, rounds)
	}
}

// TestSelectorP2CSingleNode 钉住边界：单节点直接返回。
func TestSelectorP2CSingleNode(t *testing.T) {
	sel, _ := NewSelector("p2c")
	nodes := nodesOf("only")
	n, done, err := sel.Select(context.Background(), nodes)
	if err != nil || n.Address() != "only" || done == nil {
		t.Fatalf("single node: %v %v %v", n, done, err)
	}
}

// TestSelectorP2CSampleStartPerRequest 回归（#108）：采样起点必须随请求走
// （done 闭包捕获独立 time.Time）。修复前起点按「地址」存进共享 map——同址
// 并发请求互相覆盖时间戳，先发请求的 done 用「后一次 Select 时刻」算耗时，
// 采样被压到接近 0，EWMA 永远学不到真实延迟，慢节点永不摘流。
func TestSelectorP2CSampleStartPerRequest(t *testing.T) {
	s := newP2CSelector()
	// 两节点同址：Select 无论命中哪个实例，地址都是 same:1，精确复现
	// 「同址覆盖起点」
	nodes := NodesFrom([]string{"a#1", "a#2"},
		func(string) string { return "same:1" }, func(string) float64 { return 1 },
		nil, func(string) string { return "svc" })

	ctx := context.Background()
	_, done1, err := s.Select(ctx, nodes)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond) // 慢请求在途
	_, _, err = s.Select(ctx, nodes)  // 第二次选点：修复前覆盖同址采样起点
	if err != nil {
		t.Fatal(err)
	}
	done1(ctx, DoneInfo{}) // 慢请求回写：采样必须 ≈ 40ms，而非被覆盖后的 ≈ 0

	s.mu.Lock()
	ewma := s.ewma["same:1"]
	s.mu.Unlock()
	// 40ms 采样经 α=0.8 首次回写：EWMA ≈ 40ms*0.2 = 8ms，远超被覆盖时的微秒级
	if ewma < float64((5 * time.Millisecond).Nanoseconds()) {
		t.Fatalf("EWMA not fed by per-request sample: %v nanos (< 5ms)", ewma)
	}
}

// TestSelectorP2CSingleNodeRecordsSample 回归（#108 附带）：候选只剩 1 个时
// 的早退分支同样要建立采样——修复前该分支从不写起点，done 无从回写，
// 单节点场景 EWMA 永不更新。
func TestSelectorP2CSingleNodeRecordsSample(t *testing.T) {
	s := newP2CSelector()
	nodes := nodesOf("only")
	ctx := context.Background()
	_, done, err := s.Select(ctx, nodes)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	done(ctx, DoneInfo{})

	s.mu.Lock()
	ewma := s.ewma["only"]
	s.mu.Unlock()
	if ewma < float64((5 * time.Millisecond).Nanoseconds()) {
		t.Fatalf("single-node branch dropped sampling: EWMA = %v nanos", ewma)
	}
}

// TestSelectorP2C_ViewChangePrunesStaleEwma 回归（#170）：候选视图变化时
// （节点下线）候选外地址的 EWMA 必须被清掉——修复前靠每请求全量清理兜底，
// 重构为「视图未变跳过清理」后，清理语义钉在视图变化一代上。
func TestSelectorP2C_ViewChangePrunesStaleEwma(t *testing.T) {
	s := newP2CSelector()
	ctx := context.Background()

	// 直接预置统计（确定性）：leaving 下线后必须被清，stay 在线必须保留
	s.mu.Lock()
	s.ewma["leaving"] = 1
	s.ewma["stay"] = 2
	s.mu.Unlock()

	// 节点下线：候选只剩 stay
	if _, _, err := s.Select(ctx, nodesOf("stay")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, leavingLeft := s.ewma["leaving"]
	_, stayLeft := s.ewma["stay"]
	s.mu.Unlock()
	if leavingLeft {
		t.Error("stale address ewma not pruned after view change (#170)")
	}
	if !stayLeft {
		t.Error("live address ewma must survive pruning")
	}
}

// TestSelectorP2C_ViewRejoinRestartsClean 钉住 #170 的对偶面：节点回线
// （离开后重新进入候选）以未知 EWMA（0）重新参与试探——离线期间的旧统计
// 不得复活。
func TestSelectorP2C_ViewRejoinRestartsClean(t *testing.T) {
	s := newP2CSelector()
	ctx := context.Background()

	// a 下线：其统计必须被清（后续回线以未知 EWMA 0 重新参与试探，
	// 离线期间的旧统计不得复活）
	s.mu.Lock()
	s.ewma["a"] = 1
	s.ewma["b"] = 2
	s.mu.Unlock()

	if _, _, err := s.Select(ctx, nodesOf("b")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, aLeft := s.ewma["a"]
	s.mu.Unlock()
	if aLeft {
		t.Error("offline address ewma must be pruned so rejoin restarts clean (#170)")
	}
}

// TestSelectorP2C_UnchangedViewKeepsStats 钉住 #170 的语义保底：候选视图
// 未变时跳过清理——视图内的既有统计不得被误删（跳过与全量清理在此场景
// 结果等价，这是选择语义不回归的护栏）。
func TestSelectorP2C_UnchangedViewKeepsStats(t *testing.T) {
	s := newP2CSelector()
	ctx := context.Background()
	nodes := nodesOf("a", "b")

	// 直接预置两个地址的统计（确定性），随后同视图多次 Select + done 回写
	s.mu.Lock()
	s.ewma["a"] = 1
	s.ewma["b"] = 2
	s.mu.Unlock()

	for i := 0; i < 10; i++ {
		n, d, err := s.Select(ctx, nodes)
		if err != nil {
			t.Fatal(err)
		}
		d(ctx, DoneInfo{})
		if n == nil {
			t.Fatal("nil node")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, aLeft := s.ewma["a"]
	_, bLeft := s.ewma["b"]
	if !aLeft || !bLeft {
		t.Errorf("stats must not vanish on unchanged view: a-present=%v b-present=%v", aLeft, bLeft)
	}
	if len(s.ewma) != 2 {
		t.Errorf("ewma entries must stay within candidate view: got %d entries, want 2", len(s.ewma))
	}
}

// TestSelectorP2C_FilteredCandidatesPrune 钉住 WithFilter 语义不回归：候选
// 被 WithFilter 过滤时，过滤外地址的统计被清理（历史行为：live 集合按过滤后
// 候选构建）；视图在过滤/不过滤间切换时行为与重构前一致。
func TestSelectorP2C_FilteredCandidatesPrune(t *testing.T) {
	s := newP2CSelector()
	ctx := context.Background()
	nodes := nodesOf("a", "b")

	_, done, err := s.Select(ctx, nodes)
	if err != nil {
		t.Fatal(err)
	}
	done(ctx, DoneInfo{})

	// 过滤到只剩 a：b 的统计被清（历史行为保持）
	if _, _, err := s.Select(ctx, nodes, WithFilter(func(n Node) bool { return n.Address() == "a" })); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, bLeft := s.ewma["b"]
	s.mu.Unlock()
	if bLeft {
		t.Error("filtered-out address ewma must be pruned (historical behavior)")
	}
}
