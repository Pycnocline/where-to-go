package locator

import (
	"image"
	"image/color"
	"math"
)

// FeatureMask 按颜色识别 minimap 上"应该屏蔽"的像素。
//
// 屏蔽对象（保守原则——宁错过 UI 也不误伤底图特征）：
//   - 玩家箭头本体 / 视野锥（由圆心孔统一覆盖，不靠颜色）
//   - 类别图标（仅最高饱和最纯的红/紫/黄等 UI 用色）
//   - 圆外
//
// 输入：roi RGBA、圆心 (cCX, cCY)、内圆半径 cR；输出与 roi 同尺寸的 *image.Gray，
// 0 = 屏蔽，255 = 干净底图。后续的 SSD 会按这个 mask 加权。
//
// 实现（关键改动 vs 旧版）：
//  1. isNoisePixel 收紧颜色阈值（紫/品红、纯黄要求更纯净）
//  2. **不再做 3×3 形态学膨胀**——它会让 mask 沿任何噪声像素扩散到 8 邻居，
//     在密集图案区把整片底图都吞掉，导致 clean 比例骤降、SSD 信号变弱
//  3. fillMaskedWithMean（在 EdgeMatcher 里调用）已经把 mask=0 区填均值，
//     Sobel 不会被 mask 边界污染，所以"紧贴 UI 不膨胀"是安全的
func BuildFeatureMask(roi *image.RGBA, cCX, cCY, cR int) *image.Gray {
	b := roi.Bounds()
	W := b.Dx()
	H := b.Dy()
	mask := image.NewGray(image.Rect(0, 0, W, H))
	// 初始：所有像素干净 (255)
	for i := range mask.Pix {
		mask.Pix[i] = 255
	}
	// 像素级标 noise = 0
	for j := 0; j < H; j++ {
		for i := 0; i < W; i++ {
			c := roi.RGBAAt(b.Min.X+i, b.Min.Y+j)
			if isNoisePixel(c) {
				mask.Pix[j*W+i] = 0
			}
		}
	}
	// 3×3 八邻居膨胀：覆盖虚线 / 描边 / 大字的反走样灰边（这些像素单独颜色
	// 判定会漏过）。八邻居对白色虚线边界必要；颜色阈值已经收紧，不会误伤
	// 大片鲜艳图案。
	dil := image.NewGray(image.Rect(0, 0, W, H))
	copy(dil.Pix, mask.Pix)
	for j := 1; j < H-1; j++ {
		for i := 1; i < W-1; i++ {
			minV := mask.Pix[j*W+i]
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					v := mask.Pix[(j+dy)*W+(i+dx)]
					if v < minV {
						minV = v
					}
				}
			}
			dil.Pix[j*W+i] = minV
		}
	}
	mask = dil

	// 圆裁剪 + 圆心孔（玩家箭头 / 视野锥根部由这里负责吃掉）
	cx := float64(cCX - b.Min.X)
	cy := float64(cCY - b.Min.Y)
	rOut := float64(cR) - 2 // 内缩 2px 避免吃到边框
	if rOut < 1 {
		rOut = 1
	}
	rOut2 := rOut * rOut
	// 中心孔：覆盖玩家箭头 + 视野锥根部，半径 = max(8, cR/9)
	rIn := float64(cR) / 9
	if rIn < 8 {
		rIn = 8
	}
	rIn2 := rIn * rIn
	for j := 0; j < H; j++ {
		dy := float64(j) - cy
		for i := 0; i < W; i++ {
			dx := float64(i) - cx
			d2 := dx*dx + dy*dy
			if d2 > rOut2 || d2 < rIn2 {
				mask.Pix[j*W+i] = 0
			}
		}
	}
	return mask
}

// isNoisePixel 像素颜色是否属于"应屏蔽"类别。
// 注意：wiki 底图本身可能有鲜艳的水蓝、草绿、沙黄、紫红石头、黄色房屋等大块
// 区域 —— 这些是真实底图，必须保留。仅屏蔽：
//   - 完全透明 / 半透明 (a < 250)：未渲染
//   - 接近纯白：UI 描边 / 未渲染白
//   - 极纯红 / 紫红 / 纯黄：图标专用色（要求很高饱和才屏蔽）
//   - 极暗近黑：区域标记的描边 / 文字
//
// 阈值整体收紧 vs 旧版：旧版会误吃明亮黄色房屋 / 紫色石头 → 在城镇等图案密集
// 区损失大量底图特征，反而让 SSD 没有显著最小值。
func isNoisePixel(c color.RGBA) bool {
	if c.A < 250 {
		return true
	}
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
	sat := maxC - minC
	bright := maxC

	// 1) 接近纯白：高亮 + 三通道差异极小（视野锥、玩家箭头白边、未渲染白、
	//    地图边界粗描边）。这一条要保持宽松——021226 这种白色边界主导的图
	//    依赖把白边大量屏蔽掉才能给出可用结果；wiki 底图本身鲜有大片接近
	//    纯白的合法区域。
	if bright >= 230 && sat <= 18 {
		return true
	}
	// 2) 玩家箭头：橙黄（R 高 G 中 B 低）—— 收紧到非常纯的橙
	//    旧规则 r>=200 && g in [100, r-25] && b<=110 会误吃黄色建筑
	if r >= 220 && g >= 110 && g <= 170 && b <= 80 && r-b >= 130 {
		return true
	}
	// 3) 极纯红（图标）：R 高 G 低 B 低；收紧到几乎纯红
	if r >= 220 && g <= 60 && b <= 60 {
		return true
	}
	// 4) 紫 / 品红（图标）：R+B 高 G 低 + 高饱和；收紧防误吃紫色石头底图
	if r >= 220 && b >= 220 && g <= 80 && sat >= 140 {
		return true
	}
	// 5) 纯黄（图标）：R+G 高 B 极低 + 高饱和；收紧防误吃黄沙 / 黄房子
	if r >= 240 && g >= 220 && b <= 30 && sat >= 200 {
		return true
	}
	// 6) 极暗近黑：区域标记的描边 / 文字
	if bright <= 25 && sat <= 12 {
		return true
	}
	return false
}

// FindMinimapInner 在 230×230 左右的 minimap 截图里精准定位灰色内圈。
//
// 与旧 FindMinimapCircle 的区别：
//   - 灰色色调以 #8E928A ±18 为中心（用户实测的 minimap 内框颜色）
//   - 半径搜索围绕 ROI 短边 / 2，不允许外扩到 ROI 边界
//   - 多 step 圆周采样（72 + 半径 step 1px）
//   - 退化策略：失败时返回 ROI 几何中心 + 半径 = 短边/2 - 4
func FindMinimapInner(roi *image.RGBA) (cx, cy, r int, ok bool) {
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
	rMin := side/2 - 14
	rMax := side/2 - 1
	if rMin < 8 {
		rMin = 8
	}
	const searchPx = 10
	const samples = 72

	bestScore := 0
	bestCX, bestCY, bestR := midX, midY, side/2-4
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
					if isMinimapBorderColor(roi.RGBAAt(px, py)) {
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

// isMinimapBorderColor 判定灰色内边框颜色：以 #8E928A 为中心，±20。
func isMinimapBorderColor(c color.RGBA) bool {
	r, g, b := int(c.R), int(c.G), int(c.B)
	dr := r - 0x8E
	dg := g - 0x92
	db := b - 0x8A
	if dr < 0 {
		dr = -dr
	}
	if dg < 0 {
		dg = -dg
	}
	if db < 0 {
		db = -db
	}
	if dr > 20 || dg > 20 || db > 20 {
		// 兼容老 a4acac 测试源
		if r > 130 && r < 200 && g > 130 && g < 200 && b > 130 && b < 200 {
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
			return maxC-minC < 25
		}
		return false
	}
	return true
}
