package pathfind

import (
	"math"
	"math/rand"
	"testing"
)

// TestTSPSmallExact N ≤ 16 走 Held-Karp，精确解。
func TestTSPSmallExact(t *testing.T) {
	nodes := []TSPNode{
		{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1},
	}
	got := SolveTSPPath(nodes, -1, -1)
	if len(got) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(got))
	}
	// 最优是 4（顺时针环），开放路径是 3。
	if c := tourCostIdx(nodes, got); math.Abs(c-3) > 1e-9 {
		t.Fatalf("expected open-path cost 3, got %.3f (order %v)", c, got)
	}
}

// TestTSPHeuristicLarge 1000 个随机点必须在秒级返回，不崩溃。
func TestTSPHeuristicLarge(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	n := 1000
	nodes := make([]TSPNode, n)
	for i := range nodes {
		nodes[i] = TSPNode{X: r.Float64() * 1000, Y: r.Float64() * 1000}
	}
	got := SolveTSPPath(nodes, -1, -1)
	if len(got) != n {
		t.Fatalf("expected %d nodes, got %d", n, len(got))
	}
	// 一致性：顺序应为 [0..n-1] 的置换
	seen := make(map[int]bool, n)
	for _, v := range got {
		if v < 0 || v >= n || seen[v] {
			t.Fatalf("invalid order: duplicate or out-of-range index %d", v)
		}
		seen[v] = true
	}
}

// TestTSPFixedEndpoints 指定 start/end 时解必须遵守。
func TestTSPFixedEndpoints(t *testing.T) {
	nodes := []TSPNode{
		{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 5, Y: 8}, {X: 5, Y: 3},
	}
	got := SolveTSPPath(nodes, 0, 1)
	if got[0] != 0 || got[len(got)-1] != 1 {
		t.Fatalf("fixed endpoints not respected: %v", got)
	}
}

func tourCostIdx(nodes []TSPNode, order []int) float64 {
	c := 0.0
	for i := 0; i+1 < len(order); i++ {
		c += euclid(nodes[order[i]], nodes[order[i+1]])
	}
	return c
}
