package loadbalance

import (
	"context"
	"errors"
	"slices"
	"testing"
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
	if _, err := NewSelector("p2c"); err == nil {
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
