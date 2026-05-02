package locator

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/tilecache"
	"github.com/where-to-go/app/internal/wiki"
)

// MosaicProvider 把世界坐标 (centerWorldX, centerWorldY) 周围 halfSizePx
// 像素半径在指定 zoom 下的 wiki 瓦片拼接为一张灰度图。
//
// 调用方应保证 tilecache 已加载到所需瓦片；缺失瓦片处填 0。
type MosaicProvider struct {
	Cache *tilecache.Cache
	Layer wiki.Layer
}

// Render 返回一张 size×size（=2*halfSizePx）的灰度图，
// 中心对应 (centerWorldX, centerWorldY) 在 zoom 下的世界像素。
//
// pxOriginX/Y 返回该图左上角在世界像素坐标系下的位置（用于把匹配位置反算回世界坐标）。
func (m *MosaicProvider) Render(centerWorldX, centerWorldY float64, halfSizePx, zoom int) (g *image.Gray, pxOriginX, pxOriginY float64, err error) {
	g, _, pxOriginX, pxOriginY, err = m.RenderWithCoverage(centerWorldX, centerWorldY, halfSizePx, zoom)
	return
}

// RenderWithCoverage 同 Render，但额外返回 coverage mask：未渲染瓦片对应的像素位 = 0，
// 已渲染的位 = 255。SSD 匹配可以把 coverage=0 的像素一并屏蔽，避免地图边界外
// 的"全黑"误导匹配峰值。
func (m *MosaicProvider) RenderWithCoverage(centerWorldX, centerWorldY float64, halfSizePx, zoom int) (g *image.Gray, cov *image.Gray, pxOriginX, pxOriginY float64, err error) {
	if m.Cache == nil {
		return nil, nil, 0, 0, fmt.Errorf("mosaic: cache nil")
	}
	if halfSizePx < 8 {
		halfSizePx = 8
	}
	cpx, cpy := mapdata.WorldToPixel(centerWorldX, centerWorldY, zoom)
	pxOriginX = cpx - float64(halfSizePx)
	pxOriginY = cpy - float64(halfSizePx)
	size := halfSizePx * 2
	rgba := image.NewRGBA(image.Rect(0, 0, size, size))
	cov = image.NewGray(image.Rect(0, 0, size, size))

	tx0 := int(math.Floor(pxOriginX / mapdata.TileSize))
	ty0 := int(math.Floor(pxOriginY / mapdata.TileSize))
	tx1 := int(math.Floor((pxOriginX + float64(size)) / mapdata.TileSize))
	ty1 := int(math.Floor((pxOriginY + float64(size)) / mapdata.TileSize))
	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			if !mapdata.IsTileInBounds(zoom, tx, ty, m.Layer.X1, m.Layer.X2, m.Layer.Y1, m.Layer.Y2) {
				continue
			}
			t := m.Cache.Get(m.Layer, zoom, tx, ty)
			if t.State != tilecache.StateLoaded || t.Image == nil {
				continue
			}
			ox := int(float64(tx*mapdata.TileSize) - pxOriginX)
			oy := int(float64(ty*mapdata.TileSize) - pxOriginY)
			dst := image.Rect(ox, oy, ox+mapdata.TileSize, oy+mapdata.TileSize)
			draw.Draw(rgba, dst, t.Image, image.Point{}, draw.Src)
			// 标 coverage：clip 到 size 范围
			cx0, cy0 := ox, oy
			cx1, cy1 := ox+mapdata.TileSize, oy+mapdata.TileSize
			if cx0 < 0 {
				cx0 = 0
			}
			if cy0 < 0 {
				cy0 = 0
			}
			if cx1 > size {
				cx1 = size
			}
			if cy1 > size {
				cy1 = size
			}
			for jj := cy0; jj < cy1; jj++ {
				for ii := cx0; ii < cx1; ii++ {
					cov.Pix[jj*size+ii] = 255
				}
			}
		}
	}

	g = image.NewGray(rgba.Bounds())
	for i := 0; i < size*size; i++ {
		r := uint32(rgba.Pix[i*4+0])
		gg := uint32(rgba.Pix[i*4+1])
		b := uint32(rgba.Pix[i*4+2])
		g.Pix[i] = uint8((r*299 + gg*587 + b*114) / 1000)
	}
	// 屏蔽 "地图边界半透明白色描边"：wiki tile 在地图外缘有一圈很宽的
	// 半透明白色描边，叠在底图上变成接近纯白的高亮带；游戏内 minimap
	// 不绘制这条带，于是它会在 SSD 上产生强假峰，把玩家定位"吸"到边界。
	// 检测办法：对 cov=255 区做 N 像素侵蚀得到 "近边界" 区，仅在该区
	// 把近白色像素 (R,G,B 都 ≥ 235) 标为 cov=0；屏蔽后下游 fillMaskedWithMean
	// 会把它们替换成均值，Sobel 梯度自然趋零。N 取 32（约 2 个 minimap-px @ z8 /
	// K≈0.96）。
	maskBoundaryStroke(rgba, cov, size, 32)
	_ = color.Gray{}
	return g, cov, pxOriginX, pxOriginY, nil
}

// maskBoundaryStroke 把 "靠近 cov=0 的白色描边" 像素从 cov 中剔除。
// erodePx：cov=255 区内距离 cov=0 不超过该值的像素被视为 "近边界"。
// 该函数原位修改 cov，O(size^2 * erodePx)；为保证 erodePx≤32 时仍快，
// 采用一次广度有限的距离扫描（仅记 "到 cov=0 的曼哈顿距离 ≤ erodePx"）。
func maskBoundaryStroke(rgba *image.RGBA, cov *image.Gray, size, erodePx int) {
	if erodePx <= 0 {
		return
	}
	// 距离场：dist[i] = 当前像素到最近 cov=0 像素的曼哈顿距离上界 (≤ erodePx)。
	// 用两遍扫描（左上→右下，右下→左上）的 chamfer 1 距离变换。
	const INF = int16(1 << 14)
	dist := make([]int16, size*size)
	for i := 0; i < size*size; i++ {
		if cov.Pix[i] == 0 {
			dist[i] = 0
		} else {
			dist[i] = INF
		}
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := y*size + x
			if dist[i] == 0 {
				continue
			}
			best := dist[i]
			if x > 0 && dist[i-1]+1 < best {
				best = dist[i-1] + 1
			}
			if y > 0 && dist[i-size]+1 < best {
				best = dist[i-size] + 1
			}
			dist[i] = best
		}
	}
	for y := size - 1; y >= 0; y-- {
		for x := size - 1; x >= 0; x-- {
			i := y*size + x
			if dist[i] == 0 {
				continue
			}
			best := dist[i]
			if x < size-1 && dist[i+1]+1 < best {
				best = dist[i+1] + 1
			}
			if y < size-1 && dist[i+size]+1 < best {
				best = dist[i+size] + 1
			}
			dist[i] = best
		}
	}
	// 仅在 0 < dist ≤ erodePx 的窄带里检测白色描边像素并屏蔽
	limit := int16(erodePx)
	for i := 0; i < size*size; i++ {
		d := dist[i]
		if d == 0 || d > limit {
			continue
		}
		r := rgba.Pix[i*4+0]
		gg := rgba.Pix[i*4+1]
		b := rgba.Pix[i*4+2]
		if r >= 235 && gg >= 235 && b >= 235 {
			cov.Pix[i] = 0
		}
	}
}
