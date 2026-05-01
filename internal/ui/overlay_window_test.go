//go:build windows

package ui

import (
	"testing"
	"time"
)

// TestOverlayWindowLifecycle 覆盖 OverlayWindow 的状态机：未启动 → Start → Open →
// Close → 状态归位。也间接验证 Win32 窗口创建 + Gio headless 渲染管线不 panic。
//
// 不验证视觉正确性（CI 上 headless）；若本机能运行，应当看到一个 600×400
// 的悬浮窗瞬间开关。
func TestOverlayWindowLifecycle(t *testing.T) {
	// 构造最小 App（无 mapView 也允许，overlay 会画纯背景）
	a := &App{settings: NewSettingsStore(t.TempDir() + "/cfg.json")}
	o := NewOverlay(a)

	if o.IsOpen() {
		t.Fatal("NewOverlay should not be open initially")
	}

	o.Start()
	// 给足时间让 Win32 窗口创建 + 第一帧渲染
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if o.IsOpen() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !o.IsOpen() {
		t.Fatal("overlay did not open within 2s")
	}

	// 触发各 API
	o.SetAlpha(0x80)
	o.SetClickThrough(true)
	o.SetClickThrough(false)
	o.SetAlpha(0xFF)

	// 让渲染循环至少跑 3 个 tick（每 100ms）
	time.Sleep(350 * time.Millisecond)

	o.Close()
	// 等关闭
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !o.IsOpen() {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if o.IsOpen() {
		t.Fatal("overlay did not close within 2s")
	}
}
