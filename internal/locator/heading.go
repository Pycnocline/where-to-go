package locator

import (
	"image"
	"image/color"
	"math"
)

// DetectHeadingRGBA 测试用：直接接收 RGBA。
func DetectHeadingRGBA(roi *image.RGBA) (float64, bool) {
	return detectHeadingRGBA(roi)
}

// DetectHeadingRGBADebug 测试用：返回详细信息。
type HeadingDebug struct {
	OK                   bool
	Heading              float64 // 弧度
	OrangeInCenter       int
	CentroidX, CentroidY float64
	TipX, TipY           int
	SubSide              int
	// 真实 minimap 圆心 + 半径（0 = 退化为 ROI 几何中心）
	CircleCX, CircleCY, CircleR int
	CircleFound                 bool
}

func DetectHeadingRGBADebug(roi *image.RGBA) HeadingDebug {
	b := roi.Bounds()
	w := b.Dx()
	h := b.Dy()
	out := HeadingDebug{}
	if w < 16 || h < 16 {
		return out
	}

	// 1) 先精准定位 minimap 灰色内圈
	circleCX, circleCY, circleR, found := FindMinimapCircle(roi)
	out.CircleCX = circleCX
	out.CircleCY = circleCY
	out.CircleR = circleR
	out.CircleFound = found
	// 退化：未找到 → 用 ROI 几何中心 + min(W,H)/2-2
	if !found {
		circleCX = b.Min.X + w/2
		circleCY = b.Min.Y + h/2
		side := w
		if h < side {
			side = h
		}
		circleR = side/2 - 2
	}

	// 2) 在真实圆心附近紧凑半径内找橙黄+白色邻居
	tightR := float64(circleR) / 2.5 // 经验值：箭头一般在内圈 1/2.5 半径内
	if tightR < 8 {
		tightR = 8
	}
	tightR2 := tightR * tightR
	out.SubSide = int(2 * tightR)

	type pt struct{ x, y int }
	pts := make([]pt, 0, 256)
	var sumX, sumY float64
	for j := -int(tightR); j <= int(tightR); j++ {
		for i := -int(tightR); i <= int(tightR); i++ {
			if float64(i*i+j*j) > tightR2 {
				continue
			}
			ax := circleCX + i
			ay := circleCY + j
			if ax < b.Min.X || ay < b.Min.Y || ax >= b.Max.X || ay >= b.Max.Y {
				continue
			}
			c := roi.RGBAAt(ax, ay)
			if !isOrangeArrow(c) {
				continue
			}
			if !hasWhiteNeighbor(roi, ax, ay, 3) {
				continue
			}
			pts = append(pts, pt{i, j})
			sumX += float64(i)
			sumY += float64(j)
		}
	}
	out.OrangeInCenter = len(pts)
	if len(pts) < 8 {
		return out
	}
	mx := sumX / float64(len(pts))
	my := sumY / float64(len(pts))
	out.CentroidX = mx
	out.CentroidY = my

	// 3) PCA：计算箭头像素相对质心的协方差矩阵，求主轴方向
	var sxx, syy, sxy float64
	for _, p := range pts {
		dx := float64(p.x) - mx
		dy := float64(p.y) - my
		sxx += dx * dx
		syy += dy * dy
		sxy += dx * dy
	}
	// 主轴角度：θ = ½·atan2(2·Sxy, Sxx-Syy)
	axisAngle := 0.5 * math.Atan2(2*sxy, sxx-syy)
	axDX := math.Cos(axisAngle)
	axDY := math.Sin(axisAngle)

	// 4) 不对称消歧：找沿主轴投影最大 / 最小的橙黄像素
	maxProj := math.Inf(-1)
	minProj := math.Inf(1)
	var tipPlus, tipMinus pt
	for _, p := range pts {
		dx := float64(p.x) - mx
		dy := float64(p.y) - my
		proj := dx*axDX + dy*axDY
		if proj > maxProj {
			maxProj = proj
			tipPlus = p
		}
		if proj < minProj {
			minProj = proj
			tipMinus = p
		}
	}
	// 三角形箭头：尖端方向 = 沿主轴|投影|最大的方向；底边角投影绝对值更小
	// （因为底边比尖端到顶点的距离短）
	dirX, dirY := axDX, axDY
	tip := tipPlus
	if math.Abs(minProj) > math.Abs(maxProj) {
		dirX = -dirX
		dirY = -dirY
		tip = tipMinus
	}
	out.TipX = tip.x
	out.TipY = tip.y

	// 主轴长度（最大-最小投影）必须显著大于宽度，否则这不是箭头形状（可能是圆斑）
	axisLen := maxProj - minProj
	// 也算横轴方向投影范围作宽度
	perpX := -axDY
	perpY := axDX
	maxPerp := math.Inf(-1)
	minPerp := math.Inf(1)
	for _, p := range pts {
		dx := float64(p.x) - mx
		dy := float64(p.y) - my
		proj := dx*perpX + dy*perpY
		if proj > maxPerp {
			maxPerp = proj
		}
		if proj < minPerp {
			minPerp = proj
		}
	}
	width := maxPerp - minPerp
	if axisLen < 1.2*width || axisLen < 4 {
		// 形状不够细长 — 可能是噪声团
		return out
	}

	screenAngle := math.Atan2(dirY, dirX)
	heading := screenAngle + math.Pi/2
	for heading < 0 {
		heading += 2 * math.Pi
	}
	for heading >= 2*math.Pi {
		heading -= 2 * math.Pi
	}
	out.Heading = heading
	out.OK = true
	return out
}

// detectHeadingRGBA 在 RGBA 上检测玩家箭头朝向。
//
// 玩家箭头：橙黄色三角形，永远在 minimap 中心；尖端指向角色面对方向。
//
// 策略：
//  1. 在 ROI 中心 ~1/3 边长子区域中收集"橙黄"像素
//  2. 求质心 (mx, my)
//  3. 找离质心最远的橙黄像素作为"尖端"
//  4. 朝向方向 = 质心 → 尖端 向量（屏幕坐标系）
//  5. 屏幕角 → 北上系：heading = screenAngle + π/2
//
// 若橙黄像素不够，退回亮度 PCA。返回 (heading 弧度, ok)，0=北，顺时针。
func detectHeadingRGBA(roi *image.RGBA) (float64, bool) {
	dbg := DetectHeadingRGBADebug(roi)
	if dbg.OK {
		return dbg.Heading, true
	}
	// 退化：亮度 PCA（保留旧实现）
	g := grayscale(roi)
	return detectHeading(g)
}

// isOrangeArrow 玩家箭头颜色判定（橙黄色：R 高、G 中、B 低）。
func isOrangeArrow(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	// 经验阈值：R > 200, G ∈ [100, R-30], B < 100
	return r > 200 && g > 100 && g < r-30 && b < 100
}

// isWhiteOutline 玩家箭头的白色轮廓判定（高亮 + 三通道接近 = 低饱和白色）。
func isWhiteOutline(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	if r < 200 || g < 200 || b < 200 {
		return false
	}
	maxC, minC := r, r
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	return maxC-minC < 40
}

// hasWhiteNeighbor 像素 (x,y) 在 ±r 邻域内是否有白色轮廓像素（过滤地形伪橙黄）。
func hasWhiteNeighbor(roi *image.RGBA, x, y, r int) bool {
	b := roi.Bounds()
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			xx, yy := x+dx, y+dy
			if xx < b.Min.X || yy < b.Min.Y || xx >= b.Max.X || yy >= b.Max.Y {
				continue
			}
			if isWhiteOutline(roi.RGBAAt(xx, yy)) {
				return true
			}
		}
	}
	return false
}

// isGrayBorder 灰色边框判定（低饱和度 + 中等亮度）。
func isGrayBorder(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	maxC, minC := r, r
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	if maxC-minC > 30 {
		return false
	}
	avg := (r + g + b) / 3
	return avg > 50 && avg < 210
}

// FindMinimapCircle 在 ROI 中搜索 minimap 灰色内圈，返回真实圆心和半径。
//
// 算法：在 ROI 中心 ±searchPx 范围内，对每个候选 (cx, cy, r) 沿圆周采样 72 个点，
// 数其中"灰色边框"像素数；得分 = 灰色点数。最高得分的 (cx, cy, r) 就是 minimap 内圈。
//
// 若得分 < 40% 圆周 → ok=false，调用方退化用 ROI 几何中心。
func FindMinimapCircle(roi *image.RGBA) (cx, cy, r int, ok bool) {
	b := roi.Bounds()
	W := b.Dx()
	H := b.Dy()
	if W < 32 || H < 32 {
		return b.Min.X + W/2, b.Min.Y + H/2, 0, false
	}
	midX := b.Min.X + W/2
	midY := b.Min.Y + H/2
	side := W
	if H < side {
		side = H
	}
	rMin := side/2 - 16
	rMax := side / 2
	if rMin < 8 {
		rMin = 8
	}
	const searchPx = 12
	const samples = 72

	bestScore := 0
	bestCX, bestCY, bestR := midX, midY, side/2-2
	for dy := -searchPx; dy <= searchPx; dy++ {
		for dx := -searchPx; dx <= searchPx; dx++ {
			cxx := midX + dx
			cyy := midY + dy
			for rr := rMin; rr <= rMax; rr++ {
				score := 0
				for k := 0; k < samples; k++ {
					a := 2 * math.Pi * float64(k) / float64(samples)
					px := cxx + int(math.Round(float64(rr)*math.Cos(a)))
					py := cyy + int(math.Round(float64(rr)*math.Sin(a)))
					if px < b.Min.X || py < b.Min.Y || px >= b.Max.X || py >= b.Max.Y {
						continue
					}
					if isGrayBorder(roi.RGBAAt(px, py)) {
						score++
					}
				}
				if score > bestScore {
					bestScore = score
					bestCX, bestCY, bestR = cxx, cyy, rr
				}
			}
		}
	}
	ok = bestScore >= samples*4/10
	return bestCX, bestCY, bestR, ok
}

// isViewCone 视野锥颜色判定（高亮白：高亮度 + 低饱和度）。
func isViewCone(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	// 三通道都高 + 差异小（低饱和度）
	if r < 220 || g < 220 || b < 220 {
		return false
	}
	maxC, minC := r, r
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	return maxC-minC < 30
}

// isVividIcon 高饱和点位图标判定。
func isVividIcon(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	maxC, minC := r, r
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	// 亮度足够 + 饱和度高（max-min 大）
	return maxC > 130 && maxC-minC > 90
}

// detectHeading 备用：亮度 PCA。
func detectHeading(g *image.Gray) (float64, bool) {
	b := g.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w < 16 || h < 16 {
		return 0, false
	}
	side := w
	if h < side {
		side = h
	}
	side /= 2
	if side < 12 {
		return 0, false
	}
	cx := b.Min.X + w/2
	cy := b.Min.Y + h/2
	x0 := cx - side/2
	y0 := cy - side/2

	hist := [256]int{}
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			v := g.GrayAt(x0+i, y0+j).Y
			hist[v]++
		}
	}
	total := side * side
	thresh := uint8(255)
	cum := 0
	for v := 255; v >= 0; v-- {
		cum += hist[v]
		if cum >= total/20 {
			thresh = uint8(v)
			break
		}
	}
	if thresh < 180 {
		return 0, false
	}

	var sumX, sumY, sumW float64
	type p2 struct{ X, Y, W float64 }
	pts := make([]p2, 0, total/20)
	for j := 0; j < side; j++ {
		for i := 0; i < side; i++ {
			v := g.GrayAt(x0+i, y0+j).Y
			if v < thresh {
				continue
			}
			weight := float64(v - thresh)
			sumX += float64(i) * weight
			sumY += float64(j) * weight
			sumW += weight
			pts = append(pts, p2{X: float64(i), Y: float64(j), W: weight})
		}
	}
	if sumW == 0 || len(pts) < 8 {
		return 0, false
	}
	mx := sumX / sumW
	my := sumY / sumW
	half := float64(side) / 2
	dxC := mx - half
	dyC := my - half
	if math.Hypot(dxC, dyC) < 1.0 {
		return 0, false
	}
	screenAngle := math.Atan2(dyC, dxC)
	heading := screenAngle + math.Pi/2
	for heading < 0 {
		heading += 2 * math.Pi
	}
	for heading >= 2*math.Pi {
		heading -= 2 * math.Pi
	}
	return heading, true
}
