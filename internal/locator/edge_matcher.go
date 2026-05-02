package locator

import (
	"fmt"
	"image"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/where-to-go/app/internal/mapdata"
)

// EdgeMatcher 基于边缘图 SSD 的定位器。
//
// 设计理念（与旧 NCC Matcher 的关键差异）：
//   - 工作在 Sobel 边缘空间而不是原始亮度：手绘地图带轻微模糊 + 分数缩放，
//     边缘空间能跨过亚像素错位仍然给出尖锐峰；同时纯色海洋 / 草地的均匀
//     区域在边缘图里 ≈ 0，不会形成"吸引子"
//   - 用 SSD（差的平方和）而不是 NCC：真实匹配处的 SSD 是一个明确的极小值，
//     易于通过"最佳值 vs 5 分位"判断尖锐度（=置信度）。NCC 的归一化在
//     大区域均匀值上会反向放大幻觉峰
//   - 屏蔽 mask 由 BuildFeatureMask 给出：图标/箭头/视野锥/区域标记按颜色排除，
//     SSD 只在干净底图像素上累计
//   - 多尺度（K 比例）只在校准期使用；运行期 K 已锁定，搜索空间退化为 (dx, dy)
type EdgeMatcher struct {
	Mosaic *MosaicProvider

	// WorldUnitsPerMinimapPx K：最重要的运行参数。
	// 由 Calibrate 在会话开始时锁定，运行期不再变。
	WorldUnitsPerMinimapPx float64

	// SearchZoom wiki 渲染的 zoom（默认 8 = 最大）。
	SearchZoom int

	// SearchRadiusMinPx 在 minimap 像素空间里的搜索半径（±N px）。
	// 默认 30；LOST 状态会动态放大到 SearchRadiusMaxPx。
	SearchRadiusMinPx int
	SearchRadiusMaxPx int

	// LastFix 上一次匹配（=种子）。Match 会以它作为搜索区中心。
	LastFix *Fix

	// MinSharpness 置信度阈值（=最佳 SSD / 5 分位 SSD 的 1 - 比值）。
	// 越大表示峰越尖锐；默认 0.30。
	MinSharpness float64

	// HeadingDetect 是否同时输出朝向。
	HeadingDetect bool

	// AllowMissingCircle 单元测试 / 合成数据下允许 ROI 没有真实灰色圆。
	AllowMissingCircle bool

	// DebugLog 打印中间值。
	DebugLog bool

	// LastDebug 最近一次 Match 的诊断输出。
	LastDebug EdgeDebug

	// Downsample 在 Match 内部对模板 / mosaic / mask 同步降采样的因子（≥1）。
	// 1 = 全分辨率；2 = 各方向缩半（像素 1/4，搜索位置 1/4，总 ~1/16 用时）。
	// 主要用于多种子粗搜：找到候选区域后再用全分辨率 Match 精修。
	// 取 2 时对应 ~ 2 minimap-px 的定位精度，足够给 verify 阶段用作 seed。
	Downsample int

	// EarlyStopSharp 多种子搜索（MatchMultiSeed）里的早停门槛：找到 sharp ≥ 该值
	// 的 seed 后立即返回，跳过剩余 seeds。注意此字段对单次 Match 无效。
	// 0 或负值 = 不早停（默认）。常用 0.55 作为"很高信心可信任"的早停阈。
	EarlyStopSharp float64

	// failStreak LOST 状态：连续失败计数。
	failStreak int
}

// MatchMultiSeed 对每个候选 seed 各跑一次 Match，返回尖锐度最高的那次结果。
// 适合后台重定位 / LOST 兜底：用宽搜索半径（SearchRadiusMaxPx）在多个候选位置
// 独立搜索，避免把 seed 绑死到单一位置。
//
// 当所有 seed 都返回 err（例如 minimap 不可见）时，返回最后一次的 err。
// 至少有一个 seed 给出 fix 时，err=nil，即使尖锐度很低也会返回——由调用方
// 决定是否采信（通常看 fix.Confidence 阈值）。
func (m *EdgeMatcher) MatchMultiSeed(roi *image.RGBA, seeds []Fix) (best Fix, allSharp []float64, err error) {
	if len(seeds) == 0 {
		return Fix{}, nil, fmt.Errorf("edge: no seeds")
	}
	oldMin := m.SearchRadiusMinPx
	oldMax := m.SearchRadiusMaxPx
	oldSeed := m.LastFix
	// 宽搜索半径：与 MaxPx 一致，覆盖 seed 附近 ±R minimap-px
	r := m.SearchRadiusMaxPx
	if r <= 0 {
		r = 120
	}
	m.SearchRadiusMinPx = r
	m.SearchRadiusMaxPx = r
	defer func() {
		m.SearchRadiusMinPx = oldMin
		m.SearchRadiusMaxPx = oldMax
		m.LastFix = oldSeed
	}()

	bestSharp := -1.0
	var lastErr error
	any := false
	for i, s := range seeds {
		m.SetSeed(s.WorldX, s.WorldY)
		f, e := m.Match(roi)
		sharp := m.LastDebug.Sharpness
		allSharp = append(allSharp, sharp)
		if e != nil && f.Confidence <= 0 {
			// 彻底失败（minimap 不可见、clean 太少等）
			lastErr = e
			continue
		}
		any = true
		// 即使 err 非 nil（如 low sharp / boundary 警告）fix 坐标仍可用
		if sharp > bestSharp {
			bestSharp = sharp
			best = f
			best.Confidence = sharp
		}
		// 早停：sharp 已经达到很高信心，剩余 seeds 不太可能更好。
		// 对粗搜场景这能节省 50%+ 的时间。
		if m.EarlyStopSharp > 0 && sharp >= m.EarlyStopSharp {
			_ = i
			break
		}
	}
	if !any {
		return Fix{}, allSharp, lastErr
	}
	return best, allSharp, nil
}


type EdgeDebug struct {
	BestDX, BestDY   int     // 最佳偏移（minimap-px 空间，=玩家相对种子的位移）
	BestSSD          float64 // 最佳 SSD
	P05SSD           float64 // 5 分位 SSD
	Sharpness        float64 // 1 - BestSSD/P05SSD
	UsedK            float64
	WikiPxPerMinimap float64
	MinimapDiameter  int
	MaskClean        int
	MaskTotal        int
	SearchRadius     int
	WorldDX, WorldDY float64
	MinimapCenterCX  int
	MinimapCenterCY  int
	MinimapCenterCR  int
	CircleFound      bool
}

// NewEdgeMatcher 用合理默认值构造。
func NewEdgeMatcher(m *MosaicProvider, K float64) *EdgeMatcher {
	return &EdgeMatcher{
		Mosaic:                 m,
		WorldUnitsPerMinimapPx: K,
		SearchZoom:             8,
		SearchRadiusMinPx:      30,
		SearchRadiusMaxPx:      120,
		MinSharpness:           0.30,
		HeadingDetect:          true,
	}
}

// SetSeed 设置初始种子位置。
func (m *EdgeMatcher) SetSeed(worldX, worldY float64) {
	m.LastFix = &Fix{WorldX: worldX, WorldY: worldY}
}

// UpdateSeed 用最新成功的 Fix 更新种子位置。
func (m *EdgeMatcher) UpdateSeed(f Fix) {
	if m.LastFix == nil {
		m.LastFix = &Fix{}
	}
	m.LastFix.WorldX = f.WorldX
	m.LastFix.WorldY = f.WorldY
}

// Match 实现 MatchFn。
func (m *EdgeMatcher) Match(roi *image.RGBA) (Fix, error) {
	if m.Mosaic == nil || m.WorldUnitsPerMinimapPx <= 0 {
		return Fix{}, ErrNotImplemented
	}
	if m.LastFix == nil {
		return Fix{}, fmt.Errorf("locator: no seed; call SetSeed first")
	}

	// 1) 找 minimap 内圈
	cCX, cCY, cR, found := FindMinimapInner(roi)
	rb := roi.Bounds()
	if !found {
		// 判断是否已经是紧裁剪 minimap：短边 in [180, 280] → 几乎肯定就是 minimap
		side := rb.Dx()
		if rb.Dy() < side {
			side = rb.Dy()
		}
		tightCrop := side >= 180 && side <= 280
		if tightCrop || m.AllowMissingCircle {
			cCX = rb.Min.X + rb.Dx()/2
			cCY = rb.Min.Y + rb.Dy()/2
			cR = side/2 - 3
			found = false // 标记是退化结果
		} else {
			m.failStreak++
			return Fix{}, fmt.Errorf("minimap not visible (no grey ring detected)")
		}
	}
	D := 2 * cR
	if D < 32 {
		return Fix{}, fmt.Errorf("locator: minimap too small (D=%d)", D)
	}

	// 2) 模板：minimap 整图灰度 + Sobel；mask 同尺寸
	gray := ToGray(roi)
	mask := BuildFeatureMask(roi, cCX, cCY, cR)
	// 关键：把 mask=0 的像素（圆外、玩家箭头、白色未渲染等）替换为 mean gray，
	// 否则 Sobel 在 mask 边界会产生强烈"假边缘"污染 SSD。
	// 同样，wiki mosaic 在 cov=0 区也用 mean gray 填充，让两侧的 Sobel
	// 在屏蔽位置都给出 ≈ 0 梯度。
	tplGrayFilled := fillMaskedWithMean(gray, mask)
	tplEdge := Sobel(GaussianBlur3(tplGrayFilled))

	maskTotal := mask.Bounds().Dx() * mask.Bounds().Dy()
	maskClean := 0
	for _, v := range mask.Pix {
		if v > 0 {
			maskClean++
		}
	}
	// 太少干净像素：需要 D*D/16，太严会误杀纯海洋 / 纯草地图。
	if maskClean < D*D/16 {
		m.failStreak++
		return Fix{}, fmt.Errorf("locator: too few clean pixels (%d / %d)", maskClean, D*D)
	}

	// 3) 搜索半径自适应：失败连击越多，半径越大
	radius := m.SearchRadiusMinPx
	if radius <= 0 {
		radius = 30
	}
	maxR := m.SearchRadiusMaxPx
	if maxR <= 0 {
		maxR = 120
	}
	streak := m.failStreak
	if streak >= 3 {
		radius = int(math.Min(float64(maxR), float64(radius)*1.5))
	}
	if streak >= 8 {
		radius = maxR
	}

	zoom := m.SearchZoom
	if zoom <= 0 {
		zoom = 8
	}

	// 4) 计算每模板像素对应多少 wiki 像素，再决定 mosaic 渲染半径
	wikiPxPerMM := m.WorldUnitsPerMinimapPx * mapdata.BaseScale * math.Pow(2, float64(zoom))
	if wikiPxPerMM <= 0 || math.IsNaN(wikiPxPerMM) || math.IsInf(wikiPxPerMM, 0) {
		return Fix{}, fmt.Errorf("locator: invalid K=%.4f", m.WorldUnitsPerMinimapPx)
	}
	// 渲染一个非方形 mosaic：宽 = ROI.W + 2*radius，高 = ROI.H + 2*radius；
	// mosaic 中心严格对应 (LastFix.WorldX, LastFix.WorldY)。
	// wiki-px 半径取较大边的一半即可，最后 ResampleGray 截到 minimap-px。
	mosaicW := rb.Dx() + 2*radius
	mosaicH := rb.Dy() + 2*radius
	mosaicMax := mosaicW
	if mosaicH > mosaicMax {
		mosaicMax = mosaicH
	}
	wikiHalf := int(math.Ceil(float64(mosaicMax) / 2 * wikiPxPerMM))
	if wikiHalf < 32 {
		wikiHalf = 32
	}
	if wikiHalf > 1500 {
		return Fix{}, fmt.Errorf("locator: wikiHalf %d > cap (K too small?)", wikiHalf)
	}

	wikiMosaic, wikiCov, _, _, err := m.Mosaic.RenderWithCoverage(m.LastFix.WorldX, m.LastFix.WorldY, wikiHalf, zoom)
	if err != nil {
		return Fix{}, fmt.Errorf("mosaic: %w", err)
	}
	// resize 到 minimap-px 空间，再裁出 mosaicW × mosaicH 居中区域
	wikiSide := 2 * wikiHalf
	resizedSide := int(math.Round(float64(wikiSide) / wikiPxPerMM))
	mosaicSquareMM := ResampleGray(wikiMosaic, resizedSide, resizedSide)
	covSquareMM := ResampleGray(wikiCov, resizedSide, resizedSide)
	// 居中裁剪
	mosaicMM := image.NewGray(image.Rect(0, 0, mosaicW, mosaicH))
	covMM := image.NewGray(image.Rect(0, 0, mosaicW, mosaicH))
	cropX0 := (resizedSide - mosaicW) / 2
	cropY0 := (resizedSide - mosaicH) / 2
	for j := 0; j < mosaicH; j++ {
		sy := cropY0 + j
		if sy < 0 || sy >= resizedSide {
			continue
		}
		for i := 0; i < mosaicW; i++ {
			sx := cropX0 + i
			if sx < 0 || sx >= resizedSide {
				continue
			}
			mosaicMM.Pix[j*mosaicW+i] = mosaicSquareMM.Pix[sy*resizedSide+sx]
			covMM.Pix[j*mosaicW+i] = covSquareMM.Pix[sy*resizedSide+sx]
		}
	}
	mosaicEdge := Sobel(GaussianBlur3(fillMaskedWithMean(mosaicMM, covMM)))

	// 5) SSD 滑窗：mask 大小 = ROI；mosaic 大小 = (W+2R) × (H+2R)；
	//    搜索范围 dx, dy ∈ [-radius, +radius]；最佳 (dx, dy) 表示
	//    模板"应该"贴在 mosaic 上 (radius+dx, radius+dy) 的位置。
	//
	// 降采样 step = max(1, Downsample)：内层像素遍历和外层 (dx, dy) 搜索
	// 位置都按 step 跳。step=2 时总用时降到 ~1/16，定位精度变成 ±2 minimap-px,
	// 适合粗搜（之后 verify 阶段会用 step=1 全分辨率精修）。
	//
	// 并行化：外层 dy 维度由 GOMAXPROCS 个 worker 分块（每个 worker 处理
	// 一段连续的 dy），内部互不依赖（每个 (dy, dx) 是独立 SSD 计算）。
	// 在 8 核机器上 r=200 的 Match 从 ~80ms 降到 ~20ms。
	step := m.Downsample
	if step < 1 {
		step = 1
	}
	W := rb.Dx()
	H := rb.Dy()
	tplPix := tplEdge.Pix
	mosPix := mosaicEdge.Pix
	covPix := covMM.Pix
	maskPix := mask.Pix
	tplStride := tplEdge.Stride
	mosStride := mosaicEdge.Stride
	covStride := covMM.Stride
	maskStride := mask.Stride
	minClean := 100 / (step * step)
	if minClean < 30 {
		minClean = 30
	}

	// 收集 dy 候选
	dyList := make([]int, 0, (2*radius)/step+1)
	for dy := -radius; dy <= radius; dy += step {
		dyList = append(dyList, dy)
	}
	numDy := len(dyList)
	dxPerRow := (2*radius)/step + 1

	// 并行：每行（一个 dy）扫描所有 dx
	type rowResult struct {
		ssd      []float64 // 该 row 所有有效 SSD
		bestSSD  float64
		bestDX   int
		bestDY   int
		hasBest  bool
	}
	results := make([]rowResult, numDy)

	workers := runtime.GOMAXPROCS(0)
	if workers > numDy {
		workers = numDy
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	chunk := (numDy + workers - 1) / workers
	for w := 0; w < workers; w++ {
		startIdx := w * chunk
		endIdx := startIdx + chunk
		if endIdx > numDy {
			endIdx = numDy
		}
		if startIdx >= endIdx {
			break
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for idx := s; idx < e; idx++ {
				dy := dyList[idx]
				oy := radius + dy
				rr := rowResult{
					ssd:     make([]float64, 0, dxPerRow),
					bestSSD: math.Inf(1),
				}
				for dx := -radius; dx <= radius; dx += step {
					ox := radius + dx
					var sum float64
					cnt := 0
					for j := 0; j < H; j += step {
						ti := j * tplStride
						mi := j * maskStride
						si := (oy+j)*mosStride + ox
						ci := (oy+j)*covStride + ox
						for i := 0; i < W; i += step {
							if maskPix[mi+i] == 0 {
								continue
							}
							if covPix[ci+i] == 0 {
								continue
							}
							a := int(tplPix[ti+i])
							b := int(mosPix[si+i])
							d := a - b
							sum += float64(d * d)
							cnt++
						}
					}
					if cnt < minClean {
						continue
					}
					sv := sum / float64(cnt)
					rr.ssd = append(rr.ssd, sv)
					if sv < rr.bestSSD {
						rr.bestSSD = sv
						rr.bestDX = dx
						rr.bestDY = dy
						rr.hasBest = true
					}
				}
				results[idx] = rr
			}
		}(startIdx, endIdx)
	}
	wg.Wait()

	// 合并各 row：拼接 SSD list + 找全局最佳
	totalCount := 0
	for _, r := range results {
		totalCount += len(r.ssd)
	}
	allSSD := make([]float64, 0, totalCount)
	bestSSD := math.Inf(1)
	bestDX, bestDY := 0, 0
	for _, r := range results {
		allSSD = append(allSSD, r.ssd...)
		if r.hasBest && r.bestSSD < bestSSD {
			bestSSD = r.bestSSD
			bestDX = r.bestDX
			bestDY = r.bestDY
		}
	}

	if len(allSSD) == 0 {
		m.failStreak++
		return Fix{}, fmt.Errorf("locator: empty SSD landscape")
	}

	// 6) 尖锐度：最佳 SSD 与 5 分位 SSD 的反比
	sortedSSD := append([]float64(nil), allSSD...)
	sort.Float64s(sortedSSD)
	p05 := sortedSSD[len(sortedSSD)/20] // 5%
	if p05 <= 1 {
		p05 = 1
	}
	sharp := 1.0 - bestSSD/p05

	// 7) 反算世界坐标
	wikiPxPerMMNow := wikiPxPerMM
	dxWiki := float64(bestDX) * wikiPxPerMMNow
	dyWiki := float64(bestDY) * wikiPxPerMMNow
	zScale := mapdata.BaseScale * math.Pow(2, float64(zoom))
	worldDX := dxWiki / zScale
	worldDY := dyWiki / zScale
	wx := m.LastFix.WorldX + worldDX
	wy := m.LastFix.WorldY + worldDY

	m.LastDebug = EdgeDebug{
		BestDX: bestDX, BestDY: bestDY,
		BestSSD:          bestSSD,
		P05SSD:           p05,
		Sharpness:        sharp,
		UsedK:            m.WorldUnitsPerMinimapPx,
		WikiPxPerMinimap: wikiPxPerMMNow,
		MinimapDiameter:  D,
		MaskClean:        maskClean,
		MaskTotal:        maskTotal,
		SearchRadius:     radius,
		WorldDX:          worldDX,
		WorldDY:          worldDY,
		MinimapCenterCX:  cCX,
		MinimapCenterCY:  cCY,
		MinimapCenterCR:  cR,
		CircleFound:      found,
	}

	if m.DebugLog {
		fmt.Printf("[edge] K=%.3f wikiPxPerMM=%.3f D=%d radius=%d clean=%d/%d → bestSSD=%.1f p05=%.1f sharp=%.3f at (%d,%d)\n",
			m.WorldUnitsPerMinimapPx, wikiPxPerMM, D, radius, maskClean, maskTotal,
			bestSSD, p05, sharp, bestDX, bestDY)
	}

	if sharp < m.MinSharpness {
		m.failStreak++
		return Fix{
				WorldX: wx, WorldY: wy, Confidence: sharp,
			}, fmt.Errorf("locator: low sharpness %.3f < %.3f (streak=%d, radius=%d)",
				sharp, m.MinSharpness, m.failStreak, radius)
	}
	// 边界命中：最佳偏移紧贴搜索半径 → 玩家可能已超出。
	//
	// 处理策略：
	//   - 仍然 failStreak++，下一次 Match 自动扩大半径（见前面的 radius 自适应）
	//   - 但 **不返回 err**。sharp 已经达到 MinSharpness 说明这个 fix 位置在
	//     搜索范围内是可信的最佳匹配，只是真实玩家可能已略超出范围。上层
	//     应该采信此 fix（更新 seed 朝移动方向跟上），靠下次扩半径收敛。
	//     旧行为返回 err 会导致 wrapper 全部丢弃 → seed 永远不动 → 玩家持续
	//     移动时 at-boundary 永远触发 → UI 卡住靠 bg 兜底。
	atBoundary := bestDX <= -radius+1 || bestDX >= radius-1 || bestDY <= -radius+1 || bestDY >= radius-1
	if atBoundary && streak < 8 {
		m.failStreak++
		fix := Fix{WorldX: wx, WorldY: wy, Confidence: sharp}
		if m.HeadingDetect {
			if h, ok := detectHeadingRGBA(roi); ok {
				fix.Heading = h
				fix.HasHeading = true
			}
		}
		return fix, nil
	}

	m.failStreak = 0
	fix := Fix{WorldX: wx, WorldY: wy, Confidence: sharp}
	if m.HeadingDetect {
		if h, ok := detectHeadingRGBA(roi); ok {
			fix.Heading = h
			fix.HasHeading = true
		}
	}
	return fix, nil
}
