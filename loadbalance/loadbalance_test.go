package loadbalance

import (
	"testing"

	"github.com/oekit/registry/registry"
)

type testInstance struct {
	weight float64
}

func (t testInstance) GetWeight() float64 { return t.weight }

func makeInstances(weights ...float64) []Instance {
	instances := make([]Instance, len(weights))
	for i, w := range weights {
		instances[i] = testInstance{weight: w}
	}
	return instances
}

func TestRoundRobin(t *testing.T) {
	b := NewRoundRobin()
	instances := makeInstances(1, 1, 1)

	seen := make(map[int]bool)
	for i := 0; i < len(instances); i++ {
		idx, err := b.Pick(instances)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		if idx < 0 || idx >= len(instances) {
			t.Fatalf("Pick idx = %d out of range [0,%d)", idx, len(instances))
		}
		seen[idx] = true
	}
	if len(seen) != len(instances) {
		t.Errorf("round robin did not cover all instances, seen = %v", seen)
	}
}

func TestRoundRobin_Empty(t *testing.T) {
	b := NewRoundRobin()
	idx, err := b.Pick(nil)
	if err != registry.ErrNoInstances {
		t.Errorf("Pick(nil) err = %v, want %v", err, registry.ErrNoInstances)
	}
	if idx != -1 {
		t.Errorf("Pick(nil) idx = %d, want -1", idx)
	}
}

func TestRandom(t *testing.T) {
	b := NewRandom()
	instances := makeInstances(1, 1, 1, 1, 1)

	for i := 0; i < 100; i++ {
		idx, err := b.Pick(instances)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		if idx < 0 || idx >= len(instances) {
			t.Fatalf("Pick idx = %d out of range", idx)
		}
	}
}

func TestRandom_Empty(t *testing.T) {
	b := NewRandom()
	if _, err := b.Pick(nil); err != registry.ErrNoInstances {
		t.Errorf("Pick(nil) err = %v, want %v", err, registry.ErrNoInstances)
	}
}

func TestWeighted(t *testing.T) {
	b := NewWeighted()
	instances := makeInstances(1, 1, 1)

	counts := make([]int, len(instances))
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		idx, err := b.Pick(instances)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		counts[idx]++
	}

	for i, c := range counts {
		if c == 0 {
			t.Errorf("instance %d never selected in %d iterations", i, iterations)
		}
	}
}

func TestWeighted_WeightSkew(t *testing.T) {
	b := NewWeighted()
	instances := makeInstances(1000, 1)

	countHigh := 0
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		idx, err := b.Pick(instances)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		if idx == 0 {
			countHigh++
		}
	}
	if countHigh < iterations*9/10 {
		t.Errorf("high-weight instance selected %d/%d times, expected >90%%", countHigh, iterations)
	}
}

func TestWeighted_Empty(t *testing.T) {
	b := NewWeighted()
	if _, err := b.Pick(nil); err != registry.ErrNoInstances {
		t.Errorf("Pick(nil) err = %v, want %v", err, registry.ErrNoInstances)
	}
}

func TestWeightedRandom(t *testing.T) {
	b := NewWeighted()
	instances := makeInstances(1, 1, 1)

	counts := make([]int, len(instances))
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		idx, err := b.Pick(instances)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		counts[idx]++
	}
	for i, c := range counts {
		if c == 0 {
			t.Errorf("instance %d never selected", i)
		}
	}
}

func TestWeightedRandom_Empty(t *testing.T) {
	b := NewWeighted()
	if _, err := b.Pick(nil); err != registry.ErrNoInstances {
		t.Errorf("Pick(nil) err = %v, want %v", err, registry.ErrNoInstances)
	}
}

func TestNew_Factory(t *testing.T) {
	tests := []struct {
		strategy string
		ok       bool
	}{
		{StrategyRoundRobin, true},
		{"", true},
		{StrategyRandom, true},
		{StrategyWeighted, true},
		{StrategyWeightedRandom, true},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.strategy, func(t *testing.T) {
			b, err := New(tt.strategy)
			if tt.ok {
				if err != nil {
					t.Fatalf("New(%q) error = %v, want nil", tt.strategy, err)
				}
				if b == nil {
					t.Fatalf("New(%q) returned nil balancer", tt.strategy)
				}
			} else {
				if err == nil {
					t.Fatalf("New(%q) error = nil, want error", tt.strategy)
				}
			}
		})
	}
}

func TestToInstances(t *testing.T) {
	src := []testInstance{{1}, {2}, {3}}
	out := ToInstances(src)
	if len(out) != len(src) {
		t.Fatalf("ToInstances len = %d, want %d", len(out), len(src))
	}
	for i, inst := range out {
		if inst.GetWeight() != src[i].weight {
			t.Errorf("out[%d].GetWeight() = %v, want %v", i, inst.GetWeight(), src[i].weight)
		}
	}
}
