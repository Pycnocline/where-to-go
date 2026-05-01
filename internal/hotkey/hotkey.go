// Package hotkey 全局热键注册（基于 RegisterHotKey）。
package hotkey

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Modifier 修饰键。
type Modifier uint32

const (
	ModAlt      Modifier = 0x0001
	ModCtrl     Modifier = 0x0002
	ModShift    Modifier = 0x0004
	ModWin      Modifier = 0x0008
	ModNoRepeat Modifier = 0x4000
)

// HotkeyDef 一个待注册热键。
type HotkeyDef struct {
	ID     int
	Mod    Modifier
	VK     uint32
	Action func()
}

// Manager 全局热键管理。
type Manager struct {
	mu      sync.Mutex
	defs    []HotkeyDef
	stopCh  chan struct{}
	stopped bool
}

// New 构造。
func New() *Manager { return &Manager{stopCh: make(chan struct{})} }

// Add 在 Run 前注册热键。
func (m *Manager) Add(d HotkeyDef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defs = append(m.defs, d)
}

// Run 在调用线程上运行消息循环。阻塞，需要单独 goroutine 调用。
//
// 注意：一旦运行，所有 RegisterHotKey 都绑定到该线程；在退出前请勿移走该线程。
func (m *Manager) Run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	user32 := windows.NewLazySystemDLL("user32.dll")
	registerHotKey := user32.NewProc("RegisterHotKey")
	unregisterHotKey := user32.NewProc("UnregisterHotKey")
	getMessage := user32.NewProc("GetMessageW")
	peekMessage := user32.NewProc("PeekMessageW")
	translateMessage := user32.NewProc("TranslateMessage")
	dispatchMessage := user32.NewProc("DispatchMessageW")
	_ = peekMessage
	_ = translateMessage
	_ = dispatchMessage

	const WM_HOTKEY = 0x0312
	type msg struct {
		Hwnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}

	m.mu.Lock()
	defs := append([]HotkeyDef(nil), m.defs...)
	m.mu.Unlock()

	for _, d := range defs {
		registerHotKey.Call(0, uintptr(d.ID), uintptr(d.Mod), uintptr(d.VK))
	}
	defer func() {
		for _, d := range defs {
			unregisterHotKey.Call(0, uintptr(d.ID))
		}
	}()

	for {
		select {
		case <-m.stopCh:
			return
		default:
		}
		var mm msg
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&mm)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		if mm.Message == WM_HOTKEY {
			id := int(mm.WParam)
			for _, d := range defs {
				if d.ID == id && d.Action != nil {
					go d.Action()
					break
				}
			}
		}
	}
}

// Stop 触发 Run 退出。安全地多次调用。
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	close(m.stopCh)
}

// VK_* 常用按键码（Win32 VirtualKey）。
const (
	VK_M = 0x4D
	VK_T = 0x54
	VK_S = 0x53
	VK_R = 0x52
	VK_F = 0x46
)
