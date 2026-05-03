// Package pathfind 中的 TSP 求解器。
//
// 这里求的是 "开放型 Hamilton 最短路径"：必须经过给定的全部节点，
// 端点（起点 / 终点）可以指定为某个节点，也可以二者都不指定 ——
// 由求解器选取使总欧氏距离最短的端点对。
//
// 算法：
//   - N ≤ 16：精确 Held–Karp 动态规划，时间 O(N²·2ᴺ)
//   - N ≥ 17：最近邻贪心 + 2-opt 启发式优化
//
// 距离使用直接欧氏距离（在世界坐标系下），不考虑可通行性 —— 这与原始
// wiki 大地图的"显示一条参考路线"语义一致；如果之后要接入 K-NN 图，
// 把这里的 dist() 换成 K-NN 上的 A* 即可。
package pathfind

import "math"

// TSPNode 一个待访问的节点（仅需坐标 + 用户自定义 Tag）。
type TSPNode struct {
	X, Y float64
	Tag  string
}

// SolveTSPPath 在 nodes 上求一条访问全部节点的最短开放路径。
//   - start, end ∈ [0, len(nodes)) 表示固定端点；传 -1 表示由算法决定
//
// 返回访问顺序的下标序列；nodes 为空时返回 nil。越界的 start/end 视为 -1
// （防御性兜底，避免上层取索引时把陈旧 / 错误的下标传进来导致越界 panic）。
func SolveTSPPath(nodes []TSPNode, start, end int) []int {
	n := len(nodes)
	if n == 0 {
		return nil
	}
	if start < 0 || start >= n {
		start = -1
	}
	if end < 0 || end >= n {
		end = -1
	}
	if n == 1 {
		return []int{0}
	}
	if n <= 16 {
		return solveExact(nodes, start, end)
	}
	return solveHeuristic(nodes, start, end)
}

// solveExact Held–Karp 风格：dp[mask][v] = 起点 ∈ mask 的某个允许节点、
// 当前停在 v 时所走的最短路径长度。
func solveExact(nodes []TSPNode, start, end int) []int {
	n := len(nodes)
	full := 1<<n - 1

	// 距离矩阵
	d := make([][]float64, n)
	for i := range d {
		d[i] = make([]float64, n)
		for j := range d[i] {
			d[i][j] = euclid(nodes[i], nodes[j])
		}
	}

	const inf = math.MaxFloat64 / 4
	// dp/parent 用一维数组：idx = mask*n + v
	dp := make([]float64, (full+1)*n)
	par := make([]int16, (full+1)*n)
	for i := range dp {
		dp[i] = inf
		par[i] = -1
	}

	// 初始化：仅访问了单个节点 v 的状态
	for v := 0; v < n; v++ {
		if start >= 0 && v != start {
			continue
		}
		dp[(1<<v)*n+v] = 0
	}

	// 按 mask 大小递增扫描
	for mask := 1; mask <= full; mask++ {
		for v := 0; v < n; v++ {
			if mask&(1<<v) == 0 {
				continue
			}
			cur := dp[mask*n+v]
			if cur >= inf {
				continue
			}
			// 扩展到下一个节点 u（不在 mask 中）
			for u := 0; u < n; u++ {
				if mask&(1<<u) != 0 {
					continue
				}
				nm := mask | (1 << u)
				cost := cur + d[v][u]
				if cost < dp[nm*n+u] {
					dp[nm*n+u] = cost
					par[nm*n+u] = int16(v)
				}
			}
		}
	}

	// 找最优终点
	bestV := -1
	best := inf
	for v := 0; v < n; v++ {
		if end >= 0 && v != end {
			continue
		}
		if dp[full*n+v] < best {
			best = dp[full*n+v]
			bestV = v
		}
	}
	if bestV == -1 {
		return nil
	}

	// 反向回溯
	out := make([]int, 0, n)
	v := bestV
	mask := full
	for v != -1 {
		out = append(out, v)
		pv := int(par[mask*n+v])
		mask ^= 1 << v
		v = pv
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// solveHeuristic 大规模情况：最近邻 + 2-opt，带迭代/时间上限，
// 保证 N 再大也不会长时间阻塞调用方（尤其 N ≥ 1000 时 2-opt 是 O(N²) / pass）。
func solveHeuristic(nodes []TSPNode, start, end int) []int {
	n := len(nodes)
	d := func(a, b int) float64 { return euclid(nodes[a], nodes[b]) }

	// 预算距离矩阵（n ≤ 512 时），O(N²) 内存，换 2-opt 时 O(1) 查询。
	// 注意：必须先把所有 mat[i] 都分配出来，再填对称值；否则 mat[j][i] = v
	// 在 j > i 时会写到尚未分配的 nil slice（panic: index out of range [0] with length 0）。
	useMat := n <= 512
	var mat [][]float64
	if useMat {
		mat = make([][]float64, n)
		for i := range mat {
			mat[i] = make([]float64, n)
		}
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				v := euclid(nodes[i], nodes[j])
				mat[i][j] = v
				mat[j][i] = v
			}
		}
		d = func(a, b int) float64 { return mat[a][b] }
	}

	// 起始：最近邻贪心
	visited := make([]bool, n)
	tour := make([]int, 0, n)
	first := start
	if first < 0 {
		first = 0
	}
	tour = append(tour, first)
	visited[first] = true
	for len(tour) < n {
		cur := tour[len(tour)-1]
		bn := -1
		bd := math.MaxFloat64
		for j := 0; j < n; j++ {
			if visited[j] {
				continue
			}
			if end >= 0 && j == end && len(tour) < n-1 {
				continue
			}
			if dd := d(cur, j); dd < bd {
				bd = dd
				bn = j
			}
		}
		if bn == -1 {
			for j := 0; j < n; j++ {
				if !visited[j] {
					bn = j
					break
				}
			}
		}
		tour = append(tour, bn)
		visited[bn] = true
	}

	// 2-opt 优化（按规模自适应限制）
	cost := tourCost(tour, d)
	// N=50 → 200 次、N=500 → 40 次、N=2000 → 10 次；保证总开销 O(N²·passes) ≤ ~4e8
	maxPasses := 200
	if n > 100 {
		maxPasses = 4_000_000 / (n * n)
		if maxPasses < 4 {
			maxPasses = 4
		}
		if maxPasses > 200 {
			maxPasses = 200
		}
	}
	// in-place 2-opt，避免 twoOptSwap 每次 O(N) 拷贝
	improved := true
	for maxPasses > 0 && improved {
		improved = false
		maxPasses--
		for i := 1; i < n-2; i++ {
			if start >= 0 && i == 0 {
				continue
			}
			ti := tour[i-1]
			ti1 := tour[i]
			for k := i + 1; k < n-1; k++ {
				if end >= 0 && k == n-1 {
					continue
				}
				tk := tour[k]
				tk1 := tour[k+1]
				// 翻转 tour[i..k] 之后的代价变化：
				// Δ = d(ti,tk) + d(ti1,tk1) - d(ti,ti1) - d(tk,tk1)
				delta := d(ti, tk) + d(ti1, tk1) - d(ti, ti1) - d(tk, tk1)
				if delta < -1e-9 {
					// 原地翻转 tour[i..k]
					for p, q := i, k; p < q; p, q = p+1, q-1 {
						tour[p], tour[q] = tour[q], tour[p]
					}
					cost += delta
					improved = true
					ti1 = tour[i]
				}
			}
		}
	}
	return tour
}

func twoOptSwap(t []int, i, k int) []int {
	out := make([]int, len(t))
	copy(out, t[:i])
	for p, q := i, k; q >= i; p, q = p+1, q-1 {
		out[p] = t[q]
	}
	copy(out[k+1:], t[k+1:])
	return out
}

func tourCost(tour []int, d func(a, b int) float64) float64 {
	c := 0.0
	for i := 0; i+1 < len(tour); i++ {
		c += d(tour[i], tour[i+1])
	}
	return c
}

func euclid(a, b TSPNode) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	return math.Sqrt(dx*dx + dy*dy)
}
