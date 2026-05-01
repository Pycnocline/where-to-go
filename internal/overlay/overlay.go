// Package overlay 透明置顶悬浮窗（基于 Win32 layered window）。
//
// 提供：
//   - 创建 WS_EX_LAYERED|WS_EX_TOPMOST 的无边框 popup 窗口
//   - 通过 WM_NCHITTEST 手写 hit test 实现"拖动标题 + 边缘调整大小"
//   - 可插入 OnPaint / OnSize 回调供外部（如渲染 Gio headless 到位图）
//   - alpha / 鼠标穿透 / 移动 / 调整大小
//
// 使用约束：调用 New 的 goroutine 会被 LockOSThread；应当启动一个专用 goroutine
// 运行消息循环（参见 Run）。
package overlay

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Config 创建参数。
type Config struct {
	Title string
	X, Y  int32
	W, H  int32
	Alpha byte // 0=完全透明, 255=不透明

	// Resizable 允许用户通过边缘调整大小（靠 WM_NCHITTEST 手写 hit test）。
	Resizable bool

	// OnPaint 在 WM_PAINT 中被调用；回调应直接用 hdc GDI 绘制客户区。
	// nil 时不主动绘制，window 会保留上一帧内容（背景画刷已关掉擦除）。
	OnPaint func(hdc uintptr, clientW, clientH int32)

	// OnSize 用户改变窗口尺寸时调用（新的客户区 W, H）。
	OnSize func(w, h int32)
}

// Overlay 一个透明覆盖窗的句柄。
type Overlay struct {
	mu        sync.Mutex
	hwnd      windows.Handle
	className *uint16
	hInstance windows.Handle

	resizable bool
	onPaint   func(hdc uintptr, clientW, clientH int32)
	onSize    func(w, h int32)

	clickThrough bool
}

// 全局 HWND → *Overlay 映射。WndProc 是一个 C 回调，必须通过查表找到对应 Overlay。
var (
	activeMu       sync.RWMutex
	activeOverlays = map[uintptr]*Overlay{}
	wndProcOnce    sync.Once
	wndProcPtr     uintptr
)

// Win32 常量（最小子集）
const (
	IDC_ARROW      = 32512
	IDC_SIZEALL    = 32646
	IDC_SIZENWSE   = 32642
	IDC_SIZENESW   = 32643
	IDC_SIZEWE     = 32644
	IDC_SIZENS     = 32645
	WS_EX_LAYERED  = 0x00080000
	WS_EX_TOPMOST  = 0x00000008
	WS_EX_TOOLWINDOW = 0x00000080
	WS_EX_TRANSPARENT = 0x00000020
	WS_POPUP       = 0x80000000
	WS_VISIBLE     = 0x10000000
	WS_THICKFRAME  = 0x00040000
	LWA_ALPHA      = 0x00000002
	SW_SHOWNOACTIVATE = 4
	SW_HIDE           = 0

	WM_DESTROY    = 0x0002
	WM_SIZE       = 0x0005
	WM_PAINT      = 0x000F
	WM_ERASEBKGND = 0x0014
	WM_NCCALCSIZE = 0x0083
	WM_NCHITTEST  = 0x0084

	HTCLIENT      = 1
	HTCAPTION     = 2
	HTLEFT        = 10
	HTRIGHT       = 11
	HTTOP         = 12
	HTTOPLEFT     = 13
	HTTOPRIGHT    = 14
	HTBOTTOM      = 15
	HTBOTTOMLEFT  = 16
	HTBOTTOMRIGHT = 17

	SWP_NOMOVE     = 0x0002
	SWP_NOSIZE     = 0x0001
	SWP_NOACTIVATE = 0x0010
	SWP_SHOWWINDOW = 0x0040
	SWP_FRAMECHANGED = 0x0020
)

// 系统 DLL / procs（懒加载）
var (
	modUser32            = windows.NewLazySystemDLL("user32.dll")
	modKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandle  = modKernel32.NewProc("GetModuleHandleW")
	procRegisterClassEx  = modUser32.NewProc("RegisterClassExW")
	procCreateWindowEx   = modUser32.NewProc("CreateWindowExW")
	procDefWindowProc    = modUser32.NewProc("DefWindowProcW")
	procLoadCursor       = modUser32.NewProc("LoadCursorW")
	procSetLayered       = modUser32.NewProc("SetLayeredWindowAttributes")
	procShowWindow       = modUser32.NewProc("ShowWindow")
	procUpdateWindow     = modUser32.NewProc("UpdateWindow")
	procGetWindowLongPtr = modUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = modUser32.NewProc("SetWindowLongPtrW")
	procGetWindowLong    = modUser32.NewProc("GetWindowLongW")
	procSetWindowLong    = modUser32.NewProc("SetWindowLongW")
	procSetWindowPos     = modUser32.NewProc("SetWindowPos")
	procMoveWindow       = modUser32.NewProc("MoveWindow")
	procBeginPaint       = modUser32.NewProc("BeginPaint")
	procEndPaint         = modUser32.NewProc("EndPaint")
	procGetClientRect    = modUser32.NewProc("GetClientRect")
	procInvalidateRect   = modUser32.NewProc("InvalidateRect")
	procDestroyWindow    = modUser32.NewProc("DestroyWindow")
	procGetMessage       = modUser32.NewProc("GetMessageW")
	procTranslateMessage = modUser32.NewProc("TranslateMessage")
	procDispatchMessage  = modUser32.NewProc("DispatchMessageW")
	procPostQuitMessage  = modUser32.NewProc("PostQuitMessage")
	procPostMessage      = modUser32.NewProc("PostMessageW")
	procScreenToClient   = modUser32.NewProc("ScreenToClient")
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type paintStruct struct {
	HDC       uintptr
	Erase     int32
	Rect      rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type rect struct {
	L, T, R, B int32
}

type pointStruct struct {
	X, Y int32
}

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      pointStruct
	_       uint32
}

// ensureWndProc 只注册一次全局 WndProc 回调。
func ensureWndProc() {
	wndProcOnce.Do(func() {
		wndProcPtr = windows.NewCallback(wndProc)
	})
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch uint32(msg) {
	case WM_PAINT:
		o := lookupOverlay(hwnd)
		if o != nil && o.onPaint != nil {
			var ps paintStruct
			hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			var rc rect
			procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			w := rc.R - rc.L
			h := rc.B - rc.T
			o.onPaint(hdc, w, h)
			procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
			return 0
		}
	case WM_ERASEBKGND:
		// 告诉系统我们负责擦背景（避免 WM_PAINT 前闪烁）
		return 1
	case WM_NCCALCSIZE:
		// 当带 WS_THICKFRAME 时，Windows 默认在 NC 区留下一圈窄框（顶部尤其明显
		// 表现为"白边"）。返回 0 + wparam!=0 时 lparam 指向 NCCALCSIZE_PARAMS，
		// 我们让客户区 = 整个窗口矩形，从而消除所有非客户区。
		if wparam != 0 {
			return 0
		}
	case WM_SIZE:
		o := lookupOverlay(hwnd)
		if o != nil && o.onSize != nil {
			w := int32(lparam & 0xFFFF)
			h := int32((lparam >> 16) & 0xFFFF)
			o.onSize(w, h)
		}
	case WM_NCHITTEST:
		o := lookupOverlay(hwnd)
		if o != nil && o.resizable {
			// lparam 的低 16 位是屏幕 x，高 16 位是屏幕 y（signed）
			sx := int32(int16(lparam & 0xFFFF))
			sy := int32(int16((lparam >> 16) & 0xFFFF))
			pt := pointStruct{X: sx, Y: sy}
			procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
			var rc rect
			procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
			const edge = 6
			left := pt.X < edge
			right := pt.X > rc.R-edge
			top := pt.Y < edge
			bottom := pt.Y > rc.B-edge
			switch {
			case top && left:
				return HTTOPLEFT
			case top && right:
				return HTTOPRIGHT
			case bottom && left:
				return HTBOTTOMLEFT
			case bottom && right:
				return HTBOTTOMRIGHT
			case left:
				return HTLEFT
			case right:
				return HTRIGHT
			case top:
				return HTTOP
			case bottom:
				return HTBOTTOM
			}
			// 客户区当作标题栏，允许拖动
			return HTCAPTION
		}
		// 非 resizable：整个客户区当标题栏，可拖动
		return HTCAPTION
	case WM_DESTROY:
		unregisterOverlay(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

func registerOverlay(hwnd uintptr, o *Overlay) {
	activeMu.Lock()
	activeOverlays[hwnd] = o
	activeMu.Unlock()
}

func unregisterOverlay(hwnd uintptr) {
	activeMu.Lock()
	delete(activeOverlays, hwnd)
	activeMu.Unlock()
}

func lookupOverlay(hwnd uintptr) *Overlay {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return activeOverlays[hwnd]
}

// New 在调用线程创建窗口。注意：调用线程会被 LockOSThread。
// 典型流程：专用 goroutine 调用 New → Show → Run（消息循环）。
func New(cfg Config) (*Overlay, error) {
	runtime.LockOSThread()
	ensureWndProc()

	if cfg.W == 0 {
		cfg.W = 320
	}
	if cfg.H == 0 {
		cfg.H = 200
	}
	if cfg.Alpha == 0 {
		cfg.Alpha = 0xC0
	}

	hInst, _, _ := procGetModuleHandle.Call(0)
	if hInst == 0 {
		return nil, fmt.Errorf("GetModuleHandle 失败")
	}

	clsName, _ := windows.UTF16PtrFromString("WhereToGoOverlayV2")
	titleName, _ := windows.UTF16PtrFromString(cfg.Title)

	cursor, _, _ := procLoadCursor.Call(0, IDC_SIZEALL)
	wc := wndClassEx{
		Size:       uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:    wndProcPtr,
		Instance:   hInst,
		Cursor:     cursor,
		Background: 0, // 不使用默认背景画刷；WM_ERASEBKGND 直接返回 1
		ClassName:  clsName,
	}
	// 重复注册同名类会失败（ERROR_CLASS_ALREADY_EXISTS），忽略即可
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	ex := uintptr(WS_EX_LAYERED | WS_EX_TOPMOST | WS_EX_TOOLWINDOW)
	style := uintptr(WS_POPUP)
	if cfg.Resizable {
		style |= WS_THICKFRAME
	}

	hwnd, _, callErr := procCreateWindowEx.Call(
		ex,
		uintptr(unsafe.Pointer(clsName)),
		uintptr(unsafe.Pointer(titleName)),
		style,
		uintptr(cfg.X), uintptr(cfg.Y),
		uintptr(cfg.W), uintptr(cfg.H),
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return nil, fmt.Errorf("CreateWindowEx 失败: %v", callErr)
	}
	o := &Overlay{
		hwnd:      windows.Handle(hwnd),
		className: clsName,
		hInstance: windows.Handle(hInst),
		resizable: cfg.Resizable,
		onPaint:   cfg.OnPaint,
		onSize:    cfg.OnSize,
	}
	registerOverlay(hwnd, o)
	procSetLayered.Call(uintptr(hwnd), 0, uintptr(cfg.Alpha), LWA_ALPHA)
	// 触发一次 WM_NCCALCSIZE，使我们的 "无非客户区" 设置在第一次显示前生效，
	// 否则 WS_THICKFRAME 默认 NC 边框会闪一下白边。
	procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0,
		uintptr(SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE|SWP_FRAMECHANGED))
	return o, nil
}

// Show 显示窗口。
func (o *Overlay) Show() {
	procShowWindow.Call(uintptr(o.hwnd), SW_SHOWNOACTIVATE)
	procUpdateWindow.Call(uintptr(o.hwnd))
}

// Hide 隐藏。
func (o *Overlay) Hide() {
	procShowWindow.Call(uintptr(o.hwnd), SW_HIDE)
}

// Destroy 销毁窗口；消息循环 Run 会返回。
// 必须从创建窗口的线程上调用；跨线程请用 PostClose。
func (o *Overlay) Destroy() {
	procDestroyWindow.Call(uintptr(o.hwnd))
}

// PostClose 线程安全地请求关闭窗口。内部向窗口投递 WM_CLOSE，由 WndProc
// 分派到 DefWindowProc → DestroyWindow（在窗口所有者线程上）。
func (o *Overlay) PostClose() {
	const WM_CLOSE = 0x0010
	procPostMessage.Call(uintptr(o.hwnd), WM_CLOSE, 0, 0)
}

// Invalidate 标记整个客户区需要重绘（触发 WM_PAINT）。
func (o *Overlay) Invalidate() {
	procInvalidateRect.Call(uintptr(o.hwnd), 0, 0)
}

// SetAlpha 设整窗 alpha (0..255)。
func (o *Overlay) SetAlpha(a byte) {
	procSetLayered.Call(uintptr(o.hwnd), 0, uintptr(a), LWA_ALPHA)
}

// SetClickThrough 切换鼠标穿透（WS_EX_TRANSPARENT）。
func (o *Overlay) SetClickThrough(on bool) {
	const gwlExStyle = ^uintptr(19) // -20
	cur, _, _ := procGetWindowLong.Call(uintptr(o.hwnd), gwlExStyle)
	style := uint32(cur)
	if on {
		style |= WS_EX_TRANSPARENT | WS_EX_LAYERED
	} else {
		style &^= WS_EX_TRANSPARENT
		style |= WS_EX_LAYERED
	}
	procSetWindowLong.Call(uintptr(o.hwnd), gwlExStyle, uintptr(style))
	o.mu.Lock()
	o.clickThrough = on
	o.mu.Unlock()
}

// SetBounds 移动并调整大小。
func (o *Overlay) SetBounds(x, y, w, h int32) {
	procMoveWindow.Call(uintptr(o.hwnd), uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
}

// HWND 返回原生窗口句柄。
func (o *Overlay) HWND() windows.Handle { return o.hwnd }

// ClientSize 返回当前客户区大小。
func (o *Overlay) ClientSize() (w, h int32) {
	var rc rect
	procGetClientRect.Call(uintptr(o.hwnd), uintptr(unsafe.Pointer(&rc)))
	return rc.R - rc.L, rc.B - rc.T
}

// Run 在调用线程跑 Win32 消息循环，直到窗口销毁。典型用法：把 New + Run 都
// 放在同一个 LockOSThread 的 goroutine 中。
func Run() {
	var msg msgStruct
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
