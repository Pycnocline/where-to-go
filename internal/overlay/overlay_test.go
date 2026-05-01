//go:build windows

package overlay

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestCreateShowDestroy 验证能够创建 → 显示 → 销毁 Win32 layered window，
// 且 WndProc 正确回调（OnPaint / OnSize / WM_DESTROY）。
//
// 这不是渲染视觉测试（headless CI 上 Win32 仍可创建不可见窗口）；目的是捕获
// New/Run/Destroy 路径的崩溃、内存访问错误、回调绑定问题。
func TestCreateShowDestroy(t *testing.T) {
	var paintCount int32
	var sizeCount int32
	done := make(chan error, 1)
	go func() {
		o, err := New(Config{
			Title: "overlay unit test",
			X:     10, Y: 10, W: 120, H: 80,
			Alpha: 0xC0,
			Resizable: true,
			OnPaint: func(hdc uintptr, w, h int32) {
				atomic.AddInt32(&paintCount, 1)
				if w <= 0 || h <= 0 {
					t.Errorf("OnPaint got invalid size %dx%d", w, h)
				}
			},
			OnSize: func(w, h int32) {
				atomic.AddInt32(&sizeCount, 1)
			},
		})
		if err != nil {
			done <- err
			return
		}
		o.Show()
		// 触发一次重绘 + 一次 resize
		o.Invalidate()
		time.AfterFunc(100*time.Millisecond, func() { o.SetBounds(20, 20, 160, 100) })
		time.AfterFunc(500*time.Millisecond, func() { o.PostClose() })
		Run() // 阻塞到 WM_DESTROY
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("overlay New failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overlay Run did not exit within 3s")
	}
	if atomic.LoadInt32(&paintCount) == 0 {
		t.Errorf("WM_PAINT 回调从未被触发（窗口可能根本没有被系统实际创建）")
	}
	if atomic.LoadInt32(&sizeCount) == 0 {
		t.Errorf("WM_SIZE 回调从未被触发")
	}
}

// TestAlphaAndClickThrough 验证 alpha / 穿透开关不会炸。
func TestAlphaAndClickThrough(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		o, err := New(Config{
			Title: "overlay alpha test",
			X:     10, Y: 10, W: 100, H: 60,
			Alpha: 0xFF,
			Resizable: false,
			OnPaint: func(hdc uintptr, w, h int32) {},
		})
		if err != nil {
			done <- err
			return
		}
		o.Show()
		o.SetAlpha(0x80)
		o.SetClickThrough(true)
		o.SetClickThrough(false)
		o.SetAlpha(0xFF)
		time.AfterFunc(200*time.Millisecond, func() { o.PostClose() })
		Run()
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("overlay New failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overlay did not exit within 3s")
	}
}
