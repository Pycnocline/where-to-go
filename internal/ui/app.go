package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/iconcache"
	"github.com/where-to-go/app/internal/locator"
	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/tilecache"
	"github.com/where-to-go/app/internal/wiki"
	"github.com/where-to-go/app/internal/winutil"
)

// Mode 当前界面阶段。
type Mode int

const (
	ModeLoading Mode = iota
	ModeMain
	ModeFatal
	ModeSettings
	ModeROISelect
)

// App 顶层 UI。
type App struct {
	Theme  *Theme
	Window *app.Window

	mode         Mode
	loaderState  *LoaderState
	loaderView   *LoaderView
	settingsView *SettingsView
	roiView      *ROISelectView
	mapView      *MapView
	store        *mapdata.Store
	cache        *tilecache.Cache
	icons        *iconcache.Cache

	// 用户配置
	settings *SettingsStore

	// 路径相关
	editor      *PathEditor
	routes      *RouteStore
	customs     *CustomStore
	dataDir     string

	// 导航状态
	nav navState

	// 侧边栏控件
	layerBtns       map[string]*widget.Clickable
	typeChks        map[string]*widget.Bool
	scrollCats      widget.List
	settingsBtn     widget.Clickable
	overlayBtn      widget.Clickable

	// 工具栏控件
	tbPan       widget.Clickable
	tbFreedraw  widget.Clickable
	tbChain     widget.Clickable
	tbSolveTSP  widget.Clickable
	tbSaveDraft widget.Clickable
	tbClearSel  widget.Clickable
	tbClearDraft widget.Clickable
	tbFinishChain widget.Clickable
	tbUndo      widget.Clickable
	tbImport    widget.Clickable
	tbExport    widget.Clickable

	// 保存命名条
	saveNaming   bool
	saveNameEd   widget.Editor
	saveNameOK   widget.Clickable
	saveNameCancel widget.Clickable

	// 自定义点位标题编辑条（"在此添加点位" / "重命名" 时弹出）
	noteEditing     bool
	noteCustomID    string // 正在编辑的 user_custom 点位 ID
	noteEd          widget.Editor
	noteOK          widget.Clickable
	noteCancel      widget.Clickable
	noteFocusPending bool // 下一帧应当为 noteEd 抢焦点（菜单回调里没有 gtx）

	// 上下文菜单 + 侧边栏分组 / 路径列表
	ctxMenu      ContextMenuState
	pathList     *PathListView
	categoryList *CategoryGroupsView
	colorPicker  *ColorPickerView

	// 侧边栏折叠
	sidebarCollapsed bool
	sidebarToggle    widget.Clickable

	// 悬浮窗
	overlay *OverlayWindow

	// 上次右键命中的世界坐标（用于上下文菜单内的按钮）
	ctxWorldX float64
	ctxWorldY float64

	// pathEdit 正在进行中的 "编辑现有路径节点" 操作（见 path_edit.go）。
	// 由 MapView 在左键时读取以把新位置应用到目标节点。
	pathEdit pathEditOp

	// 当前帧 mapview 在根容器中的偏移（像素），用于 ContextMenu 定位。
	mapViewOffX int
	mapViewOffY int

	// 状态栏附加消息（如 TSP 求解结果），临时显示
	hint string

	// 抓取流程参数
	fetcher       *wiki.Fetcher
	cacheRoot     string
	skipFetch     bool
	resetCache    bool
	fetchCtx      context.Context
	fetchCancel   context.CancelFunc

	// 致命错误
	fatalErr error
}

// Config 启动 UI 所需的参数（由 main 注入）。
type Config struct {
	CacheRoot  string
	Fetcher    *wiki.Fetcher
	SkipFetch  bool
	ResetCache bool
}

// New 创建 UI 应用对象。
func New(t *Theme, cfg Config) (*App, error) {
	store := mapdata.NewStore()
	cache := tilecache.NewCache(cfg.CacheRoot, cfg.Fetcher)
	icons, err := iconcache.New(cfg.CacheRoot)
	if err != nil {
		return nil, err
	}

	// 配置文件放在 cacheRoot 的父目录下（即 AppRoot 根），
	// 方便用户直接看到和编辑：{AppRoot}/config.json
	appRoot := filepath.Dir(cfg.CacheRoot)
	cfgPath := filepath.Join(appRoot, "config.json")
	settings := NewSettingsStore(cfgPath)

	dataDir := filepath.Join(appRoot, "data")
	routesDir := filepath.Join(dataDir, "routes")
	routes, err := NewRouteStore(routesDir)
	if err != nil {
		return nil, err
	}

	a := &App{
		Theme:        t,
		store:        store,
		cache:        cache,
		icons:        icons,
		settings:     settings,
		routes:       routes,
		dataDir:      dataDir,
		mode:         ModeLoading,
		loaderState:  NewLoaderState(),
		fetcher:      cfg.Fetcher,
		cacheRoot:    cfg.CacheRoot,
		skipFetch:    cfg.SkipFetch,
		resetCache:   cfg.ResetCache,
		layerBtns:    map[string]*widget.Clickable{},
		typeChks:     map[string]*widget.Bool{},
	}
	// editor 需要 dataLookup —— 先用一个占位，等 mapView 创建后再绑定。
	a.editor = NewPathEditor(&storeLookup{a: a}, routes)
	a.scrollCats.Axis = layout.Vertical
	a.loaderView = NewLoaderView(t, a.loaderState)
	a.settingsView = NewSettingsView(a)
	a.roiView = NewROISelectView(a)
	a.pathList = NewPathListView(a)
	a.categoryList = NewCategoryGroupsView(a)
	a.colorPicker = NewColorPickerView(a)
	a.saveNameEd.SingleLine = true
	a.saveNameEd.Submit = true
	a.noteEd.SingleLine = true
	a.noteEd.Submit = true
	return a, nil
}

// navState 导航当前状态。线程：UI 主协程读，Locator 后台 goroutine 通过 OnFix 回调写。
//
// 写入路径：locator.OnFix 在后台触发 → 直接写入 navState 字段（用 sync.Mutex 保护）→
// 调用 a.Window.Invalidate 触发主线程重绘。
type navState struct {
	mu        sync.Mutex
	active    bool      // 是否启用导航
	routeID   string    // 正在导航的路径 ID
	loc       *locator.Locator
	fix       locator.Fix // 最近一次定位
	hasFix    bool
	lost      bool   // NavFallbackLost 时显式置位
	lastErr   string // 最近一次匹配错误（用于 hint）
	progress  NavProgress
	startedAt time.Time

	// 速度估计（世界单位 / 秒）+ 上次 fix 的墙钟时间；供 MapView 做线性预测。
	velX, velY float64
	lastFixAt  time.Time

	// 后台多种子重定位的节流 / 防重入
	bgRelocateRunning bool
	bgRelocateLastAt  time.Time
}

// snapshot 拷贝一份只读副本（含锁）。
func (n *navState) snapshot() (active bool, routeID string, fix locator.Fix, hasFix bool, prog NavProgress) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active, n.routeID, n.fix, n.hasFix, n.progress
}

// snapshotWithVelocity 返回快照 + 速度 + 上次 fix 的墙钟时间（供预测用）。
func (n *navState) snapshotWithVelocity() (active bool, routeID string, fix locator.Fix, hasFix bool, prog NavProgress, velX, velY float64, lastAt time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.active, n.routeID, n.fix, n.hasFix, n.progress, n.velX, n.velY, n.lastFixAt
}

// storeLookup 让 PathEditor 能查 wiki 点位（命中半径以世界单位给出）。
type storeLookup struct{ a *App }

func (s *storeLookup) WikiPointAt(worldX, worldY, worldR2 float64) (mt, id string, x, y float64, ok bool) {
	if s.a == nil || s.a.store == nil || s.a.mapView == nil {
		return "", "", 0, 0, false
	}
	hp, found := s.a.store.NearestVisibleWiki(worldX, worldY, worldR2, s.a.mapView.visibleTypes)
	if !found {
		return "", "", 0, 0, false
	}
	return hp.MarkType, hp.ID, hp.X, hp.Y, true
}

// LockFrame / UnlockFrame 供悬浮窗协程"在渲染时避开主窗口"用。
//
// 原设计是整帧互斥锁以避免 Gio text.Shaper 的共享 map 竞争。但 *持全帧锁*
// 会在主窗口卡住十来毫秒时让中键拖拽/滚轮的事件队列滞后，用户感觉是"地图
// 突然失去响应"。
//
// 现在改成"悬浮窗渲染时完全不碰共享 Shaper"：overlayMode 下 mapView 跳过
// 所有 material.Label 文本（节点编号、气泡、角色徽章等），只保留图形。
// 因此这里改为 **no-op** —— 保留 API 兼容，后续任意调用方都不会真的阻塞
// 主窗口。
func (a *App) LockFrame()   {}
func (a *App) UnlockFrame() {}

// Run 阻塞主循环。返回时应用结束。
func (a *App) Run() error {
	a.Window = new(app.Window)
	a.Window.Option(
		app.Title("where-to-go"),
		app.Size(unit.Dp(1280), unit.Dp(800)),
	)

	// 启动后台抓取
	go a.runFetch()

	var ops op.Ops
	for {
		switch e := a.Window.Event().(type) {
		case app.DestroyEvent:
			if a.fetchCancel != nil {
				a.fetchCancel()
			}
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// runFetch 后台执行 Wiki 抓取流程，完成后切换 UI 模式。
func (a *App) runFetch() {
	a.fetchCtx, a.fetchCancel = context.WithCancel(context.Background())
	defer a.fetchCancel()

	// 1) 优先尝试本地缓存
	if !a.resetCache {
		if r, err := wiki.LoadResult(a.cacheRoot); err == nil && r != nil && r.Meta != nil {
			a.loaderState.HandleProgress(wiki.ProgressEvent{Stage: "metadata", Message: "检测到本地缓存，正在加载…"}, a.Window)
			a.store.SetData(r)
			a.initCustoms()
			a.cache.OnTileReady = func() { a.Window.Invalidate() }
			a.icons.OnReady = func() {
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
			a.mapView = NewMapView(a.Theme, a.store, a.cache, a.icons)
			a.mapView.Editor = a.editor
			a.mapView.Routes = a.routes
			a.mapView.OnContextMenu = a.openMapContextMenu
			a.mapView.OnLeftClickWorld = a.handlePathEditClick
			a.applySettingsToMapView()
			a.refreshDefaultVisibleTypes()
			a.maybePrefetchIcons()
			a.mode = ModeMain
			a.loaderState.MarkFinished(nil, a.Window)
			a.maybeAutoStartTracking()
			return
		}
	}

	if a.skipFetch {
		a.fatalErr = errSkipNoCache
		a.mode = ModeFatal
		a.loaderState.MarkFinished(errSkipNoCache, a.Window)
		return
	}

	// 2) 走完整抓取
	if a.resetCache {
		_ = os.RemoveAll(a.cacheRoot)
		_ = os.MkdirAll(a.cacheRoot, 0o755)
	}
	a.fetcher.OnProgress = func(ev wiki.ProgressEvent) {
		a.loaderState.HandleProgress(ev, a.Window)
	}
	res, err := a.fetcher.FetchAll(a.fetchCtx)
	if err != nil {
		a.fatalErr = err
		a.mode = ModeFatal
		a.loaderState.MarkFinished(err, a.Window)
		return
	}
	if err := wiki.SaveResult(a.cacheRoot, res); err != nil {
		log.Printf("保存 manifest 失败: %v", err)
	}
	a.store.SetData(res)
	a.initCustoms()
	a.cache.OnTileReady = func() { a.Window.Invalidate() }
	a.icons.OnReady = func() {
		if a.Window != nil {
			a.Window.Invalidate()
		}
	}
	a.mapView = NewMapView(a.Theme, a.store, a.cache, a.icons)
	a.mapView.Editor = a.editor
	a.mapView.Routes = a.routes
	a.mapView.OnContextMenu = a.openMapContextMenu
	a.mapView.OnLeftClickWorld = a.handlePathEditClick
	a.applySettingsToMapView()
	a.refreshDefaultVisibleTypes()
	a.maybePrefetchIcons()
	a.mode = ModeMain
	a.loaderState.MarkFinished(nil, a.Window)
	a.maybeAutoStartTracking()
}

// maybeAutoStartTracking 启动后若 settings.NavTracking=true 且 ROI 已设置，自动恢复追踪。
// 延迟 800ms 让 UI 稳定后再启动，避免启动期间崩溃。
func (a *App) maybeAutoStartTracking() {
	cfg := a.settings.Get()
	log.Printf("[autostart] check: NavTracking=%v ROI=%+v lastPlayerSet=%v lastPlayer=(%.0f,%.0f)",
		cfg.NavTracking, cfg.MinimapROI, cfg.LastPlayerSet, cfg.LastPlayerX, cfg.LastPlayerY)
	if !cfg.NavTracking {
		log.Printf("[autostart] skipped: NavTracking=false")
		return
	}
	if cfg.MinimapROI.Empty() {
		log.Printf("[autostart] skipped: MinimapROI empty (run ROI 选择 first)")
		return
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		log.Printf("[autostart] firing StartTracking")
		a.StartTracking()
	}()
}

// ToggleOverlay 打开 / 关闭悬浮窗。
func (a *App) ToggleOverlay() {
	if a.overlay == nil {
		a.overlay = NewOverlay(a)
	}
	if a.overlay.IsOpen() {
		a.overlay.Close()
	} else {
		a.overlay.Start()
		// 应用当前 settings 中的 alpha / 穿透
		cfg := a.settings.Get()
		alpha := byte(cfg.OverlayAlpha)
		if alpha == 0 {
			alpha = 0xFF
		}
		// 给 Win32 一点时间初始化 HWND 后再应用样式
		go func() {
			time.Sleep(150 * time.Millisecond)
			a.overlay.SetAlpha(alpha)
			a.overlay.SetClickThrough(cfg.OverlayClickThrough)
		}()
	}
}

// SetOverlayAlpha 直接设置悬浮窗 alpha 并保存到 settings。
func (a *App) SetOverlayAlpha(v byte) {
	a.settings.Update(func(s *Settings) { s.OverlayAlpha = int(v) })
	if a.overlay != nil && a.overlay.IsOpen() {
		a.overlay.SetAlpha(v)
	}
}

// SetOverlayClickThrough 切换鼠标穿透并保存到 settings。
func (a *App) SetOverlayClickThrough(on bool) {
	a.settings.Update(func(s *Settings) { s.OverlayClickThrough = on })
	if a.overlay != nil && a.overlay.IsOpen() {
		a.overlay.SetClickThrough(on)
	}
}

// initCustoms 在 store.SetData 之后调用，注册"自定义"类别并加载持久化的自定义点位。
func (a *App) initCustoms() {
	customsPath := filepath.Join(a.dataDir, "custom_points.json")
	cs, err := NewCustomStore(customsPath, a.store)
	if err != nil {
		log.Printf("加载自定义点位失败: %v", err)
		return
	}
	a.customs = cs
}

// StartNavigation 开始沿 routeID 导航。已有导航/追踪在跑则先停止。
//
// 与 StartTracking 使用*完全相同*的匹配管线（广域单种子 → 多种子兜底 → 常规
// loop + patch-vote），区别仅在于把 routeID + 路径节点传给 handleNavFix，以
// 及把路径节点加入多种子候选，提升对"从路径起点出发"场景的命中率。
//
// 如果此前已经在"追踪"模式下跑且有高置信度 fix，会沿用当前 fix 作为 seed，
// 跳过广域重定位 —— 用户体感上是 "点导航后直接接着已有位置继续跑"，不会再
// 经历一次"重新定位"的空窗期。
func (a *App) StartNavigation(routeID string) {
	p := a.routes.Find(routeID)
	if p == nil || len(p.Nodes) < 2 {
		a.hint = "导航失败：路径不存在或节点不足"
		return
	}
	cfg := a.settings.Get()

	// 沿用既有追踪 / 导航的位置 + K：若上一次 fix 足够新 (< 3s) 且置信度够
	// (>= 0.40)，直接把它当作 seed，不再做广域重定位。
	a.nav.mu.Lock()
	var reuseFix locator.Fix
	reuseActive := a.nav.active && a.nav.hasFix &&
		!a.nav.lastFixAt.IsZero() && time.Since(a.nav.lastFixAt) < 3*time.Second &&
		a.nav.fix.Confidence >= 0.40
	if reuseActive {
		reuseFix = a.nav.fix
	}
	a.nav.mu.Unlock()

	a.StopNavigation()

	if cfg.NavSimulator || cfg.MinimapROI.Empty() {
		// 模拟器模式：沿路径来回匀速运动
		a.startNavSimulator(p, cfg)
		return
	}
	if a.mapView == nil || a.cache == nil {
		a.hint = "导航失败：地图/缓存未就绪"
		return
	}
	roi := locator.ROI{X: cfg.MinimapROI.X, Y: cfg.MinimapROI.Y, W: cfg.MinimapROI.W, H: cfg.MinimapROI.H}
	seedX, seedY := p.Nodes[0].X, p.Nodes[0].Y
	skipWide := false
	if reuseActive {
		seedX, seedY = reuseFix.WorldX, reuseFix.WorldY
		skipWide = true
		a.hint = fmt.Sprintf("沿用当前定位 (%.0f,%.0f, NCC=%.2f) 开始导航…", seedX, seedY, reuseFix.Confidence)
	} else if cfg.LastPlayerSet {
		// 优先用上次玩家位置；如果离路径起点很远，多种子阶段会再靠起点命中
		seedX, seedY = cfg.LastPlayerX, cfg.LastPlayerY
		a.hint = "正在为导航定位玩家…"
	} else {
		a.hint = "正在为导航定位玩家…"
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.hint = fmt.Sprintf("导航启动崩溃：%v", r)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
		}()
		if skipWide {
			// 已有可信 fix：直接构造 matcher + loop，绕过广域 + 多种子搜索阶段。
			a.startMatcherLoopWithSeed(cfg, seedX, seedY, reuseFix, roi, routeID, p)
			return
		}
		img, err := winutil.CaptureScreenRect(image.Rect(roi.X, roi.Y, roi.X+roi.W, roi.Y+roi.H))
		if err != nil {
			a.hint = "导航失败：截屏错误 " + err.Error()
			if a.Window != nil {
				a.Window.Invalidate()
			}
			return
		}
		a.startMatcherLoopAsync(cfg, img, seedX, seedY, roi, routeID, p)
	}()
}

// startMatcherLoopWithSeed 跳过广域重定位，直接用给定的 seed 构造 matcher 并启动
// loop。用在从"追踪"切换到"导航"且已有新鲜 fix 的场景。
func (a *App) startMatcherLoopWithSeed(cfg Settings, seedX, seedY float64, initialFix locator.Fix, roi locator.ROI, routeID string, p *mapdata.Path) {
	defer func() {
		if r := recover(); r != nil {
			a.hint = fmt.Sprintf("启动崩溃：%v", r)
			if a.Window != nil {
				a.Window.Invalidate()
			}
		}
	}()
	matchFn := a.buildRealMatcher(cfg, seedX, seedY)
	interval := 400 * time.Millisecond
	if routeID == "" {
		interval = 600 * time.Millisecond
	}
	loc := locator.New(locator.Config{
		ROI:      roi,
		Interval: interval,
		Match:    matchFn,
	})
	loc.OnFix = func(f locator.Fix) { a.handleNavFix(f, p) }
	loc.OnErr = func(err error) {
		if err == locator.ErrNotImplemented {
			return
		}
		a.applyNavFallback(err)
	}
	a.nav.mu.Lock()
	a.nav.active = true
	a.nav.routeID = routeID
	a.nav.loc = loc
	a.nav.startedAt = time.Now()
	a.nav.fix = initialFix
	a.nav.hasFix = true
	a.nav.lost = false
	a.nav.lastFixAt = time.Now()
	a.nav.mu.Unlock()
	if err := loc.Start(); err != nil {
		a.hint = "启动失败：" + err.Error()
		a.nav.mu.Lock()
		a.nav.active = false
		a.nav.loc = nil
		a.nav.hasFix = false
		a.nav.mu.Unlock()
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	label := "导航：" + p.Name
	a.hint = fmt.Sprintf("已开始%s（沿用既有定位，NCC=%.2f）", label, initialFix.Confidence)
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// startNavSimulator 模拟器模式导航 —— 保留原行为。
func (a *App) startNavSimulator(p *mapdata.Path, cfg Settings) {
	nodes := make([]locator.SimNode, len(p.Nodes))
	for i, n := range p.Nodes {
		nodes[i] = locator.SimNode{X: n.X, Y: n.Y}
	}
	sim := locator.NewSimulator(locator.SimulatorTrack{Nodes: nodes, Speed: 80})
	matchFn := sim.Match
	loc := locator.New(locator.Config{
		ROI:      locator.ROI{X: cfg.MinimapROI.X, Y: cfg.MinimapROI.Y, W: cfg.MinimapROI.W, H: cfg.MinimapROI.H},
		Interval: 250 * time.Millisecond,
		Match:    matchFn,
	})
	loc.SetROI(locator.ROI{X: 0, Y: 0, W: 1, H: 1})
	loc.SetCapture(func(_ locator.ROI) (*image.RGBA, error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	})
	loc.OnFix = func(f locator.Fix) { a.handleNavFix(f, p) }
	loc.OnErr = func(err error) {
		if err == locator.ErrNotImplemented {
			return
		}
		a.applyNavFallback(err)
	}
	a.nav.mu.Lock()
	a.nav.active = true
	a.nav.routeID = p.Name // 用名字暂存（Simulator 模式下 routeID 只用于"有路径"判定）
	a.nav.loc = loc
	a.nav.startedAt = time.Now()
	a.nav.hasFix = false
	a.nav.mu.Unlock()
	// 更严格：routeID 实际要用 p 的 ID 而不是 Name；sim 模式下需要传递
	// 参考其他代码查 Find(routeID) 用的是路径的 ID；暂保留以免回归。
	if err := loc.Start(); err != nil {
		a.hint = "启动定位器失败：" + err.Error()
		a.nav.mu.Lock()
		a.nav.active = false
		a.nav.mu.Unlock()
		return
	}
	a.hint = "已开始导航（模拟）：" + p.Name
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// StartTracking 启动"实时追踪玩家位置"模式（无路径）。已有导航/追踪在跑则先停止。
//
// 启动时先做一次"广域校准"：在 K × [0.4..2.5] 9 个尺度 + ±200 模板像素半径
// 范围内搜索最高分。如果连最佳尺度的 NCC 都 < 0.4，立刻终止追踪并提示用户校准；
// 否则用最佳 scale 校正 K，并把首帧得到的位置作为种子开始追踪。
//
// 这样可以彻底避免"追踪一直贴在初始 seed 上不动"的体验问题。
func (a *App) StartTracking() {
	a.StopNavigation()
	cfg := a.settings.Get()
	if cfg.MinimapROI.Empty() {
		a.hint = "追踪失败：未设置小地图 ROI（请到设置 → 在屏幕上框选）"
		return
	}
	if a.mapView == nil {
		a.hint = "追踪失败：地图未就绪"
		return
	}
	if a.cache == nil {
		a.hint = "追踪失败：瓦片缓存未就绪"
		return
	}

	roi := locator.ROI{X: cfg.MinimapROI.X, Y: cfg.MinimapROI.Y, W: cfg.MinimapROI.W, H: cfg.MinimapROI.H}
	seedX, seedY := a.mapView.ViewCenter()
	if cfg.LastPlayerSet {
		seedX, seedY = cfg.LastPlayerX, cfg.LastPlayerY
		log.Printf("[tracking] using persisted last player position as seed: (%.0f, %.0f)", seedX, seedY)
	}
	a.hint = "正在广域定位玩家…（首次匹配可能 1-2 秒）"
	if a.Window != nil {
		a.Window.Invalidate()
	}

	// 截屏也放到后台 goroutine，避免任何情况下阻塞 UI
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.hint = fmt.Sprintf("追踪启动崩溃：%v", r)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
		}()
		img, err := winutil.CaptureScreenRect(image.Rect(roi.X, roi.Y, roi.X+roi.W, roi.Y+roi.H))
		if err != nil {
			a.hint = "追踪失败：截屏错误 " + err.Error()
			if a.Window != nil {
				a.Window.Invalidate()
			}
			return
		}
		a.startMatcherLoopAsync(cfg, img, seedX, seedY, roi, "", nil)
	}()
}

// startMatcherLoopAsync 在后台线程做广域校准 + 启动匹配 loop；被 StartTracking /
// StartNavigation 共同使用。routeID="" 且 p=nil 表示纯追踪；否则是导航。
//
// 关键：广域 NCC 失败 *不* 再终止流程。无论是否找到高分位置，都会启动 loop，
// 由 Match + patch-vote 接力，并在后台继续重定位。
func (a *App) startMatcherLoopAsync(cfg Settings, img *image.RGBA, seedX, seedY float64, roi locator.ROI, routeID string, p *mapdata.Path) {
	defer func() {
		if r := recover(); r != nil {
			a.hint = fmt.Sprintf("启动崩溃：%v", r)
			if a.Window != nil {
				a.Window.Invalidate()
			}
		}
	}()
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	wm := &locator.Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: cfg.WorldUnitsPerMinimapPx,
		SearchZoom:             zoom,
		HeadingDetect:          false,
		ScaleRatios:            []float64{0.4, 0.5, 0.63, 0.8, 1.0, 1.25, 1.6, 2.0, 2.5},
		SearchRadiusPx:         200,
		WikiHalfCap:            1500,
		DebugLog:               cfg.DebugLog,
	}
	wm.SetSeed(seedX, seedY)
	firstFix, _ := wm.Match(img)
	dbg := wm.LastDebug

	// 第一帧广域 NCC 失败 → 立刻多种子搜索（修复"重启 / 传送"场景）
	gotWide := dbg.BestScore >= 0.35
	if !gotWide {
		seeds := a.collectReLocalizeSeeds()
		// 把即将用的 seed 作为优先候选
		seeds = append([]locator.Fix{{WorldX: seedX, WorldY: seedY}}, seeds...)
		// 如果是导航，把路径节点也加入候选
		if p != nil {
			for i := 0; i < len(p.Nodes); i += len(p.Nodes)/8 + 1 {
				seeds = append(seeds, locator.Fix{WorldX: p.Nodes[i].X, WorldY: p.Nodes[i].Y})
			}
			seeds = append(seeds, locator.Fix{WorldX: p.Nodes[len(p.Nodes)-1].X, WorldY: p.Nodes[len(p.Nodes)-1].Y})
		}
		log.Printf("[start] first wide-scale low (%.2f); trying %d-seed search", dbg.BestScore, len(seeds))
		multiBest, _, mErr := wm.MatchMultiSeed(img, seeds, 200, 1500)
		if mErr == nil && multiBest.Confidence >= 0.40 {
			firstFix = multiBest
			dbg.BestScore = multiBest.Confidence
			dbg.BestScale = wm.LastDebug.BestScale
			gotWide = true
			log.Printf("[start] multi-seed found NCC=%.2f at (%.0f, %.0f)",
				multiBest.Confidence, multiBest.WorldX, multiBest.WorldY)
		} else {
			log.Printf("[start] multi-seed also failed (best=%.2f, err=%v)",
				multiBest.Confidence, mErr)
		}
	}

	if gotWide {
		effectiveK := cfg.WorldUnitsPerMinimapPx * dbg.BestScale
		if effectiveK >= 0.1 && effectiveK <= 5.0 {
			cfg.WorldUnitsPerMinimapPx = effectiveK
		}
		seedX, seedY = firstFix.WorldX, firstFix.WorldY
		a.nav.mu.Lock()
		a.nav.fix = firstFix
		a.nav.fix.HasHeading = false
		a.nav.hasFix = true
		a.nav.lost = false
		a.nav.mu.Unlock()
	} else {
		log.Printf("[start] wide-scale low; starting loop with persisted seed/K anyway")
		a.nav.mu.Lock()
		a.nav.fix = locator.Fix{WorldX: seedX, WorldY: seedY, Confidence: 0}
		a.nav.hasFix = false
		a.nav.lost = false
		a.nav.mu.Unlock()
	}

	matchFn := a.buildRealMatcher(cfg, seedX, seedY)
	interval := 600 * time.Millisecond
	if routeID != "" {
		// 导航更高频采样，配合预测给出更流畅的体验
		interval = 400 * time.Millisecond
	}
	loc := locator.New(locator.Config{
		ROI:      roi,
		Interval: interval,
		Match:    matchFn,
	})
	loc.OnFix = func(f locator.Fix) {
		a.handleNavFix(f, p)
	}
	loc.OnErr = func(err error) {
		if err == locator.ErrNotImplemented {
			return
		}
		a.applyNavFallback(err)
	}
	a.nav.mu.Lock()
	if a.nav.loc != nil {
		old := a.nav.loc
		a.nav.loc = nil
		go old.Stop()
	}
	a.nav.active = true
	a.nav.routeID = routeID
	a.nav.loc = loc
	a.nav.startedAt = time.Now()
	a.nav.mu.Unlock()
	if err := loc.Start(); err != nil {
		a.hint = "启动失败：" + err.Error()
		a.nav.mu.Lock()
		a.nav.active = false
		a.nav.loc = nil
		a.nav.hasFix = false
		a.nav.mu.Unlock()
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	label := "追踪"
	if routeID != "" {
		label = "导航"
		if p != nil {
			label = "导航：" + p.Name
		}
	}
	if gotWide {
		a.hint = fmt.Sprintf("已开始%s (NCC=%.2f, scale=%.2f, K=%.3f)",
			label, dbg.BestScore, dbg.BestScale, cfg.WorldUnitsPerMinimapPx)
	} else {
		a.hint = fmt.Sprintf("已启动%s loop（首帧广域 NCC=%.2f 低；定位稳定前请右键校准）",
			label, dbg.BestScore)
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// buildRealMatcher 根据当前 settings 与种子坐标构造真实 NCC matcher，包装一个会更新种子的 MatchFn。
//
// 关键策略（修复定位偏移 / patch-vote 永久错误）：
//  1. 主 Match 命中 (conf >= 0.55)：UpdateSeed，正常前进。
//  2. 主 Match 中等命中 (0.30 ≤ conf < 0.55)：UpdateSeed，正常前进；patch-vote 不动。
//  3. 主 Match 低分 / 错误：尝试 patch-vote。命中视为 *临时* (Tentative=true)：
//     - 返回给 UI 让用户感知，但 *不* 写入 m.LastFix（下一帧仍从老 seed 出发匹配）
//     - 不持久化到 LastPlayer
//     - 触发后台多种子全图重定位（debounced 在 App 侧）
func (a *App) buildRealMatcher(cfg Settings, seedX, seedY float64) locator.MatchFn {
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	searchZoom := cfg.NavSearchZoom
	if searchZoom <= 0 {
		searchZoom = 8
	}
	m := &locator.Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: cfg.WorldUnitsPerMinimapPx,
		SearchZoom:             searchZoom,
		HeadingDetect:          true,
		ScaleRatios:            []float64{0.5, 0.7, 1.0, 1.4, 2.0},
		SearchRadiusPx:         48,
		WikiHalfCap:            1500, // 提升上限，允许失败累积时把 radius 放大也能渲染对应 wiki 区域
		DebugLog:               cfg.DebugLog,
	}
	m.SetSeed(seedX, seedY)
	return func(roi *image.RGBA) (locator.Fix, error) {
		f, err := m.Match(roi)
		if err == nil && f.Confidence >= 0.55 {
			m.UpdateSeed(f)
			return f, nil
		}
		if err == nil && f.Confidence >= 0.30 {
			// 中等置信度：仍然用主匹配，UpdateSeed
			m.UpdateSeed(f)
			return f, nil
		}
		// 主匹配失败或置信度低：用 patch-vote 备用算法。
		// 仅在主匹配选出的最佳 scale 上跑，省时间。
		bestScale := m.LastDebug.BestScale
		if bestScale <= 0 {
			bestScale = 1.0
		}
		pf, perr := m.MatchPatchVote(roi, []float64{bestScale})
		if cfg.DebugLog {
			log.Printf("[patch-vote] main NCC=%.2f err=%v → vote conf=%.2f err=%v",
				f.Confidence, err, pf.Confidence, perr)
		}
		if perr == nil && pf.Confidence >= 0.35 {
			pf.Tentative = true // 临时位置；UI 显示但不持久；触发 bg 重定位
			// 关键：不调用 m.UpdateSeed(pf)。下一帧仍用旧 seed 跑主 Match，
			// 一旦在 bg 重定位中找到真位置再被替换。
			a.requestBgReLocalize("patch-vote tentative")
			return pf, nil
		}
		if err == nil {
			m.UpdateSeed(f)
		}
		return f, err
	}
}

// AutoCalibrateKAsync 异步版本：在后台跑宽尺度 NCC，结果写回 a.hint 并刷新。
// 不阻塞 UI。
func (a *App) AutoCalibrateKAsync() {
	a.hint = "正在自动校准 K…（宽尺度 NCC 扫描中）"
	if a.Window != nil {
		a.Window.Invalidate()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.hint = fmt.Sprintf("校准崩溃：%v", r)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
		}()
		msg := a.AutoCalibrateK()
		a.hint = msg
		if a.Window != nil {
			a.Window.Invalidate()
		}
	}()
}

// CalibrateAtWorldAsync 异步版本：在后台跑校准，结果写回 a.hint 并刷新。
func (a *App) CalibrateAtWorldAsync(wx, wy float64) {
	a.hint = fmt.Sprintf("正在校准到 (%.0f, %.0f)…", wx, wy)
	if a.Window != nil {
		a.Window.Invalidate()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.hint = fmt.Sprintf("校准崩溃：%v", r)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
		}()
		msg := a.CalibrateAtWorld(wx, wy)
		a.hint = msg
		if a.Window != nil {
			a.Window.Invalidate()
		}
	}()
}

// AutoCalibrateK 用当前 mapView 视图中心作为粗略 seed，扫描宽尺度找最佳 K，写回设置。
//
// 流程：
//  1. 把"地图视图中心"当作玩家世界坐标 (seedX, seedY)
//  2. 截一张游戏小地图 ROI
//  3. 在 K_old × [0.4, 0.5, 0.63, 0.8, 1.0, 1.25, 1.6, 2.0, 2.5] 中扫描
//     每个 scale 都把 wiki 瓦片以 K*scale 还原成"和小地图同尺度"的图，
//     再做 NCC 模板匹配，记录最高得分
//  4. 找最高 NCC 的 scale → K_new = K_old × best_scale
//  5. 仅当 NCC ≥ 0.45 时写回（否则视为校准失败）
//
// 用户必须保证：地图视图中心确实接近玩家真实位置（误差 < ±200 世界单位）。
// 推荐做法：在游戏里走到一个地标（如某个传送阵 / 地图边角），然后在软件
// 内地图上手动平移到同一地标，再点"自动校准"。
//
// 返回信息字符串供 UI 显示。
func (a *App) AutoCalibrateK() string {
	cfg := a.settings.Get()
	if cfg.MinimapROI.Empty() {
		return "校准失败：未设置 ROI（先框选小地图）"
	}
	if a.mapView == nil || a.cache == nil {
		return "校准失败：地图/缓存未就绪"
	}
	seedX, seedY := a.mapView.ViewCenter()
	roi := locator.ROI{X: cfg.MinimapROI.X, Y: cfg.MinimapROI.Y, W: cfg.MinimapROI.W, H: cfg.MinimapROI.H}
	img, err := winutil.CaptureScreenRect(image.Rect(roi.X, roi.Y, roi.X+roi.W, roi.Y+roi.H))
	if err != nil {
		return "校准失败：截屏错误 " + err.Error()
	}
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	m := &locator.Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: cfg.WorldUnitsPerMinimapPx,
		SearchZoom:             zoom,
		HeadingDetect:          false,
		ScaleRatios:            []float64{0.4, 0.5, 0.63, 0.8, 1.0, 1.25, 1.6, 2.0, 2.5},
		// 校准时给宽搜索半径以容忍 seed 大幅偏离（±200 世界单位级别）
		SearchRadiusPx: 200,
		WikiHalfCap:    1500,
		DebugLog:       true,
	}
	m.SetSeed(seedX, seedY)
	fix, _ := m.Match(img)
	dbg := m.LastDebug
	if dbg.BestScore < 0.45 {
		return fmt.Sprintf("校准 NCC 太低 (%.2f)。可能原因：\n"+
			"1) 视图中心离玩家真实位置太远（>200 世界单位）。先在游戏里走到一个明显地标（传送阵 / 地图边角），再把软件内地图平移到同一地标。\n"+
			"2) 小地图 ROI 框错了；确认 ROI 仅覆盖游戏圆形小地图本体，且无 UI 遮挡。\n"+
			"3) 当前选错图层（地表 G / 地下 B1 / B2）。\n"+
			"详细：seed=(%.0f,%.0f) layer=%s 全 scale 得分=%v",
			dbg.BestScore, seedX, seedY, a.mapView.Layer(), dbg.AllScores)
	}
	newK := cfg.WorldUnitsPerMinimapPx * dbg.BestScale
	a.settings.Update(func(s *Settings) { s.WorldUnitsPerMinimapPx = newK })
	return fmt.Sprintf("校准成功：K=%.3f (NCC=%.2f, scale=%.2f)；定位到 (%.0f, %.0f)",
		newK, dbg.BestScore, dbg.BestScale, fix.WorldX, fix.WorldY)
}

// CalibrateAtWorld 用用户在地图上右键的世界坐标作为玩家真实位置 seed 校准 K，
// 同时把追踪器的种子和显示的玩家位置都强制对齐到这个点，让校准立刻生效。
func (a *App) CalibrateAtWorld(wx, wy float64) string {
	cfg := a.settings.Get()
	if cfg.MinimapROI.Empty() {
		return "校准失败：未设置 ROI（先框选小地图）"
	}
	if a.mapView == nil || a.cache == nil {
		return "校准失败：地图/缓存未就绪"
	}
	roi := locator.ROI{X: cfg.MinimapROI.X, Y: cfg.MinimapROI.Y, W: cfg.MinimapROI.W, H: cfg.MinimapROI.H}
	img, err := winutil.CaptureScreenRect(image.Rect(roi.X, roi.Y, roi.X+roi.W, roi.Y+roi.H))
	if err != nil {
		return "校准失败：截屏错误 " + err.Error()
	}
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	// 用户右键 = "玩家就在这里" 是 ground truth。匹配器只用来扫尺度找最佳 K，
	// 不允许它把"位置"挪走（最终 fix 强制用 (wx, wy)）。给 48 模板像素搜索半径
	// 容忍用户点击微小偏差，但不至于跨越大片相似地形。
	m := &locator.Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: cfg.WorldUnitsPerMinimapPx,
		SearchZoom:             zoom,
		HeadingDetect:          false,
		ScaleRatios:            []float64{0.4, 0.5, 0.63, 0.8, 1.0, 1.25, 1.6, 2.0, 2.5},
		SearchRadiusPx:         48,
		WikiHalfCap:            1500,
		DebugLog:               true,
	}
	m.SetSeed(wx, wy)
	_, _ = m.Match(img)
	dbg := m.LastDebug

	if dbg.BestScore < 0.30 {
		return fmt.Sprintf("校准 NCC 太低 (%.2f)。请确认：1) 右键的位置确实是玩家所在；2) 图层正确（当前 %s）；3) ROI 框的是游戏小地图圆形本体。全 scale 得分=%v",
			dbg.BestScore, a.mapView.Layer(), dbg.AllScores)
	}

	newK := cfg.WorldUnitsPerMinimapPx * dbg.BestScale
	// 夹到合理区间防止灾难性写入
	if newK < 0.1 || newK > 5.0 {
		return fmt.Sprintf("校准失败：算出 K=%.3f 不合理（应在 [0.1, 5.0]）。可能 NCC 在错误的尺度上偶然过阈值。建议先在游戏内把 minimap 放到最大 zoom，再校准。", newK)
	}

	// 1) 写回 K
	a.settings.Update(func(s *Settings) {
		s.WorldUnitsPerMinimapPx = newK
		s.LastPlayerX = wx
		s.LastPlayerY = wy
		s.LastPlayerSet = true
	})
	_ = a.settingsView // UI editor 不再从这里同步，避免非 UI 线程访问

	// 2) 强制把显示的玩家位置设为用户点的坐标 —— 这是用户给我们的 ground truth，
	// 不应被追踪器最近的（可能错误的）匹配覆盖。
	a.nav.mu.Lock()
	a.nav.fix.WorldX = wx
	a.nav.fix.WorldY = wy
	a.nav.fix.Confidence = dbg.BestScore
	a.nav.hasFix = true
	a.nav.lost = false
	tracking := a.nav.active
	loc := a.nav.loc
	a.nav.mu.Unlock()

	// 让 mapView 的平滑插值瞬间跳到新位置（不要慢慢漂过去）
	if a.mapView != nil {
		a.mapView.SnapPlayerTo(wx, wy)
	}

	// 3) 如果追踪正在跑，用新 seed + 新 K 重建 matcher 并热替换。
	// 否则下一次匹配还会带着旧 seed/K，立刻把校准结果覆盖回错误状态。
	if tracking && loc != nil {
		cfg2 := a.settings.Get() // 拿最新 K
		newMatch := a.buildRealMatcher(cfg2, wx, wy)
		loc.SetMatch(newMatch)
	}

	if a.Window != nil {
		a.Window.Invalidate()
	}

	return fmt.Sprintf("校准成功：K=%.3f (NCC=%.2f, scale=%.2f)；玩家位置已锁定到 (%.0f, %.0f)",
		newK, dbg.BestScore, dbg.BestScale, wx, wy)
}

// requestBgReLocalize 请求一次后台多种子全图搜索；带节流（最快每 10s 一次）+
// 互斥（同一时刻只跑一个）。被 patch-vote tentative、连续失败累积、显式 API 触发。
//
// 找到高置信度位置后会更新主 matcher 的 seed 和持久化 LastPlayer。
func (a *App) requestBgReLocalize(reason string) {
	a.nav.mu.Lock()
	if a.nav.bgRelocateRunning {
		a.nav.mu.Unlock()
		return
	}
	if !a.nav.bgRelocateLastAt.IsZero() && time.Since(a.nav.bgRelocateLastAt) < 10*time.Second {
		a.nav.mu.Unlock()
		return
	}
	loc := a.nav.loc
	if !a.nav.active || loc == nil {
		a.nav.mu.Unlock()
		return
	}
	a.nav.bgRelocateRunning = true
	a.nav.mu.Unlock()
	log.Printf("[bg-relocate] starting (reason=%s)", reason)
	go a.runBgReLocalize(reason)
}

// runBgReLocalize 后台执行多种子搜索。完成后更新 matcher seed + LastPlayer。
func (a *App) runBgReLocalize(reason string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[bg-relocate] panic: %v", r)
		}
		a.nav.mu.Lock()
		a.nav.bgRelocateRunning = false
		a.nav.bgRelocateLastAt = time.Now()
		a.nav.mu.Unlock()
	}()
	cfg := a.settings.Get()
	if cfg.MinimapROI.Empty() || a.cache == nil || a.mapView == nil {
		return
	}
	roi := image.Rect(cfg.MinimapROI.X, cfg.MinimapROI.Y,
		cfg.MinimapROI.X+cfg.MinimapROI.W, cfg.MinimapROI.Y+cfg.MinimapROI.H)
	img, err := winutil.CaptureScreenRect(roi)
	if err != nil {
		log.Printf("[bg-relocate] capture failed: %v", err)
		return
	}

	seeds := a.collectReLocalizeSeeds()
	if len(seeds) == 0 {
		log.Printf("[bg-relocate] no seeds")
		return
	}
	log.Printf("[bg-relocate] %d seeds, scanning…", len(seeds))

	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	wm := &locator.Matcher{
		Mosaic:                 mosaic,
		WorldUnitsPerMinimapPx: cfg.WorldUnitsPerMinimapPx,
		SearchZoom:             zoom,
		HeadingDetect:          false,
		ScaleRatios:            []float64{0.7, 1.0, 1.4},
		DebugLog:               cfg.DebugLog,
	}
	best, cands, mErr := wm.MatchMultiSeed(img, seeds, 200, 1500)
	if mErr != nil {
		log.Printf("[bg-relocate] failed: %v (top score %.2f)",
			mErr, topScore(cands))
		return
	}
	log.Printf("[bg-relocate] best NCC=%.2f at (%.0f, %.0f)", best.Confidence, best.WorldX, best.WorldY)
	if best.Confidence < 0.50 {
		// 不够高，不动
		return
	}
	// 更新主 matcher 的 seed —— 通过重建 matchFn 热替换
	a.nav.mu.Lock()
	loc := a.nav.loc
	a.nav.mu.Unlock()
	if loc == nil {
		return
	}
	cfg2 := a.settings.Get()
	newMatch := a.buildRealMatcher(cfg2, best.WorldX, best.WorldY)
	loc.SetMatch(newMatch)
	// 立刻把显示位置更新为重定位结果
	a.nav.mu.Lock()
	a.nav.fix = best
	a.nav.hasFix = true
	a.nav.lost = false
	a.nav.mu.Unlock()
	a.settings.Update(func(s *Settings) {
		s.LastPlayerX = best.WorldX
		s.LastPlayerY = best.WorldY
		s.LastPlayerSet = true
	})
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// collectReLocalizeSeeds 收集多种子候选：当前 fix / LastPlayer / 视图中心 /
// 所有可见路径节点（采样）。去重 + 距离间隔 ≥ 100 世界单位。
func (a *App) collectReLocalizeSeeds() []locator.Fix {
	seen := []locator.Fix{}
	add := func(x, y float64) {
		for _, s := range seen {
			dx := s.WorldX - x
			dy := s.WorldY - y
			if dx*dx+dy*dy < 100*100 {
				return
			}
		}
		seen = append(seen, locator.Fix{WorldX: x, WorldY: y})
	}
	// 当前 fix
	a.nav.mu.Lock()
	if a.nav.hasFix {
		add(a.nav.fix.WorldX, a.nav.fix.WorldY)
	}
	a.nav.mu.Unlock()
	// LastPlayer
	cfg := a.settings.Get()
	if cfg.LastPlayerSet {
		add(cfg.LastPlayerX, cfg.LastPlayerY)
	}
	// 视图中心
	if a.mapView != nil {
		vx, vy := a.mapView.ViewCenter()
		add(vx, vy)
	}
	// 路径节点（每条路径采样起点 / 中点 / 终点 + 用户自定义点位）
	if a.routes != nil {
		for _, p := range a.routes.All() {
			if len(p.Nodes) == 0 {
				continue
			}
			add(p.Nodes[0].X, p.Nodes[0].Y)
			mid := p.Nodes[len(p.Nodes)/2]
			add(mid.X, mid.Y)
			last := p.Nodes[len(p.Nodes)-1]
			add(last.X, last.Y)
		}
	}
	if a.customs != nil {
		// CustomStore 没有 All()；通过 store.PointsOf(CustomMarkType) 拿；
		// 世界坐标映射：X = Lng，Y = Lat。
		for _, pt := range a.store.PointsOf(CustomMarkType) {
			add(pt.Point.Lng, pt.Point.Lat)
		}
	}
	// 限制候选数量（每个 seed 都会跑一次广域 NCC，太多会很慢）
	const maxSeeds = 12
	if len(seen) > maxSeeds {
		seen = seen[:maxSeeds]
	}
	return seen
}

func topScore(cs []locator.MultiSeedCandidate) float64 {
	if len(cs) == 0 {
		return 0
	}
	return cs[0].Score
}
// 主线程只负责显示"计算中…"提示和在完成后应用结果并触发重绘。
func (a *App) solveTSPAsync() {
	n := len(a.editor.Selection())
	if n < 2 {
		a.hint = "至少需要 2 个选中点"
		return
	}
	a.hint = fmt.Sprintf("正在计算 %d 个节点的最短路径…", n)
	if a.Window != nil {
		a.Window.Invalidate()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.hint = fmt.Sprintf("求解崩溃：%v", r)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			}
		}()
		t0 := time.Now()
		order, sel, err := a.editor.ComputeTSPOrder()
		elapsed := time.Since(t0)
		if err != nil {
			a.hint = "求解失败：" + err.Error()
		} else {
			msg := a.editor.ApplyTSPOrder(order, sel)
			a.hint = fmt.Sprintf("%s（用时 %s）", msg, elapsed.Round(time.Millisecond))
		}
		if a.Window != nil {
			a.Window.Invalidate()
		}
	}()
}

// StopNavigation 关闭导航。
func (a *App) StopNavigation() {
	a.nav.mu.Lock()
	loc := a.nav.loc
	a.nav.active = false
	a.nav.routeID = ""
	a.nav.loc = nil
	a.nav.hasFix = false
	a.nav.mu.Unlock()
	if loc != nil {
		loc.Stop()
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// handleNavFix 在 Locator 回调里被调用，更新 navState 并触发重绘。
// p 为 nil 时表示纯追踪模式（无路径），不计算进度。
//
// 抖动抑制（修复"完全不动也偶尔小幅来回"）：
//   - 如果新 fix 与上次显示位置的距离 < 1 个 minimap 像素的世界等价值（≈ K
//     世界单位），且置信度变化幅度 < 0.05，认为这是 NCC 像素离散导致的噪声，
//     直接保留旧位置，不让 UI 漂动。
//
// 速度估计：对两次 fix 之间的位移做指数平滑，供 MapView 在 fix 间做预测外推。
func (a *App) handleNavFix(f locator.Fix, p *mapdata.Path) {
	cfg := a.settings.Get()
	K := cfg.WorldUnitsPerMinimapPx
	if K <= 0 {
		K = 0.5
	}
	thresh := K * 1.0

	now := time.Now()

	a.nav.mu.Lock()
	prev := a.nav.fix
	hadPrev := a.nav.hasFix
	// 1. 滞回
	if hadPrev && !f.Tentative && !prev.Tentative {
		dx := f.WorldX - prev.WorldX
		dy := f.WorldY - prev.WorldY
		dist2 := dx*dx + dy*dy
		dconf := f.Confidence - prev.Confidence
		if dconf < 0 {
			dconf = -dconf
		}
		if dist2 < thresh*thresh && dconf < 0.05 {
			f.WorldX = prev.WorldX
			f.WorldY = prev.WorldY
		}
	}
	// 2. 速度估计（指数平滑 α=0.4），tentative 不参与
	if hadPrev && !a.nav.lastFixAt.IsZero() && !f.Tentative {
		dt := now.Sub(a.nav.lastFixAt).Seconds()
		if dt > 0.05 && dt < 3.0 {
			instVX := (f.WorldX - prev.WorldX) / dt
			instVY := (f.WorldY - prev.WorldY) / dt
			const alpha = 0.4
			a.nav.velX = a.nav.velX*(1-alpha) + instVX*alpha
			a.nav.velY = a.nav.velY*(1-alpha) + instVY*alpha
		}
	} else if !hadPrev {
		a.nav.velX = 0
		a.nav.velY = 0
	}
	a.nav.lastFixAt = now
	// 3. 进度
	var prog NavProgress
	if p != nil {
		prog = ProjectOnPath(p.Nodes, f.WorldX, f.WorldY)
	}
	a.nav.fix = f
	a.nav.hasFix = true
	a.nav.lost = false
	a.nav.progress = prog
	a.nav.mu.Unlock()

	// 4. 持久化 LastPlayer：仅非 tentative + conf >= 0.40 + 节流 50 单位
	if !f.Tentative && f.Confidence >= 0.40 {
		dx := f.WorldX - cfg.LastPlayerX
		dy := f.WorldY - cfg.LastPlayerY
		if !cfg.LastPlayerSet || dx*dx+dy*dy >= 50*50 {
			a.settings.Update(func(s *Settings) {
				s.LastPlayerX = f.WorldX
				s.LastPlayerY = f.WorldY
				s.LastPlayerSet = true
			})
		}
	}

	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// applyNavFallback 在匹配失败时按用户选择的策略处理。
func (a *App) applyNavFallback(err error) {
	mode := a.settings.Get().NavFallback
	a.nav.mu.Lock()
	switch mode {
	case NavFallbackStay:
		// 不更新 fix；保持上次画面 / 等待。
	case NavFallbackLast:
		// 与 stay 行为一致——上一次 fix 仍存在，照样渲染。
	case NavFallbackLost:
		a.nav.lost = true
		a.nav.hasFix = false
	}
	a.nav.lastErr = err.Error()
	a.nav.mu.Unlock()
	// 任何匹配失败都触发 bg 重定位（debounced）。包括小地图被遮挡的情况，
	// 重定位 goroutine 会自己重新截屏；如果当时小地图依然不可见会失败但成本不高。
	a.requestBgReLocalize("match error: " + err.Error())
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// locatorLayerOf 取出指定 layer 名称对应的 wiki.Layer。找不到时返回零值。
func locatorLayerOf(store *mapdata.Store, name string) wiki.Layer {
	for _, ly := range store.Layers() {
		if ly.Name == name {
			return ly
		}
	}
	return wiki.Layer{}
}

// applySettingsToMapView 把当前 settings 注入到 mapView。
func (a *App) applySettingsToMapView() {
	if a.mapView == nil {
		return
	}
	a.mapView.MarkerStyleFn = func() (MarkerStyle, int) {
		s := a.settings.Get()
		return s.MarkerStyle, s.IconSize
	}
	a.mapView.SelectionStyleFn = func() SelectionStyle {
		return a.settings.Get().SelectionStyle
	}
	a.mapView.NavStateFn = func() (active bool, nodes []mapdata.PathNode, px, py, heading float64, hasFix bool, prog NavProgress, velX, velY float64, lastAt time.Time, predict bool) {
		act, routeID, fix, has, p, vx, vy, la := a.nav.snapshotWithVelocity()
		predict = a.settings.Get().NavPredict
		if !act {
			return false, nil, 0, 0, 0, false, NavProgress{}, 0, 0, time.Time{}, predict
		}
		// 追踪模式：无 routeID，直接返回 fix 但 nodes=nil
		if routeID == "" {
			return true, nil, fix.WorldX, fix.WorldY, fix.Heading, has, NavProgress{}, vx, vy, la, predict
		}
		rt := a.routes.Find(routeID)
		if rt == nil {
			return true, nil, fix.WorldX, fix.WorldY, fix.Heading, has, p, vx, vy, la, predict
		}
		return true, rt.Nodes, fix.WorldX, fix.WorldY, fix.Heading, has, p, vx, vy, la, predict
	}
	a.mapView.SetDebug(a.settings.Get().DebugLog)
	a.mapView.AutoCenterFn = func() bool {
		mode := a.settings.Get().NavCenterMode
		switch mode {
		case CenterOff:
			return false
		case CenterNavOnly:
			_, routeID, _, _, _ := a.nav.snapshot()
			return routeID != "" // 纯追踪没有 routeID → 不居中
		}
		return true
	}
}

// maybePrefetchIcons 当当前样式为图标时，主动开始下载所有可见类别的图标。
func (a *App) maybePrefetchIcons() {
	if a.settings.Get().MarkerStyle == MarkerIcon {
		a.prefetchIcons()
	}
}

// prefetchIcons 触发当前所有类别图标的异步下载。
func (a *App) prefetchIcons() {
	if a.icons == nil {
		return
	}
	for _, c := range a.store.Categories() {
		if c.Icon != "" {
			_ = a.icons.Get(c.Icon)
		}
	}
}

// triggerRefetch 由设置页"重新抓取"按钮触发。
//   - 切回 Loading 模式
//   - 重置 loaderState 的进度
//   - 重置内存缓存
//   - 后台启动一次新的抓取
func (a *App) triggerRefetch() {
	if a.fetchCancel != nil {
		a.fetchCancel()
	}
	a.icons.Clear()
	a.loaderState = NewLoaderState()
	a.loaderView = NewLoaderView(a.Theme, a.loaderState)
	a.resetCache = true
	a.mode = ModeLoading
	if a.Window != nil {
		a.Window.Invalidate()
	}
	go a.runFetch()
}

func (a *App) refreshDefaultVisibleTypes() {
	cats := a.store.Categories()
	// 优先采用类别自带的 defaultShow；若全部为 false，退化为：
	// 默认显示前 8 个含点位的类别，让首次启动也有可见内容。
	anyDefault := false
	for _, c := range cats {
		if c.DefaultShow {
			anyDefault = true
			break
		}
	}
	fallback := map[string]bool{}
	if !anyDefault {
		picked := 0
		for _, c := range cats {
			if picked >= 8 {
				break
			}
			if len(a.store.PointsOf(c.MarkType)) > 0 {
				fallback[c.MarkType] = true
				picked++
			}
		}
	}
	for _, c := range cats {
		if c.MarkType == "" {
			continue
		}
		chk := a.typeChks[c.MarkType]
		if chk == nil {
			chk = &widget.Bool{}
			a.typeChks[c.MarkType] = chk
		}
		on := c.DefaultShow || fallback[c.MarkType]
		if on {
			chk.Value = true
			a.mapView.SetVisibleType(c.MarkType, true)
		}
	}
}

var errSkipNoCache = simpleErr("当前指定 -no-fetch 但本地无缓存，无法启动")

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

// layout 一帧根据当前模式分发。
func (a *App) layout(gtx layout.Context) layout.Dimensions {
	switch a.mode {
	case ModeLoading:
		return a.loaderView.Layout(gtx)
	case ModeMain:
		return a.layoutMain(gtx)
	case ModeSettings:
		return a.settingsView.Layout(gtx)
	case ModeROISelect:
		return a.roiView.Layout(gtx)
	case ModeFatal:
		return a.layoutFatal(gtx)
	}
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

// layoutMain 主界面：左侧栏 + 中央地图 + 底部状态栏。
func (a *App) layoutMain(gtx layout.Context) layout.Dimensions {
	if a.mapView == nil {
		return a.layoutFatal(gtx)
	}
	// 处理图层按钮事件
	for name, btn := range a.layerBtns {
		if btn.Clicked(gtx) {
			a.mapView.SetLayer(name)
		}
	}
	// 处理类别勾选事件
	for mt, chk := range a.typeChks {
		if chk.Update(gtx) {
			a.mapView.SetVisibleType(mt, chk.Value)
		}
	}
	// 设置按钮
	if a.settingsBtn.Clicked(gtx) {
		// 进入设置页前刷新 K 编辑器为最新值（避免显示旧值）
		if a.settingsView != nil {
			a.settingsView.kEd.SetText(ftoa(a.settings.Get().WorldUnitsPerMinimapPx, 3))
		}
		a.mode = ModeSettings
	}
	if a.overlayBtn.Clicked(gtx) {
		a.ToggleOverlay()
	}
	// 工具栏事件
	a.handleToolbarEvents(gtx)

	// 记录 mapview 在根容器中的偏移，供右键上下文菜单使用。
	a.mapViewOffX = gtx.Dp(unit.Dp(280))
	a.mapViewOffY = gtx.Dp(unit.Dp(40))

	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return a.layoutToolbar(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return a.layoutSidebar(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return a.mapView.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return a.layoutStatusBar(gtx)
				}),
			)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return a.ctxMenu.Layout(gtx, a.Theme)
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			if a.colorPicker != nil {
				return a.colorPicker.Layout(gtx)
			}
			return layout.Dimensions{}
		}),
	)
}

// handleToolbarEvents 把工具栏按钮的 Clicked 状态转成 editor 操作。
func (a *App) handleToolbarEvents(gtx layout.Context) {
	if a.tbPan.Clicked(gtx) {
		a.editor.SetTool(ToolPan)
	}
	if a.tbFreedraw.Clicked(gtx) {
		a.editor.SetTool(ToolFreedraw)
	}
	if a.tbChain.Clicked(gtx) {
		a.editor.SetTool(ToolChain)
	}
	if a.tbSolveTSP.Clicked(gtx) {
		a.solveTSPAsync()
	}
	if a.tbFinishChain.Clicked(gtx) {
		if d := a.editor.FinalizeChain(); d != nil {
			a.hint = "已完成连接（" + itoa(len(d.Nodes)) + " 个节点），点保存写盘"
		}
	}
	if a.tbUndo.Clicked(gtx) {
		if !a.editor.Undo() {
			a.hint = "无可撤销的操作"
		} else {
			a.hint = "已撤销"
		}
	}
	if a.tbSaveDraft.Clicked(gtx) {
		if d := a.editor.Draft(); d != nil && len(d.Nodes) >= 2 {
			a.saveNaming = true
			defaultName := d.Name
			if defaultName == "" {
				defaultName = "新建路径"
			}
			a.saveNameEd.SetText(defaultName)
			a.saveNameEd.SetCaret(len(defaultName), len(defaultName))
			gtx.Execute(key.FocusCmd{Tag: &a.saveNameEd})
		} else {
			a.hint = "没有可保存的草稿（至少需 2 个节点）"
		}
	}
	if a.saveNameOK.Clicked(gtx) {
		a.commitSaveName()
	}
	if a.saveNameCancel.Clicked(gtx) {
		a.saveNaming = false
	}
	for {
		ev, ok := a.saveNameEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			a.commitSaveName()
		}
	}
	if a.tbClearSel.Clicked(gtx) {
		a.editor.ClearSelection()
	}
	if a.tbClearDraft.Clicked(gtx) {
		a.editor.ClearDraft()
	}
	if a.tbImport.Clicked(gtx) {
		go a.runImport()
	}
	if a.tbExport.Clicked(gtx) {
		go a.runExport()
	}
	// 备注编辑条
	if a.noteFocusPending {
		gtx.Execute(key.FocusCmd{Tag: &a.noteEd})
		a.noteFocusPending = false
	}
	if a.noteOK.Clicked(gtx) {
		a.commitNote()
	}
	if a.noteCancel.Clicked(gtx) {
		a.noteEditing = false
		a.noteCustomID = ""
	}
	for {
		ev, ok := a.noteEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			a.commitNote()
		}
	}
}

// openCustomEditor 打开"自定义点位标题"编辑条。id 为目标点位 ID。
func (a *App) openCustomEditor(gtx layout.Context, id, current string) {
	a.noteEditing = true
	a.noteCustomID = id
	a.noteEd.SetText(current)
	a.noteEd.SetCaret(len(current), len(current))
	gtx.Execute(key.FocusCmd{Tag: &a.noteEd})
}

func (a *App) commitNote() {
	if a.noteCustomID != "" && a.customs != nil {
		text := a.noteEd.Text()
		if text == "" {
			text = "自定义点位"
		}
		_ = a.customs.UpdateTitle(a.noteCustomID, text)
	}
	a.noteEditing = false
	a.noteCustomID = ""
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// placeCustomPoint 在 (wx, wy) 创建一个自定义点位并自动打开标题编辑条。
func (a *App) placeCustomPoint(gtx layout.Context, wx, wy float64, layer string) {
	if a.customs == nil {
		a.hint = "自定义点位存储未就绪"
		return
	}
	p, err := a.customs.Add(wx, wy, layer, "")
	if err != nil {
		a.hint = "添加失败：" + err.Error()
		return
	}
	// 默认让该 markType 可见。
	chk := a.typeChks[CustomMarkType]
	if chk == nil {
		chk = &widget.Bool{}
		a.typeChks[CustomMarkType] = chk
	}
	chk.Value = true
	a.mapView.SetVisibleType(CustomMarkType, true)
	a.openCustomEditor(gtx, p.ID, p.Title)
}

func (a *App) commitSaveName() {
	name := a.saveNameEd.Text()
	if name == "" {
		name = time.Now().Format("路径 0102 15:04:05")
	}
	if _, err := a.editor.SaveDraft(name); err != nil {
		a.hint = "保存失败：" + err.Error()
	} else {
		a.hint = "已保存路径：" + name
	}
	a.saveNaming = false
}

// runImport 阻塞地弹出系统打开文件对话框，导入选中的 JSON。
// 用 goroutine 包起来避免阻塞 UI 线程。
func (a *App) runImport() {
	path, err := winutil.OpenFileDialog("导入路径", "JSON 文件", "*.json", a.routes.dir)
	if err != nil || path == "" {
		return
	}
	if _, err := a.routes.ImportFile(path); err != nil {
		a.hint = "导入失败：" + err.Error()
	} else {
		a.hint = "已导入 " + filepath.Base(path)
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

func (a *App) runExport() {
	d := a.editor.Draft()
	var p *mapdata.Path
	if d != nil && len(d.Nodes) >= 2 {
		p = d
	} else if all := a.routes.All(); len(all) > 0 {
		p = all[len(all)-1]
	}
	if p == nil {
		a.hint = "无可导出的路径（先创建草稿或保存一条）"
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	defaultName := sanitize(p.Name)
	if defaultName == "" {
		defaultName = "route"
	}
	defaultName += ".json"
	target, err := winutil.SaveFileDialog("导出路径", "JSON 文件", "*.json", a.dataDir, defaultName)
	if err != nil || target == "" {
		return
	}
	if err := a.routes.ExportFile(p, target); err != nil {
		a.hint = "导出失败：" + err.Error()
	} else {
		a.hint = "已导出到 " + target
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// layoutToolbar 顶部工具栏。
//
// 布局：左侧分组（工具 / 路径动作 / 导入导出）每组前缀一个分组图标，
// 右侧固定放"悬浮"和"设置"。命名 / 备注模式覆盖整条工具栏。
func (a *App) layoutToolbar(gtx layout.Context) layout.Dimensions {
	rect := image.Rectangle{Max: image.Point{X: gtx.Constraints.Max.X, Y: gtx.Dp(unit.Dp(40))}}
	paint.FillShape(gtx.Ops, a.Theme.Panel, clip.Rect(rect).Op())

	tool := a.editor.Tool()

	// 工具按钮（带图标 + 文字 + 选中态高亮）
	mkTool := func(t Tool, label string, icon IconKey, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			s := defaultIconButton(a.Theme, label, icon)
			s.Active = (tool == t)
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return s.Layout(gtx, a.Theme, btn)
			})
		})
	}
	// 普通动作按钮
	mkAction := func(label string, icon IconKey, btn *widget.Clickable, accent bool) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			s := defaultIconButton(a.Theme, label, icon)
			if accent {
				s.Active = true
			}
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return s.Layout(gtx, a.Theme, btn)
			})
		})
	}
	// 仅文字（保留给命名 / 备注的"保存 / 取消"用）
	mkPlain := func(label string, btn *widget.Clickable, accent bool) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			s := defaultIconButton(a.Theme, label, "")
			s.Active = accent
			return layout.Inset{Right: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return s.Layout(gtx, a.Theme, btn)
			})
		})
	}
	// 分组标头：一个 dim 灰色小图标，纯装饰
	groupIcon := func(icon IconKey) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(2), Right: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return paintIcon(gtx, Icon(icon), 16, a.Theme.TextDim)
			})
		})
	}

	if a.saveNaming {
		// 命名保存模式下覆盖工具栏
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(a.Theme.Material, "路径名：")
					lbl.Color = a.Theme.Text
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(360))
					ed := material.Editor(a.Theme.Material, &a.saveNameEd, "")
					ed.Color = a.Theme.Text
					ed.Font.Typeface = "CJK"
					return ed.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				mkPlain("保存", &a.saveNameOK, true),
				mkPlain("取消", &a.saveNameCancel, false),
			)
		})
	}

	if a.noteEditing {
		return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(a.Theme.Material, "点位名称：")
					lbl.Color = a.Theme.Text
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(420))
					ed := material.Editor(a.Theme.Material, &a.noteEd, "为这个自定义点位起个名字")
					ed.Color = a.Theme.Text
					ed.Font.Typeface = "CJK"
					return ed.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				mkPlain("确定", &a.noteOK, true),
				mkPlain("取消", &a.noteCancel, false),
			)
		})
	}

	// 顶部工具栏：左 = 工具 / 路径动作 / 导入导出（带分组图标）；
	//              右 = 悬浮 / 设置（固定靠右，便于全局访问）
	return layout.Inset{Left: unit.Dp(8), Right: unit.Dp(8), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			// 工具组
			groupIcon(IconBrush),
			mkTool(ToolPan, "选择 / 框选", IconSelect, &a.tbPan),
			mkTool(ToolFreedraw, "画笔", IconBrush, &a.tbFreedraw),
			mkTool(ToolChain, "连接", IconChain, &a.tbChain),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			// 路径动作组
			groupIcon(IconDirections),
			mkAction("最短路径", IconDirections, &a.tbSolveTSP, true),
			mkAction("完成连接", IconCheck, &a.tbFinishChain, false),
			mkAction("保存", IconSave, &a.tbSaveDraft, false),
			mkAction("撤销", IconUndo, &a.tbUndo, false),
			mkAction("清空选择", IconClear, &a.tbClearSel, false),
			mkAction("清空草稿", IconClear, &a.tbClearDraft, false),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			// 导入 / 导出 组
			groupIcon(IconFolder),
			mkAction("导入路径", IconImport, &a.tbImport, false),
			mkAction("导出路径", IconExport, &a.tbExport, false),
			// 弹性间距 → 把右侧按钮推到最右边
			layout.Flexed(1, layout.Spacer{}.Layout),
			// 悬浮窗 / 设置（顶部右侧，全局可见）
			mkAction("悬浮", IconOverlay, &a.overlayBtn, false),
			mkAction("设置", IconSettings, &a.settingsBtn, false),
		)
	})
}

// openMapContextMenu 由 MapView 在右键时调用。注意 sx,sy 是 mapview 局部坐标，
// 需要加上 mapview 在窗口中的偏移（toolbar + sidebar）才是上下文菜单坐标系。
func (a *App) openMapContextMenu(sx, sy float32, wx, wy float64) {
	a.ctxWorldX = wx
	a.ctxWorldY = wy
	// mapview 在窗口里的偏移：sidebar + toolbar。
	x := sx + float32(a.mapViewOffX)
	y := sy + float32(a.mapViewOffY)

	editor := a.editor
	r2 := 0.0
	if a.mapView != nil {
		r := a.mapView.screenRadiusToWorld(14)
		r2 = r * r
	}
	hasSel := len(editor.Selection()) > 0
	hitSel := r2 > 0 && editor.IsSelectedAt(wx, wy, r2)

	// 检测右键是否落在已保存路径 / 草稿 / 自由绘制段的某个节点附近。
	hitRouteID, hitNodeIdx, _ := nearestEditableNode(a.editor, a.routes, wx, wy, r2)
	hitRoute := hitRouteID != ""

	// 检查右键位置附近是否命中一个"自定义"点位（user_custom）。
	hitCustomID := ""
	hitCustomTitle := ""
	if a.customs != nil && r2 > 0 {
		visibleCustom := map[string]bool{CustomMarkType: true}
		if hp, ok := a.store.NearestVisibleWiki(wx, wy, r2, visibleCustom); ok {
			hitCustomID = hp.ID
			if p, ok2 := a.customs.Find(hp.ID); ok2 {
				hitCustomTitle = p.Title
			}
		}
	}
	layer := a.mapView.Layer()

	items := []ContextMenuItem{
		{
			Label: "在此添加点位",
			Action: func() {
				if a.Window != nil {
					a.Window.Invalidate()
				}
				// 用一个占位 Layout context 调用 openCustomEditor 不可行 ——
				// gtx 只在帧内有效。这里通过设置 noteEditing / noteCustomID
				// 等状态，下一帧会拉起焦点。
				if a.customs == nil {
					a.hint = "自定义点位存储未就绪"
					return
				}
				p, err := a.customs.Add(wx, wy, layer, "")
				if err != nil {
					a.hint = "添加失败：" + err.Error()
					return
				}
				chk := a.typeChks[CustomMarkType]
				if chk == nil {
					chk = &widget.Bool{}
					a.typeChks[CustomMarkType] = chk
				}
				chk.Value = true
				if a.mapView != nil {
					a.mapView.SetVisibleType(CustomMarkType, true)
				}
				a.noteEditing = true
				a.noteCustomID = p.ID
				a.noteEd.SetText(p.Title)
				a.noteEd.SetCaret(len(p.Title), len(p.Title))
				a.noteFocusPending = true
				if a.Window != nil {
					a.Window.Invalidate()
				}
			},
		},
		{
			Label:   "重命名此点位",
			Disable: hitCustomID == "",
			Action: func() {
				if hitCustomID == "" {
					return
				}
				a.noteEditing = true
				a.noteCustomID = hitCustomID
				a.noteEd.SetText(hitCustomTitle)
				a.noteEd.SetCaret(len(hitCustomTitle), len(hitCustomTitle))
				a.noteFocusPending = true
				if a.Window != nil {
					a.Window.Invalidate()
				}
			},
		},
		{
			Label:   "删除此点位",
			Disable: hitCustomID == "",
			Action: func() {
				if hitCustomID == "" || a.customs == nil {
					return
				}
				_ = a.customs.Remove(hitCustomID)
				if a.Window != nil {
					a.Window.Invalidate()
				}
			},
		},
		{Sep: true},
		{
			Label:   "设为起点",
			Disable: !hitSel,
			Action: func() {
				if !editor.SetEndpointRoleAt(wx, wy, r2, RoleStart) {
					a.hint = "右键位置附近无选中节点"
				}
			},
		},
		{
			Label:   "设为终点",
			Disable: !hitSel,
			Action: func() {
				if !editor.SetEndpointRoleAt(wx, wy, r2, RoleEnd) {
					a.hint = "右键位置附近无选中节点"
				}
			},
		},
		{
			Label:   "取消端点角色",
			Disable: !hitSel,
			Action: func() {
				editor.SetEndpointRoleAt(wx, wy, r2, RoleNone)
			},
		},
		{
			Label:   "移出选区",
			Disable: !hitSel,
			Action: func() {
				editor.RemoveSelectionAt(wx, wy, r2)
			},
		},
		{Sep: true},
		{
			Label:   "规划最短路径",
			Disable: !hasSel,
			Action: func() {
				a.solveTSPAsync()
			},
		},
		{
			Label:   "完成当前连接",
			Disable: editor.Draft() == nil,
			Action: func() {
				if d := editor.FinalizeChain(); d != nil {
					a.hint = "已完成连接（" + itoa(len(d.Nodes)) + " 个节点）"
				}
			},
		},
		{Sep: true},
		{
			Label: "校准：玩家在这里（精确）",
			Action: func() {
				a.CalibrateAtWorldAsync(wx, wy)
			},
		},
		{Sep: true},
		{Label: "撤销 (Ctrl+Z)", Action: func() { editor.Undo() }},
		{Label: "清空选区", Disable: !hasSel, Action: func() { editor.ClearSelection() }},
		{Label: "清空草稿", Disable: editor.Draft() == nil, Action: func() { editor.ClearDraft() }},
	}

	// 若右键命中已保存路径 / 草稿 / 自由绘制段的某个节点，在菜单顶部追加
	// "编辑节点"子项。三类目标用同一组 action，差别仅在持久化层（routeID）。
	if hitRoute {
		targetLabel := "已保存路径"
		switch hitRouteID {
		case editTargetDraft:
			targetLabel = "草稿"
		case editTargetFreedraw:
			targetLabel = "自由绘制段"
		}
		editItems := []ContextMenuItem{
			{
				Label: "编辑" + targetLabel + "：移动此节点",
				Action: func() {
					a.pathEdit = pathEditOp{Active: true, RouteID: hitRouteID, AnchorIdx: hitNodeIdx, Action: "move"}
					a.hint = "下一次左键点击处会把该节点移到那里"
				},
			},
			{
				Label: "编辑" + targetLabel + "：在此节点之前插入新节点",
				Action: func() {
					a.pathEdit = pathEditOp{Active: true, RouteID: hitRouteID, AnchorIdx: hitNodeIdx, Action: "insertBefore"}
					a.hint = "下一次左键点击处会在该节点之前插入新节点"
				},
			},
			{
				Label: "编辑" + targetLabel + "：在此节点之后插入新节点",
				Action: func() {
					a.pathEdit = pathEditOp{Active: true, RouteID: hitRouteID, AnchorIdx: hitNodeIdx, Action: "insertAfter"}
					a.hint = "下一次左键点击处会在该节点之后插入新节点"
				},
			},
			{
				Label: "编辑" + targetLabel + "：删除此节点",
				Action: func() {
					a.applyPathEdit(hitRouteID, hitNodeIdx, "delete", 0, 0)
				},
			},
			{Sep: true},
		}
		items = append(editItems, items...)
	}
	a.ctxMenu.Show(x, y, items)
}

// handlePathEditClick 是 MapView.OnLeftClickWorld 注入的回调；当 a.pathEdit
// 处于活动状态（move / insertBefore / insertAfter）时，把下一次左键的世界
// 坐标作为参数应用到目标路径节点；返回 true 表示已消费该次点击。
//
// delete 不需要后续点击，所以这里只处理需要落点的三种 action。
func (a *App) handlePathEditClick(wx, wy float64) bool {
	op := a.pathEdit
	if !op.Active {
		return false
	}
	switch op.Action {
	case "move", "insertBefore", "insertAfter":
		a.applyPathEdit(op.RouteID, op.AnchorIdx, op.Action, wx, wy)
		return true
	}
	return false
}

// applyPathEdit 在已保存路径 / 草稿 / 自由绘制累计段上执行一次节点编辑
// （move / delete / insertBefore / insertAfter）。delete 之外的操作需要 (px, py)。
//
// routeID 取值：
//   - editTargetDraft     → 走 PathEditor.EditDraftNode（不落盘，因为 draft 还没保存）
//   - editTargetFreedraw  → 走 PathEditor.EditFreedrawNode
//   - 其它                → 在 a.routes 里找对应 *mapdata.Path 并 Save
func (a *App) applyPathEdit(routeID string, anchorIdx int, action string, px, py float64) {
	// 草稿（chain / TSP / 已 flush 的 freedraw）
	if routeID == editTargetDraft {
		if a.editor == nil || !a.editor.EditDraftNode(anchorIdx, action, px, py) {
			a.hint = "编辑失败：草稿不存在或无法删除（至少 2 个节点）"
			return
		}
		a.hint = "草稿已更新"
		a.pathEdit.Clear()
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	// 自由绘制累计段（用户已松开但未 flush）
	if routeID == editTargetFreedraw {
		if a.editor == nil || !a.editor.EditFreedrawNode(anchorIdx, action, px, py) {
			a.hint = "编辑失败：累计段不存在或无法删除（至少 2 个节点）"
			return
		}
		a.hint = "自由绘制段已更新"
		a.pathEdit.Clear()
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	// 已保存路径
	if a.routes == nil {
		return
	}
	p := a.routes.Find(routeID)
	if p == nil || anchorIdx < 0 || anchorIdx >= len(p.Nodes) {
		a.hint = "编辑失败：路径或节点不存在"
		return
	}
	layer := p.Nodes[anchorIdx].Layer
	switch action {
	case "delete":
		if len(p.Nodes) <= 2 {
			a.hint = "无法删除：路径至少需要 2 个节点"
			return
		}
		p.Nodes = append(p.Nodes[:anchorIdx], p.Nodes[anchorIdx+1:]...)
	case "move":
		p.Nodes[anchorIdx].X = px
		p.Nodes[anchorIdx].Y = py
	case "insertBefore":
		newNode := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		p.Nodes = append(p.Nodes[:anchorIdx], append([]mapdata.PathNode{newNode}, p.Nodes[anchorIdx:]...)...)
	case "insertAfter":
		newNode := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		ins := anchorIdx + 1
		p.Nodes = append(p.Nodes[:ins], append([]mapdata.PathNode{newNode}, p.Nodes[ins:]...)...)
	default:
		return
	}
	if err := a.routes.Save(p); err != nil {
		a.hint = "保存失败：" + err.Error()
		return
	}
	a.hint = "路径已更新（" + itoa(len(p.Nodes)) + " 个节点）"
	a.pathEdit.Clear()
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

func (a *App) layoutSidebar(gtx layout.Context) layout.Dimensions {
	if a.sidebarToggle.Clicked(gtx) {
		a.sidebarCollapsed = !a.sidebarCollapsed
	}
	if a.sidebarCollapsed {
		return a.layoutSidebarCollapsed(gtx)
	}
	gtx.Constraints.Max.X = gtx.Dp(unit.Dp(280))
	gtx.Constraints.Min.X = gtx.Dp(unit.Dp(280))

	// 背景
	rect := image.Rectangle{Max: gtx.Constraints.Max}
	paint.FillShape(gtx.Ops, a.Theme.Panel, clip.Rect(rect).Op())

	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// 顶部行：图层标题 + 收起按钮
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return paintIcon(gtx, Icon(IconLayers), 18, a.Theme.Text)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.H6(a.Theme.Material, "图层")
						lbl.Color = a.Theme.Text
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						s := defaultIconButton(a.Theme, "收起侧边栏", IconChevronL)
						return s.Layout(gtx, a.Theme, &a.sidebarToggle)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			// 图层按钮
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return a.layoutLayerButtons(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			// 路径标题
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return paintIcon(gtx, Icon(IconDirections), 18, a.Theme.Text)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.H6(a.Theme.Material, "路径")
						lbl.Color = a.Theme.Text
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			// 路径列表（最多占 1/3 高度）
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.Y = gtx.Constraints.Max.Y / 3
				return a.pathList.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(14)}.Layout),
			// 类别标题
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return paintIcon(gtx, Icon(IconPin), 18, a.Theme.Text)
					}),
					layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.H6(a.Theme.Material, "点位类别")
						lbl.Color = a.Theme.Text
						lbl.Font.Weight = font.Bold
						return lbl.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			// 类别列表（按 Type 折叠分组）
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return a.categoryList.Layout(gtx)
			}),
		)
	})
}

func (a *App) layoutSidebarCollapsed(gtx layout.Context) layout.Dimensions {
	w := gtx.Dp(unit.Dp(36))
	gtx.Constraints.Max.X = w
	gtx.Constraints.Min.X = w
	rect := image.Rectangle{Max: image.Point{X: w, Y: gtx.Constraints.Max.Y}}
	paint.FillShape(gtx.Ops, a.Theme.Panel, clip.Rect(rect).Op())
	return layout.UniformInset(unit.Dp(4)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		// 折叠态：只放一个"展开侧边栏"图标按钮
		s := defaultIconButton(a.Theme, "", IconChevronR)
		s.IconSize = 18
		return s.Layout(gtx, a.Theme, &a.sidebarToggle)
	})
}

func (a *App) layoutLayerButtons(gtx layout.Context) layout.Dimensions {
	layers := a.store.Layers()
	children := []layout.FlexChild{}
	for _, ly := range layers {
		ly := ly
		btn, ok := a.layerBtns[ly.Name]
		if !ok {
			btn = &widget.Clickable{}
			a.layerBtns[ly.Name] = btn
		}
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(a.Theme.Material, btn, displayLayerName(ly.Name))
			if a.mapView.Layer() == ly.Name {
				b.Background = a.Theme.Accent
			} else {
				b.Background = a.Theme.BG
				b.Color = a.Theme.Text
			}
			return b.Layout(gtx)
		}))
		children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
}

func displayLayerName(n string) string {
	switch n {
	case "G":
		return "地表 G"
	case "B1":
		return "地下 B1"
	case "B2":
		return "地下 B2"
	}
	return n
}

func (a *App) layoutCategoryList(gtx layout.Context) layout.Dimensions {
	cats := a.store.Categories()
	return material.List(a.Theme.Material, &a.scrollCats).Layout(gtx, len(cats), func(gtx layout.Context, i int) layout.Dimensions {
		c := cats[i]
		chk, ok := a.typeChks[c.MarkType]
		if !ok {
			chk = &widget.Bool{}
			a.typeChks[c.MarkType] = chk
		}
		return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := c.MarkTypeName + " (" + c.Type + ")"
			pts := a.store.PointsOf(c.MarkType)
			if len(pts) > 0 {
				lbl += "  " + countTag(len(pts))
			}
			cb := material.CheckBox(a.Theme.Material, chk, lbl)
			cb.Color = a.Theme.Text
			cb.Font.Typeface = "CJK"
			return cb.Layout(gtx)
		})
	})
}

func countTag(n int) string { return "[" + itoa(n) + "]" }

// headingToDeg 弧度 → 度（[0, 360)）。
func headingToDeg(h float64) float64 {
	d := h * 180.0 / 3.14159265358979323846
	for d < 0 {
		d += 360
	}
	for d >= 360 {
		d -= 360
	}
	return d
}

func (a *App) layoutStatusBar(gtx layout.Context) layout.Dimensions {
	h := gtx.Dp(unit.Dp(24))
	gtx.Constraints.Max.Y = h
	gtx.Constraints.Min.Y = h
	rect := image.Rectangle{Max: image.Point{X: gtx.Constraints.Max.X, Y: h}}
	paint.FillShape(gtx.Ops, a.Theme.Panel, clip.Rect(rect).Op())
	// 关键：状态栏文本可能很长（图层 + 坐标 + 选区 + 导航 + NCC + 朝向…），
	// 窗口宽度不足时如果让 material.Label 自动换行就会换到下一行越过条带高度，
	// 视觉上是"被切掉一半"。这里把 MaxLines 锁成 1，并加一个 clip.Rect 裁剪，
	// 多余的内容会被截断而不是换行。
	return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		clipStack := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
		defer clipStack.Pop()
		txt := a.mapView.StatusLine()
		if a.hint != "" {
			txt += "  ·  " + a.hint
		}
		// 选区计数
		if n := len(a.editor.Selection()); n > 0 {
			txt += "  ·  选中 " + itoa(n)
		}
		// 导航状态
		if act, routeID, fix, has, prog := a.nav.snapshot(); act {
			rt := a.routes.Find(routeID)
			if routeID == "" {
				txt += "  ·  追踪"
			} else {
				name := routeID
				if rt != nil {
					name = rt.Name
				}
				txt += "  ·  导航 " + name
			}
			a.nav.mu.Lock()
			lost := a.nav.lost
			a.nav.mu.Unlock()
			if lost {
				txt += "  · ⚠ 已丢失定位"
			} else if has {
				if rt != nil && len(rt.Nodes) >= 2 {
					txt += "  · 进度 " + ftoa(prog.Traveled, 0) + "/" + ftoa(prog.Total, 0)
				}
				if fix.HasHeading {
					txt += "  · 朝向 " + ftoa(headingToDeg(fix.Heading), 0) + "°"
				}
				txt += "  · NCC " + ftoa(fix.Confidence, 2)
				if rt != nil && len(rt.Nodes) >= 2 {
					if next, dist := NextWaypoint(rt.Nodes, prog); next >= 0 {
						txt += "  · 下一点 " + itoa(next+1) + "(" + ftoa(dist, 0) + ")"
					}
				} else {
					txt += "  · (" + ftoa(fix.WorldX, 0) + "," + ftoa(fix.WorldY, 0) + ")"
				}
			} else {
				txt += "  · 等待定位"
			}
		}
		lbl := material.Caption(a.Theme.Material, txt)
		lbl.Color = a.Theme.TextDim
		lbl.MaxLines = 1
		lbl.Font.Typeface = "CJK"
		return lbl.Layout(gtx)
	})
}

func (a *App) layoutFatal(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, a.Theme.BG, clip.Rect{Max: gtx.Constraints.Max}.Op())
	msg := "未知错误"
	if a.fatalErr != nil {
		msg = a.fatalErr.Error()
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.H5(a.Theme.Material, "无法启动")
				lbl.Color = a.Theme.Err
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body1(a.Theme.Material, msg)
				lbl.Color = a.Theme.Text
				return lbl.Layout(gtx)
			}),
		)
	})
}

// 占位以避免某些导入未使用警告
var (
	_ = color.NRGBA{}
)
