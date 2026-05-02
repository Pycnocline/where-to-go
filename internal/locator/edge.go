package locator

import (
	"image"
	"image/color"
	"math"
)

// ToGray 把 RGBA 转灰度（Rec.709 近似）。
func ToGray(src *image.RGBA) *image.Gray {
	b := src.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := src.RGBAAt(b.Min.X+x, b.Min.Y+y)
			l := (uint32(c.R)*299 + uint32(c.G)*587 + uint32(c.B)*114) / 1000
			g.SetGray(x, y, color.Gray{Y: uint8(l)})
		}
	}
	return g
}

// Sobel 3x3 梯度幅值。返回同尺寸 *image.Gray，
// 边界处梯度为 0；幅值线性映射到 0..255。
//
// 选择 Sobel 而不是 Scharr：对手绘地图的"粗轮廓 / 柔边缘"足够区分，同时计算开销低。
func Sobel(g *image.Gray) *image.Gray {
	b := g.Bounds()
	W := b.Dx()
	H := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, W, H))
	if W < 3 || H < 3 {
		return dst
	}
	get := func(x, y int) int {
		return int(g.Pix[y*g.Stride+x])
	}
	// 先算原始幅值到 float 缓存；统一归一化到 0..255。
	tmp := make([]float64, W*H)
	var vmax float64
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			gx := -get(x-1, y-1) - 2*get(x-1, y) - get(x-1, y+1) +
				get(x+1, y-1) + 2*get(x+1, y) + get(x+1, y+1)
			gy := -get(x-1, y-1) - 2*get(x, y-1) - get(x+1, y-1) +
				get(x-1, y+1) + 2*get(x, y+1) + get(x+1, y+1)
			m := math.Sqrt(float64(gx*gx + gy*gy))
			tmp[y*W+x] = m
			if m > vmax {
				vmax = m
			}
		}
	}
	if vmax <= 0 {
		return dst
	}
	scale := 255.0 / vmax
	for i, v := range tmp {
		dst.Pix[i] = uint8(v * scale)
	}
	return dst
}

// SobelNormalized 用固定归一化因子做 Sobel，方便模板和搜索图在相同尺度下比较。
// 如果 maxRaw=0，退化到自适应（等同 Sobel）。
func SobelNormalized(g *image.Gray, maxRaw float64) *image.Gray {
	b := g.Bounds()
	W := b.Dx()
	H := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, W, H))
	if W < 3 || H < 3 {
		return dst
	}
	get := func(x, y int) int {
		return int(g.Pix[y*g.Stride+x])
	}
	scale := 255.0 / maxRaw
	if maxRaw <= 0 {
		return Sobel(g)
	}
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			gx := -get(x-1, y-1) - 2*get(x-1, y) - get(x-1, y+1) +
				get(x+1, y-1) + 2*get(x+1, y) + get(x+1, y+1)
			gy := -get(x-1, y-1) - 2*get(x, y-1) - get(x+1, y-1) +
				get(x-1, y+1) + 2*get(x, y+1) + get(x+1, y+1)
			m := math.Sqrt(float64(gx*gx + gy*gy))
			v := m * scale
			if v > 255 {
				v = 255
			}
			dst.Pix[y*W+x] = uint8(v)
		}
	}
	return dst
}

// GaussianBlur3 3x3 高斯模糊（σ≈1），用于降噪后再做 Sobel。
func GaussianBlur3(g *image.Gray) *image.Gray {
	b := g.Bounds()
	W := b.Dx()
	H := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, W, H))
	if W < 3 || H < 3 {
		copy(dst.Pix, g.Pix)
		return dst
	}
	get := func(x, y int) int {
		return int(g.Pix[y*g.Stride+x])
	}
	// 核: [1 2 1; 2 4 2; 1 2 1] / 16
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			s := get(x-1, y-1) + 2*get(x, y-1) + get(x+1, y-1) +
				2*get(x-1, y) + 4*get(x, y) + 2*get(x+1, y) +
				get(x-1, y+1) + 2*get(x, y+1) + get(x+1, y+1)
			dst.Pix[y*W+x] = uint8(s / 16)
		}
	}
	// 边界直接复制
	for x := 0; x < W; x++ {
		dst.Pix[x] = g.Pix[x]
		dst.Pix[(H-1)*W+x] = g.Pix[(H-1)*g.Stride+x]
	}
	for y := 0; y < H; y++ {
		dst.Pix[y*W] = g.Pix[y*g.Stride]
		dst.Pix[y*W+W-1] = g.Pix[y*g.Stride+W-1]
	}
	return dst
}

// ResampleGray 双线性缩放到 (dstW, dstH)。
func ResampleGray(src *image.Gray, dstW, dstH int) *image.Gray {
	b := src.Bounds()
	sW := b.Dx()
	sH := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, dstW, dstH))
	if sW == 0 || sH == 0 {
		return dst
	}
	xR := float64(sW-1) / float64(dstW)
	yR := float64(sH-1) / float64(dstH)
	for j := 0; j < dstH; j++ {
		sy := float64(j) * yR
		y0 := int(sy)
		fy := sy - float64(y0)
		if y0 >= sH-1 {
			y0 = sH - 2
			fy = 1
		}
		if y0 < 0 {
			y0 = 0
			fy = 0
		}
		for i := 0; i < dstW; i++ {
			sx := float64(i) * xR
			x0 := int(sx)
			fx := sx - float64(x0)
			if x0 >= sW-1 {
				x0 = sW - 2
				fx = 1
			}
			if x0 < 0 {
				x0 = 0
				fx = 0
			}
			a := float64(src.Pix[(b.Min.Y+y0)*src.Stride+b.Min.X+x0])
			c := float64(src.Pix[(b.Min.Y+y0)*src.Stride+b.Min.X+x0+1])
			d := float64(src.Pix[(b.Min.Y+y0+1)*src.Stride+b.Min.X+x0])
			e := float64(src.Pix[(b.Min.Y+y0+1)*src.Stride+b.Min.X+x0+1])
			top := a*(1-fx) + c*fx
			bot := d*(1-fx) + e*fx
			v := top*(1-fy) + bot*fy
			dst.Pix[j*dstW+i] = uint8(v + 0.5)
		}
	}
	return dst
}

// fillMaskedWithMean 把 src 的 mask=0 像素用 mask>0 区域的均值替代，并保持
// 原图大小与位置。这样 Sobel/Gaussian 在 mask 边界不会产生陡峭的假边缘。
func fillMaskedWithMean(src *image.Gray, mask *image.Gray) *image.Gray {
	b := src.Bounds()
	W := b.Dx()
	H := b.Dy()
	dst := image.NewGray(image.Rect(0, 0, W, H))
	// 计算 mask>0 区均值
	var sum int64
	cnt := 0
	for i := 0; i < W*H; i++ {
		if mask.Pix[i] > 0 {
			sum += int64(src.Pix[i])
			cnt++
		}
	}
	mean := uint8(128)
	if cnt > 0 {
		mean = uint8(sum / int64(cnt))
	}
	for i := 0; i < W*H; i++ {
		if mask.Pix[i] > 0 {
			dst.Pix[i] = src.Pix[i]
		} else {
			dst.Pix[i] = mean
		}
	}
	return dst
}
