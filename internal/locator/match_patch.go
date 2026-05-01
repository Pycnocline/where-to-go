package locator

import (
	"fmt"
	"image"
	"math"

	"github.com/where-to-go/app/internal/mapdata"
)

// MatchPatchVote 备用匹配：把模板切成若干小 patch，每个 patch 在 wiki mosaic 里
// 各自做 NCC 全局搜索，根据最佳匹配位置投票出"模板该贴在哪里"。
//
// 思路（用户提议）：即使小地图被玩家箭头/视野锥/图标等遮挡了一部分，
// 没被遮挡的小区域仍然完全等同 wiki 底图。如果多个 patch 投票指向同一个
// (dx, dy) 偏移，那么该位置被几何一致性强烈支持，比单一全局 NCC 更鲁棒。
//
// 输入:
//   roi    : 当前 minimap 截图
//   scales : 候选 K 比例（取最高一致性的尺度）
//   N      : grid 边长（4 → 16 个 patch）
//   patchPx: 每个 patch 在模板像素中的边长（默认 12）
// 输出 Fix.Confidence = 一致性强度 ∈ [0, 1]
//
// 当 scales=nil 时使用 [0.7, 1.0, 1.4]。
func (m *Matcher) MatchPatchVote(roi *image.RGBA, scales []float64) (Fix, error) {
	if m.Mosaic == nil || m.WorldUnitsPerMinimapPx <= 0 {
		return Fix{}, ErrNotImplemented
	}
	if m.LastFix == nil {
		return Fix{}, fmt.Errorf("locator: no seed")
	}
	templateSize := m.TemplateSize
	if templateSize <= 0 {
		templateSize = 48
	}
	radius := m.SearchRadiusPx
	if radius <= 0 {
		radius = 48
	}
	zoom := m.SearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	if len(scales) == 0 {
		scales = []float64{0.7, 1.0, 1.4}
	}
	const N = 4         // 4x4 grid
	const patchPx = 12  // 12x12 patch in template space

	// 1. 准备模板（同 Match 的圆心对齐裁剪 + 噪声 mask）
	gray := grayscale(roi)
	cCX, cCY, cR, found := FindMinimapCircle(roi)
	if !found {
		if !m.AllowMissingCircle {
			return Fix{}, fmt.Errorf("patch-vote: minimap not visible")
		}
		rb := roi.Bounds()
		cCX = rb.Min.X + rb.Dx()/2
		cCY = rb.Min.Y + rb.Dy()/2
		side := rb.Dx()
		if rb.Dy() < side {
			side = rb.Dy()
		}
		cR = side/2 - 2
	}
	D := 2 * cR
	if D < 16 {
		return Fix{}, fmt.Errorf("locator: minimap too small")
	}
	tplGray := cropGraySquareAt(gray, cCX, cCY, D)
	tplResized := resizeGrayBilinear(tplGray, templateSize, templateSize)
	circleMask := buildCircleMask(templateSize)
	centerHole := buildCenterHoleMask(templateSize, templateSize/12)
	noiseMask := buildNoiseMaskFromRGBAAt(roi, cCX, cCY, D, templateSize)
	combined := make([]bool, templateSize*templateSize)
	for i := range combined {
		combined[i] = circleMask[i] && centerHole[i] && noiseMask[i]
	}

	// 2. 选出 N×N 网格中每个 patch 的位置和有效像素数
	type patch struct {
		px, py int    // 模板内的 patch 左上角
		valid  []bool // patch 内 mask
		mean   float64
		denom  float64
	}
	patchStep := templateSize / N
	patches := make([]patch, 0, N*N)
	for gy := 0; gy < N; gy++ {
		for gx := 0; gx < N; gx++ {
			px := gx*patchStep + (patchStep-patchPx)/2
			py := gy*patchStep + (patchStep-patchPx)/2
			if px < 0 || py < 0 || px+patchPx > templateSize || py+patchPx > templateSize {
				continue
			}
			pv := make([]bool, patchPx*patchPx)
			validCount := 0
			for j := 0; j < patchPx; j++ {
				for i := 0; i < patchPx; i++ {
					if combined[(py+j)*templateSize+(px+i)] {
						pv[j*patchPx+i] = true
						validCount++
					}
				}
			}
			// 至少 50% 有效像素才作为 patch 候选（之前 70% 太严，
			// 4×4 网格下四角 patch 落在圆外、加上 center-hole / 噪声 mask 吃掉一部分
			// 之后几乎都过不了 70% 阈值，导致整个 patch-vote 路径直接失败）。
			if validCount < (patchPx*patchPx)*5/10 {
				continue
			}
			// 计算 patch 的均值与方差（masked）
			var sum, sumSq float64
			cnt := 0
			for j := 0; j < patchPx; j++ {
				for i := 0; i < patchPx; i++ {
					if !pv[j*patchPx+i] {
						continue
					}
					v := float64(tplResized.GrayAt(px+i, py+j).Y)
					sum += v
					sumSq += v * v
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			mean := sum / float64(cnt)
			denom := sumSq - float64(cnt)*mean*mean
			if denom <= 1 {
				// 几乎纯色 patch，无信息量
				continue
			}
			patches = append(patches, patch{px: px, py: py, valid: pv, mean: mean, denom: denom})
		}
	}
	if len(patches) < 3 {
		return Fix{}, fmt.Errorf("patch-vote: too few valid patches (%d)", len(patches))
	}

	// 3. 对每个候选 scale 渲染 mosaic 并做 patch 投票
	type result struct {
		scale          float64
		consensus      float64
		voteX, voteY   int // mosaic 像素中的 player 位置
		wikiPerTplPx   float64
		mosaicTotal    int
	}
	mosaicTotal := templateSize + 2*radius
	bestRes := result{}
	for _, s := range scales {
		Keff := m.WorldUnitsPerMinimapPx * s
		wikiPerTplPx := float64(D) / float64(templateSize) * Keff * mapdata.BaseScale * math.Pow(2, float64(zoom))
		if wikiPerTplPx <= 0 || math.IsNaN(wikiPerTplPx) || math.IsInf(wikiPerTplPx, 0) {
			continue
		}
		wikiHalf := int(math.Ceil(float64(mosaicTotal)/2 * wikiPerTplPx))
		if wikiHalf < 16 {
			wikiHalf = 16
		}
		cap := m.WikiHalfCap
		if cap <= 0 {
			cap = 600
		}
		if wikiHalf > cap {
			continue
		}
		wikiMosaic, _, _, err := m.Mosaic.Render(m.LastFix.WorldX, m.LastFix.WorldY, wikiHalf, zoom)
		if err != nil {
			continue
		}
		mosaic := resizeGrayBilinear(wikiMosaic, mosaicTotal, mosaicTotal)

		// 每个 patch 投票
		votes := make(map[int]float64) // bin → 累计 NCC 分
		const binSize = 4              // 把投票 bin 到 4-px 桶以容忍小偏移
		var totalNCC float64
		for _, p := range patches {
			tplPatch := cropPatchFromGray(tplResized, p.px, p.py, patchPx)
			bx, by, ncc := nccArgMaxPatchMasked(mosaic, tplPatch, p.valid, p.mean, p.denom)
			if ncc < 0.25 {
				continue
			}
			totalNCC += ncc
			// 这个 patch 找到位置 (bx, by)，意味着 player 在 mosaic 中位于:
			//   playerX = bx - p.px + templateSize/2
			//   playerY = by - p.py + templateSize/2
			vx := bx - p.px + templateSize/2
			vy := by - p.py + templateSize/2
			// bin
			bxv := vx / binSize
			byv := vy / binSize
			key := bxv*100000 + byv
			votes[key] += ncc
		}
		if totalNCC < 1.0 {
			continue
		}

		// 找到累计 NCC 分最高的 bin
		var bestKey int
		bestSum := 0.0
		for k, v := range votes {
			if v > bestSum {
				bestSum = v
				bestKey = k
			}
		}
		consensus := bestSum / totalNCC // 主导 bin 占比 ∈ [0, 1]
		if consensus > bestRes.consensus {
			bxv := bestKey / 100000
			byv := bestKey - bxv*100000
			vx := bxv*binSize + binSize/2
			vy := byv*binSize + binSize/2
			bestRes = result{
				scale:        s,
				consensus:    consensus,
				voteX:        vx,
				voteY:        vy,
				wikiPerTplPx: wikiPerTplPx,
				mosaicTotal:  mosaicTotal,
			}
		}
	}
	if bestRes.consensus < 0.20 {
		// 没有任何 scale 能让 20% 的 patch 投票一致
		return Fix{}, fmt.Errorf("patch-vote: no consensus (best=%.2f, %d patches)", bestRes.consensus, len(patches))
	}

	// 4. 把投票转世界坐标
	dxTpl := float64(bestRes.voteX) - float64(bestRes.mosaicTotal)/2
	dyTpl := float64(bestRes.voteY) - float64(bestRes.mosaicTotal)/2
	dxWiki := dxTpl * bestRes.wikiPerTplPx
	dyWiki := dyTpl * bestRes.wikiPerTplPx
	zScale := mapdata.BaseScale * math.Pow(2, float64(zoom))
	wx := m.LastFix.WorldX + dxWiki/zScale
	wy := m.LastFix.WorldY + dyWiki/zScale

	fix := Fix{
		WorldX:     wx,
		WorldY:     wy,
		Confidence: bestRes.consensus,
	}
	if m.HeadingDetect {
		if h, ok := detectHeadingRGBA(roi); ok {
			fix.Heading = h
			fix.HasHeading = true
		}
	}
	return fix, nil
}

// cropPatchFromGray 从 src 中截一个 sz×sz 的 patch，左上角在 (x, y)。
func cropPatchFromGray(src *image.Gray, x, y, sz int) *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, sz, sz))
	for j := 0; j < sz; j++ {
		for i := 0; i < sz; i++ {
			dst.SetGray(i, j, src.GrayAt(x+i, y+j))
		}
	}
	return dst
}

// nccArgMaxPatchMasked 在 src 中滑窗匹配 patch（带预算好的 mean/denom 与 mask），
// 返回最高 NCC 处的左上角。
func nccArgMaxPatchMasked(src, patch *image.Gray, valid []bool, pMean, pDenom float64) (bx, by int, best float64) {
	sb := src.Bounds()
	tb := patch.Bounds()
	tw := tb.Dx()
	th := tb.Dy()
	if sb.Dx() < tw || sb.Dy() < th || pDenom == 0 {
		return 0, 0, -1
	}
	cnt := 0
	for _, ok := range valid {
		if ok {
			cnt++
		}
	}
	if cnt == 0 {
		return 0, 0, -1
	}
	sqrtPDenom := math.Sqrt(pDenom)
	maxX := sb.Dx() - tw
	maxY := sb.Dy() - th
	stride := src.Stride
	pStride := patch.Stride
	best = -2
	for y := 0; y <= maxY; y++ {
		for x := 0; x <= maxX; x++ {
			var sumS float64
			for j := 0; j < th; j++ {
				row := (y+j)*stride + x
				vrow := j * tw
				for i := 0; i < tw; i++ {
					if !valid[vrow+i] {
						continue
					}
					sumS += float64(src.Pix[row+i])
				}
			}
			meanS := sumS / float64(cnt)
			var num, denomS float64
			for j := 0; j < th; j++ {
				row := (y+j)*stride + x
				prow := j * pStride
				vrow := j * tw
				for i := 0; i < tw; i++ {
					if !valid[vrow+i] {
						continue
					}
					ds := float64(src.Pix[row+i]) - meanS
					dp := float64(patch.Pix[prow+i]) - pMean
					num += ds * dp
					denomS += ds * ds
				}
			}
			if denomS <= 0 {
				continue
			}
			score := num / (sqrtPDenom * math.Sqrt(denomS))
			if score > best {
				best = score
				bx = x
				by = y
			}
		}
	}
	return
}
