package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"log"
	"math"
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

	// 全量瓦片下载状态（DownloadAllTilesAsync）。
	// progress 形如 "已下载 1234 / 65536 (3.2 GB)"；done=true 时显示完成。
	tilesDL tilesDLState
}

// tilesDLState 全量瓦片下载状态机。
type tilesDLState struct {
	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	total    int
	done     int
	skipped  int
	failed   int
	bytes    int64
	finished bool
	err      string
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

	// 加载 data/calibration.json 覆盖 Settings.WorldUnitsPerMinimapPx。
	// 避免用户在 -cmd test-locator 里扫出 K=0.96 但 UI 启动时仍用默认 K=0.5
	// —— 这是"无论怎么校准 sharp 都很低"的常见原因。
	calPath := filepath.Join(dataDir, "calibration.json")
	if cal, ok, cerr := locator.LoadCalibration(calPath); cerr == nil && ok && cal.K > 0 {
		cur := settings.Get().WorldUnitsPerMinimapPx
		if math.Abs(cur-cal.K) > 1e-6 {
			log.Printf("[calibration] 从 %s 加载 K=%.3f（原 Settings=%.3f，覆盖）", calPath, cal.K, cur)
			settings.Update(func(s *Settings) { s.WorldUnitsPerMinimapPx = cal.K })
		}
	} else if cerr != nil {
		log.Printf("[calibration] 读取 %s 失败: %v", calPath, cerr)
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

	// 速度统计：用 |velX,velY| 的 EMA 给"典型移动速度"建模，供异常移动检测用。
	speedEMA      float64 // 平滑速度估计
	speedEMAReady bool

	// 后台多种子重定位的节流 / 防重入
	bgRelocateRunning bool
	bgRelocateLastAt  time.Time
	// bgFailStreak 周期 bg 连续失败次数（coarse / verify 任一失败计一次）。
	// 每次 runBgReLocalize 根据此值决定 coarse 搜索半径：
	//   - 0~2 次失败：r=200（轻量）
	//   - 3~5 次失败：r=400
	//   - ≥6 次失败：r=600（封顶；玩家可能远离所有 seed 候选）
	// 成功（verify sharp ≥ 0.30）时清零；让 bg 能自动从小半径开始恢复。
	bgFailStreak int
	// bgRelocateConfirmAt 最近一次后台重定位"高置信度确认"的墙钟时间。
	// 被 Task 4 的异常移动拒绝逻辑用来判断："刚被 bg-relocate 认可为 (X,Y)，
	// 所以即使主匹配给出远距离跳变也可信（传送场景）"。
	bgRelocateConfirmAt time.Time
	bgRelocateConfirmX  float64
	bgRelocateConfirmY  float64

	// periodicRelocateCancel 控制 10s 周期 bg 重定位 goroutine 停止。
	periodicRelocateCancel context.CancelFunc

	// snapFreezeUntil / snapFreezeX/Y 用户校准"玩家在这里"后的显示冻结期。
	// 在此时间之前，handleNavFix 会把任何新 fix 的显示位置强制回滚到
	// (snapFreezeX, snapFreezeY)，避免用户校准后立刻被运行期 matcher 略偏
	// 的新结果推走造成视觉瞬移。
	snapFreezeUntil time.Time
	snapFreezeX     float64
	snapFreezeY     float64

	// lastFrameHash 主匹配上一帧 ROI 截图的哈希。玩家完全不动时，连续帧的
	// minimap 截图字节级一致，hash 命中即跳过整次 EdgeMatcher.Match（节省
	// ~100 ms/帧），直接复用 lastFrameFix。仅在上次 fix 高置信度（≥ 0.30）
	// 时启用，避免低置信度数据被冻结放大。
	//
	// 任何 UI 变化（视野锥旋转、玩家箭头闪烁、弹窗叠加）都会让字节序列改变
	// → hash 不命中 → 正常 Match → 安全。
	lastFrameHash      uint64
	lastFrameFix       locator.Fix
	lastFrameHashValid bool

	// lastBgFrameHash 周期 bg 重定位的 ROI 截图哈希。命中则直接 return，
	// 不重跑多种子粗搜 + verify（节省 ~1~3 s）。
	lastBgFrameHash      uint64
	lastBgFrameHashValid bool
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

// imgHashFNV64 对 RGBA 的像素字节做 FNV-1a 64bit 哈希。~0.2 ms for 230×230×4。
// 玩家完全不动时连续帧字节级一致，hash 命中 → 跳过 Match / bg 全流程。
func imgHashFNV64(img *image.RGBA) uint64 {
	h := fnv.New64a()
	h.Write(img.Pix)
	return h.Sum64()
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

	if cfg.MinimapROI.Empty() {
		a.hint = "导航失败：未设置小地图 ROI（请到设置 → 在屏幕上框选）"
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
		// 预抓 seed 周围 z=NavSearchZoom 的瓦片（5×5），让首帧 Match 不空 cov
		a.prefetchTilesForLocator(seedX, seedY, 2)
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
		// 预抓 seed 周围 5×5 瓦片
		a.prefetchTilesForLocator(seedX, seedY, 2)
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

// startMatcherLoopAsync 在后台线程做首帧定位 + 启动匹配 loop；被 StartTracking /
// StartNavigation 共同使用。routeID="" 且 p=nil 表示纯追踪；否则是导航。
//
// 关键：首帧定位用 EdgeMatcher.MatchMultiSeed（和运行期一致的匹配管线），
// 门槛 sharp ≥ 0.30。未达标 *不* 采信结果（不写 nav.fix），直接以旧 seed
// 启动 loop，由运行期 + 6s 周期 bg 重定位兜底。
//
// 旧实现用 NCC Matcher 在启动期广域扫描，门槛低（0.35/0.40），常在纯色区域
// 误命中假峰，把玩家定到海面 / 草地上，后续 EdgeMatcher 从错位开始再也回不来。
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

	// 组装候选 seeds：提供的 seed 优先，然后 collectReLocalizeSeeds，然后路径节点
	seeds := []locator.Fix{{WorldX: seedX, WorldY: seedY}}
	seeds = append(seeds, a.collectReLocalizeSeeds()...)
	if p != nil {
		for i := 0; i < len(p.Nodes); i += len(p.Nodes)/8 + 1 {
			seeds = append(seeds, locator.Fix{WorldX: p.Nodes[i].X, WorldY: p.Nodes[i].Y})
		}
		seeds = append(seeds, locator.Fix{WorldX: p.Nodes[len(p.Nodes)-1].X, WorldY: p.Nodes[len(p.Nodes)-1].Y})
	}

	em := locator.NewEdgeMatcher(mosaic, cfg.WorldUnitsPerMinimapPx)
	em.SearchZoom = zoom
	em.SearchRadiusMinPx = 200
	em.SearchRadiusMaxPx = 200
	em.MinSharpness = -1
	em.HeadingDetect = false
	em.DebugLog = cfg.DebugLog
	em.Downsample = 2          // 降采样粗搜，~16× 加速
	em.EarlyStopSharp = 0.55   // 找到很高信心立即停
	t0 := time.Now()
	firstFix, allSharp, mErr := em.MatchMultiSeed(img, seeds)
	log.Printf("[start] edge multi-seed: sharp=%.3f at (%.0f, %.0f) took %v; all=%v; err=%v",
		firstFix.Confidence, firstFix.WorldX, firstFix.WorldY, time.Since(t0).Round(time.Millisecond), allSharp, mErr)

	gotFirst := mErr == nil && firstFix.Confidence >= 0.30
	if gotFirst {
		seedX, seedY = firstFix.WorldX, firstFix.WorldY
		a.nav.mu.Lock()
		a.nav.fix = firstFix
		a.nav.fix.HasHeading = false
		a.nav.hasFix = true
		a.nav.lost = false
		// 记为"刚被 bg 确认"，让 plausibility 在启动后的头 12s 不做误拒
		a.nav.bgRelocateConfirmAt = time.Now()
		a.nav.bgRelocateConfirmX = firstFix.WorldX
		a.nav.bgRelocateConfirmY = firstFix.WorldY
		a.nav.mu.Unlock()
	} else {
		log.Printf("[start] first-frame sharp low (< 0.30); starting loop with persisted seed, bg will relocalize")
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
	if a.nav.periodicRelocateCancel != nil {
		a.nav.periodicRelocateCancel()
		a.nav.periodicRelocateCancel = nil
	}
	a.nav.active = true
	a.nav.routeID = routeID
	a.nav.loc = loc
	a.nav.startedAt = time.Now()
	// 新 session 清理：上一次会话的 progress / bgFailStreak / 截图哈希都可能和
	// 新路径 / 新位置不兼容。
	a.nav.progress = NavProgress{}
	a.nav.bgFailStreak = 0
	a.nav.lastFrameHashValid = false
	a.nav.lastBgFrameHashValid = false
	pCtx, pCancel := context.WithCancel(context.Background())
	a.nav.periodicRelocateCancel = pCancel
	a.nav.mu.Unlock()
	if err := loc.Start(); err != nil {
		a.hint = "启动失败：" + err.Error()
		a.nav.mu.Lock()
		a.nav.active = false
		a.nav.loc = nil
		a.nav.hasFix = false
		a.nav.progress = NavProgress{}
		a.nav.bgFailStreak = 0
		if a.nav.periodicRelocateCancel != nil {
			a.nav.periodicRelocateCancel()
			a.nav.periodicRelocateCancel = nil
		}
		a.nav.mu.Unlock()
		if a.Window != nil {
			a.Window.Invalidate()
		}
		return
	}
	go a.runPeriodicRelocate(pCtx)
	label := "追踪"
	if routeID != "" {
		label = "导航"
		if p != nil {
			label = "导航：" + p.Name
		}
	}
	if gotFirst {
		a.hint = fmt.Sprintf("已开始%s（sharp=%.2f at (%.0f,%.0f)，K=%.3f）",
			label, firstFix.Confidence, firstFix.WorldX, firstFix.WorldY, cfg.WorldUnitsPerMinimapPx)
	} else {
		a.hint = fmt.Sprintf("已启动%s loop（首帧 sharp=%.2f 低；等待后台重定位或手动校准）",
			label, firstFix.Confidence)
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// buildRealMatcher 构造导航期使用的匹配器。
//
// 实现：使用 EdgeMatcher（基于 Sobel 边缘 + 掩码 SSD + 尖锐度置信度）。
//
// 两档处理：
//   - sharp ≥ 0.30：高信心，UpdateSeed，正常返回 → OnFix → handleNavFix
//   - sharp <  0.30：低信心，标 Tentative 并返回 err → 走 OnErr → applyNavFallback，
//                    不写入 nav.fix，不污染 speedEMA / LastPlayer。
//
// 职责分工（修复"追踪几十秒无反应"的 regression）：
//   - **主匹配**：小半径（60~200）快速跟踪，单次 Match ~100ms，跟得上 400ms loop
//   - **bg 兜底**：navState.bgFailStreak 自适应扩到 600，6s 周期，独立 goroutine
//
// 此前错误尝试 MinSharpness=0.30 启用 EdgeMatcher 内部自适应半径 —— 它会在
// 连续失败时把 radius 扩到 MaxPx=600，导致单次 Match 耗时 1~2s，主匹配 loop
// 完全堵塞。大半径搜索应该由 bg goroutine 独立承担，不应挤占主匹配资源。
//
// at-boundary 修复仍保留：EdgeMatcher.Match 里 sharp≥MinSharpness 但命中边界
// 时返回 nil err（原来返回 err 导致 wrapper 丢弃，玩家高速移动时卡住）。
//
// 日志节流到每 10 秒一条。
func (a *App) buildRealMatcher(cfg Settings, seedX, seedY float64) locator.MatchFn {
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}
	searchZoom := cfg.NavSearchZoom
	if searchZoom <= 0 {
		searchZoom = 8
	}
	m := locator.NewEdgeMatcher(mosaic, cfg.WorldUnitsPerMinimapPx)
	m.SearchZoom = searchZoom
	m.SearchRadiusMinPx = 60
	m.SearchRadiusMaxPx = 200 // 主匹配上限；更大的搜索由 bg 承担
	m.MinSharpness = -1       // wrapper 自判 sharp。启用 0.30 会让 radius 扩到 MaxPx 拖慢主 loop
	m.HeadingDetect = true
	m.DebugLog = cfg.DebugLog
	m.SetSeed(seedX, seedY)
	var lastLowLogAt time.Time
	var lowCount int
	return func(roi *image.RGBA) (locator.Fix, error) {
		// 截图哈希跳过：玩家完全不动时 minimap 字节级一致 → 直接复用上次
		// 高置信度 fix，跳过 Match（~100 ms → ~0.2 ms）。任何 UI 或玩家
		// 变化（视野锥旋转 / 箭头闪烁）都会让 hash 变，安全。
		h := imgHashFNV64(roi)
		a.nav.mu.Lock()
		if a.nav.lastFrameHashValid && a.nav.lastFrameHash == h && a.nav.lastFrameFix.Confidence >= 0.30 {
			cached := a.nav.lastFrameFix
			a.nav.mu.Unlock()
			return cached, nil
		}
		a.nav.mu.Unlock()

		f, err := m.Match(roi)
		sharp := m.LastDebug.Sharpness
		if err == nil && sharp >= 0.30 {
			m.UpdateSeed(f)
			lowCount = 0
			a.nav.mu.Lock()
			a.nav.lastFrameHash = h
			a.nav.lastFrameFix = f
			a.nav.lastFrameHashValid = true
			a.nav.mu.Unlock()
			return f, nil
		}
		// 低尖锐度或 err：nav.fix 不被覆盖；bg 6s 周期 + applyNavFallback 触发
		// 的 requestBgReLocalize 会兜底。
		// hash 缓存不更新（只缓存高置信度成功），保持上次的 cached fix 可用 —
		// 下一帧如果玩家静止 + minimap 相同仍然可跳过到上次成功那帧。
		lowCount++
		if cfg.DebugLog && time.Since(lastLowLogAt) > 10*time.Second {
			log.Printf("[edge-matcher] 连续 %d 帧 sharp<0.30（last sharp=%.3f err=%v）；等待 bg 兜底",
				lowCount, sharp, err)
			lastLowLogAt = time.Now()
			lowCount = 0
		}
		if err == nil {
			err = fmt.Errorf("edge: sharp %.2f < 0.30", sharp)
		}
		f.Tentative = true
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

// CalibrateAtWorld 用用户在地图上右键的世界坐标作为玩家真实位置，同时：
//  1. 以 (wx, wy) 为真值在 K ∈ [0.50, 1.00] step 0.02 扫一遍，找出尖锐度最高的 K
//  2. 如果扫出的 K 显著优于当前 K（或者当前 K 下 sharp < 0.30），写回 Settings
//     并持久化到 data/calibration.json（供下次启动自动加载）
//  3. 把玩家位置锁到 (wx, wy)，启动 3 秒显示冻结 + 12 秒 bgRelocateConfirm 窗口
//  4. 如果追踪在跑，用新 K / 新 seed 重建 matcher 并热替换
//
// 为什么要扫 K：
//   这游戏的正确 K ≈ 0.96（minimap 缩放 ≈ wiki z=7.3），但 Settings 默认值是 0.5。
//   如果用户没跑过 -cmd test-locator（或跑了但没被应用加载），运行期就用 K=0.5，
//   此时即使玩家位置完全正确，sharp 也只有 0.03~0.15 —— 这是"无论怎么校准 sharp
//   都 < 0.1"的真正原因（算法本身没问题，K 一错全错）。
//
//   既然用户已经声明了 (wx, wy) 是真值，这就是最理想的 K 校准时机 —— 有真值，
//   扫一遍 K 选 sharp 最高的就是正确 K。
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
	// 预抓 (wx,wy) 附近 5×5 瓦片：扫 K 阶段会用到 z=NavSearchZoom 的 mosaic
	a.prefetchTilesForLocator(wx, wy, 2)

	// 步骤 1：当前 K 快速评估
	probe := func(k float64) float64 {
		em := locator.NewEdgeMatcher(mosaic, k)
		em.SearchZoom = zoom
		em.SearchRadiusMinPx = 60
		em.SearchRadiusMaxPx = 60
		em.MinSharpness = -1
		em.HeadingDetect = false
		em.AllowMissingCircle = true
		em.SetSeed(wx, wy)
		_, _ = em.Match(img)
		return em.LastDebug.Sharpness
	}
	curK := cfg.WorldUnitsPerMinimapPx
	curSharp := probe(curK)
	log.Printf("[calibrate-here] 当前 K=%.3f at (%.0f,%.0f) sharp=%.3f", curK, wx, wy, curSharp)

	bestK := curK
	bestSharp := curSharp
	scanned := false

	// 步骤 2：当前 K 明显不够好 → 扫 K 找正确值
	if curSharp < 0.30 {
		scanned = true
		log.Printf("[calibrate-here] 当前 K 下 sharp 偏低，扫 K ∈ [0.50, 1.00] step 0.02…")
		cal, cerr := locator.CalibrateK(mosaic, img, wx, wy, 0.50, 1.00, 0.02, zoom, 60, func(k, s float64) {
			if cfg.DebugLog {
				log.Printf("    K=%.2f sharp=%.3f", k, s)
			}
		})
		if cerr != nil {
			log.Printf("[calibrate-here] 扫 K 失败: %v", cerr)
		} else {
			log.Printf("[calibrate-here] 扫 K 最佳：K=%.3f sharp=%.3f (当前 K=%.3f sharp=%.3f)",
				cal.K, cal.BestSharpness, curK, curSharp)
			if cal.BestSharpness > bestSharp+0.05 {
				bestK = cal.K
				bestSharp = cal.BestSharpness
			}
		}
	}

	// 步骤 3：无条件锁定用户位置
	a.nav.mu.Lock()
	a.nav.fix.WorldX = wx
	a.nav.fix.WorldY = wy
	a.nav.fix.Confidence = bestSharp
	a.nav.hasFix = true
	a.nav.lost = false
	a.nav.bgRelocateConfirmAt = time.Now()
	a.nav.bgRelocateConfirmX = wx
	a.nav.bgRelocateConfirmY = wy
	a.nav.bgFailStreak = 0 // 用户手动校准了正确位置 → 让 bg 从小半径重新开始
	// 清空截图哈希：位置被强制改到 (wx, wy)，旧 hash 对应的 fix 已失效
	a.nav.lastFrameHashValid = false
	a.nav.lastBgFrameHashValid = false
	// 路径进度重置：用户可能跳到路径任意段位置，旧的 LockedSegmentIndex 会
	// 让 ProjectOnPathLocked 在错段开始；清零后 handleNavFix 下一帧走"首次
	// 全局投影"分支，按 (wx, wy) 真实最近段重新初始化锁。
	a.nav.progress = NavProgress{}
	a.nav.snapFreezeUntil = time.Now().Add(3 * time.Second)
	a.nav.snapFreezeX = wx
	a.nav.snapFreezeY = wy
	tracking := a.nav.active
	loc := a.nav.loc
	a.nav.mu.Unlock()

	// 步骤 4：写回 K + LastPlayer，持久化 K 到 data/calibration.json
	kChanged := math.Abs(bestK-curK) > 1e-6
	a.settings.Update(func(s *Settings) {
		s.LastPlayerX = wx
		s.LastPlayerY = wy
		s.LastPlayerSet = true
		if kChanged {
			s.WorldUnitsPerMinimapPx = bestK
		}
	})
	if kChanged {
		calPath := filepath.Join(a.dataDir, "calibration.json")
		if werr := locator.SaveCalibration(calPath, locator.CalibrationResult{
			K:             bestK,
			BestSharpness: bestSharp,
			WorldX:        wx,
			WorldY:        wy,
			ZoomUsed:      zoom,
		}); werr != nil {
			log.Printf("[calibrate-here] 保存 calibration.json 失败: %v", werr)
		} else {
			log.Printf("[calibrate-here] 已保存 K=%.3f 到 %s", bestK, calPath)
		}
	}

	if a.mapView != nil {
		a.mapView.SnapPlayerTo(wx, wy)
	}

	// 步骤 5：追踪在跑 → 用最新 K + 新 seed 重建 matcher
	if tracking && loc != nil {
		cfgNow := a.settings.Get()
		newMatch := a.buildRealMatcher(cfgNow, wx, wy)
		loc.SetMatch(newMatch)
	}

	if a.Window != nil {
		a.Window.Invalidate()
	}

	// 步骤 6：分档返回 hint
	switch {
	case kChanged && bestSharp >= 0.30:
		return fmt.Sprintf("校准成功：K 从 %.3f 修正为 %.3f（sharp %.2f → %.2f），位置锁到 (%.0f, %.0f)。"+
			"已保存，下次启动自动加载。",
			curK, bestK, curSharp, bestSharp, wx, wy)
	case kChanged:
		return fmt.Sprintf("K 已修正为 %.3f（sharp %.2f → %.2f，仍偏低），位置锁到 (%.0f, %.0f)。"+
			"弱特征区追踪靠后台重定位兜底。",
			bestK, curSharp, bestSharp, wx, wy)
	case scanned && bestSharp < 0.10:
		return fmt.Sprintf("位置已锁到 (%.0f, %.0f)，但扫 K 未找到更好的解（最佳 sharp=%.2f）。"+
			"可能原因：1) ROI 没框对小地图圆形本体；2) 图层错（当前 %s，换地表/地下试试）；"+
			"3) 点击位置与玩家实际相差 > 60 世界单位。K 保持 %.3f。",
			wx, wy, bestSharp, a.mapView.Layer(), curK)
	case bestSharp >= 0.30:
		return fmt.Sprintf("校准成功（当前 K=%.3f 正确）：sharp=%.2f at (%.0f, %.0f)",
			curK, bestSharp, wx, wy)
	default:
		return fmt.Sprintf("位置已锁到 (%.0f, %.0f)，sharp=%.2f（弱特征区，追踪靠后台重定位）。K=%.3f",
			wx, wy, bestSharp, curK)
	}
}

// requestBgReLocalize 请求一次后台多种子全图搜索；带节流（最快每 6s 一次）+
// 互斥（同一时刻只跑一个）。被周期 ticker / 主匹配低尖锐度 / 匹配错误触发。
//
// 找到高置信度位置后会更新主 matcher 的 seed 和持久化 LastPlayer。
func (a *App) requestBgReLocalize(reason string) {
	a.requestBgReLocalizeOpts(reason, false)
}

// requestBgReLocalizeOpts 同 requestBgReLocalize，但可选 bypassThrottle：
// 周期 ticker 调用时 bypassThrottle=true，仅受互斥（bgRelocateRunning）约束，
// 不受"距上次完成 ≥ 6s"节流约束 —— 否则 ticker 间隔 6s + 执行耗时 3s 的
// 场景下，实际 cadence 会退化为 9s+。
func (a *App) requestBgReLocalizeOpts(reason string, bypassThrottle bool) {
	a.nav.mu.Lock()
	if a.nav.bgRelocateRunning {
		a.nav.mu.Unlock()
		return
	}
	if !bypassThrottle {
		if !a.nav.bgRelocateLastAt.IsZero() && time.Since(a.nav.bgRelocateLastAt) < 6*time.Second {
			a.nav.mu.Unlock()
			return
		}
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

// runBgReLocalize 后台执行两段定位：
//
//  1. 多种子全图粗搜：用 EdgeMatcher 在每个候选 seed 位置以宽搜索半径
//     各跑一次，取尖锐度最高者。这一步可能很慢（秒级）。
//
//  2. 新截图 + 小半径精确验证：粗搜返回候选位置后，重新截取当前小地图
//     （玩家此刻的位置可能已经比粗搜起始时移动），以候选位置为 seed，
//     在新截图上跑一次"动态半径"的 EdgeMatcher.Match。半径随粗搜耗时
//     放大（基础 100 + 耗时 × 典型速度 × 安全系数），容忍玩家在
//     两次截图间合理位移。
//
// 只有验证步骤给出 sharp ≥ 0.30 的结果才采信。否则（玩家已走出验证半径 /
// 传送 / 粗搜本身是误命中）直接抛弃，不更新任何状态。
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
	img1, err := winutil.CaptureScreenRect(roi)
	if err != nil {
		log.Printf("[bg-relocate] capture1 failed: %v", err)
		return
	}

	// 截图哈希跳过：玩家不动时 minimap 字节级一致，直接复用上次 bg 结论
	// 跳过整个两阶段（节省 1~3 s）。仅在上次有 valid hash 时命中。
	h1 := imgHashFNV64(img1)
	a.nav.mu.Lock()
	if a.nav.lastBgFrameHashValid && a.nav.lastBgFrameHash == h1 {
		a.nav.mu.Unlock()
		log.Printf("[bg-relocate] 截图未变 (hash=%x)，跳过（玩家静止）", h1)
		return
	}
	a.nav.mu.Unlock()

	seeds := a.collectReLocalizeSeeds()
	if len(seeds) == 0 {
		log.Printf("[bg-relocate] no seeds")
		return
	}

	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	mosaic := &locator.MosaicProvider{Cache: a.cache, Layer: layer}

	// 阶段 1：多种子粗搜。搜索半径按连续失败次数自适应扩大：
	//   streak 0~2：r=200（轻量，覆盖小偏移）
	//   streak 3~5：r=400（玩家走远）
	//   streak ≥6：r=600（完全失联，接近全图兜底）
	// 降采样 step=2 + 早停 sharp≥0.55，速度比全分辨率快 ~16×。
	a.nav.mu.Lock()
	streak := a.nav.bgFailStreak
	a.nav.mu.Unlock()
	coarseR := 200
	if streak >= 3 {
		coarseR = 400
	}
	if streak >= 6 {
		coarseR = 600
	}
	log.Printf("[bg-relocate] %d seeds, coarse scan (r=%d downsample=2 early-stop=0.55, streak=%d)…",
		len(seeds), coarseR, streak)
	t0 := time.Now()
	em := locator.NewEdgeMatcher(mosaic, cfg.WorldUnitsPerMinimapPx)
	em.SearchZoom = zoom
	em.SearchRadiusMinPx = coarseR
	em.SearchRadiusMaxPx = coarseR
	em.MinSharpness = -1
	em.HeadingDetect = false
	em.DebugLog = cfg.DebugLog
	em.Downsample = 2
	em.EarlyStopSharp = 0.55
	candidate, allSharp, mErr := em.MatchMultiSeed(img1, seeds)
	coarseElapsed := time.Since(t0)
	if mErr != nil && candidate.Confidence <= 0 {
		log.Printf("[bg-relocate] coarse failed: %v (sharps=%v)", mErr, allSharp)
		a.nav.mu.Lock()
		a.nav.bgFailStreak++
		a.nav.mu.Unlock()
		return
	}
	log.Printf("[bg-relocate] coarse: sharp=%.3f at (%.0f, %.0f) took %v; tested=%d/%d sharps=%v",
		candidate.Confidence, candidate.WorldX, candidate.WorldY,
		coarseElapsed.Round(time.Millisecond), len(allSharp), len(seeds), allSharp)
	if candidate.Confidence < 0.30 {
		maxAll := 0.0
		for _, s := range allSharp {
			if s > maxAll {
				maxAll = s
			}
		}
		log.Printf("[bg-relocate] coarse 全部 %d 个 seed 最高 sharp=%.3f（<0.30），本次 discard；"+
			"下次 streak=%d 会用更大半径", len(allSharp), maxAll, streak+1)
		a.nav.mu.Lock()
		a.nav.bgFailStreak++
		a.nav.mu.Unlock()
		return
	}

	// 阶段 2：重截小地图 + 小半径验证
	//
	// 动态半径：玩家在粗搜期间可能走了 speed×dt 世界单位。按 80 世界单位/秒
	// 的"正常上限"估算，再乘 1.5 安全系数，转成 minimap-px。封顶 500 px
	// 防止退化回全图搜。
	img2, err := winutil.CaptureScreenRect(roi)
	if err != nil {
		log.Printf("[bg-relocate] capture2 failed: %v; discarded", err)
		return
	}
	K := cfg.WorldUnitsPerMinimapPx
	if K <= 0 {
		K = 1.0
	}
	driftSeconds := coarseElapsed.Seconds() + 0.3 // +0.3 覆盖截图 / 调度
	verifyRadius := 100 + int(driftSeconds*80.0/K*1.5)
	if verifyRadius < 120 {
		verifyRadius = 120
	}
	if verifyRadius > 500 {
		verifyRadius = 500
	}
	em2 := locator.NewEdgeMatcher(mosaic, cfg.WorldUnitsPerMinimapPx)
	em2.SearchZoom = zoom
	em2.SearchRadiusMinPx = verifyRadius
	em2.SearchRadiusMaxPx = verifyRadius
	em2.MinSharpness = -1
	em2.HeadingDetect = true
	em2.DebugLog = cfg.DebugLog
	em2.SetSeed(candidate.WorldX, candidate.WorldY)
	verified, vErr := em2.Match(img2)
	vSharp := em2.LastDebug.Sharpness
	log.Printf("[bg-relocate] verify: sharp=%.3f radius=%d at (%.0f, %.0f); candidate was (%.0f, %.0f)",
		vSharp, verifyRadius, verified.WorldX, verified.WorldY, candidate.WorldX, candidate.WorldY)
	if vErr != nil && vSharp <= 0 {
		log.Printf("[bg-relocate] verify err=%v (sharp=0); discarded", vErr)
		a.nav.mu.Lock()
		a.nav.bgFailStreak++
		a.nav.mu.Unlock()
		return
	}
	if vSharp < 0.30 {
		log.Printf("[bg-relocate] verify sharp %.3f < 0.30; discarded (玩家可能已走远或粗搜误命中)", vSharp)
		a.nav.mu.Lock()
		a.nav.bgFailStreak++
		a.nav.mu.Unlock()
		return
	}
	// 验证通过：清零失败计数 + 写入 hash 缓存 + 用 verify 的 fix 作为权威结果
	a.nav.mu.Lock()
	a.nav.bgFailStreak = 0
	a.nav.lastBgFrameHash = h1
	a.nav.lastBgFrameHashValid = true
	a.nav.mu.Unlock()
	best := verified
	best.Confidence = vSharp

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
	// 立刻把显示位置更新为重定位结果，并登记为"刚被确认"（供 plausibility 判传送）
	a.nav.mu.Lock()
	a.nav.fix = best
	a.nav.hasFix = true
	a.nav.lost = false
	a.nav.bgRelocateConfirmAt = time.Now()
	a.nav.bgRelocateConfirmX = best.WorldX
	a.nav.bgRelocateConfirmY = best.WorldY
	// 主匹配 seed 已被热替换，旧 lastFrameFix 对应旧 seed，清空避免误命中
	a.nav.lastFrameHashValid = false
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
	// 限制候选数量（每个 seed 都会跑一次 EdgeMatcher.Match，太多会很慢）。
	// 6 个足以覆盖：当前 fix / LastPlayer / 视图中心 / 最有可能的 1~2 个路径节点。
	const maxSeeds = 6
	if len(seen) > maxSeeds {
		seen = seen[:maxSeeds]
	}
	return seen
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
	cancel := a.nav.periodicRelocateCancel
	a.nav.active = false
	a.nav.routeID = ""
	a.nav.loc = nil
	a.nav.hasFix = false
	a.nav.periodicRelocateCancel = nil
	// 清理跨 session 状态：防止下次 Start 带着旧 progress / bgFailStreak / 哈希
	a.nav.progress = NavProgress{}
	a.nav.bgFailStreak = 0
	a.nav.lastFrameHashValid = false
	a.nav.lastBgFrameHashValid = false
	a.nav.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if loc != nil {
		loc.Stop()
	}
	if a.Window != nil {
		a.Window.Invalidate()
	}
}

// runPeriodicRelocate 在 nav 活动期间每 6 秒触发一次后台多种子重定位。
// requestBgReLocalize 内部已有 6s 节流和互斥，所以即使主匹配途中也触发了
// 重定位，本协程不会造成重复运行；它只保证"哪怕主匹配把玩家锁在错误吸引子
// 上、永远到不了 LOST 阈值"的场景下也会有一个稳定的 ground truth 流。
func (a *App) runPeriodicRelocate(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[periodic-relocate] panic: %v", r)
		}
	}()
	t := time.NewTicker(6 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.nav.mu.Lock()
			active := a.nav.active
			a.nav.mu.Unlock()
			if !active {
				return
			}
			// 周期 ticker 绕过 6s 节流：只受"上次未完成"互斥约束，
			// 避免"ticker 6s + 执行 3s"退化为 9s 周期。
			a.requestBgReLocalizeOpts("periodic 6s", true)
		}
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
	// snap 冻结期：用户校准后的 3 秒内，新 fix 的位置被强制回滚到
	// (snapFreezeX, snapFreezeY)；matcher 的 seed 仍会推进到真实匹配点，
	// 但显示不动，避免视觉瞬移。
	if !a.nav.snapFreezeUntil.IsZero() && now.Before(a.nav.snapFreezeUntil) {
		f.WorldX = a.nav.snapFreezeX
		f.WorldY = a.nav.snapFreezeY
	}
	// rejected 标记：本帧因为异常移动被 plausibility 拒绝。
	// 拒绝后需要做一系列"不污染"操作：保留 prev 位置 / 朝向、不更新 lastFixAt、
	// 不进 speedEMA。
	rejected := false
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
	// 1b. 异常移动拒绝：连续追踪中突发大跳变（远超 EMA 速度 × dt）大概率是
	// 主匹配掉到了别处的吸引子上。把它降级为 tentative 并丢弃 seed 推进，
	// 等待 10 秒周期 bg 重定位来给出真值（传送 / 进新地图场景下 bg 会确认
	// 新位置，本拒绝就不再触发）。
	//
	// 通过条件：
	//   - 有前一帧 fix
	//   - dt 在合理范围 (0.05 ~ 3s)
	//   - 已经攒过 EMA 速度
	//   - 隐含速度 > max(80 世界单位/秒, 6 × speedEMA)（80 是绝对下限，对应
	//     ≈ 250 ms 内跨 20 minimap-px，正常步行 / 跑酷不会触发）
	//   - 最近 12 秒内没有 bg 重定位"确认"了新位置（=不是传送）
	if hadPrev && !f.Tentative && !a.nav.lastFixAt.IsZero() {
		dt := now.Sub(a.nav.lastFixAt).Seconds()
		if dt > 0.05 && dt < 3.0 {
			ddx := f.WorldX - prev.WorldX
			ddy := f.WorldY - prev.WorldY
			impliedSpeed := math.Sqrt(ddx*ddx+ddy*ddy) / dt
			speedCap := 80.0
			if a.nav.speedEMAReady && a.nav.speedEMA*6 > speedCap {
				speedCap = a.nav.speedEMA * 6
			}
			confirmedTeleport := !a.nav.bgRelocateConfirmAt.IsZero() &&
				time.Since(a.nav.bgRelocateConfirmAt) < 12*time.Second
			if impliedSpeed > speedCap && !confirmedTeleport {
				if cfg.DebugLog {
					log.Printf("[plausibility] reject fix: speed=%.1f > cap=%.1f (ema=%.1f) dt=%.2fs",
						impliedSpeed, speedCap, a.nav.speedEMA, dt)
				}
				// 不污染原则：位置 / 朝向回滚到 prev；本帧不计入速度 EMA、
				// 不更新 lastFixAt。Confidence 仍写入新值，让 UI 状态栏告知
				// 用户当前匹配很差。
				f.Tentative = true
				f.WorldX = prev.WorldX
				f.WorldY = prev.WorldY
				f.Heading = prev.Heading
				f.HasHeading = prev.HasHeading
				rejected = true
				go a.requestBgReLocalize("plausibility reject")
			}
		}
	}
	// 2. 速度估计：tentative 不参与，rejected 不参与。
	//    speedEMA 进一步要求 sharp ≥ 0.30（高档）—— 中档数据的隐含速度
	//    不可信，放进 EMA 会拉漂基线、削弱 plausibility 检测的有效性。
	if hadPrev && !a.nav.lastFixAt.IsZero() && !f.Tentative && !rejected {
		dt := now.Sub(a.nav.lastFixAt).Seconds()
		if dt > 0.05 && dt < 3.0 {
			instVX := (f.WorldX - prev.WorldX) / dt
			instVY := (f.WorldY - prev.WorldY) / dt
			const alpha = 0.4
			a.nav.velX = a.nav.velX*(1-alpha) + instVX*alpha
			a.nav.velY = a.nav.velY*(1-alpha) + instVY*alpha
			// |inst speed| 的 EMA：仅高档参与，避免中档错位污染基线
			if f.Confidence >= 0.30 {
				const speedAlpha = 0.15
				instSpeed := math.Sqrt(instVX*instVX + instVY*instVY)
				if a.nav.speedEMAReady {
					a.nav.speedEMA = a.nav.speedEMA*(1-speedAlpha) + instSpeed*speedAlpha
				} else {
					a.nav.speedEMA = instSpeed
					a.nav.speedEMAReady = true
				}
			}
		}
	} else if !hadPrev {
		a.nav.velX = 0
		a.nav.velY = 0
		a.nav.speedEMA = 0
		a.nav.speedEMAReady = false
	}
	// rejected 帧不更新 lastFixAt：让下次 dt 仍以最后一个真实 fix 为基准，
	// 防止"密集错误帧 → 短 dt → 隐含速度变小 → 漏检"的自降级。
	if !rejected {
		a.nav.lastFixAt = now
	}
	// 3. 进度
	//
	// 分段锁定：用 ProjectOnPathLocked 替代全局 ProjectOnPath，避免玩家偏离
	// 时投影被"串"到后续段（路径自交 / 平行段 / 回环处特别容易出这种问题）。
	//
	// 初始化：导航刚启动 / 刚从无 progress 切到有 progress 时，用一次全局
	// ProjectOnPath 给 LockedSegmentIndex 一个合理初值（基于玩家当前实际位置，
	// 不强制从段 0 开始）。之后每一帧都只允许在 [locked, locked+1] 范围内投影，
	// 并按 T ≥ 0.95 推进。
	var prog NavProgress
	if p != nil && len(p.Nodes) >= 2 {
		prevProg := a.nav.progress
		if !hadPrev || (prevProg.Total == 0 && prevProg.SegmentIndex == 0 && prevProg.T == 0) {
			// 首次：全局投影得初始段
			init := ProjectOnPath(p.Nodes, f.WorldX, f.WorldY)
			init.LockedSegmentIndex = init.SegmentIndex
			prog = init
		} else {
			prog = ProjectOnPathLocked(p.Nodes, f.WorldX, f.WorldY, prevProg.LockedSegmentIndex)
		}
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

// prefetchTilesForLocator 在 (wx, wy) 周围、当前匹配 zoom 下预抓 (2*radius+1)²
// 块瓦片。Cache.Get 在本地有就立即返回 cached，否则异步触发下载（不阻塞）。
//
// 调用时机：StartTracking / StartNavigation / CalibrateAtWorld。让 locator 真正
// 开始 Match 时所需瓦片大概率已在本地或正在下载，避免冷启动时 cov=0 一片
// 导致前几次 Match 命中率低。
func (a *App) prefetchTilesForLocator(wx, wy float64, radius int) {
	if a.cache == nil || a.mapView == nil {
		return
	}
	cfg := a.settings.Get()
	zoom := cfg.NavSearchZoom
	if zoom <= 0 {
		zoom = 8
	}
	layer := locatorLayerOf(a.store, a.mapView.Layer())
	if layer.Name == "" {
		return
	}
	cpx, cpy := mapdata.WorldToPixel(wx, wy, zoom)
	cx := int(cpx) / mapdata.TileSize
	cy := int(cpy) / mapdata.TileSize
	count := 0
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			tx := cx + dx
			ty := cy + dy
			if !mapdata.IsTileInBounds(zoom, tx, ty, layer.X1, layer.X2, layer.Y1, layer.Y2) {
				continue
			}
			a.cache.Get(layer, zoom, tx, ty)
			count++
		}
	}
	if cfg.DebugLog {
		log.Printf("[prefetch] layer=%s z=%d center=(%d,%d) radius=%d → %d 瓦片预抓",
			layer.Name, zoom, cx, cy, radius, count)
	}
}

// DownloadAllTilesAsync 在后台批量下载所有图层、z=5..8 的有效瓦片。
//
// 工作原理：OSS 对超出地图范围的瓦片返回 404，因此理论枚举（每图层 z=8 高达
// 262144 张）的绝大多数并不存在。实际实现按 zoom 逐级推进：z 层的某 (x,y)
// 只有当 z-1 层 (⌊x/2⌋,⌊y/2⌋) 已作为 .png 落盘时才会被加入队列——这样每升
// 一级最多把已知有效区翻四倍，避开了整片空白海域的假阴性请求。
//
// 进度通过 a.tilesDL 公开：done = 已处理 spec 数（含跳过），skipped 含已缓
// 存 + 已知 404 + 本次 404，failed 只计真正的网络/HTTP 错误。
func (a *App) DownloadAllTilesAsync() {
	a.tilesDL.mu.Lock()
	if a.tilesDL.running {
		a.tilesDL.mu.Unlock()
		return
	}
	if a.cache == nil || a.fetcher == nil {
		a.tilesDL.err = "缓存或抓取器未就绪"
		a.tilesDL.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.tilesDL.running = true
	a.tilesDL.cancel = cancel
	a.tilesDL.total = 0
	a.tilesDL.done = 0
	a.tilesDL.skipped = 0
	a.tilesDL.failed = 0
	a.tilesDL.bytes = 0
	a.tilesDL.finished = false
	a.tilesDL.err = ""
	a.tilesDL.mu.Unlock()

	go a.runDownloadAllTiles(ctx)
}

// CancelDownloadAllTiles 中断正在跑的全量下载。
func (a *App) CancelDownloadAllTiles() {
	a.tilesDL.mu.Lock()
	cancel := a.tilesDL.cancel
	a.tilesDL.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TilesDLSnapshot 返回当前下载状态的副本，供 UI 渲染。
func (a *App) TilesDLSnapshot() (running bool, done, total, skipped, failed int, bytes int64, finished bool, errMsg string) {
	a.tilesDL.mu.Lock()
	defer a.tilesDL.mu.Unlock()
	return a.tilesDL.running, a.tilesDL.done, a.tilesDL.total, a.tilesDL.skipped, a.tilesDL.failed, a.tilesDL.bytes, a.tilesDL.finished, a.tilesDL.err
}

// runDownloadAllTiles 后台 worker pool 实现。按 zoom 分阶段：z=5 的枚举依赖
// z=4（首次抓取时已下载）；z=6 依赖 z=5 本次刚完成的结果；依此类推。
//
// 枚举策略：遍历父层（z-1）在磁盘上已有的 .png 瓦片集合，每张父瓦片展开出
// 4 张子瓦片（2x, 2y ~ 2x+1, 2y+1）。相比"全枚举子坐标 + 每个都 stat 父"
// 的做法，stat 次数从 O(子面积) 降到 O(父面积)，减少约 16 倍 IO。
func (a *App) runDownloadAllTiles(ctx context.Context) {
	defer func() {
		a.tilesDL.mu.Lock()
		a.tilesDL.running = false
		a.tilesDL.finished = true
		a.tilesDL.cancel = nil
		a.tilesDL.mu.Unlock()
		if a.Window != nil {
			a.Window.Invalidate()
		}
	}()

	type tileSpec struct {
		layer   wiki.Layer
		z, x, y int
	}

	layers := a.store.Layers()
	if len(layers) == 0 {
		a.tilesDL.mu.Lock()
		a.tilesDL.err = "图层列表为空（请先完成首次 Wiki 抓取）"
		a.tilesDL.mu.Unlock()
		return
	}

	const workers = 8
	for _, z := range []int{5, 6, 7, 8} {
		if ctx.Err() != nil {
			return
		}
		parentZ := z - 1

		// 枚举：遍历父层 (parentZ) 的理论范围，对每个存在 .png 的父坐标
		// (px, py) 产出 4 张子瓦片 (2px..2px+1, 2py..2py+1)。对负数坐标
		// 同样成立（子坐标范围 = 2 * 父坐标范围，首尾对齐）。
		var specs []tileSpec
		for _, ly := range layers {
			pr := mapdata.TileRefer(parentZ)
			px0, px1 := -pr*ly.X1, pr*ly.X2
			py0, py1 := -pr*ly.Y1, pr*ly.Y2
			for py := py0; py < py1; py++ {
				for px := px0; px < px1; px++ {
					parentPath := wiki.TilePath(a.cacheRoot, ly.Name, parentZ, px, py)
					if _, err := os.Stat(parentPath); err != nil {
						continue
					}
					for dy := 0; dy < 2; dy++ {
						for dx := 0; dx < 2; dx++ {
							specs = append(specs, tileSpec{ly, z, 2*px + dx, 2*py + dy})
						}
					}
				}
			}
		}
		a.tilesDL.mu.Lock()
		a.tilesDL.total += len(specs)
		a.tilesDL.mu.Unlock()
		log.Printf("[tiles-dl] z=%d 枚举 %d 张（来自 z=%d 已覆盖父瓦片的 4 倍展开）", z, len(specs), parentZ)

		specCh := make(chan tileSpec, 64)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for s := range specCh {
					if ctx.Err() != nil {
						return
					}
					pngPath := wiki.TilePath(a.cacheRoot, s.layer.Name, s.z, s.x, s.y)
					// 已缓存：直接计 skipped，不打扰网络。
					if fi, err := os.Stat(pngPath); err == nil {
						a.tilesDL.mu.Lock()
						a.tilesDL.done++
						a.tilesDL.skipped++
						a.tilesDL.bytes += fi.Size()
						a.tilesDL.mu.Unlock()
						continue
					}
					if _, err := os.Stat(pngPath + ".404"); err == nil {
						a.tilesDL.mu.Lock()
						a.tilesDL.done++
						a.tilesDL.skipped++
						a.tilesDL.mu.Unlock()
						continue
					}

					path, err := a.fetcher.FetchTileOnDemand(ctx, s.layer, s.z, s.x, s.y)
					if ctx.Err() != nil {
						return
					}
					a.tilesDL.mu.Lock()
					a.tilesDL.done++
					switch {
					case err == wiki.ErrTileOutOfBounds:
						a.tilesDL.skipped++
					case err != nil:
						a.tilesDL.failed++
					case path != "":
						if fi, ferr := os.Stat(path); ferr == nil {
							a.tilesDL.bytes += fi.Size()
						}
					}
					a.tilesDL.mu.Unlock()
				}
			}()
		}

		stopped := false
		for i, s := range specs {
			select {
			case <-ctx.Done():
				stopped = true
			case specCh <- s:
			}
			if stopped {
				break
			}
			if i%200 == 0 && a.Window != nil {
				a.Window.Invalidate()
			}
		}
		close(specCh)
		wg.Wait()
		if stopped {
			return
		}
	}
}
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
			if routeID == "" {
				return false
			}
		}
		// 用户最近 10 秒内拖动 / 缩放过地图 → 暂时关掉自动居中，
		// 让用户能自由查看地图其他区域；空闲 10s 后再恢复。
		if a.mapView != nil && a.mapView.TimeSinceUserInteract() < 10*time.Second {
			return false
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
