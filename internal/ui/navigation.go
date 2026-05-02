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
	// LockedSegmentIndex 已"锁定完成"的最大段索引 + 1（=玩家当前至少在此段或之后）。
	// 投影受此锁约束：不允许回跳到更早的段，也不允许越过下一段。只有当玩家
	// 在当前段 T ≥ 0.95 且进入下一段附近时才推进。
	// 用途：防止路径自交 / 平行段 / 回环处的"投影串线"—— 例如玩家在第 3 段
	// 附近走偏了 10 px，全局最近投影可能跳到第 7 段（恰好经过玩家位置），
	// 但分段锁会把玩家钉在第 3 段，显示为"偏离路径 X 单位"。
	LockedSegmentIndex int
}

// ProjectOnPath 把 (px, py) 投影到 nodes 折线，返回最近段、t 参数和距离。
// 全局最近段投影（不受分段锁约束）。启动导航时用一次给 LockedSegmentIndex
// 一个初值；运行期用 ProjectOnPathLocked 保证顺序推进。
func ProjectOnPath(nodes []mapdata.PathNode, px, py float64) NavProgress {
	return projectOnPathRange(nodes, px, py, 0, len(nodes)-1)
}

// ProjectOnPathLocked 受分段锁约束的投影：只允许在 [lockedSeg, lockedSeg+1]
// 两段范围内找最近投影。返回的 progress 的 LockedSegmentIndex 已按推进规则
// 更新：
//   - 若 ProjSeg == lockedSeg 且 T ≥ 0.95 → 推进到 lockedSeg + 1（除非已是最后一段）
//   - 若 ProjSeg > lockedSeg（玩家跨进下一段主体）→ 直接推进
//   - 否则保持 lockedSeg 不变
//
// 玩家即使大幅偏离路径，投影也只会落在 [lockedSeg, lockedSeg+1]，OffRoute
// 会反映真实偏离距离，不会突然跳到后段造成"路径串线"。
func ProjectOnPathLocked(nodes []mapdata.PathNode, px, py float64, lockedSeg int) NavProgress {
	if len(nodes) < 2 {
		return NavProgress{}
	}
	maxSeg := len(nodes) - 2 // 段索引最大为 N-2（对应 nodes[N-2] -> nodes[N-1]）
	if lockedSeg < 0 {
		lockedSeg = 0
	}
	if lockedSeg > maxSeg {
		lockedSeg = maxSeg
	}
	// 候选范围：当前段 + 下一段（不允许跳段）
	hi := lockedSeg + 1
	if hi > maxSeg {
		hi = maxSeg
	}
	prog := projectOnPathRange(nodes, px, py, lockedSeg, hi)

	// 推进规则
	newLock := lockedSeg
	if prog.SegmentIndex > lockedSeg {
		newLock = prog.SegmentIndex
	} else if prog.SegmentIndex == lockedSeg && prog.T >= 0.95 && lockedSeg < maxSeg {
		newLock = lockedSeg + 1
	}
	prog.LockedSegmentIndex = newLock
	return prog
}

// projectOnPathRange 在 nodes 的段索引 [segLo, segHi] 范围内做投影。
// 段索引 = 折线段下标，对应 nodes[i] -> nodes[i+1]。
func projectOnPathRange(nodes []mapdata.PathNode, px, py float64, segLo, segHi int) NavProgress {
	if len(nodes) < 2 {
		return NavProgress{}
	}
	maxSeg := len(nodes) - 2
	if segLo < 0 {
		segLo = 0
	}
	if segHi > maxSeg {
		segHi = maxSeg
	}
	if segHi < segLo {
		segHi = segLo
	}
	bestSeg := segLo
	bestT := 0.0
	bestD2 := math.Inf(1)
	bestProjX, bestProjY := nodes[segLo].X, nodes[segLo].Y
	for i := segLo; i <= segHi; i++ {
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

	// 累计长度（始终用全路径计算，保证 Traveled / Total 含义稳定）
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
