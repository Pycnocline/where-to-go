package locator

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"image/png"

	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/tilecache"
	"github.com/where-to-go/app/internal/wiki"
)

// TestMatchOnSyntheticMinimap 用 wiki 瓦片合成一张"假"minimap，验证 Matcher 能定位回原始世界坐标。
//
// 这是一个 sanity check：匹配框架若正确，合成数据应当 100% 高分定位。
// 缓存目录由 -wt-cache-root 标志或 WT_CACHE_ROOT 环境变量指定，
// 否则尝试当前目录下的 cache/ 或 winutil 默认 CacheRoot。
func TestMatchOnSyntheticMinimap(t *testing.T) {
	cacheRoot := findCacheRoot()
	if cacheRoot == "" {
		t.Skip("未找到 wiki 缓存目录；跳过（设置 WT_CACHE_ROOT 或在仓库根目录运行）")
	}
	res, err := wiki.LoadResult(cacheRoot)
	if err != nil || res == nil || res.Meta == nil {
		t.Skipf("无法加载本地 manifest（%v）；先运行 -cmd fetch", err)
	}
	var layer wiki.Layer
	for _, ly := range res.Meta.Layers {
		if ly.Name == "G" {
			layer = ly
			break
		}
	}
	if layer.Name == "" {
		t.Skip("找不到 G 图层")
	}

	// 找一个已下载的 zoom 8 G 瓦片
	tileDir := filepath.Join(cacheRoot, "tiles", "G", "8")
	entries, err := os.ReadDir(tileDir)
	if err != nil {
		t.Skipf("找不到 G/8 瓦片目录：%v；建议先在 GUI 中浏览到 zoom 8 让缓存填充", tileDir)
	}
	var tilePath string
	var tx, ty int
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".png" {
			continue
		}
		// 解析 "X_Y.png"
		base := name[:len(name)-4]
		var x, y int
		if _, err := parseTileName(base, &x, &y); err != nil {
			continue
		}
		tilePath = filepath.Join(tileDir, name)
		tx, ty = x, y
		break
	}
	if tilePath == "" {
		t.Skip("G/8 没有任何可用瓦片；先在 GUI 中放大到 zoom 8 让缓存下载")
	}

	tileImg, err := loadPNGFile(tilePath)
	if err != nil {
		t.Fatalf("加载瓦片失败：%v", err)
	}
	// 在瓦片中部裁出一块作为 "minimap"（128x128，足够大）
	const cropSide = 128
	cx0 := (tileImg.Bounds().Dx() - cropSide) / 2
	cy0 := (tileImg.Bounds().Dy() - cropSide) / 2
	mini := image.NewRGBA(image.Rect(0, 0, cropSide, cropSide))
	for j := 0; j < cropSide; j++ {
		for i := 0; i < cropSide; i++ {
			mini.Set(i, j, tileImg.At(cx0+i, cy0+j))
		}
	}

	// 真实世界中心：tile (tx, ty) 在 zoom 8 下对应像素 (tx*256+128, ty*256+128) 加上 crop 中心偏移
	tilePxCenterX := float64(tx*mapdata.TileSize) + float64(cropSide)/2 + float64(cx0)
	tilePxCenterY := float64(ty*mapdata.TileSize) + float64(cropSide)/2 + float64(cy0)
	worldX, worldY := mapdata.PixelToWorld(tilePxCenterX, tilePxCenterY, 8)

	// seed 偏离真实位置（搜索半径内）
	seedOffset := 5.0 // 世界单位
	seedX := worldX + seedOffset
	seedY := worldY - seedOffset

	// 设置 K：合成数据下，1 minimap像素 = 1 wiki像素 at z=8 → K = 1/(BaseScale*2^8) = 0.5
	K := 0.5

	fetcher := wiki.NewFetcher(cacheRoot, nil)
	cache := tilecache.NewCache(cacheRoot, fetcher)
	mosaic := &MosaicProvider{Cache: cache, Layer: layer}

	m := &Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: K,
		SearchZoom:             8,
		HeadingDetect:          false,
		AllowMissingCircle:     true, // 合成数据没有灰色内边框
		DebugLog:               testing.Verbose(),
	}
	m.SetSeed(seedX, seedY)

	fix, err := m.Match(mini)
	t.Logf("synthetic seed=(%.1f,%.1f) truth=(%.1f,%.1f) → fix=(%.1f,%.1f) score=%.3f scale=%.2f err=%v",
		seedX, seedY, worldX, worldY, fix.WorldX, fix.WorldY, fix.Confidence, m.LastDebug.BestScale, err)
	t.Logf("  全部尺度得分：%v", m.LastDebug.AllScores)
	if err != nil {
		t.Fatalf("匹配错误：%v", err)
	}
	if fix.Confidence < 0.7 {
		t.Errorf("合成测试 NCC 得分过低（%.3f < 0.7），说明匹配框架本身有问题", fix.Confidence)
	}
	dx := fix.WorldX - worldX
	dy := fix.WorldY - worldY
	if math.Sqrt(dx*dx+dy*dy) > 4.0 {
		t.Errorf("合成测试位置偏差 > 4 世界单位：dx=%.2f dy=%.2f", dx, dy)
	}
}

// TestMatchTestImagesSmoke 加载 testMinimapImg/*.png，跑一遍 matcher 流程不 panic。
// 没有 ground truth seed，只验证流程可执行。
func TestMatchTestImagesSmoke(t *testing.T) {
	root := findRepoRoot()
	if root == "" {
		t.Skip("找不到仓库根目录")
	}
	dir := filepath.Join(root, "testMinimapImg")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("找不到 %s", dir)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".png" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		img, err := loadPNGFile(path)
		if err != nil {
			t.Errorf("加载 %s 失败：%v", e.Name(), err)
			continue
		}
		// 灰度 + mask 验证
		gray := grayscale(img)
		bnd := gray.Bounds()
		side := bnd.Dx()
		if h := bnd.Dy(); h < side {
			side = h
		}
		if side < 16 {
			t.Errorf("%s ROI 太小：%d", e.Name(), side)
			continue
		}
		// 朝向检测：橙黄箭头（带详细诊断）
		dbg := DetectHeadingRGBADebug(img)
		bb := img.Bounds()
		t.Logf("[%s] minimap 圆: center=(%d,%d) r=%d found=%v (ROI %dx%d 几何中心=(%d,%d))",
			e.Name(), dbg.CircleCX, dbg.CircleCY, dbg.CircleR, dbg.CircleFound,
			bb.Dx(), bb.Dy(), bb.Min.X+bb.Dx()/2, bb.Min.Y+bb.Dy()/2)
		if dbg.OK {
			t.Logf("       朝向 ≈ %.1f° (橙黄=%d 在圆心紧凑半径内, 质心相对圆心偏移=(%.2f,%.2f))",
				dbg.Heading*180/math.Pi, dbg.OrangeInCenter, dbg.CentroidX, dbg.CentroidY)
		} else {
			t.Logf("       朝向：未检测到 (中心橙黄=%d)", dbg.OrangeInCenter)
		}
		// 整图橙黄像素总数（diagnostic）
		orange := 0
		for y := bb.Min.Y; y < bb.Max.Y; y++ {
			for x := bb.Min.X; x < bb.Max.X; x++ {
				if isOrangeArrow(img.RGBAAt(x, y)) {
					orange++
				}
			}
		}
		t.Logf("       整图橙黄像素=%d（%dx%d）", orange, bb.Dx(), bb.Dy())
	}
}

// findCacheRoot 尝试常见缓存路径。
func findCacheRoot() string {
	if v := os.Getenv("WT_CACHE_ROOT"); v != "" {
		return v
	}
	root := findRepoRoot()
	if root == "" {
		return ""
	}
	candidates := []string{
		filepath.Join(root, "cache"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "AppData", "Local", "where-to-go"),
			filepath.Join(home, ".local", "share", "where-to-go"),
		)
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "manifest", "manifest.json")); err == nil {
			return c
		}
	}
	return ""
}

// findRepoRoot 由 _test.go 自身路径上溯。
func findRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func loadPNGFile(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	b := img.Bounds()
	rgba := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.At(x, y)
			r, g, b2, a := c.RGBA()
			rgba.Set(x, y, color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b2 >> 8), A: uint8(a >> 8)})
		}
	}
	return rgba, nil
}

func parseTileName(s string, x, y *int) (int, error) {
	xv, yv := 0, 0
	sign := 1
	state := 0
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' && (i == 0 || s[i-1] == '_') {
			sign = -1
			continue
		}
		if c == '_' {
			if state == 0 {
				*x = xv * sign
				xv = 0
				sign = 1
				state = 1
				count++
				continue
			}
			return count, nil
		}
		if c < '0' || c > '9' {
			return count, nil
		}
		if state == 0 {
			xv = xv*10 + int(c-'0')
		} else {
			yv = yv*10 + int(c-'0')
		}
	}
	if state == 1 {
		*y = yv * sign
		count++
	}
	if count != 2 {
		return count, nil
	}
	return count, nil
}
