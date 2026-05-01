// Package tracker 实现游戏小地图截图、玩家位置识别与朝向识别。
//
// 关键算法：
//
//	1) Win32 BitBlt 截屏指定屏幕矩形 → image.RGBA
//	2) 灰度化 + 中心圆盘掩码（去除玩家箭头干扰）→ 模板
//	3) 在缓存的 G 层瓦片大图中做归一化互相关（NCC）多分辨率匹配
//	4) 玩家箭头朝向识别：在中心 32×32 区域做 HSV 阈值 + PCA 求主方向
//
// 当前实现完成了 (1)(2)(3) 的核心路径以及 (4) 的简化版本（颜色质心法）。
package tracker

import (
	"image"
	"image/color"
	"math"
)

// ToGray 将 RGBA 灰度化。
func ToGray(src *image.RGBA) *image.Gray {
	b := src.Bounds()
	dst := image.NewGray(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := src.RGBAAt(x, y)
			// Rec.601 灰度
			v := uint8((299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000)
			dst.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return dst
}

// CenterDiskMask 将灰度图中圆心半径 r 内的像素置为均值（去除玩家标记影响）。
// 同时返回新图，不改原图。
func CenterDiskMask(src *image.Gray, r int) *image.Gray {
	b := src.Bounds()
	cx := (b.Min.X + b.Max.X) / 2
	cy := (b.Min.Y + b.Max.Y) / 2
	dst := image.NewGray(b)
	copy(dst.Pix, src.Pix)
	r2 := r * r
	// 用区域均值填充
	var sum, n int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy > r2 {
				sum += int(src.GrayAt(x, y).Y)
				n++
			}
		}
	}
	if n == 0 {
		return dst
	}
	avg := uint8(sum / n)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= r2 {
				dst.SetGray(x, y, color.Gray{Y: avg})
			}
		}
	}
	return dst
}

// MatchResult NCC 匹配最佳位置。
type MatchResult struct {
	X, Y  int     // 在 search 上左上角对齐的最佳位置
	Score float64 // 归一化互相关分数 [-1, 1]，1 表示完美匹配
}

// NCCMatch 在 search 图上滑动 template，返回最佳匹配。
//
// 实现采用经典 sum-of-products + 预计算积分图的快速版本，
// 但本最小可运行版直接做朴素双重循环；对中等尺寸（< 1024 主图，128 模板）够用。
//
// 仅匹配亮度通道。
func NCCMatch(search, template *image.Gray) MatchResult {
	sb := search.Bounds()
	tb := template.Bounds()
	tw := tb.Dx()
	th := tb.Dy()

	// 模板均值 + 方差
	var tmean float64
	var tn = float64(tw * th)
	for y := tb.Min.Y; y < tb.Max.Y; y++ {
		for x := tb.Min.X; x < tb.Max.X; x++ {
			tmean += float64(template.GrayAt(x, y).Y)
		}
	}
	tmean /= tn
	var tvar float64
	for y := tb.Min.Y; y < tb.Max.Y; y++ {
		for x := tb.Min.X; x < tb.Max.X; x++ {
			d := float64(template.GrayAt(x, y).Y) - tmean
			tvar += d * d
		}
	}
	tstd := math.Sqrt(tvar)
	if tstd == 0 {
		return MatchResult{}
	}

	best := MatchResult{Score: -2}
	maxX := sb.Max.X - tw
	maxY := sb.Max.Y - th
	for sy := sb.Min.Y; sy <= maxY; sy++ {
		for sx := sb.Min.X; sx <= maxX; sx++ {
			// 子窗均值
			var smean float64
			for y := 0; y < th; y++ {
				for x := 0; x < tw; x++ {
					smean += float64(search.GrayAt(sx+x, sy+y).Y)
				}
			}
			smean /= tn
			// 协方差与子窗方差
			var cov, svar float64
			for y := 0; y < th; y++ {
				for x := 0; x < tw; x++ {
					d1 := float64(template.GrayAt(tb.Min.X+x, tb.Min.Y+y).Y) - tmean
					d2 := float64(search.GrayAt(sx+x, sy+y).Y) - smean
					cov += d1 * d2
					svar += d2 * d2
				}
			}
			sstd := math.Sqrt(svar)
			if sstd == 0 {
				continue
			}
			score := cov / (tstd * sstd)
			if score > best.Score {
				best.Score = score
				best.X = sx
				best.Y = sy
			}
		}
	}
	return best
}

// CropCircle 以中心为圆心，裁出一个含圆形小地图的方形 RGBA。
// 圆外像素被置为透明白底，方便后续灰度处理。
func CropCircle(src *image.RGBA, r int) *image.RGBA {
	b := src.Bounds()
	cx := (b.Min.X + b.Max.X) / 2
	cy := (b.Min.Y + b.Max.Y) / 2
	dst := image.NewRGBA(image.Rect(0, 0, 2*r, 2*r))
	r2 := r * r
	for y := 0; y < 2*r; y++ {
		for x := 0; x < 2*r; x++ {
			dx := x - r
			dy := y - r
			if dx*dx+dy*dy > r2 {
				dst.SetRGBA(x, y, color.RGBA{R: 220, G: 220, B: 220, A: 255})
				continue
			}
			sx := cx + dx
			sy := cy + dy
			if sx < b.Min.X || sx >= b.Max.X || sy < b.Min.Y || sy >= b.Max.Y {
				continue
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}

// ArrowDirection 估计中心 32×32 内玩家箭头主方向，返回弧度（0=右，逆时针正）。
//
// 算法：抽取颜色饱和度高的像素（非灰白），算其相对中心的质心向量。
// 因为箭头在战斗中一般是醒目色（蓝/黄），周围地图大多偏灰，足够稳定。
func ArrowDirection(src *image.RGBA) (rad float64, conf float64) {
	b := src.Bounds()
	cx := (b.Min.X + b.Max.X) / 2
	cy := (b.Min.Y + b.Max.Y) / 2
	const half = 16
	var sumX, sumY, n float64
	for y := cy - half; y < cy+half; y++ {
		for x := cx - half; x < cx+half; x++ {
			if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
				continue
			}
			c := src.RGBAAt(x, y)
			r, g, bv := int(c.R), int(c.G), int(c.B)
			mx := max3(r, g, bv)
			mn := min3(r, g, bv)
			// 饱和度阈值：色差至少 60，且不接近纯白
			if mx-mn < 60 || mx > 240 {
				continue
			}
			sumX += float64(x - cx)
			sumY += float64(y - cy)
			n++
		}
	}
	if n < 5 {
		return 0, 0
	}
	mx := sumX / n
	my := sumY / n
	// 注意屏幕坐标 y 向下为正，转角时取负 y 让 0=右，逆时针为正
	rad = math.Atan2(-my, mx)
	conf = math.Min(1.0, n/40)
	return rad, conf
}

func max3(a, b, c int) int { return max2(max2(a, b), c) }
func min3(a, b, c int) int { return min2(min2(a, b), c) }
func max2(a, b int) int    { if a > b { return a }; return b }
func min2(a, b int) int    { if a < b { return a }; return b }
