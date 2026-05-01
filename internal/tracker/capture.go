package tracker

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// CaptureScreenRect 截取 Win32 屏幕坐标系下的指定矩形，返回 RGBA。
//
// 使用 GetDC/CreateCompatibleDC/CreateDIBSection/BitBlt/GetDIBits 链路。
// 若 r 越出主屏，由 BitBlt 自行裁剪（仍返回完整大小图，越界部分为黑）。
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
	const BI_RGB = 0
	const DIB_RGB_COLORS = 0

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

	type bitmapInfoHeader struct {
		Size            uint32
		Width           int32
		Height          int32
		Planes          uint16
		BitCount        uint16
		Compression     uint32
		SizeImage       uint32
		XPelsPerMeter   int32
		YPelsPerMeter   int32
		ClrUsed         uint32
		ClrImportant    uint32
	}
	type bitmapInfo struct {
		Header bitmapInfoHeader
		Colors [3]uint32
	}
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        40,
			Width:       int32(w),
			Height:      -int32(h), // 自上而下
			Planes:      1,
			BitCount:    32,
			Compression: BI_RGB,
		},
	}
	var bits unsafe.Pointer
	hBitmap, _, _ := createDIBSection.Call(hMemDC,
		uintptr(unsafe.Pointer(&bi)),
		DIB_RGB_COLORS,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hBitmap == 0 || bits == nil {
		return nil, fmt.Errorf("CreateDIBSection 失败")
	}
	defer deleteObject.Call(hBitmap)

	selectObject.Call(hMemDC, hBitmap)

	ok, _, _ := bitBlt.Call(hMemDC, 0, 0, uintptr(w), uintptr(h),
		hScreenDC, uintptr(r.Min.X), uintptr(r.Min.Y), SRCCOPY)
	if ok == 0 {
		return nil, fmt.Errorf("BitBlt 失败")
	}

	// 读取像素：DIB 是 BGRA
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

// SavePNG 用于调试：把 RGBA 写到磁盘。
func SavePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
