package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/iconcache"
	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/tilecache"
	"github.com/where-to-go/app/internal/wiki"
)

// MapView 瓦片地图渲染 + 交互。线程模型：仅在 Gio 主协程中调用 Layout。
type MapView struct {
	Theme  *Theme
	Store  *mapdata.Store
	Cache  *tilecache.Cache
	Icons  *iconcache.Cache
	Editor *PathEditor
	Routes *RouteStore

	// MarkerStyleFn 由外部注入，用来读当前用户选择的标记样式。
	MarkerStyleFn func() (MarkerStyle, int)

	// SelectionStyleFn 当前选中点位的高亮样式（halo / dot）。
	SelectionStyleFn func() SelectionStyle

	// NavStateFn 由 App 注入：返回当前导航状态。nil 或返回 active=false 时不画导航图层。
	// pathNodes 是当前正在导航的路径节点（按顺序）。playerX/Y 是玩家世界坐标，
	// heading 是朝向（弧度，0=北），hasFix 表示是否有有效定位。progress 是
	// 投影到路径上的进度（用于灰化已走部分）。
	// velX/Y 是速度估计（世界单位/秒），lastFixAt 是上次 fix 的墙钟时间；
	// 二者供预测外推用。predictEnabled 开关是否真的做预测。
	NavStateFn func() (active bool, pathNodes []mapdata.PathNode, playerX, playerY, heading float64, hasFix bool, progress NavProgress, velX, velY float64, lastFixAt time.Time, predictEnabled bool)

	// AutoCenterFn 由 App 注入：返回当前是否要自动居中地图到玩家。
	AutoCenterFn func() bool

	// OnContextMenu 由 App 注入：右键命中地图时调用，由 App 决定如何渲染上下文菜单。
	// (sx, sy) 为屏幕坐标（mapview 内部坐标系），(wx, wy) 为对应世界坐标。
	OnContextMenu func(sx, sy float32, wx, wy float64)

	// OnLeftClickWorld 由 App 注入：在普通工具左键单击之前优先调用。
	// 返回 true 表示已被消费（用于"路径节点编辑：移动 / 插入"等需要拦截下次
	// 左键的场景）。返回 false 时 MapView 继续按当前工具处理。
	OnLeftClickWorld func(wx, wy float64) bool

	// 摄像机：以世界坐标为中心，z 为当前缩放级别。
	centerX float64
	centerY float64
	z       int

	// 当前选定图层（"G"/"B1"/"B2"）。
	layerName string

	// 点位过滤（markType -> 是否显示）。空表示全部不显示。
	visibleTypes map[string]bool

	// 内部交互状态
	tag        int       // 事件 tag
	dragStart  f32.Point // 鼠标按下时刻的屏幕坐标
	dragCenter f32.Point // 鼠标按下时刻的世界中心（屏幕等价像素）
	dragging   bool      // 中键平移中
	leftDown   bool      // 左键按下中（用于框选 / 区分单击与拖拽）
	leftStart  f32.Point // 左键按下时屏幕坐标
	leftMoved  bool      // 是否已发生位移（区分 click 与 drag）
	leftMod    int       // 左键按下瞬间的修饰键状态：0 replace / 1 add / 2 remove

	// 键盘焦点（用于 Ctrl+Z）
	kbdFocused bool

	// 鼠标当前世界坐标（用于状态栏）
	mouseWorldX float64
	mouseWorldY float64

	// 调试：把 drawPoints / drawTiles 的统计打印到 stderr。
	debugLog bool

	// 玩家位置/朝向平滑显示状态（指数衰减插值，让屏幕上玩家移动平滑）
	smoothInit    bool
	smoothPX      float64
	smoothPY      float64
	smoothHeading float64

	// layoutMu 串行化 Layout 调用。主窗口的 Gio 协程与悬浮窗 renderLoop 协程
	// 都会调用 Layout —— 若不串行化，会在 Editor.Selection / smoothPX 写入 /
	// Routes.All 这些共享状态上发生 race，开启路径显示后偶发崩溃。
	layoutMu sync.Mutex

	// overlayMode 仅在悬浮窗 renderLoop 调用 Layout 时置 true（layoutMu 保护下读写）。
	// Gio 的 text.Shaper 内部有共享 map，两窗口并发 Shape 会出 "concurrent map
	// writes" 致命错误（runtime fatal, recover 拦不住）。层级解决方案是"悬浮窗
	// 完全不渲染文本" —— 节点编号、气泡、徽章等全部跳过，只保留图形层。
	// 主窗口保持 overlayMode=false，功能完整。
	overlayMode bool
}

// LayoutAs 用 mode 包装 Layout 调用，供悬浮窗在 overlayMode=true 下渲染。
// 保证 overlayMode 的读写都在 layoutMu 保护下完成，避免与主窗口 Layout 交错。
func (m *MapView) LayoutAs(gtx layout.Context, overlay bool) layout.Dimensions {
	m.layoutMu.Lock()
	prev := m.overlayMode
	m.overlayMode = overlay
	dims := m.layoutLocked(gtx)
	m.overlayMode = prev
	m.layoutMu.Unlock()
	return dims
}

// SetDebug 开启/关闭调试日志输出。
func (m *MapView) SetDebug(on bool) { m.debugLog = on }

// SnapPlayerTo 立刻把玩家显示位置设到 (wx, wy)，跳过平滑插值。
// 用在用户手动校准时，让标记瞬间跳到指定点而不是慢慢漂过去。
func (m *MapView) SnapPlayerTo(wx, wy float64) {
	m.smoothPX = wx
	m.smoothPY = wy
	m.smoothInit = true
}

// NewMapView 构造。Store 中应已加载元数据。
func NewMapView(t *Theme, store *mapdata.Store, cache *tilecache.Cache, icons *iconcache.Cache) *MapView {
	mv := &MapView{
		Theme:        t,
		Store:        store,
		Cache:        cache,
		Icons:        icons,
		layerName:    "G",
		z:            4,
		visibleTypes: map[string]bool{},
	}
	if meta := store.Meta(); meta != nil {
		mv.z = meta.MinZoom
		if len(meta.Layers) > 0 {
			// 找 index==0 的图层作为默认
			for _, ly := range meta.Layers {
				if ly.Index == 0 {
					mv.layerName = ly.Name
					break
				}
			}
		}
	}
	return mv
}

// SetLayer 切换底图图层。
func (m *MapView) SetLayer(name string) { m.layerName = name }

// Layer 当前图层名。
func (m *MapView) Layer() string { return m.layerName }

// SetVisibleType 控制某 markType 点位是否显示。
func (m *MapView) SetVisibleType(markType string, on bool) {
	if on {
		m.visibleTypes[markType] = true
	} else {
		delete(m.visibleTypes, markType)
	}
}

// IsTypeVisible 是否显示该 markType 点位。
func (m *MapView) IsTypeVisible(markType string) bool {
	return m.visibleTypes[markType]
}

// MouseWorld 当前鼠标对应的世界坐标。
func (m *MapView) MouseWorld() (float64, float64) { return m.mouseWorldX, m.mouseWorldY }

// Zoom 当前缩放级别。
func (m *MapView) Zoom() int { return m.z }

// Layout 渲染并处理交互。
//
// 所有瓦片 / 点位的绘制都在 Layout 内的 clip.Rect 之下完成，
// 防止拖动时 tile 偏移到负坐标越界画进相邻控件（侧边栏）。
func (m *MapView) Layout(gtx layout.Context) layout.Dimensions {
	// 串行化：主窗口与悬浮窗共享同一个 MapView。多协程并发进入 Layout 会
	// 在 smooth* / Editor / Routes / Icons 等共享状态上 race（已观察到
	// "悬浮窗打开后再开启路径显示偶发崩溃"），此锁是最简单可靠的保护。
	m.layoutMu.Lock()
	defer m.layoutMu.Unlock()
	return m.layoutLocked(gtx)
}

// layoutLocked 实际的 Layout 主体；调用方必须已持有 layoutMu。
// 给 LayoutAs 复用（它也是在 layoutMu 保护下调用）。
func (m *MapView) layoutLocked(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max

	// 关键：先压入一个 size 大小的裁剪栈，函数返回前再弹出。
	// 这样后续所有 paint 操作都被限制在 mapview 自己的 slot 内。
	clipStack := clip.Rect{Max: size}.Push(gtx.Ops)
	defer clipStack.Pop()

	// 处理事件
	m.handleEvents(gtx, size)

	// 背景
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xdf, G: 0xd4, B: 0xc5, A: 0xff},
		clip.Rect{Max: size}.Op())

	// 找到当前图层定义
	layer := m.findLayer(m.layerName)
	if layer == nil {
		// 数据未就绪
		return m.placeholder(gtx, "图层数据未就绪")
	}

	// 渲染瓦片
	m.drawTiles(gtx, *layer, size)

	// 渲染点位
	m.drawPoints(gtx, size)

	// 路径 / 选区 / 框选矩形（在点位之上）
	m.drawPathOverlay(gtx, size)

	// 注册交互区域（与最外层 clip.Rect 同尺寸即可，无需另起一层）
	event.Op(gtx.Ops, &m.tag)

	return layout.Dimensions{Size: size}
}

func (m *MapView) findLayer(name string) *wiki.Layer {
	for _, ly := range m.Store.Layers() {
		if ly.Name == name {
			lyc := ly
			return &lyc
		}
	}
	return nil
}

func (m *MapView) placeholder(gtx layout.Context, msg string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Body1(m.Theme.Material, msg)
		lbl.Color = m.Theme.TextDim
		lbl.Alignment = text.Middle
		return lbl.Layout(gtx)
	})
}

// drawTiles 绘制视口内的所有可见瓦片。
func (m *MapView) drawTiles(gtx layout.Context, ly wiki.Layer, size image.Point) {
	// 屏幕中心对应的瓦片像素坐标
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	// 屏幕左上角对应瓦片像素
	originPX := cpx - float64(size.X)/2
	originPY := cpy - float64(size.Y)/2
	// 起止瓦片索引
	tx0 := int(math.Floor(originPX / mapdata.TileSize))
	ty0 := int(math.Floor(originPY / mapdata.TileSize))
	tx1 := int(math.Floor((originPX + float64(size.X)) / mapdata.TileSize))
	ty1 := int(math.Floor((originPY + float64(size.Y)) / mapdata.TileSize))

	for ty := ty0; ty <= ty1; ty++ {
		for tx := tx0; tx <= tx1; tx++ {
			if !mapdata.IsTileInBounds(m.z, tx, ty, ly.X1, ly.X2, ly.Y1, ly.Y2) {
				continue
			}
			tile := m.Cache.Get(ly, m.z, tx, ty)
			if tile.State != tilecache.StateLoaded || tile.Image == nil {
				// 占位：浅色块
				ox := int(float64(tx*mapdata.TileSize) - originPX)
				oy := int(float64(ty*mapdata.TileSize) - originPY)
				rect := image.Rect(ox, oy, ox+mapdata.TileSize, oy+mapdata.TileSize)
				paint.FillShape(gtx.Ops,
					color.NRGBA{R: 0xee, G: 0xe6, B: 0xd9, A: 0xff},
					clip.Rect(rect).Op())
				continue
			}
			ox := int(float64(tx*mapdata.TileSize) - originPX)
			oy := int(float64(ty*mapdata.TileSize) - originPY)
			imgOp := paint.NewImageOp(tile.Image)
			stack := op.Offset(image.Point{X: ox, Y: oy}).Push(gtx.Ops)
			cstack := clip.Rect{Max: image.Point{X: mapdata.TileSize, Y: mapdata.TileSize}}.Push(gtx.Ops)
			imgOp.Add(gtx.Ops)
			paint.PaintOp{}.Add(gtx.Ops)
			cstack.Pop()
			stack.Pop()
		}
	}
}

// drawPoints 绘制可见 markType 的所有点位。
func (m *MapView) drawPoints(gtx layout.Context, size image.Point) {
	if len(m.visibleTypes) == 0 {
		if m.debugLog {
			fmt.Printf("[drawPoints] visibleTypes 为空\n")
		}
		return
	}
	style := MarkerBubble
	iconSize := 24
	if m.MarkerStyleFn != nil {
		style, iconSize = m.MarkerStyleFn()
	}
	selStyle := SelectionHalo
	if m.SelectionStyleFn != nil {
		selStyle = m.SelectionStyleFn()
	}
	// 选中的 wiki 点 (markType + pointID) 集合，用于绘制时画 halo。
	selectedWiki := map[string]EndpointRole{}
	if m.Editor != nil && selStyle == SelectionHalo {
		for _, s := range m.Editor.Selection() {
			if s.Source == "wiki" && s.PointID != "" {
				selectedWiki[s.MarkType+"|"+s.PointID] = s.Role
			}
		}
	}
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	originPX := cpx - float64(size.X)/2
	originPY := cpy - float64(size.Y)/2

	// 关键：Go 的 map 迭代顺序是随机的，叠加点位时每帧会以不同顺序绘制，
	// 导致重叠图标的 z-order 在帧间抖动 → 视觉上闪烁。
	// 这里取出 markType 列表并按字典序固定，保证每帧的覆盖关系一致。
	mts := make([]string, 0, len(m.visibleTypes))
	for mt := range m.visibleTypes {
		mts = append(mts, mt)
	}
	sort.Strings(mts)

	totalDrawn := 0
	for _, mt := range mts {
		points := m.Store.PointsOf(mt)
		col := categoryColor(mt)
		cat := m.Store.CategoryOf(mt)
		name := mt
		iconURL := ""
		catType := ""
		if cat != nil {
			if cat.MarkTypeName != "" {
				name = cat.MarkTypeName
			}
			iconURL = cat.Icon
			catType = cat.Type
		}
		// "采集" / "收集" 等 wiki 类别用的 png 是 tooltip 形状（下尖上胖），
		// 几何意义的定位点在底部尖端；用底部锚点让图标底边对准坐标点。
		// wiki 的 category.type 字段就是 "采集" / "收集" / "任务" 等，markTypeName
		// 才是具体物品名（比如"红果"），所以匹配类型要看 type，不是 name。
		bottomAnchor := categoryHasTooltipShape(catType)
		for _, p := range points {
			px, py := mapdata.WorldToPixel(p.Point.Lng, p.Point.Lat, m.z)
			sx := int(px - originPX)
			sy := int(py - originPY)
			if sx < -64 || sy < -64 || sx > size.X+64 || sy > size.Y+64 {
				continue
			}
			c := image.Point{X: sx, Y: sy}
			// 选中点：先画 halo（在图标背后）
			if role, sel := selectedWiki[mt+"|"+p.ID]; sel {
				m.drawHalo(gtx, c, iconSize, role)
			}
			switch style {
			case MarkerIcon:
				m.drawIconMarker(gtx, c, iconURL, iconSize, col, bottomAnchor)
			case MarkerDot:
				drawDot(gtx.Ops, c, 5, col)
			default: // MarkerBubble
				m.drawBubble(gtx, c, name, col)
			}
			totalDrawn++
		}
	}
	if m.debugLog {
		fmt.Printf("[drawPoints] style=%s visibleTypes=%d 总绘制=%d 视口origin=(%.1f,%.1f) z=%d\n",
			style, len(m.visibleTypes), totalDrawn, originPX, originPY, m.z)
	}
}

// drawHalo 在选中点位背后画一个柔和的彩色光晕，不遮挡图标本体。
// role 决定颜色：起点绿、终点红、普通选中黄。
func (m *MapView) drawHalo(gtx layout.Context, c image.Point, iconSize int, role EndpointRole) {
	r := iconSize/2 + 6
	if r < 14 {
		r = 14
	}
	var base color.NRGBA
	switch role {
	case RoleStart:
		base = color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e}
	case RoleEnd:
		base = color.NRGBA{R: 0xe2, G: 0x6a, B: 0x6a}
	default:
		base = color.NRGBA{R: 0xff, G: 0xc8, B: 0x3d}
	}
	// 三层叠加：外层最虚 / 中层中等 / 边缘清晰彩环。
	outer := color.NRGBA{R: base.R, G: base.G, B: base.B, A: 0x40}
	mid := color.NRGBA{R: base.R, G: base.G, B: base.B, A: 0x70}
	paint.FillShape(gtx.Ops, outer,
		clip.Ellipse(image.Rect(c.X-r-4, c.Y-r-4, c.X+r+4, c.Y+r+4)).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, mid,
		clip.Ellipse(image.Rect(c.X-r, c.Y-r, c.X+r, c.Y+r)).Op(gtx.Ops))
	// 用 clip.Stroke 画清晰的边缘环。
	var path clip.Path
	path.Begin(gtx.Ops)
	rf := float32(r)
	cx, cy := float32(c.X), float32(c.Y)
	// 用四段贝塞尔近似一个圆。
	const kappa = 0.5522847498
	k := kappa * rf
	path.MoveTo(f32.Pt(cx+rf, cy))
	path.CubeTo(f32.Pt(cx+rf, cy+k), f32.Pt(cx+k, cy+rf), f32.Pt(cx, cy+rf))
	path.CubeTo(f32.Pt(cx-k, cy+rf), f32.Pt(cx-rf, cy+k), f32.Pt(cx-rf, cy))
	path.CubeTo(f32.Pt(cx-rf, cy-k), f32.Pt(cx-k, cy-rf), f32.Pt(cx, cy-rf))
	path.CubeTo(f32.Pt(cx+k, cy-rf), f32.Pt(cx+rf, cy-k), f32.Pt(cx+rf, cy))
	spec := path.End()
	stroke := clip.Stroke{Path: spec, Width: 2}.Op()
	paint.FillShape(gtx.Ops, color.NRGBA{R: base.R, G: base.G, B: base.B, A: 0xff}, stroke)
}

// drawIconMarker 用 wiki 提供的 png 图标作为标记。
// 图标尚未下载时回退到圆点；下载完成后会通过 OnReady → Invalidate 自动重绘。
//
// bottomAnchor=true 时，图标底部中心对准 c（用于 tooltip 形状的图标，比如
// "采集" / "收集" 这些 wiki 类别 —— 它们的 png 本身是下尖上胖的气泡，
// 几何意义的定位点在底部尖端）。否则图标中心对准 c（默认）。
func (m *MapView) drawIconMarker(gtx layout.Context, c image.Point, url string, size int, fallback color.NRGBA, bottomAnchor bool) {
	if m.Icons == nil || url == "" {
		drawDot(gtx.Ops, c, 5, fallback)
		return
	}
	imgOp := m.Icons.GetOp(url)
	if imgOp == nil {
		// 还没有下载完成时使用小圆点占位（与已加载的图标视觉差异小，
		// 避免下载完成瞬间出现"跳大"的感觉）。
		drawDot(gtx.Ops, c, 4, fallback)
		return
	}
	if size < 12 {
		size = 12
	}
	src := imgOp.Size()
	if src.X == 0 || src.Y == 0 {
		return
	}
	// 以最长边贴齐 size 做等比缩放，避免长宽比被强制压成正方形。
	maxDim := src.X
	if src.Y > maxDim {
		maxDim = src.Y
	}
	scale := float32(size) / float32(maxDim)
	dispW := int(float32(src.X)*scale + 0.5)
	dispH := int(float32(src.Y)*scale + 0.5)
	offX := c.X - dispW/2
	offY := c.Y - dispH/2
	if bottomAnchor {
		// 底部中心对准 c：x 依旧居中，y 让图标底边落在 c.Y。
		offY = c.Y - dispH
	}
	off := op.Offset(image.Point{X: offX, Y: offY}).Push(gtx.Ops)
	aff := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops)
	cstack := clip.Rect{Max: src}.Push(gtx.Ops)
	imgOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cstack.Pop()
	aff.Pop()
	off.Pop()
}

// drawBubble 绘制白底 + 类别名 + 彩色边框的小气泡，并在锚点处画一个小圆点。
func (m *MapView) drawBubble(gtx layout.Context, c image.Point, name string, col color.NRGBA) {
	// 悬浮窗：完全跳过，只画锚点。两窗口并发 text.Shape 是 runtime fatal。
	if m.overlayMode {
		drawDot(gtx.Ops, c, 3, col)
		return
	}
	// 标签裁剪到 6 个字符以内，避免一些类别名很长撑爆地图。
	runes := []rune(name)
	if len(runes) > 6 {
		runes = append(runes[:6], '…')
		name = string(runes)
	}
	// 估算尺寸：CJK ≈ 14px、ASCII ≈ 7px、上下左右 padding 4px。
	cjk := 0
	ascii := 0
	for _, r := range runes {
		if r > 127 {
			cjk++
		} else {
			ascii++
		}
	}
	w := cjk*14 + ascii*7 + 12
	h := 20
	bx := c.X - w/2
	by := c.Y - h - 8

	// 阴影：在主体下偏移 1px 的暗色块，提升对比。
	shadow := image.Rect(bx-1, by+1, bx+w+1, by+h+1)
	rr := gtx.Dp(unit.Dp(4))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 0x40}, clip.UniformRRect(shadow, rr).Op(gtx.Ops))
	// 彩色边框
	outer := image.Rect(bx-1, by-1, bx+w+1, by+h+1)
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(outer, rr).Op(gtx.Ops))
	// 白色填充
	body := image.Rect(bx, by, bx+w, by+h)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.UniformRRect(body, rr).Op(gtx.Ops))
	// 文字
	pad := 6
	stack := op.Offset(image.Point{X: bx + pad, Y: by + 2}).Push(gtx.Ops)
	gtx2 := gtx
	gtx2.Constraints = layout.Constraints{
		Min: image.Point{},
		Max: image.Point{X: w - 2*pad, Y: h - 4},
	}
	lbl := material.Label(m.Theme.Material, unit.Sp(12), name)
	lbl.Color = color.NRGBA{R: 0x20, G: 0x20, B: 0x28, A: 0xff}
	lbl.Font.Typeface = "CJK"
	lbl.MaxLines = 1
	lbl.Alignment = text.Start
	lbl.Layout(gtx2)
	stack.Pop()
	// 锚点圆点
	drawDot(gtx.Ops, c, 3, col)
}

// drawDot 在 (cx,cy) 画半径 r 的实心圆，外加 1px 白色边框。
// 用两层 FillShape 实现（外层稍大白色 + 内层目标颜色），避免 clip.Stroke 在某些后端的问题。
func drawDot(ops *op.Ops, c image.Point, r int, col color.NRGBA) {
	outer := image.Rect(c.X-r-1, c.Y-r-1, c.X+r+1, c.Y+r+1)
	paint.FillShape(ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.Ellipse(outer).Op(ops))
	inner := image.Rect(c.X-r, c.Y-r, c.X+r, c.Y+r)
	paint.FillShape(ops, col, clip.Ellipse(inner).Op(ops))
}

// categoryHasTooltipShape 判断给定 wiki 类别名是否对应"下尖上胖"的 tooltip
// 形状图标（采集 / 收集 / 任务 NPC 等）。这些图标的几何定位点在底部尖端，
// 应当用 bottomAnchor 让底部对准坐标。
//
// wiki 没有显式标记图标形状，这里靠类别名启发式匹配。
func categoryHasTooltipShape(name string) bool {
	if name == "" {
		return false
	}
	for _, kw := range []string{"采集", "收集"} {
		if containsRune(name, kw) {
			return true
		}
	}
	return false
}

// containsRune 简化版 strings.Contains，避免再 import。
func containsRune(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// categoryColor 根据 markType 简单哈希出颜色（避免无类别表也能区分）。
func categoryColor(markType string) color.NRGBA {
	h := uint32(0)
	for i := 0; i < len(markType); i++ {
		h = h*31 + uint32(markType[i])
	}
	r := byte(80 + h%150)
	g := byte(80 + (h/150)%150)
	b := byte(80 + (h/22500)%150)
	return color.NRGBA{R: r, G: g, B: b, A: 0xff}
}

// handleEvents 处理鼠标 + 键盘事件。
//
// 操作模型（与 Wiki 大地图保持一致）：
//   - 中键拖拽          → 平移地图（任何工具下生效）
//   - 滚轮              → 缩放（任何工具下生效）
//   - 右键单击          → 唤出"功能列表"上下文菜单（由 App 渲染）
//   - 左键              → 当前工具决定具体行为
//   - Ctrl+Z            → 撤销最近一次编辑
//
// 左键在不同工具下：
//   - ToolPan       单击 = 切换该 wiki 点的选中状态；拖拽 = 框选
//                   Shift 追加 / Ctrl 减去 / 默认替换
//   - ToolPlace     单击 = 命中 wiki 点 → 加入选区；空白 → 创建用户路径点
//   - ToolFreedraw  按下 → 拖拽 → 松开：自由笔触
//   - ToolChain     单击 = 串联节点（命中 wiki 用 wiki，否则放置临时用户点）
func (m *MapView) handleEvents(gtx layout.Context, size image.Point) {
	// 键盘焦点：仅在用户点入地图（pointer.Press）时主动声明，避免与
	// 工具栏 / 备注 / 重命名等输入框频繁互抢焦点。

	// 键盘：Ctrl+Z 撤销
	for {
		ev, ok := gtx.Source.Event(key.Filter{Focus: &m.tag, Name: "Z", Required: key.ModCtrl})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press && m.Editor != nil {
			m.Editor.Undo()
		}
	}
	// 焦点变化通知
	for {
		ev, ok := gtx.Source.Event(key.FocusFilter{Target: &m.tag})
		if !ok {
			break
		}
		if fe, ok := ev.(key.FocusEvent); ok {
			m.kbdFocused = fe.Focus
		}
	}

	// 指针事件
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target:  &m.tag,
			Kinds:   pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll | pointer.Move | pointer.Cancel,
			ScrollY: pointer.ScrollRange{Min: -1, Max: 1},
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		// 通用：移动 / 滚轮
		switch pe.Kind {
		case pointer.Move:
			m.updateMouseWorld(pe.Position, size)
			continue
		case pointer.Scroll:
			m.handleScroll(pe.Scroll.Y, pe.Position, size)
			continue
		}

		// 任何按键按下时也确保键盘焦点（点回地图）
		if pe.Kind == pointer.Press && !m.kbdFocused {
			gtx.Execute(key.FocusCmd{Tag: &m.tag})
		}

		// 中键 = 平移（与工具无关）。
		//
		// 兼容"自由绘制 / 框选过程中再按下中键"的场景：Gio 在另一颗按键已经
		// 按下时新加按下的键不一定再发 pointer.Press（实测：左键已按下后再按
		// 中键，事件可能直接是 Drag 且 Buttons 同时含 Primary | Tertiary）。
		// 因此除了 Press，Drag 事件里也要检测"中键刚刚被加进来"的状态转换。
		hasMid := pe.Buttons.Contain(pointer.ButtonTertiary)
		startMidDrag := func() {
			m.dragging = true
			m.dragStart = pe.Position
			cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
			m.dragCenter = f32.Pt(float32(cpx), float32(cpy))
		}
		if pe.Kind == pointer.Press && hasMid {
			startMidDrag()
			continue
		}
		if !m.dragging && hasMid && pe.Kind == pointer.Drag {
			startMidDrag()
			continue
		}
		if m.dragging && pe.Kind == pointer.Drag {
			// 中键已松开 → 退出平移，把控制权还给当前工具。
			if !hasMid {
				m.dragging = false
			} else {
				dx := pe.Position.X - m.dragStart.X
				dy := pe.Position.Y - m.dragStart.Y
				newPx := float64(m.dragCenter.X - dx)
				newPy := float64(m.dragCenter.Y - dy)
				m.centerX, m.centerY = mapdata.PixelToWorld(newPx, newPy, m.z)
				m.updateMouseWorld(pe.Position, size)
				continue
			}
		}
		if m.dragging && (pe.Kind == pointer.Release || pe.Kind == pointer.Cancel) {
			// 仅当中键确实松开时才结束平移；其它键松开（左键 / 右键）保留 dragging。
			if !hasMid {
				m.dragging = false
			}
			// 不 continue —— 让本次 Release 也派发到当前工具，否则 freedraw 的
			// 左键松开会被吞掉、笔触不会结束。
		}

		// 右键 = 上下文菜单（按下时唤出，由 App 决定）
		if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonSecondary) {
			if m.OnContextMenu != nil {
				wx, wy := m.screenToWorld(pe.Position, size)
				m.OnContextMenu(pe.Position.X, pe.Position.Y, wx, wy)
			}
			continue
		}

		// 左键单击拦截器（路径节点编辑等）：仅在 Press 且只含 Primary 时尝试。
		// 命中拦截器后跳过工具分发；后续的 Drag / Release 由工具正常处理也无副作用，
		// 因为编辑流程是"一次点击即完成"的。
		if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonPrimary) && m.OnLeftClickWorld != nil {
			wx, wy := m.screenToWorld(pe.Position, size)
			if m.OnLeftClickWorld(wx, wy) {
				continue
			}
		}

		// 左键：分发到工具
		tool := ToolPan
		if m.Editor != nil {
			tool = m.Editor.Tool()
		}
		switch tool {
		case ToolPan:
			m.handleSelectEvent(pe, size)
		case ToolPlace:
			m.handlePlaceEvent(pe, size)
		case ToolFreedraw:
			m.handleFreedrawEvent(pe, size)
		case ToolChain:
			m.handleChainEvent(pe, size)
		}
		if pe.Kind == pointer.Drag {
			m.updateMouseWorld(pe.Position, size)
		}
	}
}

// handleSelectEvent 默认工具：单击 = 切换该 wiki 点选中；拖拽 = 框选。
//
// 单击 vs 拖拽的判定：left 按下记录 leftStart；移动超过阈值（4px）算拖拽，
// 此时启动 marquee；松开时如果未拖动（leftMoved==false）则按单击处理：
// 命中半径内的 wiki 点 → 切换其选中状态（Shift 追加 / Ctrl 减去 / 默认替换为单点）。
func (m *MapView) handleSelectEvent(pe pointer.Event, size image.Point) {
	if m.Editor == nil {
		return
	}
	const dragThreshold = 4
	switch pe.Kind {
	case pointer.Press:
		if !pe.Buttons.Contain(pointer.ButtonPrimary) {
			return
		}
		m.leftDown = true
		m.leftStart = pe.Position
		m.leftMoved = false
		mod := 0
		if pe.Modifiers.Contain(key.ModShift) {
			mod = 1
		} else if pe.Modifiers.Contain(key.ModCtrl) {
			mod = 2
		}
		m.leftMod = mod
	case pointer.Drag:
		if !m.leftDown {
			return
		}
		dx := pe.Position.X - m.leftStart.X
		dy := pe.Position.Y - m.leftStart.Y
		if !m.leftMoved && (dx*dx+dy*dy) > dragThreshold*dragThreshold {
			m.leftMoved = true
			m.Editor.BeginMarquee(m.leftStart.X, m.leftStart.Y, m.leftMod)
		}
		if m.leftMoved {
			m.Editor.UpdateMarquee(pe.Position.X, pe.Position.Y)
		}
	case pointer.Release:
		if !m.leftDown {
			return
		}
		if m.leftMoved {
			active, x0, y0, x1, y1 := m.Editor.MarqueeRect()
			if active {
				wx0, wy0 := m.screenToWorld(f32.Pt(x0, y0), size)
				wx1, wy1 := m.screenToWorld(f32.Pt(x1, y1), size)
				hits := m.Store.PointInWorldRect(m.visibleTypes, wx0, wy0, wx1, wy1)
				applied := make([]SelectionItem, 0, len(hits))
				for _, h := range hits {
					applied = append(applied, SelectionItem{
						Source: "wiki", PointID: h.ID, MarkType: h.MarkType,
						X: h.X, Y: h.Y, Layer: h.Layer,
					})
				}
				m.Editor.EndMarquee(applied)
			}
		} else {
			// 单击：尝试切换选中
			wx, wy := m.screenToWorld(pe.Position, size)
			r := m.screenRadiusToWorld(12)
			if hp, ok := m.Store.NearestVisibleWiki(wx, wy, r*r, m.visibleTypes); ok {
				it := SelectionItem{Source: "wiki", PointID: hp.ID, MarkType: hp.MarkType, X: hp.X, Y: hp.Y, Layer: hp.Layer}
				switch m.leftMod {
				case 0: // replace
					m.Editor.ClearSelection()
					m.Editor.AddWikiToSelection(hp.MarkType, hp.ID, hp.X, hp.Y, hp.Layer)
				case 1: // add → 已选则去除
					if m.Editor.IsSelectedAt(hp.X, hp.Y, r*r) {
						m.Editor.RemoveSelectionAt(hp.X, hp.Y, r*r)
					} else {
						m.Editor.AddWikiToSelection(hp.MarkType, hp.ID, hp.X, hp.Y, hp.Layer)
					}
				case 2:
					m.Editor.RemoveSelectionAt(hp.X, hp.Y, r*r)
				}
				_ = it
			} else if m.leftMod == 0 {
				// 单击空白且无修饰：清空选区
				m.Editor.ClearSelection()
			}
		}
		m.leftDown = false
		m.leftMoved = false
	case pointer.Cancel:
		if m.leftMoved {
			m.Editor.EndMarquee(nil)
		}
		m.leftDown = false
		m.leftMoved = false
	}
}

func (m *MapView) handlePlaceEvent(pe pointer.Event, size image.Point) {
	if m.Editor == nil {
		return
	}
	if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonPrimary) {
		wx, wy := m.screenToWorld(pe.Position, size)
		// 加点工具：始终创建一个新的用户路径点，不与已有的 wiki 点合并。
		// 由 UI 决定是否随后弹出备注编辑。
		m.Editor.PlaceUserWaypoint(wx, wy, m.layerName)
	}
}

func (m *MapView) handleFreedrawEvent(pe pointer.Event, size image.Point) {
	if m.Editor == nil {
		return
	}
	switch pe.Kind {
	case pointer.Press:
		if pe.Buttons.Contain(pointer.ButtonPrimary) {
			wx, wy := m.screenToWorld(pe.Position, size)
			m.Editor.BeginFreedraw(wx, wy, m.layerName)
		}
	case pointer.Drag:
		wx, wy := m.screenToWorld(pe.Position, size)
		// 至少 4 px 一采样
		minStep := m.screenRadiusToWorld(4)
		m.Editor.AppendFreedraw(wx, wy, m.layerName, minStep)
	case pointer.Release:
		m.Editor.EndFreedraw()
	case pointer.Cancel:
		m.Editor.EndFreedraw()
	}
}

func (m *MapView) handleChainEvent(pe pointer.Event, size image.Point) {
	if m.Editor == nil {
		return
	}
	if pe.Kind == pointer.Press && pe.Buttons.Contain(pointer.ButtonPrimary) {
		wx, wy := m.screenToWorld(pe.Position, size)
		r := m.screenRadiusToWorld(12)
		if hp, ok := m.Store.NearestVisibleWiki(wx, wy, r*r, m.visibleTypes); ok {
			m.Editor.ChainClick(hp.X, hp.Y, hp.Layer, hp.ID, hp.MarkType)
		} else {
			m.Editor.ChainClick(wx, wy, m.layerName, "", "")
		}
	}
}

// screenToWorld 把屏幕像素坐标转世界坐标。size 是当前 mapview slot。
func (m *MapView) screenToWorld(pos f32.Point, size image.Point) (float64, float64) {
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	px := cpx + float64(pos.X) - float64(size.X)/2
	py := cpy + float64(pos.Y) - float64(size.Y)/2
	return mapdata.PixelToWorld(px, py, m.z)
}

// worldToScreen 将世界坐标转 mapview 局部屏幕像素。
func (m *MapView) worldToScreen(wx, wy float64, size image.Point) (int, int) {
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	px, py := mapdata.WorldToPixel(wx, wy, m.z)
	return int(px - cpx + float64(size.X)/2), int(py - cpy + float64(size.Y)/2)
}

// screenRadiusToWorld 把以像素为单位的半径换算到世界坐标系。
func (m *MapView) screenRadiusToWorld(pixels float64) float64 {
	scale := mapdata.BaseScale * math.Pow(2, float64(m.z))
	return pixels / scale
}

func (m *MapView) updateMouseWorld(pos f32.Point, size image.Point) {
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	px := cpx + float64(pos.X) - float64(size.X)/2
	py := cpy + float64(pos.Y) - float64(size.Y)/2
	m.mouseWorldX, m.mouseWorldY = mapdata.PixelToWorld(px, py, m.z)
}

func (m *MapView) handleScroll(dy float32, pos f32.Point, size image.Point) {
	meta := m.Store.Meta()
	if meta == nil {
		return
	}
	delta := -1
	if dy < 0 {
		delta = 1
	}
	newZ := m.z + delta
	if newZ < meta.MinZoom || newZ > meta.MaxZoom {
		return
	}
	// 以鼠标处为锚点缩放：鼠标对应的世界坐标在缩放前后保持不变。
	cpx, cpy := mapdata.WorldToPixel(m.centerX, m.centerY, m.z)
	mx := cpx + float64(pos.X) - float64(size.X)/2
	my := cpy + float64(pos.Y) - float64(size.Y)/2
	mwx, mwy := mapdata.PixelToWorld(mx, my, m.z)

	m.z = newZ
	// 在新 z 下，使鼠标处仍指向相同世界坐标：
	// 即新 mouse_pixel = WorldToPixel(mwx,mwy,newZ)，且
	// 新 center_pixel = mouse_pixel - (pos - size/2)
	mx2, my2 := mapdata.WorldToPixel(mwx, mwy, m.z)
	cx2 := mx2 - (float64(pos.X) - float64(size.X)/2)
	cy2 := my2 - (float64(pos.Y) - float64(size.Y)/2)
	m.centerX, m.centerY = mapdata.PixelToWorld(cx2, cy2, m.z)
}

// 提供给状态栏/调试用的便捷格式
func (m *MapView) StatusLine() string {
	return fmtStatus(m.layerName, m.z, m.mouseWorldX, m.mouseWorldY)
}

func fmtStatus(layer string, z int, x, y float64) string {
	return "图层 " + layer +
		"  缩放 " + itoa(z) +
		"  X=" + ftoa(x, 1) +
		"  Y=" + ftoa(y, 1)
}

// 局部数字格式化
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(v float64, prec int) string {
	// 极简实现
	neg := v < 0
	if neg {
		v = -v
	}
	intPart := int64(v)
	frac := v - float64(intPart)
	mul := 1.0
	for i := 0; i < prec; i++ {
		mul *= 10
	}
	fracInt := int64(frac*mul + 0.5)
	s := itoa64(intPart)
	if prec > 0 {
		fs := itoa64(fracInt)
		for len(fs) < prec {
			fs = "0" + fs
		}
		s += "." + fs
	}
	if neg {
		s = "-" + s
	}
	return s
}
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// silence unused
var _ = clip.Rect{}

// CenterOn 让相机中心移动到给定世界坐标。
func (m *MapView) CenterOn(wx, wy float64) {
	m.centerX = wx
	m.centerY = wy
}

// ViewCenter 返回当前相机中心的世界坐标。
func (m *MapView) ViewCenter() (wx, wy float64) {
	return m.centerX, m.centerY
}

// drawPathOverlay 在点位之上绘制：
//   - 已保存的可见路径（来自 RouteStore）
//   - 当前草稿路径（如有）
//   - 进行中的自由绘制笔触
//   - 选区高亮（彩色环 + 起点 / 终点徽标）
//   - 框选矩形
//   - 导航图层（活动路径着色 + 玩家标记 + 朝向）
func (m *MapView) drawPathOverlay(gtx layout.Context, size image.Point) {
	// 导航活动路径 ID（用于 drawSavedPath 跳过它，由专门的导航绘制接管）
	navActive := false
	var navNodes []mapdata.PathNode
	var navPlayerX, navPlayerY, navHeading float64
	navHasFix := false
	var navProg NavProgress
	var navVelX, navVelY float64
	var navLastAt time.Time
	navPredict := false
	if m.NavStateFn != nil {
		navActive, navNodes, navPlayerX, navPlayerY, navHeading, navHasFix, navProg, navVelX, navVelY, navLastAt, navPredict = m.NavStateFn()
	}
	// 预测外推：基于速度 + 距上次 fix 的时间。限制最大外推 1.5 秒，避免失联后
	// 玩家一直被"推"到很远。|velocity| 很小时（< 2 世界单位/秒，几乎静止）跳过。
	if navActive && navHasFix && navPredict && !navLastAt.IsZero() {
		speed2 := navVelX*navVelX + navVelY*navVelY
		if speed2 > 4 {
			dt := time.Since(navLastAt).Seconds()
			if dt > 0 {
				if dt > 1.5 {
					dt = 1.5
				}
				navPlayerX += navVelX * dt
				navPlayerY += navVelY * dt
			}
		}
	}

	// 已保存路径（导航中的那条由 drawNavRoute 接管）
	if m.Routes != nil {
		idx := 0
		for _, p := range m.Routes.All() {
			if !p.Visible {
				continue
			}
			if navActive && len(navNodes) > 0 && samePathNodes(p.Nodes, navNodes) {
				idx++
				continue
			}
			m.drawSavedPath(gtx, size, p, idx)
			idx++
		}
	}

	// 草稿路径
	if m.Editor != nil {
		if d := m.Editor.Draft(); d != nil && len(d.Nodes) > 0 {
			col := resolvePathColor(d)
			m.drawPathLine(gtx, size, d.Nodes, col, !d.Freeform)
		}
		// 进行中的 freedraw 实时笔触
		if pts := m.Editor.FreedrawPoints(); len(pts) >= 2 {
			m.drawPathLine(gtx, size, pts, color.NRGBA{R: 0xf2, G: 0xb0, B: 0x4f, A: 0xff}, false)
		}

		// 选区高亮
		m.drawSelectionRings(gtx, size)

		// 框选矩形
		if active, x0, y0, x1, y1 := m.Editor.MarqueeRect(); active {
			rect := image.Rect(int(x0), int(y0), int(x1), int(y1))
			fill := color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e, A: 0x33}
			edge := color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e, A: 0xff}
			paint.FillShape(gtx.Ops, fill, clip.Rect(rect).Op())
			drawRectBorder(gtx.Ops, rect, 1, edge)
		}
	}

	// 导航图层（最上层）
	if navActive {
		// 仅当有路径节点时才画 nav 路线
		if len(navNodes) >= 2 {
			m.drawNavRoute(gtx, size, navNodes, navProg, navHasFix)
		}
		// 玩家标记 + 朝向：路径导航 / 纯追踪 都需要
		if navHasFix {
			// 平滑插值：每帧把显示位置/朝向向最新 fix 衰减靠拢，
			// 让低频的 NCC 采样在画面上呈现平顺移动。
			if !m.smoothInit {
				m.smoothPX = navPlayerX
				m.smoothPY = navPlayerY
				m.smoothHeading = navHeading
				m.smoothInit = true
			} else {
				const alpha = 0.18 // 越小越平滑（每帧靠近 18%）
				m.smoothPX += (navPlayerX - m.smoothPX) * alpha
				m.smoothPY += (navPlayerY - m.smoothPY) * alpha
				// 朝向走最近角度
				dh := navHeading - m.smoothHeading
				for dh > math.Pi {
					dh -= 2 * math.Pi
				}
				for dh < -math.Pi {
					dh += 2 * math.Pi
				}
				m.smoothHeading += dh * alpha
			}
			sx, sy := m.worldToScreen(m.smoothPX, m.smoothPY, size)
			m.drawPlayerMarker(gtx, image.Pt(sx, sy), m.smoothHeading)
			// 自动居中：把相机也平滑拉向玩家
			if m.AutoCenterFn != nil && m.AutoCenterFn() {
				const camAlpha = 0.12
				m.centerX += (m.smoothPX - m.centerX) * camAlpha
				m.centerY += (m.smoothPY - m.centerY) * camAlpha
			}
		} else {
			m.smoothInit = false
		}
		// 持续刷新（流动箭头动画 + 平滑插值 + 等待定位）
		gtx.Execute(op.InvalidateCmd{})
	}
}

// samePathNodes 判断两个节点序列是否同一对象（按指针长度），避免重复绘制。
func samePathNodes(a, b []mapdata.PathNode) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// drawNavRoute 渲染导航活动路径：
//   - 先在内部节点处画 "joint" 圆（在路径直线下层，仅用于把折线尖角填平为圆滑过渡）
//   - 然后画段；段会覆盖 joint 中心，仅在折角处的角度缝隙里露出 joint 边缘
//   - 视口外段直接跳过，不参与绘制 / 动画
func (m *MapView) drawNavRoute(gtx layout.Context, size image.Point, nodes []mapdata.PathNode, prog NavProgress, hasFix bool) {
	traveledOuter := color.NRGBA{R: 0x35, G: 0x35, B: 0x3a, A: 0xff} // 深灰描边
	traveledInner := color.NRGBA{R: 0x9a, G: 0x9a, B: 0xa2, A: 0xff} // 浅灰主色
	remainingOuter := color.NRGBA{R: 0x18, G: 0x46, B: 0x22, A: 0xff} // 深绿描边
	remainingInner := color.NRGBA{R: 0x3c, G: 0xd2, B: 0x62, A: 0xff} // 亮绿主色
	arrowCol := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}        // 内嵌白色箭头

	const (
		coreW    = 9  // 主线宽度
		outlineW = 13 // 描边宽度（coreW + 4）
	)

	// 视口剔除判定（额外 pad 防止边缘段误剔）
	pad := outlineW + 4
	inView := func(x, y int) bool {
		return x >= -pad && y >= -pad && x <= size.X+pad && y <= size.Y+pad
	}
	segVisible := func(ax, ay, bx, by int) bool {
		// 两端任一在视口内 → 可见；否则若两端在同一外侧 → 整段不可见。
		if inView(ax, ay) || inView(bx, by) {
			return true
		}
		if (ax < -pad && bx < -pad) || (ax > size.X+pad && bx > size.X+pad) ||
			(ay < -pad && by < -pad) || (ay > size.Y+pad && by > size.Y+pad) {
			return false
		}
		return true
	}

	// 1. 预计算每段的可见性 + traveled / split 状态
	segCount := len(nodes) - 1
	type segMeta struct {
		ax, ay, bx, by int
		traveled       bool // 整段已走
		split          bool // 当前段：被玩家投影点切成两段
		pjx, pjy       int  // split=true 时的玩家投影屏幕坐标
		visible        bool
	}
	segs := make([]segMeta, segCount)
	for i := 0; i < segCount; i++ {
		ax, ay := m.worldToScreen(nodes[i].X, nodes[i].Y, size)
		bx, by := m.worldToScreen(nodes[i+1].X, nodes[i+1].Y, size)
		s := segMeta{ax: ax, ay: ay, bx: bx, by: by, visible: segVisible(ax, ay, bx, by)}
		switch {
		case hasFix && i < prog.SegmentIndex:
			s.traveled = true
		case hasFix && i == prog.SegmentIndex:
			s.split = true
			s.pjx, s.pjy = m.worldToScreen(prog.ProjX, prog.ProjY, size)
		}
		segs[i] = s
	}

	// 2. 节点 joints —— **先画**（位于段直线下层）。在每个内部节点画两层圆
	//    （外深色 + 内主色），把相邻段的折线尖角填成圆滑过渡，避免视觉断裂。
	//    后续段会覆盖 joint 中心，只在锐角处的缝隙里露出 joint 的边缘。
	jointR := outlineW / 2
	coreR := coreW / 2
	drawJoint := func(c image.Point, traveled bool) {
		var outer, inner color.NRGBA
		if traveled {
			outer, inner = traveledOuter, traveledInner
		} else {
			outer, inner = remainingOuter, remainingInner
		}
		paint.FillShape(gtx.Ops, outer,
			clip.Ellipse(image.Rect(c.X-jointR, c.Y-jointR, c.X+jointR, c.Y+jointR)).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, inner,
			clip.Ellipse(image.Rect(c.X-coreR, c.Y-coreR, c.X+coreR, c.Y+coreR)).Op(gtx.Ops))
	}
	// 端点 + 内部节点（端点也画，避免段端在直线起点 / 终点也是平头切角）
	for i := 0; i < len(nodes); i++ {
		sx, sy := m.worldToScreen(nodes[i].X, nodes[i].Y, size)
		if !inView(sx, sy) {
			continue
		}
		traveled := false
		if hasFix && i <= prog.SegmentIndex && i > 0 {
			// 节点 i 之前的段全部走过 → traveled
			traveled = i <= prog.SegmentIndex
			if hasFix && i-1 == prog.SegmentIndex && segs[i-1].split {
				// split 段的末端（节点 i）尚未走过 → 仍属于未走色
				traveled = false
			}
		}
		drawJoint(image.Pt(sx, sy), traveled)
	}
	// 玩家投影点（split 段中点）也画一个 joint，让灰→绿过渡平滑
	if hasFix && prog.SegmentIndex >= 0 && prog.SegmentIndex < segCount {
		s := segs[prog.SegmentIndex]
		if s.split && inView(s.pjx, s.pjy) {
			drawJoint(image.Pt(s.pjx, s.pjy), false)
		}
	}

	// 3. 段绘制（双层：先深色描边，再亮色芯，突出 "管道感"）
	drawSeg := func(ax, ay, bx, by int, traveled bool) {
		if traveled {
			drawLine(gtx.Ops, image.Pt(ax, ay), image.Pt(bx, by), outlineW, traveledOuter)
			drawLine(gtx.Ops, image.Pt(ax, ay), image.Pt(bx, by), coreW, traveledInner)
		} else {
			drawLine(gtx.Ops, image.Pt(ax, ay), image.Pt(bx, by), outlineW, remainingOuter)
			drawLine(gtx.Ops, image.Pt(ax, ay), image.Pt(bx, by), coreW, remainingInner)
		}
	}
	for i := 0; i < segCount; i++ {
		s := segs[i]
		if !s.visible {
			continue
		}
		if s.split {
			drawSeg(s.ax, s.ay, s.pjx, s.pjy, true)
			drawSeg(s.pjx, s.pjy, s.bx, s.by, false)
		} else {
			drawSeg(s.ax, s.ay, s.bx, s.by, s.traveled)
		}
	}

	// 4. 流动白色箭头：嵌在亮绿段内，仅可见段参与（最上层）
	phase := float64(time.Now().UnixMilli()%1400) / 1400.0
	for i := 0; i < segCount; i++ {
		s := segs[i]
		if !s.visible || s.traveled {
			continue
		}
		ax, ay := nodes[i].X, nodes[i].Y
		bx, by := nodes[i+1].X, nodes[i+1].Y
		if s.split {
			ax, ay = prog.ProjX, prog.ProjY
		}
		dx := bx - ax
		dy := by - ay
		segLenW := math.Sqrt(dx*dx + dy*dy)
		if segLenW < 1e-6 {
			continue
		}
		var sax, say, sbx, sby int
		if s.split {
			sax, say = s.pjx, s.pjy
		} else {
			sax, say = s.ax, s.ay
		}
		sbx, sby = s.bx, s.by
		segLenS := math.Sqrt(float64((sbx-sax)*(sbx-sax) + (sby-say)*(sby-say)))
		if segLenS < 24 {
			continue
		}
		// 每 50 屏幕像素一个内嵌箭头
		const step = 50.0
		count := int(segLenS / step)
		if count < 1 {
			count = 1
		}
		for k := 0; k < count; k++ {
			t := (float64(k)/float64(count) + phase/float64(count))
			if t > 1 {
				t -= 1
			}
			cx := sax + int(float64(sbx-sax)*t)
			cy := say + int(float64(sby-say)*t)
			drawArrow(gtx.Ops, image.Pt(sax, say), image.Pt(sbx, sby), image.Pt(cx, cy), 5, arrowCol)
		}
	}
	_ = arrowCol
}

// drawPlayerMarker 玩家标记：白底蓝心圆 + 朝向三角箭头。
func (m *MapView) drawPlayerMarker(gtx layout.Context, c image.Point, heading float64) {
	// 朝向三角（heading: 0=北，弧度，顺时针）
	const r = 20
	// 旋转：头朝 heading 方向。屏幕 +Y 向下，所以 angleScreen = heading - π/2
	angle := heading - math.Pi/2
	// 三角形：顶点指向 +X，底边对称；放大尺寸更醒目
	var path clip.Path
	path.Begin(gtx.Ops)
	path.MoveTo(f32.Pt(float32(r+22), 0))      // 尖端伸出更远
	path.LineTo(f32.Pt(float32(r-2), float32(14)))
	path.LineTo(f32.Pt(float32(r-2), float32(-14)))
	path.Close()
	spec := path.End()
	aff := op.Affine(f32.Affine2D{}.Rotate(f32.Pt(0, 0), float32(angle)).Offset(f32.Pt(float32(c.X), float32(c.Y)))).Push(gtx.Ops)
	// 三角白色描边（先画大白三角，再画小绿三角）
	var path2 clip.Path
	path2.Begin(gtx.Ops)
	path2.MoveTo(f32.Pt(float32(r+24), 0))
	path2.LineTo(f32.Pt(float32(r-4), float32(16)))
	path2.LineTo(f32.Pt(float32(r-4), float32(-16)))
	path2.Close()
	spec2 := path2.End()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.Outline{Path: spec2}.Op())
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0x2a, G: 0xb6, B: 0x55, A: 0xff}, clip.Outline{Path: spec}.Op())
	aff.Pop()

	// 圆形外圈 + 蓝色实心
	outer := image.Rect(c.X-r-3, c.Y-r-3, c.X+r+3, c.Y+r+3)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		clip.Ellipse(outer).Op(gtx.Ops))
	inner := image.Rect(c.X-r, c.Y-r, c.X+r, c.Y+r)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0x2b, G: 0x6f, B: 0xff, A: 0xff},
		clip.Ellipse(inner).Op(gtx.Ops))
	// 内点
	in2 := image.Rect(c.X-6, c.Y-6, c.X+6, c.Y+6)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		clip.Ellipse(in2).Op(gtx.Ops))
}

func (m *MapView) drawSavedPath(gtx layout.Context, size image.Point, p *mapdata.Path, _ int) {
	if len(p.Nodes) < 2 {
		return
	}
	col := resolvePathColor(p)
	m.drawPathLine(gtx, size, p.Nodes, col, !p.Freeform)
}

// drawPathLine 把世界坐标节点序列画成屏幕上的折线，可选画方向箭头 + 节点编号。
// 当 numbered==true 时，每个节点画带数字的小圆；适合 TSP / 手动连接的离散路径，
// freeform 则只画线。
//
// 性能优化：
//   - 始终做视口剔除：完全在屏幕外的段 / 节点 / 箭头不参与绘制
//   - 短段（屏幕长度 < 12 像素）跳过箭头与编号
func (m *MapView) drawPathLine(gtx layout.Context, size image.Point, nodes []mapdata.PathNode, col color.NRGBA, numbered bool) {
	if len(nodes) < 2 {
		if len(nodes) == 1 {
			sx, sy := m.worldToScreen(nodes[0].X, nodes[0].Y, size)
			drawDot(gtx.Ops, image.Pt(sx, sy), 5, col)
		}
		return
	}
	// 预转换屏幕坐标，复用避免重复计算
	type pt struct{ x, y int }
	scr := make([]pt, len(nodes))
	for i, n := range nodes {
		x, y := m.worldToScreen(n.X, n.Y, size)
		scr[i] = pt{x, y}
	}
	pad := 64
	inView := func(x, y int) bool {
		return x >= -pad && y >= -pad && x <= size.X+pad && y <= size.Y+pad
	}
	segVisible := func(a, b pt) bool {
		if inView(a.x, a.y) || inView(b.x, b.y) {
			return true
		}
		// 两端在同一外侧 → 整段不可见
		if (a.x < -pad && b.x < -pad) || (a.x > size.X+pad && b.x > size.X+pad) ||
			(a.y < -pad && b.y < -pad) || (a.y > size.Y+pad && b.y > size.Y+pad) {
			return false
		}
		return true
	}
	// 折线段
	for i := 1; i < len(nodes); i++ {
		a, b := scr[i-1], scr[i]
		if !segVisible(a, b) {
			continue
		}
		drawLine(gtx.Ops, image.Pt(a.x, a.y), image.Pt(b.x, b.y), 3, col)
		if numbered {
			dxs := b.x - a.x
			dys := b.y - a.y
			if dxs*dxs+dys*dys >= 12*12 {
				mx := (a.x + b.x) / 2
				my := (a.y + b.y) / 2
				drawArrow(gtx.Ops, image.Pt(a.x, a.y), image.Pt(b.x, b.y), image.Pt(mx, my), 7, col)
			}
		}
	}
	// 节点编号
	if numbered {
		for i, p := range scr {
			if !inView(p.x, p.y) {
				continue
			}
			m.drawNumberedNode(gtx, image.Pt(p.x, p.y), i+1, col)
		}
	}
}

func (m *MapView) drawSelectionRings(gtx layout.Context, size image.Point) {
	selStyle := SelectionHalo
	if m.SelectionStyleFn != nil {
		selStyle = m.SelectionStyleFn()
	}
	for _, s := range m.Editor.Selection() {
		sx, sy := m.worldToScreen(s.X, s.Y, size)
		if sx < -32 || sy < -32 || sx > size.X+32 || sy > size.Y+32 {
			continue
		}
		col := color.NRGBA{R: 0xff, G: 0xc8, B: 0x3d, A: 0xff}
		switch s.Role {
		case RoleStart:
			col = color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e, A: 0xff}
		case RoleEnd:
			col = color.NRGBA{R: 0xe2, G: 0x6a, B: 0x6a, A: 0xff}
		}
		// user 路径点用菱形（独立于 wiki 图标），始终绘制。
		if s.Source == "user" {
			drawDiamond(gtx.Ops, image.Pt(sx, sy), 7, col)
		} else if selStyle == SelectionDot {
			// 旧版样式：在 wiki 图标上叠加白色圆环。
			drawWhiteRing(gtx.Ops, image.Pt(sx, sy), 14, 2, col)
		}
		// 角色徽章："S" / "E"
		if s.Role != RoleNone {
			label := "S"
			if s.Role == RoleEnd {
				label = "E"
			}
			m.drawBadge(gtx, image.Pt(sx+10, sy-16), label, col)
		}
	}
}

// drawLine 用 Affine 旋转的填充矩形画粗线，避免 clip.Stroke 在某些 GPU 后端的问题。
func drawLine(ops *op.Ops, p1, p2 image.Point, width int, c color.NRGBA) {
	dx := float64(p2.X - p1.X)
	dy := float64(p2.Y - p1.Y)
	length := math.Sqrt(dx*dx + dy*dy)
	if length < 1 {
		return
	}
	angle := math.Atan2(dy, dx)
	rect := image.Rect(0, -width/2, int(length+0.5), width-width/2)
	aff := op.Affine(f32.Affine2D{}.Rotate(f32.Pt(0, 0), float32(angle)).Offset(f32.Pt(float32(p1.X), float32(p1.Y)))).Push(ops)
	paint.FillShape(ops, c, clip.Rect(rect).Op())
	aff.Pop()
}

// drawArrow 以 (a→b) 为方向画一个填充三角形箭头，质心在 c 处。
func drawArrow(ops *op.Ops, a, b, c image.Point, sz int, col color.NRGBA) {
	dx := float64(b.X - a.X)
	dy := float64(b.Y - a.Y)
	length := math.Sqrt(dx*dx + dy*dy)
	if length < 1 {
		return
	}
	angle := math.Atan2(dy, dx)
	// 局部三角形：(0,0) 顶点指向 +X，底边 (-sz, ±sz/1.5)
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(float32(sz), 0))
	path.LineTo(f32.Pt(-float32(sz)/2, float32(sz)/1.5))
	path.LineTo(f32.Pt(-float32(sz)/2, -float32(sz)/1.5))
	path.Close()
	spec := path.End()
	aff := op.Affine(f32.Affine2D{}.Rotate(f32.Pt(0, 0), float32(angle)).Offset(f32.Pt(float32(c.X), float32(c.Y)))).Push(ops)
	paint.FillShape(ops, col, clip.Outline{Path: spec}.Op())
	aff.Pop()
}

// drawNumberedNode 节点编号小圆 + 数字。
// 圆半径与盒尺寸根据数字位数自适应，避免 ≥3 位数字被切掉。
func (m *MapView) drawNumberedNode(gtx layout.Context, c image.Point, n int, col color.NRGBA) {
	digits := 1
	for nn := n; nn >= 10; nn /= 10 {
		digits++
	}
	// 半径根据位数：1 位=9，2 位=10，3 位=12，4+ 位=14
	r := 9
	switch digits {
	case 2:
		r = 10
	case 3:
		r = 12
	default:
		if digits >= 4 {
			r = 14
		}
	}
	outer := image.Rect(c.X-r-1, c.Y-r-1, c.X+r+1, c.Y+r+1)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.Ellipse(outer).Op(gtx.Ops))
	inner := image.Rect(c.X-r, c.Y-r, c.X+r, c.Y+r)
	paint.FillShape(gtx.Ops, col, clip.Ellipse(inner).Op(gtx.Ops))
	// 悬浮窗跳过编号文字。
	if m.overlayMode {
		return
	}
	// 文本盒：宽=2r+2，居中。由于 material.Label 的 Middle 对齐需要约束
	// Min.X == Max.X 才能在完整盒宽内居中（否则会按文本自然宽度摆放，表现为偏左）。
	boxW := 2*r + 4
	boxH := 2*r + 2
	textOffX := c.X - boxW/2
	textOffY := c.Y - 9
	stack := op.Offset(image.Pt(textOffX, textOffY)).Push(gtx.Ops)
	gtx2 := gtx
	gtx2.Constraints = layout.Constraints{
		Min: image.Pt(boxW, 0),
		Max: image.Pt(boxW, boxH),
	}
	lbl := material.Label(m.Theme.Material, unit.Sp(11), itoa(n))
	lbl.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	lbl.MaxLines = 1
	lbl.Alignment = text.Middle
	lbl.Layout(gtx2)
	stack.Pop()
}

// drawBadge 在 (c) 位置绘制一个带文字的小圆牌（用于 S / E 标识）。
func (m *MapView) drawBadge(gtx layout.Context, c image.Point, label string, col color.NRGBA) {
	r := 8
	outer := image.Rect(c.X-r-1, c.Y-r-1, c.X+r+1, c.Y+r+1)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.Ellipse(outer).Op(gtx.Ops))
	inner := image.Rect(c.X-r, c.Y-r, c.X+r, c.Y+r)
	paint.FillShape(gtx.Ops, col, clip.Ellipse(inner).Op(gtx.Ops))
	// 悬浮窗跳过 S / E 文字。
	if m.overlayMode {
		return
	}
	boxW := 16
	boxH := 16
	stack := op.Offset(image.Pt(c.X-boxW/2, c.Y-8)).Push(gtx.Ops)
	gtx2 := gtx
	gtx2.Constraints = layout.Constraints{
		Min: image.Pt(boxW, 0),
		Max: image.Pt(boxW, boxH),
	}
	lbl := material.Label(m.Theme.Material, unit.Sp(10), label)
	lbl.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	lbl.MaxLines = 1
	lbl.Alignment = text.Middle
	lbl.Layout(gtx2)
	stack.Pop()
}

// drawWhiteRing 一个真正的中空圆环（通过 clip.Stroke 实现），用于 SelectionDot 模式。
func drawWhiteRing(ops *op.Ops, c image.Point, r, w int, col color.NRGBA) {
	rf := float32(r)
	cx, cy := float32(c.X), float32(c.Y)
	const kappa = 0.5522847498
	k := kappa * rf
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(cx+rf, cy))
	path.CubeTo(f32.Pt(cx+rf, cy+k), f32.Pt(cx+k, cy+rf), f32.Pt(cx, cy+rf))
	path.CubeTo(f32.Pt(cx-k, cy+rf), f32.Pt(cx-rf, cy+k), f32.Pt(cx-rf, cy))
	path.CubeTo(f32.Pt(cx-rf, cy-k), f32.Pt(cx-k, cy-rf), f32.Pt(cx, cy-rf))
	path.CubeTo(f32.Pt(cx+k, cy-rf), f32.Pt(cx+rf, cy-k), f32.Pt(cx+rf, cy))
	spec := path.End()
	paint.FillShape(ops, col, clip.Stroke{Path: spec, Width: float32(w)}.Op())
}

// drawRing 兼容旧调用 —— 已不再使用，保留空实现避免编译错误。
func drawRing(ops *op.Ops, c image.Point, r, w int, col color.NRGBA) {
	drawWhiteRing(ops, c, r, w, col)
}

// drawDiamond 简单的菱形选中标记（user 路径点）。
func drawDiamond(ops *op.Ops, c image.Point, r int, col color.NRGBA) {
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(float32(c.X), float32(c.Y-r)))
	path.LineTo(f32.Pt(float32(c.X+r), float32(c.Y)))
	path.LineTo(f32.Pt(float32(c.X), float32(c.Y+r)))
	path.LineTo(f32.Pt(float32(c.X-r), float32(c.Y)))
	path.Close()
	paint.FillShape(ops, col, clip.Outline{Path: path.End()}.Op())
}

// drawRectBorder 画矩形边框（4 个细长矩形）。
func drawRectBorder(ops *op.Ops, r image.Rectangle, w int, col color.NRGBA) {
	if w <= 0 {
		w = 1
	}
	paint.FillShape(ops, col, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w)).Op())
	paint.FillShape(ops, col, clip.Rect(image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y)).Op())
	paint.FillShape(ops, col, clip.Rect(image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y)).Op())
	paint.FillShape(ops, col, clip.Rect(image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y)).Op())
}
