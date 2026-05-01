// Package winutil 中的自动截屏支持。
package winutil

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FindWindowByTitle 按标题查找窗口句柄。
//
// 注意：Gio 给窗口的标题就是 app.Title 的值。若同名窗口存在多个仅返回第一个。
func FindWindowByTitle(title string) (windows.Handle, error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	findWindow := user32.NewProc("FindWindowW")
	tp, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(tp)))
	if hwnd == 0 {
		return 0, fmt.Errorf("未找到标题为 %q 的窗口", title)
	}
	return windows.Handle(hwnd), nil
}

// GetWindowRect 拿窗口屏幕坐标矩形。
func GetWindowRect(hwnd windows.Handle) (image.Rectangle, error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	getRect := user32.NewProc("GetWindowRect")
	type rect struct{ Left, Top, Right, Bottom int32 }
	var r rect
	ret, _, _ := getRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r)))
	if ret == 0 {
		return image.Rectangle{}, fmt.Errorf("GetWindowRect 失败")
	}
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)), nil
}

// CaptureWindow 截取指定窗口当前渲染内容。
//
// 实现策略：先用 BitBlt 从桌面 DC 直接抓窗口对应屏幕矩形（这能正确
// 拿到 D3D11/OpenGL 等硬件加速内容，因为 DWM 已把它们合成到屏幕上）。
// PrintWindow 对硬件加速窗口经常返回黑屏，因此不用。
func CaptureWindow(hwnd windows.Handle) (*image.RGBA, error) {
	rect, err := GetWindowRect(hwnd)
	if err != nil {
		return nil, err
	}
	return CaptureScreenRect(rect)
}

// CaptureScreenRect 截取屏幕矩形（与 tracker.CaptureScreenRect 同实现，
// 这里复制一份避免循环依赖）。
func CaptureScreenRect(r image.Rectangle) (*image.RGBA, error) {
	w := r.Dx()
	h := r.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("矩形非法: %v", r)
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	gdi32 := windows.NewLazySystemDLL("gdi32.dll")

	getDC := user32.NewProc("GetDC")
	releaseDC := user32.NewProc("ReleaseDC")
	createCompatibleDC := gdi32.NewProc("CreateCompatibleDC")
	createDIBSection := gdi32.NewProc("CreateDIBSection")
	selectObject := gdi32.NewProc("SelectObject")
	bitBlt := gdi32.NewProc("BitBlt")
	deleteObject := gdi32.NewProc("DeleteObject")
	deleteDC := gdi32.NewProc("DeleteDC")

	const SRCCOPY = 0x00CC0020

	hScreenDC, _, _ := getDC.Call(0)
	if hScreenDC == 0 {
		return nil, fmt.Errorf("GetDC 失败")
	}
	defer releaseDC.Call(0, hScreenDC)

	hMemDC, _, _ := createCompatibleDC.Call(hScreenDC)
	if hMemDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC 失败")
	}
	defer deleteDC.Call(hMemDC)

	type bih struct {
		Size          uint32
		Width         int32
		Height        int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPelsPerMeter int32
		YPelsPerMeter int32
		ClrUsed       uint32
		ClrImportant  uint32
	}
	type bi struct {
		Header bih
		Colors [3]uint32
	}
	info := bi{Header: bih{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: 0}}
	var bits unsafe.Pointer
	hBitmap, _, _ := createDIBSection.Call(hMemDC, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBitmap == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection 失败")
	}
	defer deleteObject.Call(hBitmap)
	selectObject.Call(hMemDC, hBitmap)

	ok, _, _ := bitBlt.Call(hMemDC, 0, 0, uintptr(w), uintptr(h), hScreenDC, uintptr(r.Min.X), uintptr(r.Min.Y), SRCCOPY)
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt 失败")
	}

	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	src := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < w*h; i++ {
		b := src[i*4+0]
		g := src[i*4+1]
		rr := src[i*4+2]
		a := src[i*4+3]
		rgba.Pix[i*4+0] = rr
		rgba.Pix[i*4+1] = g
		rgba.Pix[i*4+2] = b
		rgba.Pix[i*4+3] = a
	}
	return rgba, nil
}

// SavePNG 写到磁盘。
func SavePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// VirtualScreen 返回虚拟桌面（所有显示器并集）的左上角和大小。
func VirtualScreen() (x, y, w, h int) {
	const (
		smXVirtualScreen  = 76
		smYVirtualScreen  = 77
		smCxVirtualScreen = 78
		smCyVirtualScreen = 79
	)
	user32 := windows.NewLazySystemDLL("user32.dll")
	getMetrics := user32.NewProc("GetSystemMetrics")
	rx, _, _ := getMetrics.Call(smXVirtualScreen)
	ry, _, _ := getMetrics.Call(smYVirtualScreen)
	rw, _, _ := getMetrics.Call(smCxVirtualScreen)
	rh, _, _ := getMetrics.Call(smCyVirtualScreen)
	return int(int32(rx)), int(int32(ry)), int(int32(rw)), int(int32(rh))
}

// PostClose 给指定窗口发 WM_CLOSE，使其优雅退出。
func PostClose(hwnd windows.Handle) error {
	user32 := windows.NewLazySystemDLL("user32.dll")
	postMessage := user32.NewProc("PostMessageW")
	const WM_CLOSE = 0x0010
	r, _, _ := postMessage.Call(uintptr(hwnd), WM_CLOSE, 0, 0)
	if r == 0 {
		return fmt.Errorf("PostMessage WM_CLOSE 失败")
	}
	return nil
}

// SimulateClick 在指定屏幕坐标模拟鼠标左键点击（按下 + 抬起）。
//
// 用于 UI 自检：先 SetCursorPos 移动光标，再用 mouse_event 触发左键。
func SimulateClick(x, y int32) error {
	user32 := windows.NewLazySystemDLL("user32.dll")
	setCursorPos := user32.NewProc("SetCursorPos")
	mouseEvent := user32.NewProc("mouse_event")
	const (
		MOUSEEVENTF_LEFTDOWN = 0x0002
		MOUSEEVENTF_LEFTUP   = 0x0004
	)
	if r, _, _ := setCursorPos.Call(uintptr(x), uintptr(y)); r == 0 {
		return fmt.Errorf("SetCursorPos 失败")
	}
	mouseEvent.Call(MOUSEEVENTF_LEFTDOWN, 0, 0, 0, 0)
	mouseEvent.Call(MOUSEEVENTF_LEFTUP, 0, 0, 0, 0)
	return nil
}
