package ui

import (
	"math"

	"github.com/where-to-go/app/internal/mapdata"
)

// NavProgress 把 (px, py) 投影到一条折线路径，得到当前进度。
type NavProgress struct {
	// SegmentIndex 玩家所在段：nodes[SegmentIndex] -> nodes[SegmentIndex+1]
	SegmentIndex int
	// T 在该段上的参数 [0, 1]，0 = 起点，1 = 终点
	T float64
	// ProjX, ProjY 投影点（玩家在路径上对应的"理想"位置）
	ProjX, ProjY float64
	// OffRoute 玩家到投影点的世界距离
	OffRoute float64
	// Traveled 距离起点已走的世界长度（沿路径）
	Traveled float64
	// Total 全路径长度
	Total float64
}

// ProjectOnPath 把 (px, py) 投影到 nodes 折线，返回最近段、t 参数和距离。
// 折线 < 2 节点时返回零值。
func ProjectOnPath(nodes []mapdata.PathNode, px, py float64) NavProgress {
	if len(nodes) < 2 {
		return NavProgress{}
	}
	bestSeg := 0
	bestT := 0.0
	bestD2 := math.Inf(1)
	bestProjX, bestProjY := nodes[0].X, nodes[0].Y
	for i := 0; i+1 < len(nodes); i++ {
		ax, ay := nodes[i].X, nodes[i].Y
		bx, by := nodes[i+1].X, nodes[i+1].Y
		dx := bx - ax
		dy := by - ay
		l2 := dx*dx + dy*dy
		t := 0.0
		if l2 > 0 {
			t = ((px-ax)*dx + (py-ay)*dy) / l2
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
		}
		ix := ax + t*dx
		iy := ay + t*dy
		d2 := (px-ix)*(px-ix) + (py-iy)*(py-iy)
		if d2 < bestD2 {
			bestD2 = d2
			bestSeg = i
			bestT = t
			bestProjX = ix
			bestProjY = iy
		}
	}

	// 累计长度
	total := 0.0
	traveled := 0.0
	for i := 0; i+1 < len(nodes); i++ {
		dx := nodes[i+1].X - nodes[i].X
		dy := nodes[i+1].Y - nodes[i].Y
		l := math.Sqrt(dx*dx + dy*dy)
		if i < bestSeg {
			traveled += l
		} else if i == bestSeg {
			traveled += l * bestT
		}
		total += l
	}

	return NavProgress{
		SegmentIndex: bestSeg,
		T:            bestT,
		ProjX:        bestProjX,
		ProjY:        bestProjY,
		OffRoute:     math.Sqrt(bestD2),
		Traveled:     traveled,
		Total:        total,
	}
}

// NextWaypoint 根据进度返回"下一个目标点"的索引、距玩家距离。
// 没有下一个时返回 (-1, 0)。
func NextWaypoint(nodes []mapdata.PathNode, progress NavProgress) (int, float64) {
	idx := progress.SegmentIndex + 1
	if idx >= len(nodes) {
		return -1, 0
	}
	dx := nodes[idx].X - progress.ProjX
	dy := nodes[idx].Y - progress.ProjY
	return idx, math.Sqrt(dx*dx + dy*dy)
}
