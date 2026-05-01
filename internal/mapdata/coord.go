// Package mapdata 提供地图坐标变换与运行时数据存储。
package mapdata

import "math"

// 与 Wiki 中 L.CRS.Simple + Transformation(0.0078125, 0, 0.0078125, 0) 对齐。
//
// 在 Leaflet Simple CRS 中：
//
//	pixelX = transformation.a * x + transformation.b
//	pixelY = transformation.c * y + transformation.d
//
// 即在 z=0 时 1 个游戏世界单位 = 0.0078125 像素，
// 而瓦片本身 256x256 像素，
// 在缩放级别 z 下，每个世界单位对应的像素 = 0.0078125 * 2^z。
const (
	TileSize       = 256
	BaseScale      = 0.0078125 // 1 / 128
	BaseTransScale = 0.0078125
)

// WorldToPixel 给定世界坐标 (x=lng, y=lat) 与缩放级别 z，
// 返回该点在该缩放级别的瓦片像素坐标系中的位置（左上角是 (0,0) 但坐标可为负）。
func WorldToPixel(worldX, worldY float64, z int) (px, py float64) {
	scale := BaseScale * math.Pow(2, float64(z))
	return worldX * scale, worldY * scale
}

// PixelToWorld 像素坐标反推世界坐标。
func PixelToWorld(px, py float64, z int) (worldX, worldY float64) {
	scale := BaseScale * math.Pow(2, float64(z))
	return px / scale, py / scale
}

// PixelToTile 像素 -> 瓦片索引（floor）。
func PixelToTile(px, py float64) (tx, ty int) {
	return int(math.Floor(px / TileSize)), int(math.Floor(py / TileSize))
}

// TileToPixel 瓦片左上角的像素坐标。
func TileToPixel(tx, ty int) (px, py float64) {
	return float64(tx * TileSize), float64(ty * TileSize)
}

// TileRefer：refer = ceil((1 << (z-1)) / 2)
func TileRefer(z int) int {
	if z <= 0 {
		return 1
	}
	v := 1 << (z - 1)
	return (v + 1) / 2
}

// IsTileInBounds 判断给定瓦片索引在该缩放级别下是否落在有效范围内。
// myBounds 顺序为左右上下（x1, x2, y1, y2）。
func IsTileInBounds(z, x, y, x1, x2, y1, y2 int) bool {
	r := TileRefer(z)
	return -r*x1 <= x && x < r*x2 && -r*y1 <= y && y < r*y2
}
