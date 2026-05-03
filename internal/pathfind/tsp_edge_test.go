package pathfind

import (
	"runtime/debug"
	"testing"
)

// TestTSPEdgeCases 各种端点 / 节点数边界，确保不 panic。
func TestTSPEdgeCases(t *testing.T) {
	cases := []struct {
		name        string
		nodes       []TSPNode
		start, end  int
		expectEmpty bool
	}{
		{"empty", []TSPNode{}, -1, -1, true},
		{"single", []TSPNode{{X: 0, Y: 0}}, -1, -1, false},
		{"single-with-start", []TSPNode{{X: 0, Y: 0}}, 0, -1, false},
		{"two-no-fix", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}}, -1, -1, false},
		{"two-start-end-same", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}}, 0, 0, true},
		{"two-start-end-distinct", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}}, 0, 1, false},
		{"three-start-only", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}, 1, -1, false},
		{"three-end-only", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}, -1, 2, false},
		{"three-start-end-same", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}, 1, 1, true},
		{"three-start-out-of-range", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}, 5, -1, false},
		{"three-end-out-of-range", []TSPNode{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 0, Y: 1}}, -1, 5, false},
		{"duplicate-points", []TSPNode{{X: 1, Y: 1}, {X: 1, Y: 1}, {X: 1, Y: 1}}, -1, -1, false},
		{"heuristic-edge", makeNodes(20), 5, 19, false},
		{"heuristic-start-end-same", makeNodes(20), 5, 5, false},
		{"heuristic-start-out-of-range", makeNodes(20), 25, -1, false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic: %v\nstack:\n%s", r, debug.Stack())
				}
			}()
			got := SolveTSPPath(c.nodes, c.start, c.end)
			if c.expectEmpty {
				if len(got) != 0 {
					t.Fatalf("expected empty, got %v", got)
				}
			} else {
				if len(got) == 0 {
					t.Fatalf("expected non-empty result for valid input")
				}
			}
		})
	}
}

func makeNodes(n int) []TSPNode {
	out := make([]TSPNode, n)
	for i := range out {
		out[i] = TSPNode{X: float64(i), Y: float64(i % 5)}
	}
	return out
}
