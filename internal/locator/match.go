package locator

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"github.com/where-to-go/app/internal/mapdata"
)

// Matcher 通过模板匹配在 wiki 大地图瓦片上定位玩家在 ROI 中心对应的世界坐标。
//
// 关键设计要点（针对游戏内圆形小地图）：
//  1. ROI 截图先转灰度并按内切圆 mask（圆外置 0），避免四角无效内容污染相关性
//  2. 模板和搜索图对齐到统一像素尺度：1 template 像素 = (D/T) * K * BaseScale * 2^Z wiki 像素，
//     其中 D=ROI 内切圆直径，T=TemplateSize，K=WorldUnitsPerMinimapPx
//  3. 搜索 mosaic 先在 zoom Z 渲染若干 wiki 像素半径，再按比例缩放到 (T+2R) 个
//     "模板像素"，与模板像素 1:1
//  4. 多尺度匹配：在 K 上下做几个尺度（默认 [0.5, 0.7, 1.0, 1.4, 2.0]），
//     用以对抗未校准/偏离的 K，取最高分
//  5. NCC 使用同一圆形 mask 计算均值/方差/相关性，确保只比较圆内信息
type Matcher struct {
	Mosaic                 *MosaicProvider
	WorldUnitsPerMinimapPx float64 // K
	SearchZoom             int     // 默认 8（wiki 最大）

	// 上一次定位（限定搜索区中心）。nil → 报错。
	LastFix *Fix

	// SearchRadiusPx 搜索半径（"模板像素"为单位）。默认 32（性能优先）。
	// 实际搜索图 = (T+2R) × (T+2R)。
	// 连续匹配失败时会自动放大搜索半径（最多 4×）。
	SearchRadiusPx int

	// TemplateSize 模板边长。默认 48（兼顾性能与判别力）。
	TemplateSize int

	// MinConfidence NCC 阈值；低于此值视为未匹配。默认 0.30。
	MinConfidence float64

	// HeadingDetect 是否尝试从 ROI 检测玩家朝向。默认 true。
	HeadingDetect bool

	// ScaleRatios 多尺度乘子（针对 K）。空 → 用默认。
	ScaleRatios []float64

	// WikiHalfCap 单 scale 渲染的 wiki-px 半径上限（防止 K 偏离过远拉海量瓦片）。
	// 0 → 用默认 600。校准时可以调到 1500+。
	WikiHalfCap int

	// AllowMissingCircle 允许 ROI 里没有灰色内边框圆（仅用于合成测试 /
	// 非游戏源图）。默认 false —— 生产环境必须有圆，否则视为小地图不可见。
	AllowMissingCircle bool

	// DebugLog 打印每次匹配的中间值。
	DebugLog bool

	// LastDebug 最近一次 Match 的诊断信息。
	LastDebug DebugInfo

	// failStreak 连续失败计数；用于自适应搜索半径。
	failStreak int

	// 上一次匹配用的 ROI / mask 缓存（避免重复构造）
	cachedRGBA   *image.RGBA
	cachedTpl    *image.Gray
	cachedMask   []bool
	cachedSize   int
}

// DebugInfo Match 的诊断输出。
type DebugInfo struct {
	BestScale    float64
	BestScore    float64
	BestX, BestY int
	WikiPerTplPx float64
	MosaicSize   int
	TemplateSize int
	WorldDeltaX  float64
	WorldDeltaY  float64
	AllScores    []float64
}

// ErrNotImplemented 真实匹配器配置缺失时的占位错误。
var ErrNotImplemented = fmt.Errorf("locator: matcher not configured (mosaic / scale missing)")

// Match 实现 MatchFn。
func (m *Matcher) Match(roi *image.RGBA) (Fix, error) {
	if m.Mosaic == nil || m.WorldUnitsPerMinimapPx <= 0 {
		return Fix{}, ErrNotImplemented
	}
	if m.LastFix == nil {
		return Fix{}, fmt.Errorf("locator: no seed position; call SetSeed first")
	}
	templateSize := m.TemplateSize
	if templateSize <= 0 {
		templateSize = 48
	}
	radius := m.SearchRadiusPx
	if radius <= 0 {
		radius = 32
	}
	// 自适应放大：连续失败时把搜索半径放大（重新捕获走远的玩家）
	streak := m.failStreak
	radiusMul := 1
	if streak >= 3 {
		radiusMul = 2
	}
	if streak >= 8 {
		radiusMul = 3
	}
	if streak >= 15 {
		radiusMul = 4
	}
	radius *= radiusMul
	zoom := m.SearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	minConf := m.MinConfidence
	if minConf == 0 {
		minConf = 0.30
	}
	scales := m.ScaleRatios
	if len(scales) == 0 {
		scales = []float64{0.7, 1.0, 1.4}
	}

	// 1. ROI → grayscale → 用真实小地图圆心对齐裁剪 + 噪声 mask
	gray := grayscale(roi)
	rb := roi.Bounds()
	roiW := rb.Dx()
	roiH := rb.Dy()
	if roiW < 16 || roiH < 16 {
		return Fix{}, fmt.Errorf("locator: ROI too small (%dx%d)", roiW, roiH)
	}

	// 关键：定位真实小地图圆心（灰色内边框 a4acac），用它做 mask 中心，
	// 而不是 ROI 几何中心（ROI 可能比小地图圆稍大或略偏）。
	cCX, cCY, cR, circleFound := FindMinimapCircle(roi)
	if !circleFound {
		if !m.AllowMissingCircle {
			// 小地图没在画面里（被界面遮挡 / 玩家进了不显示小地图的菜单等）。
			// 直接拒绝本帧，让 Locator 走 OnErr → applyNavFallback("stay")，
			// 不要拿 ROI 几何中心硬撑出一个错误位置。
			m.failStreak++
			return Fix{}, fmt.Errorf("minimap not visible (no grey ring detected)")
		}
		// 兼容合成测试：没有圆时退化到 ROI 几何中心 + min(W,H)/2-2
		cCX = rb.Min.X + roiW/2
		cCY = rb.Min.Y + roiH/2
		cR = roiW
		if roiH < cR {
			cR = roiH
		}
		cR = cR/2 - 2
	}
	// 额外验证：玩家箭头（橙色三角）应在小地图中心附近。如果完全找不到也很
	// 可能是被遮挡。容忍：若找到圆但找不到箭头，仍允许继续匹配（玩家可能
	// 隐身 / 视角特殊），仅把它作为失败累积时的诊断信号。
	_, hasArrow := detectHeadingRGBA(roi)
	_ = hasArrow // 仅作诊断；不强制
	D := 2 * cR
	if D < 16 {
		return Fix{}, fmt.Errorf("locator: minimap circle too small (r=%d)", cR)
	}

	tplGray := cropGraySquareAt(gray, cCX, cCY, D)
	tplResized := resizeGrayBilinear(tplGray, templateSize, templateSize)

	// 圆形 mask（屏蔽四角；裁剪后圆正好内切于方形）
	circleMask := buildCircleMask(templateSize)
	// 中心小圆 mask（屏蔽玩家箭头本体；T=48 → 半径 4 ≈ 8% 圆直径，
	// 对应原图直径 D 的 ~16%，只够盖住箭头+白边轮廓而不浪费有效区域）
	centerHole := buildCenterHoleMask(templateSize, templateSize/12)
	// 颜色噪声 mask（屏蔽箭头/视野锥；以圆心为中心采样）
	noiseMask := buildNoiseMaskFromRGBAAt(roi, cCX, cCY, D, templateSize)

	// 合成最终 mask：圆内 ∧ 非中心孔 ∧ 非噪声
	mask := make([]bool, templateSize*templateSize)
	cnt := 0
	for i := range mask {
		mask[i] = circleMask[i] && centerHole[i] && noiseMask[i]
		if mask[i] {
			cnt++
		}
	}
	if cnt < templateSize*templateSize/8 {
		// 有效像素太少（<12.5%），匹配不可靠
		m.failStreak++
		return Fix{}, fmt.Errorf("locator: too few clean pixels (%d / %d)", cnt, templateSize*templateSize)
	}
	applyMaskInplace(tplResized, mask)

	mosaicTotal := templateSize + 2*radius

	tMean, tDenom := masksumStats(tplResized, mask)
	if tDenom == 0 {
		return Fix{}, fmt.Errorf("locator: template variance zero")
	}

	bestScore := -2.0
	bestScale := 1.0
	bestX, bestY := 0, 0
	var bestWikiPerTplPx float64
	allScores := make([]float64, 0, len(scales))

	for _, s := range scales {
		Keff := m.WorldUnitsPerMinimapPx * s
		wikiPerTplPx := float64(D) / float64(templateSize) * Keff * mapdata.BaseScale * math.Pow(2, float64(zoom))
		if wikiPerTplPx <= 0 || math.IsNaN(wikiPerTplPx) || math.IsInf(wikiPerTplPx, 0) {
			allScores = append(allScores, -1)
			continue
		}
		wikiHalf := int(math.Ceil(float64(mosaicTotal)/2 * wikiPerTplPx))
		if wikiHalf < 16 {
			wikiHalf = 16
		}
		// 防止 K 偏离过远导致渲染巨大区域（触发海量瓦片下载/OOM）
		wikiHalfCap := m.WikiHalfCap
		if wikiHalfCap <= 0 {
			wikiHalfCap = 600
		}
		if wikiHalf > wikiHalfCap {
			allScores = append(allScores, -1)
			if m.DebugLog {
				fmt.Printf("[matcher] scale=%.2f skipped (wikiHalf=%d > cap=%d)\n", s, wikiHalf, wikiHalfCap)
			}
			continue
		}
		wikiMosaic, _, _, err := m.Mosaic.Render(m.LastFix.WorldX, m.LastFix.WorldY, wikiHalf, zoom)
		if err != nil {
			allScores = append(allScores, -1)
			continue
		}
		mosaic := resizeGrayBilinear(wikiMosaic, mosaicTotal, mosaicTotal)
		x, y, score := nccArgMaxMasked(mosaic, tplResized, mask, tMean, tDenom)
		allScores = append(allScores, score)
		if m.DebugLog {
			fmt.Printf("[matcher] scale=%.2f wikiPerTplPx=%.3f wikiHalf=%d → score=%.3f at (%d,%d)\n",
				s, wikiPerTplPx, wikiHalf, score, x, y)
		}
		if score > bestScore {
			bestScore = score
			bestScale = s
			bestX, bestY = x, y
			bestWikiPerTplPx = wikiPerTplPx
		}
	}

	// 反算世界坐标：模板中心相对搜索图中心的位移
	dxTpl := float64(bestX+templateSize/2) - float64(mosaicTotal)/2
	dyTpl := float64(bestY+templateSize/2) - float64(mosaicTotal)/2
	dxWiki := dxTpl * bestWikiPerTplPx
	dyWiki := dyTpl * bestWikiPerTplPx
	zScale := mapdata.BaseScale * math.Pow(2, float64(zoom))
	wx := m.LastFix.WorldX + dxWiki/zScale
	wy := m.LastFix.WorldY + dyWiki/zScale

	m.LastDebug = DebugInfo{
		BestScale:    bestScale,
		BestScore:    bestScore,
		BestX:        bestX,
		BestY:        bestY,
		WikiPerTplPx: bestWikiPerTplPx,
		MosaicSize:   mosaicTotal,
		TemplateSize: templateSize,
		WorldDeltaX:  dxWiki / zScale,
		WorldDeltaY:  dyWiki / zScale,
		AllScores:    allScores,
	}

	if bestScore < minConf {
		m.failStreak++
		return Fix{
				WorldX: wx, WorldY: wy, Confidence: bestScore,
			}, fmt.Errorf("locator: low confidence %.2f (best scale %.2f, streak=%d, radius×%d)",
				bestScore, bestScale, m.failStreak, radiusMul)
	}

	// 边界命中检测：如果最佳匹配紧贴搜索区四边（≤1 px），
	// 玩家很可能在搜索范围之外，匹配只是搜索框边缘的偶然像素。
	// 视为定位失败，避免把错误位置反复写入 seed 形成发散。
	maxBest := mosaicTotal - templateSize
	atBoundary := bestX <= 1 || bestY <= 1 || bestX >= maxBest-1 || bestY >= maxBest-1
	if atBoundary {
		m.failStreak++
		return Fix{
				WorldX: wx, WorldY: wy, Confidence: bestScore,
			}, fmt.Errorf("locator: best match at search boundary (%.2f at scale %.2f); 玩家可能已超出搜索半径，请右键校准",
				bestScore, bestScale)
	}

	m.failStreak = 0
	fix := Fix{WorldX: wx, WorldY: wy, Confidence: bestScore}
	if m.HeadingDetect {
		if h, ok := detectHeadingRGBA(roi); ok {
			fix.Heading = h
			fix.HasHeading = true
		}
	}
	return fix, nil
}

// SetSeed 给定一个起始位置（世界坐标）。
func (m *Matcher) SetSeed(worldX, worldY float64) {
	m.LastFix = &Fix{WorldX: worldX, WorldY: worldY}
}

// UpdateSeed 用最新成功的 Fix 更新种子。
func (m *Matcher) UpdateSeed(f Fix) {
	if m.LastFix == nil {
		m.LastFix = &Fix{}
	}
	m.LastFix.WorldX = f.WorldX
	m.LastFix.WorldY = f.WorldY
}

// ---- 工具函数 ----

func grayscale(src *image.RGBA) *image.Gray {
	b := src.Bounds()
	g := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := src.RGBAAt(x, y)
			l := uint8((uint32(c.R)*299 + uint32(c.G)*587 + uint32(c.B)*114) / 1000)
			g.SetGray(x, y, color.Gray{Y: l})
		}
	}
	return g
}

func centerCropSquare(src *image.Gray, side int) *image.Gray {
	b := src.Bounds()
	w := b.Dx()
	h := b.Dy()
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	dst := image.NewGray(image.Rect(0, 0, side, side))
	for dy := 0; dy < side; dy++ {
		for dx := 0; dx < side; dx++ {
			dst.SetGray(dx, dy, src.GrayAt(x0+dx, y0+dy))
		}
	}
	return dst
}

// cropGraySquareAt 以 (cx, cy) 为中心裁出 side×side 方块（越界处置 0）。
func cropGraySquareAt(src *image.Gray, cx, cy, side int) *image.Gray {
	b := src.Bounds()
	x0 := cx - side/2
	y0 := cy - side/2
	dst := image.NewGray(image.Rect(0, 0, side, side))
	for dy := 0; dy < side; dy++ {
		for dx := 0; dx < side; dx++ {
			sx := x0 + dx
			sy := y0 + dy
			if sx < b.Min.X || sx >= b.Max.X || sy < b.Min.Y || sy >= b.Max.Y {
				continue
			}
			dst.SetGray(dx, dy, src.GrayAt(sx, sy))
		}
	}
	return dst
}

func resizeGrayBilinear(src *image.Gray, dstW, dstH int) *image.Gray {
	b := src.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, dstW, dstH))
	if srcW == 0 || srcH == 0 {
		return dst
	}
	xRatio := float64(srcW-1) / float64(dstW)
	yRatio := float64(srcH-1) / float64(dstH)
	for j := 0; j < dstH; j++ {
		sy := float64(j) * yRatio
		y0 := int(sy)
		fy := sy - float64(y0)
		if y0 >= srcH-1 {
			y0 = srcH - 2
			fy = 1
		}
		if y0 < 0 {
			y0 = 0
			fy = 0
		}
		for i := 0; i < dstW; i++ {
			sx := float64(i) * xRatio
			x0 := int(sx)
			fx := sx - float64(x0)
			if x0 >= srcW-1 {
				x0 = srcW - 2
				fx = 1
			}
			if x0 < 0 {
				x0 = 0
				fx = 0
			}
			a := float64(src.GrayAt(b.Min.X+x0, b.Min.Y+y0).Y)
			c := float64(src.GrayAt(b.Min.X+x0+1, b.Min.Y+y0).Y)
			d := float64(src.GrayAt(b.Min.X+x0, b.Min.Y+y0+1).Y)
			e := float64(src.GrayAt(b.Min.X+x0+1, b.Min.Y+y0+1).Y)
			top := a*(1-fx) + c*fx
			bot := d*(1-fx) + e*fx
			v := top*(1-fy) + bot*fy
			dst.SetGray(i, j, color.Gray{Y: uint8(v + 0.5)})
		}
	}
	return dst
}

// buildCircleMask 构造 size × size 的圆形 mask（true=有效，false=无效），稍微内缩 1px。
func buildCircleMask(size int) []bool {
	m := make([]bool, size*size)
	cx := float64(size-1) / 2
	cy := float64(size-1) / 2
	r := float64(size)/2 - 1.0
	if r < 1 {
		r = 1
	}
	r2 := r * r
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			dx := float64(i) - cx
			dy := float64(j) - cy
			if dx*dx+dy*dy <= r2 {
				m[j*size+i] = true
			}
		}
	}
	return m
}

// buildCenterHoleMask 构造 size × size 的"中心孔"mask（true=有效=圆外，false=圆内）。
// 用于屏蔽玩家箭头：箭头永远在中心，半径 ~size/8 的小圆把它扣掉。
func buildCenterHoleMask(size, holeR int) []bool {
	m := make([]bool, size*size)
	cx := float64(size-1) / 2
	cy := float64(size-1) / 2
	r2 := float64(holeR) * float64(holeR)
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			dx := float64(i) - cx
			dy := float64(j) - cy
			m[j*size+i] = dx*dx+dy*dy > r2
		}
	}
	return m
}

// buildNoiseMaskFromRGBA 在 RGBA 上识别噪声像素（玩家箭头/视野锥/高饱和图标），
// 降采样到模板尺度，返回 size × size 的 bool（true=干净底图，false=噪声需屏蔽）。
//
// 采用最近邻 + 3×3 窗口"任一邻居为噪声则该 dst 像素也算噪声"（轻度膨胀），让边缘也被屏蔽。
func buildNoiseMaskFromRGBA(roi *image.RGBA, srcSide, dstSize int) []bool {
	b := roi.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	x0 := b.Min.X + (srcW-srcSide)/2
	y0 := b.Min.Y + (srcH-srcSide)/2

	// 先在源尺度下计算噪声标记
	srcMask := make([]bool, srcSide*srcSide) // true = noise
	for j := 0; j < srcSide; j++ {
		for i := 0; i < srcSide; i++ {
			c := roi.RGBAAt(x0+i, y0+j)
			if isOrangeArrow(c) || isViewCone(c) || isVividIcon(c) {
				srcMask[j*srcSide+i] = true
			}
		}
	}

	// 降采样到 dstSize：dst 像素覆盖的 src 区域中任一为噪声 → dst 视为噪声
	dst := make([]bool, dstSize*dstSize)
	step := float64(srcSide) / float64(dstSize)
	for j := 0; j < dstSize; j++ {
		sy0 := int(float64(j) * step)
		sy1 := int(float64(j+1) * step)
		if sy1 > srcSide {
			sy1 = srcSide
		}
		if sy1 == sy0 {
			sy1 = sy0 + 1
		}
		for i := 0; i < dstSize; i++ {
			sx0 := int(float64(i) * step)
			sx1 := int(float64(i+1) * step)
			if sx1 > srcSide {
				sx1 = srcSide
			}
			if sx1 == sx0 {
				sx1 = sx0 + 1
			}
			noise := false
		outer:
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					if srcMask[yy*srcSide+xx] {
						noise = true
						break outer
					}
				}
			}
			dst[j*dstSize+i] = !noise // true = clean
		}
	}
	return dst
}

func applyMaskInplace(g *image.Gray, mask []bool) {
	for i, ok := range mask {
		if !ok && i < len(g.Pix) {
			g.Pix[i] = 0
		}
	}
}

// buildNoiseMaskFromRGBAAt 与 buildNoiseMaskFromRGBA 相同，但 srcSide×srcSide
// 区域以 (cx, cy) 为中心截取（而不是 ROI 几何中心）。越界处视为噪声（mask out）。
func buildNoiseMaskFromRGBAAt(roi *image.RGBA, cx, cy, srcSide, dstSize int) []bool {
	b := roi.Bounds()
	x0 := cx - srcSide/2
	y0 := cy - srcSide/2

	srcMask := make([]bool, srcSide*srcSide) // true = noise
	for j := 0; j < srcSide; j++ {
		for i := 0; i < srcSide; i++ {
			sx := x0 + i
			sy := y0 + j
			if sx < b.Min.X || sx >= b.Max.X || sy < b.Min.Y || sy >= b.Max.Y {
				srcMask[j*srcSide+i] = true // 越界 = 屏蔽
				continue
			}
			c := roi.RGBAAt(sx, sy)
			if isOrangeArrow(c) || isViewCone(c) {
				srcMask[j*srcSide+i] = true
			}
		}
	}

	dst := make([]bool, dstSize*dstSize)
	step := float64(srcSide) / float64(dstSize)
	for j := 0; j < dstSize; j++ {
		sy0 := int(float64(j) * step)
		sy1 := int(float64(j+1) * step)
		if sy1 > srcSide {
			sy1 = srcSide
		}
		if sy1 == sy0 {
			sy1 = sy0 + 1
		}
		for i := 0; i < dstSize; i++ {
			sx0 := int(float64(i) * step)
			sx1 := int(float64(i+1) * step)
			if sx1 > srcSide {
				sx1 = srcSide
			}
			if sx1 == sx0 {
				sx1 = sx0 + 1
			}
			noise := false
		outer:
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					if srcMask[yy*srcSide+xx] {
						noise = true
						break outer
					}
				}
			}
			dst[j*dstSize+i] = !noise
		}
	}
	return dst
}

// masksumStats 计算 mask 内的均值和 Σ(x-μ)²。
func masksumStats(g *image.Gray, mask []bool) (mean, denom float64) {
	var sum float64
	cnt := 0
	for i, ok := range mask {
		if !ok {
			continue
		}
		sum += float64(g.Pix[i])
		cnt++
	}
	if cnt == 0 {
		return 0, 0
	}
	mean = sum / float64(cnt)
	for i, ok := range mask {
		if !ok {
			continue
		}
		d := float64(g.Pix[i]) - mean
		denom += d * d
	}
	return
}

// nccArgMaxMasked 在 src 中滑窗匹配 tpl，仅统计 mask 内像素，返回最高 NCC 处的左上角。
func nccArgMaxMasked(src, tpl *image.Gray, mask []bool, tMean, tDenom float64) (bx, by int, best float64) {
	sb := src.Bounds()
	tb := tpl.Bounds()
	tw := tb.Dx()
	th := tb.Dy()
	if sb.Dx() < tw || sb.Dy() < th {
		return 0, 0, -1
	}
	if tDenom == 0 {
		return 0, 0, -1
	}
	sqrtTDenom := math.Sqrt(tDenom)
	maskCount := 0
	for _, ok := range mask {
		if ok {
			maskCount++
		}
	}
	if maskCount == 0 {
		return 0, 0, -1
	}

	best = -2
	maxX := sb.Dx() - tw
	maxY := sb.Dy() - th
	stride := src.Stride
	for y := 0; y <= maxY; y++ {
		for x := 0; x <= maxX; x++ {
			var sumW float64
			for ty := 0; ty < th; ty++ {
				row := (y+ty)*stride + x
				mrow := ty * tw
				for tx := 0; tx < tw; tx++ {
					if !mask[mrow+tx] {
						continue
					}
					sumW += float64(src.Pix[row+tx])
				}
			}
			meanW := sumW / float64(maskCount)
			var num, denomW float64
			for ty := 0; ty < th; ty++ {
				row := (y+ty)*stride + x
				mrow := ty * tw
				for tx := 0; tx < tw; tx++ {
					if !mask[mrow+tx] {
						continue
					}
					dw := float64(src.Pix[row+tx]) - meanW
					dt := float64(tpl.Pix[mrow+tx]) - tMean
					num += dt * dw
					denomW += dw * dw
				}
			}
			if denomW <= 0 {
				continue
			}
			score := num / (sqrtTDenom * math.Sqrt(denomW))
			if score > best {
				best = score
				bx = x
				by = y
			}
		}
	}
	return
}
