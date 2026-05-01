package pathfind

import "testing"

func TestAStarSimple(t *testing.T) {
	// 五个节点近似一字排列
	nodes := []Node{
		{X: 0, Y: 0, Tag: "A"},
		{X: 1, Y: 0, Tag: "B"},
		{X: 2, Y: 0, Tag: "C"},
		{X: 3, Y: 0, Tag: "D"},
		{X: 4, Y: 0, Tag: "E"},
	}
	g := BuildKNN(nodes, 2)
	path, ok := AStar(g, 0, 4)
	if !ok {
		t.Fatalf("应可达 A→E")
	}
	if path[0] != 0 || path[len(path)-1] != 4 {
		t.Fatalf("路径起止错误: %v", path)
	}
	// 验证路径是连续可走的
	total := 0.0
	for i := 1; i < len(path); i++ {
		total += dist(nodes[path[i-1]], nodes[path[i]])
	}
	if total > 4.0001 {
		t.Fatalf("路径总距应 ≈ 4，实际 %v", total)
	}
}

func TestNearest(t *testing.T) {
	nodes := []Node{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 0, Y: 10}}
	g := BuildKNN(nodes, 2)
	if NearestNode(g, 0.1, 0.1) != 0 {
		t.Fatalf("最近节点应为 0")
	}
	if NearestNode(g, 9, 0) != 1 {
		t.Fatalf("最近节点应为 1")
	}
}
