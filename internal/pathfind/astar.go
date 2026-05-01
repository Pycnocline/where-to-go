// Package pathfind 提供基于 K 近邻图 + A* 的最短路径搜索。
//
// 用法：
//
//	g := pathfind.BuildKNN(points, 8)        // 构建 8-近邻图
//	path, ok := pathfind.AStar(g, srcIdx, dstIdx)
//
// 这里把地图上的所有官方点位视作图节点，节点间欧氏距离作为边权。
// K 近邻保证了图稀疏（约 O(n*k) 边），同时通常能保持连通；
// 调用方亦可通过 BuildRadius 用半径 R 内邻接的方式构图。
package pathfind

import (
	"container/heap"
	"math"
	"sort"
)

// Node 单个图节点，X/Y 为坐标，Tag 是上层用户业务标识。
type Node struct {
	X, Y float64
	Tag  string
}

// Graph 邻接表表示。
type Graph struct {
	Nodes []Node
	// adj[i] = []edge{to, weight}
	adj [][]edge
}

type edge struct {
	to     int
	weight float64
}

// BuildKNN 构造 K 近邻图（K 一般取 6~10）。
func BuildKNN(nodes []Node, k int) *Graph {
	if k <= 0 {
		k = 6
	}
	g := &Graph{Nodes: nodes, adj: make([][]edge, len(nodes))}
	type pair struct {
		idx int
		d   float64
	}
	for i := range nodes {
		pairs := make([]pair, 0, len(nodes)-1)
		for j := range nodes {
			if i == j {
				continue
			}
			d := dist(nodes[i], nodes[j])
			pairs = append(pairs, pair{j, d})
		}
		sort.Slice(pairs, func(a, b int) bool { return pairs[a].d < pairs[b].d })
		n := k
		if n > len(pairs) {
			n = len(pairs)
		}
		for _, p := range pairs[:n] {
			g.adj[i] = append(g.adj[i], edge{p.idx, p.d})
		}
	}
	return g
}

// BuildRadius 半径 R 内全连接图（保证局部完全连接，但稀疏度依赖密度）。
func BuildRadius(nodes []Node, r float64) *Graph {
	g := &Graph{Nodes: nodes, adj: make([][]edge, len(nodes))}
	r2 := r * r
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			dx := nodes[i].X - nodes[j].X
			dy := nodes[i].Y - nodes[j].Y
			d2 := dx*dx + dy*dy
			if d2 <= r2 {
				d := math.Sqrt(d2)
				g.adj[i] = append(g.adj[i], edge{j, d})
				g.adj[j] = append(g.adj[j], edge{i, d})
			}
		}
	}
	return g
}

func dist(a, b Node) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// AStar 在 g 上求 src→dst 的最短路径。返回节点索引序列；不可达返回 nil,false。
func AStar(g *Graph, src, dst int) ([]int, bool) {
	if src < 0 || dst < 0 || src >= len(g.Nodes) || dst >= len(g.Nodes) {
		return nil, false
	}
	if src == dst {
		return []int{src}, true
	}
	gScore := make(map[int]float64, len(g.Nodes))
	cameFrom := make(map[int]int, len(g.Nodes))
	gScore[src] = 0

	open := &pq{}
	heap.Init(open)
	heap.Push(open, &pqItem{idx: src, f: heuristic(g.Nodes[src], g.Nodes[dst])})

	for open.Len() > 0 {
		cur := heap.Pop(open).(*pqItem)
		if cur.idx == dst {
			return reconstruct(cameFrom, dst), true
		}
		curG := gScore[cur.idx]
		for _, e := range g.adj[cur.idx] {
			tentative := curG + e.weight
			if old, ok := gScore[e.to]; !ok || tentative < old {
				gScore[e.to] = tentative
				cameFrom[e.to] = cur.idx
				f := tentative + heuristic(g.Nodes[e.to], g.Nodes[dst])
				heap.Push(open, &pqItem{idx: e.to, f: f})
			}
		}
	}
	return nil, false
}

// NearestNode 在 g 中找离 (x,y) 最近的节点索引。
func NearestNode(g *Graph, x, y float64) int {
	best := -1
	bestD := math.Inf(1)
	for i, n := range g.Nodes {
		dx := n.X - x
		dy := n.Y - y
		d := dx*dx + dy*dy
		if d < bestD {
			bestD = d
			best = i
		}
	}
	return best
}

func heuristic(a, b Node) float64 {
	return dist(a, b)
}

func reconstruct(cameFrom map[int]int, dst int) []int {
	// 反向回溯并反转
	out := []int{dst}
	cur := dst
	for {
		p, ok := cameFrom[cur]
		if !ok {
			break
		}
		out = append(out, p)
		cur = p
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// 优先队列实现（最小 f 在堆顶）
type pqItem struct {
	idx int
	f   float64
}
type pq []*pqItem

func (p pq) Len() int            { return len(p) }
func (p pq) Less(i, j int) bool  { return p[i].f < p[j].f }
func (p pq) Swap(i, j int)       { p[i], p[j] = p[j], p[i] }
func (p *pq) Push(x interface{}) { *p = append(*p, x.(*pqItem)) }
func (p *pq) Pop() interface{} {
	o := *p
	n := len(o)
	v := o[n-1]
	*p = o[:n-1]
	return v
}
