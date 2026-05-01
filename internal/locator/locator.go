// Package locator 负责实时定位玩家在世界坐标里的位置和朝向。
//
// 核心思路：周期性地用 Win32 GDI 截取屏幕中的"游戏小地图"区域（ROI），
// 与本地缓存的 wiki 地图瓦片做归一化互相关 (NCC) 模板匹配，得到 (worldX, worldY)。
// 朝向 (heading) 通过检测 ROI 内的"玩家箭头"图标方向得到。
//
// 具体实现分两步：
//   - locator.go: 调度（goroutine + 定时器 + 状态机）
//   - match.go:   匹配算法（位置）
//   - heading.go: 朝向检测（占位实现 + 校准点）
//
// 业务层只需要：
//   loc := locator.New(cfg)
//   loc.OnFix = func(fix locator.Fix) { ... 更新 UI ... }
//   loc.Start()
//   defer loc.Stop()
package locator

import (
	"context"
	"fmt"
	"image"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/where-to-go/app/internal/winutil"
)

// ROI 屏幕上小地图的矩形（绝对屏幕坐标）。
type ROI struct {
	X, Y, W, H int
}

// Empty 是否未配置。
func (r ROI) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Fix 一次定位结果。
type Fix struct {
	WorldX     float64
	WorldY     float64
	Heading    float64 // 弧度。0 = 北 / 上方；顺时针正向。
	HasHeading bool
	Confidence float64 // 0..1
	When       time.Time

	// Tentative=true 表示这是一个"临时 / 不确信"位置（例如来自 patch-vote
	// 备用算法）。UI 应该显示它供用户感知，但不应将其作为持久 seed，
	// 也不应写回 LastPlayer，调用方还应调度后台全图重定位以替换它。
	Tentative bool
}

// CaptureFn 抽象屏幕截取，方便测试 / 模拟模式注入。
type CaptureFn func(roi ROI) (*image.RGBA, error)

// MatchFn 给定一帧 ROI 截图，返回匹配出的世界坐标和朝向。
//
// 真正的实现见 match.go / heading.go；模拟模式可以替换为返回沿路径的预设位置。
type MatchFn func(roi *image.RGBA) (Fix, error)

// Config 定位器配置。
type Config struct {
	ROI      ROI
	Interval time.Duration // 抓取间隔，默认 250ms
	Capture  CaptureFn     // 默认 winutil.CaptureScreenRect
	Match    MatchFn       // 必填
}

// Locator 一个长期运行的定位器。
type Locator struct {
	cfg    Config
	mu     sync.Mutex
	cancel context.CancelFunc
	last   Fix
	OnFix  func(Fix)
	OnErr  func(error)
}

// New 用给定配置构造定位器。
func New(cfg Config) *Locator {
	if cfg.Interval <= 0 {
		cfg.Interval = 250 * time.Millisecond
	}
	if cfg.Capture == nil {
		cfg.Capture = func(r ROI) (*image.RGBA, error) {
			return winutil.CaptureScreenRect(image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H))
		}
	}
	return &Locator{cfg: cfg}
}

// Start 启动后台 goroutine。多次 Start 之前需 Stop。
func (l *Locator) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return fmt.Errorf("locator already running")
	}
	if l.cfg.Match == nil {
		return fmt.Errorf("locator: Match function required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	go l.run(ctx)
	return nil
}

// Stop 关闭定位器。可重复调用。
func (l *Locator) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
}

// Last 返回最近一次 Fix（拷贝）。可能为零值（尚未定位成功）。
func (l *Locator) Last() Fix {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// SetROI 运行时更新 ROI（下次抓取生效）。
func (l *Locator) SetROI(r ROI) {
	l.mu.Lock()
	l.cfg.ROI = r
	l.mu.Unlock()
}

// SetCapture 运行时替换截屏函数（如模拟器旁路）。
func (l *Locator) SetCapture(c CaptureFn) {
	l.mu.Lock()
	l.cfg.Capture = c
	l.mu.Unlock()
}

// SetMatch 运行时替换匹配函数。
func (l *Locator) SetMatch(m MatchFn) {
	l.mu.Lock()
	l.cfg.Match = m
	l.mu.Unlock()
}

func (l *Locator) run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.tick()
		}
	}
}

func (l *Locator) tick() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[locator] tick panic: %v\n%s", r, debug.Stack())
			if l.OnErr != nil {
				l.OnErr(fmt.Errorf("tick panic: %v", r))
			}
		}
	}()
	l.mu.Lock()
	roi := l.cfg.ROI
	cap := l.cfg.Capture
	mt := l.cfg.Match
	l.mu.Unlock()
	if roi.Empty() {
		return
	}
	img, err := cap(roi)
	if err != nil {
		if l.OnErr != nil {
			l.OnErr(fmt.Errorf("capture: %w", err))
		}
		return
	}
	fix, err := mt(img)
	if err != nil {
		if l.OnErr != nil {
			l.OnErr(fmt.Errorf("match: %w", err))
		}
		return
	}
	fix.When = time.Now()
	l.mu.Lock()
	l.last = fix
	l.mu.Unlock()
	if l.OnFix != nil {
		l.OnFix(fix)
	}
}
