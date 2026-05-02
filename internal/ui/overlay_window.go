//go:build windows

package ui

// 悬浮窗（最终架构）：
//
//   1. Win32 layered 窗口（internal/overlay 负责创建 + 消息循环）—— 经 runTestOverlay
//      验证能可靠显示。WS_EX_LAYERED|TOPMOST|TOOLWINDOW，WS_THICKFRAME 允许
//      用户拖动边缘调整大小。
//
//   2. 用 gioui.org/gpu/headless 离屏渲染 MapView 到 RGBA 位图。
//
//   3. WM_PAINT 中用 StretchDIBits 把最新 RGBA 贴到窗口。
//
//   4. 100ms ticker 触发重渲染 → Invalidate。
//
// 这样绕开了"第二个 Gio 窗口 + WS_EX_LAYERED 在某些机器上黑屏 / 不可见"的问题。

import (
	"image"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"golang.org/x/sys/windows"
	"image/color"

	"github.com/where-to-go/app/internal/crashlog"
	"github.com/where-to-go/app/internal/overlay"
)

// OverlayWindow 公共 API 与前几版保持一致。
type OverlayWindow struct {
	app *App

	mu   sync.Mutex
	win  *overlay.Overlay
	open atomic.Bool

	alpha        atomic.Uint32
	clickThrough atomic.Bool

	// 后台渲染状态
	stopCh chan struct{}

	// 最近一帧位图（WM_PAINT 会 blit 它）
	frameMu    sync.RWMutex
	frameRGBA  *image.RGBA
	frameW     int
	frameH     int

	// 当前客户区尺寸（WM_SIZE 更新，渲染 goroutine 读）
	sizeMu sync.Mutex
	clientW int
	clientH int
}

// NewOverlay 构造（未启动）。
func NewOverlay(a *App) *OverlayWindow {
	o := &OverlayWindow{app: a}
	o.alpha.Store(0xDC)
	o.clientW = 720
	o.clientH = 540
	return o
}

// IsOpen 窗口是否还在。
func (o *OverlayWindow) IsOpen() bool { return o.open.Load() }

// Start 启动悬浮窗。开启一个 LockOSThread 的 goroutine 负责 Win32 消息循环；
// 另一个 goroutine 负责 Gio headless 渲染。
func (o *OverlayWindow) Start() {
	o.mu.Lock()
	if o.win != nil {
		o.mu.Unlock()
		log.Printf("[overlay] Start ignored: already running")
		return
	}
	o.stopCh = make(chan struct{})
	o.mu.Unlock()

	ready := make(chan error, 1)
	go o.winLoop(ready)
	if err := <-ready; err != nil {
		log.Printf("[overlay] Start failed: %v", err)
		return
	}
	go o.renderLoop()
}

// Close 请求关闭；Destroy 窗口会驱动 WM_DESTROY → PostQuitMessage → Run 返回。
func (o *OverlayWindow) Close() {
	o.mu.Lock()
	w := o.win
	stop := o.stopCh
	o.mu.Unlock()
	if w == nil {
		return
	}
	if stop != nil {
		close(stop)
	}
	w.PostClose()
}

// SetAlpha 整窗 alpha。
func (o *OverlayWindow) SetAlpha(a byte) {
	o.alpha.Store(uint32(a))
	o.mu.Lock()
	w := o.win
	o.mu.Unlock()
	if w != nil {
		w.SetAlpha(a)
	}
}

// SetClickThrough 切换鼠标穿透。
func (o *OverlayWindow) SetClickThrough(on bool) {
	o.clickThrough.Store(on)
	o.mu.Lock()
	w := o.win
	o.mu.Unlock()
	if w != nil {
		w.SetClickThrough(on)
	}
}

// winLoop 创建窗口 + 跑消息循环。必须 LockOSThread。
func (o *OverlayWindow) winLoop(ready chan<- error) {
	runtime.LockOSThread()
	defer func() {
		if crashlog.Recover("overlay.winLoop") {
			// Win32 消息循环崩了：把 open 置否避免外部再访问。
			o.open.Store(false)
			o.mu.Lock()
			o.win = nil
			o.mu.Unlock()
		}
	}()
	// 注意：不能在 Win32 消息循环线程里做 Gio headless 渲染 —— 两者会抢 OS 线程。
	w, err := overlay.New(overlay.Config{
		Title:     "where-to-go 悬浮",
		X:         120, Y: 120,
		W:         int32(o.clientW),
		H:         int32(o.clientH),
		Alpha:     byte(o.alpha.Load()),
		Resizable: true,
		OnPaint: func(hdc uintptr, cw, ch int32) {
			o.onPaint(hdc, cw, ch)
		},
		OnSize: func(nw, nh int32) {
			if nw <= 0 || nh <= 0 {
				return
			}
			o.sizeMu.Lock()
			o.clientW = int(nw)
			o.clientH = int(nh)
			o.sizeMu.Unlock()
		},
	})
	if err != nil {
		ready <- err
		return
	}
	o.mu.Lock()
	o.win = w
	o.mu.Unlock()
	o.open.Store(true)
	w.Show()
	if o.clickThrough.Load() {
		w.SetClickThrough(true)
	}
	log.Printf("[overlay] window created; hwnd=%x alpha=%d size=%dx%d",
		uintptr(w.HWND()), o.alpha.Load(), o.clientW, o.clientH)
	ready <- nil
	// 消息循环：阻塞到 WM_DESTROY
	overlay.Run()
	o.open.Store(false)
	o.mu.Lock()
	o.win = nil
	o.mu.Unlock()
	log.Printf("[overlay] window closed")
}

// renderLoop 用 gio headless 定期渲染 MapView，更新 frameRGBA，Invalidate 窗口。
//
// 每帧都跑在 recover 守护下：headless GPU 资源吃紧 / 共享 Gio 状态偶发的 panic
// 都会被收住，保证悬浮窗不会把整个进程拖崩。下一帧会自适应放慢 200ms 重试。
func (o *OverlayWindow) renderLoop() {
	runtime.LockOSThread() // headless D3D11 需要独占线程
	defer crashlog.Recover("overlay.renderLoop")
	const baseTick = 120 * time.Millisecond
	const slowTick = 320 * time.Millisecond
	tick := baseTick
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var hw *headless.Window
	var hwW, hwH int
	defer func() {
		if hw != nil {
			hw.Release()
		}
	}()

	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
		}

		if !o.open.Load() {
			return
		}

		o.sizeMu.Lock()
		w, h := o.clientW, o.clientH
		o.sizeMu.Unlock()
		if w < 16 || h < 16 {
			continue
		}
		// 单次 tick 用 closure 包起来，让 recover 抓住任何 panic（包括 Gio
		// 内部 D3D11 资源耗尽 / 共享 ImageOp 等 shape 在 headless 上偶发出错）。
		// 出错后下一 tick 自动放慢，避免持续高频 panic 拖垮进程。
		ok := func() (success bool) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[overlay] render panic recovered: %v", r)
					success = false
				}
			}()
			if hw == nil || hwW != w || hwH != h {
				if hw != nil {
					hw.Release()
					hw = nil
				}
				nw, err := headless.NewWindow(w, h)
				if err != nil {
					log.Printf("[overlay] headless.NewWindow(%d,%d) failed: %v", w, h, err)
					return false
				}
				hw = nw
				hwW, hwH = w, h
				log.Printf("[overlay] headless renderer resized → %dx%d", w, h)
			}
			// 每帧都用全新的 router + ops，避免任何跨帧引用残留。
			var router input.Router
			var ops op.Ops
			gtx := layout.Context{
				Ops:    &ops,
				Source: router.Source(),
				Constraints: layout.Constraints{
					Max: image.Point{X: w, Y: h},
				},
				Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			}
			// 关键：悬浮窗必须"完全不渲染文本"，否则两窗口并发调用 Gio
			// text.Shaper 内部 map 会出 "concurrent map writes" runtime fatal
			// (recover 抓不住)。LayoutAs(..., true) 会在 layoutMu 保护下切换
			// MapView.overlayMode 并跳过所有 material.Label。
			o.layoutOverlay(gtx)
			router.Frame(&ops)
			if err := hw.Frame(&ops); err != nil {
				log.Printf("[overlay] headless Frame: %v", err)
				return false
			}
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			if err := hw.Screenshot(img); err != nil {
				log.Printf("[overlay] Screenshot: %v", err)
				return false
			}
			o.frameMu.Lock()
			o.frameRGBA = img
			o.frameW = w
			o.frameH = h
			o.frameMu.Unlock()
			return true
		}()

		// 自适应：连续失败 → 拉长间隔；恢复成功 → 回到正常间隔。
		if ok {
			if tick != baseTick {
				tick = baseTick
				ticker.Reset(tick)
			}
		} else {
			if tick != slowTick {
				tick = slowTick
				ticker.Reset(tick)
			}
			// panic 后 hw 状态不可信，强制下一帧重建。
			if hw != nil {
				hw.Release()
				hw = nil
				hwW, hwH = 0, 0
			}
			continue
		}

		// 通知窗口线程重绘
		o.mu.Lock()
		win := o.win
		o.mu.Unlock()
		if win != nil {
			win.Invalidate()
		}
	}
}

// layoutOverlay 渲染悬浮窗内容：MapView 本体（无侧栏 / 工具栏）。
// 若导航激活且 settings.OverlayProgressBar 开启，则在顶部叠一条窗等宽的进度条。
func (o *OverlayWindow) layoutOverlay(gtx layout.Context) layout.Dimensions {
	// 背景：不透明深色。用户感知的"透明"来自 WS_EX_LAYERED 的 alpha。
	bg := color.NRGBA{R: 0x10, G: 0x14, B: 0x1c, A: 0xff}
	if o.app != nil && o.app.Theme != nil {
		if o.app.Theme.BG.A != 0 {
			bg = o.app.Theme.BG
		}
	}
	paint.FillShape(gtx.Ops, bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	if o.app == nil || o.app.mapView == nil {
		return layout.Dimensions{Size: gtx.Constraints.Max}
	}
	dims := o.app.mapView.LayoutAs(gtx, true)
	o.drawNavProgressBar(gtx)
	return dims
}

// drawNavProgressBar 在窗口顶部绘制 4px 高的进度条（仅导航激活 + 设置开启时）。
// 不带文字 / 数字，避免触发 text.Shaper 的并发 map 写。
func (o *OverlayWindow) drawNavProgressBar(gtx layout.Context) {
	if o.app == nil || o.app.settings == nil {
		return
	}
	cfg := o.app.settings.Get()
	if !cfg.OverlayProgressBarEnabled() {
		return
	}
	active, _, _, _, prog := o.app.nav.snapshot()
	if !active || prog.Total <= 0 {
		return
	}
	frac := prog.Traveled / prog.Total
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	w := gtx.Constraints.Max.X
	h := gtx.Dp(unit.Dp(4))
	// 背景轨：半透明黑
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 0x80},
		clip.Rect{Max: image.Pt(w, h)}.Op())
	// 已完成段：亮绿
	filled := int(float64(w)*frac + 0.5)
	if filled > 0 {
		paint.FillShape(gtx.Ops, color.NRGBA{R: 0x3c, G: 0xd2, B: 0x62, A: 0xff},
			clip.Rect{Max: image.Pt(filled, h)}.Op())
	}
}

// ---- WM_PAINT 处理：把最新 frameRGBA BitBlt 到窗口 ----

const (
	biRGB            = 0
	diBRGBColors     = 0
	srcCopy          = 0x00CC0020
	bkModeTransparent = 1
)

// BITMAPINFOHEADER + RGBQUAD[0] ~= BITMAPINFO
type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

var (
	modGdi32            = windows.NewLazySystemDLL("gdi32.dll")
	procStretchDIBits   = modGdi32.NewProc("StretchDIBits")
	modUser32Overlay    = windows.NewLazySystemDLL("user32.dll")
	procFillRect        = modUser32Overlay.NewProc("FillRect")
	modGdi32Overlay     = windows.NewLazySystemDLL("gdi32.dll")
	procCreateSolidBrushOL = modGdi32Overlay.NewProc("CreateSolidBrush")
	procDeleteObjectOL     = modGdi32Overlay.NewProc("DeleteObject")
)

// onPaint 在 Win32 消息线程被调用（overlay 的 WndProc）。
// 任何 panic 都被就地 recover，避免单帧绘制错误把 WndProc 拖崩。
func (o *OverlayWindow) onPaint(hdc uintptr, cw, ch int32) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[overlay] onPaint panic recovered: %v", r)
		}
	}()
	o.frameMu.RLock()
	img := o.frameRGBA
	fw, fh := o.frameW, o.frameH
	o.frameMu.RUnlock()

	if img == nil || fw == 0 || fh == 0 {
		// 还没第一帧：画一个纯色占位（避免系统 random 像素闪烁）
		fillRectSolid(hdc, 0, 0, cw, ch, 0x1c1410)
		return
	}

	// Gio RGBA 是 straight alpha，字节顺序 R,G,B,A；Windows DIB 用 BGR(A)。
	// 简单做法：直接把 RGBA 传 StretchDIBits 会得到颜色反转（B↔R 互换）。
	// 解决：交换一次 B/R（逐像素）到一块 BGRA buffer。
	buf := toBGRA(img)

	// DIB 头。Top-down：BiHeight 为负。
	var bih bitmapInfoHeader
	bih.BiSize = uint32(unsafe.Sizeof(bih))
	bih.BiWidth = int32(fw)
	bih.BiHeight = -int32(fh) // negative → top-down
	bih.BiPlanes = 1
	bih.BiBitCount = 32
	bih.BiCompression = biRGB

	procStretchDIBits.Call(
		hdc,
		0, 0, uintptr(cw), uintptr(ch), // dest rect
		0, 0, uintptr(fw), uintptr(fh), // src rect
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bih)),
		diBRGBColors,
		srcCopy,
	)
	putBGRABuf(buf)
}

func fillRectSolid(hdc uintptr, x, y, w, h int32, rgb uint32) {
	// rgb 是 0xRRGGBB；Win32 COLORREF = 0x00BBGGRR
	cr := uintptr(((rgb & 0xFF0000) >> 16) | ((rgb & 0x00FF00)) | ((rgb & 0x0000FF) << 16))
	brush, _, _ := procCreateSolidBrushOL.Call(cr)
	rc := struct{ L, T, R, B int32 }{x, y, x + w, y + h}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObjectOL.Call(brush)
}

// bgraBufPool 复用 toBGRA 的输出 slice，避免每帧 ~200KB 的 GC 压力。
// 用 pointer-to-slice 避免 sync.Pool 把 slice 拆解（Go 1.22+ 推荐写法）。
var bgraBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 256*1024)
		return &b
	},
}

// toBGRA 把 image.RGBA 的像素字节序从 R,G,B,A 翻到 B,G,R,A（DIB 需要的格式）。
// 返回的 slice 来自 bgraBufPool；调用方用完后调用 putBGRABuf 归还。
// 注意：fillRectSolid 的备份路径不需要 buf，所以池只服务 onPaint 主路径。
func toBGRA(src *image.RGBA) []byte {
	n := len(src.Pix)
	bp := bgraBufPool.Get().(*[]byte)
	buf := *bp
	if cap(buf) < n {
		buf = make([]byte, n)
	} else {
		buf = buf[:n]
	}
	for i := 0; i < n; i += 4 {
		buf[i+0] = src.Pix[i+2] // B <- R
		buf[i+1] = src.Pix[i+1] // G
		buf[i+2] = src.Pix[i+0] // R <- B
		buf[i+3] = src.Pix[i+3] // A
	}
	*bp = buf
	return buf
}

// putBGRABuf 归还 toBGRA 借出的 buffer。在 procStretchDIBits.Call 之后立即归还。
func putBGRABuf(buf []byte) {
	if cap(buf) == 0 {
		return
	}
	b := buf[:0]
	bgraBufPool.Put(&b)
}
