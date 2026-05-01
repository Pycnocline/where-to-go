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
	if m.Cache == nil {
		return nil, 0, 0, fmt.Errorf("mosaic: cache nil")
	}
	if halfSizePx < 8 {
		halfSizePx = 8
	}
	cpx, cpy := mapdata.WorldToPixel(centerWorldX, centerWorldY, zoom)
	pxOriginX = cpx - float64(halfSizePx)
	pxOriginY = cpy - float64(halfSizePx)
	size := halfSizePx * 2
	rgba := image.NewRGBA(image.Rect(0, 0, size, size))

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
		}
	}

	g = image.NewGray(rgba.Bounds())
	for i := 0; i < size*size; i++ {
		r := uint32(rgba.Pix[i*4+0])
		gg := uint32(rgba.Pix[i*4+1])
		b := uint32(rgba.Pix[i*4+2])
		g.Pix[i] = uint8((r*299 + gg*587 + b*114) / 1000)
	}
	_ = color.Gray{}
	return g, pxOriginX, pxOriginY, nil
}
