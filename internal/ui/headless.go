package ui

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/where-to-go/app/internal/wiki"
)

// TestAction 注入到 headless 渲染中的模拟事件。
type TestAction struct {
	Kind   ActionKind
	Point  f32.Point
	Point2 f32.Point // ActionDrag 用：从 Point 拖到 Point2
	Scroll f32.Point // 仅 Scroll 时有效
}

// ActionKind 事件类型。
type ActionKind int

const (
	ActionClick ActionKind = iota
	ActionScroll
	ActionDrag
)

// RenderHeadless 在不创建真实窗口的情况下，把当前 App 状态渲染到一张 PNG。
//
// 流程：
//  1. 同步装载本地缓存（要求事先 -cmd fetch 过）
//  2. 创建 width×height 的离屏 GPU 窗口
//  3. 渲染一帧让控件登记交互区域
//  4. 依次"派发"用户事件（Click / Scroll）+ 渲染一帧
//  5. 最终再渲染一帧并截屏
//
// 整个过程在调用线程同步完成，无需真实窗口。
func RenderHeadless(t *Theme, cfg Config, actions []TestAction, width, height int, outPath string) error {
	a, err := New(t, cfg)
	if err != nil {
		return err
	}
	if err := a.loadForHeadless(); err != nil {
		return err
	}

	hw, err := headless.NewWindow(width, height)
	if err != nil {
		return fmt.Errorf("创建 headless GPU 窗口失败: %w", err)
	}
	defer hw.Release()

	var router input.Router
	var ops op.Ops

	frame := func() error {
		ops.Reset()
		gtx := layout.Context{
			Ops:    &ops,
			Source: router.Source(),
			Constraints: layout.Constraints{
				Min: image.Point{},
				Max: image.Point{X: width, Y: height},
			},
			Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		}
		a.layout(gtx)
		router.Frame(&ops)
		return hw.Frame(&ops)
	}

	if err := frame(); err != nil {
		return err
	}

	for _, act := range actions {
		switch act.Kind {
		case ActionClick:
			router.Queue(
				pointer.Event{Kind: pointer.Press, Position: act.Point, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse},
				pointer.Event{Kind: pointer.Release, Position: act.Point, Source: pointer.Mouse},
			)
		case ActionScroll:
			router.Queue(pointer.Event{
				Kind:     pointer.Scroll,
				Position: act.Point,
				Scroll:   act.Scroll,
				Source:   pointer.Mouse,
			})
		case ActionDrag:
			// 模拟一次完整拖拽：按下 → 数次 Move（按住按键）→ 抬起。
			// 注意：Router 拒绝直接接收合成的 Drag 事件，
			// Drag 由 Gio 内部根据 "鼠标按下后的 Move" 自动产生。
			router.Queue(pointer.Event{
				Kind: pointer.Press, Position: act.Point,
				Buttons: pointer.ButtonPrimary, Source: pointer.Mouse,
			})
			if err := frame(); err != nil {
				return err
			}
			steps := 8
			for i := 1; i <= steps; i++ {
				t := float32(i) / float32(steps)
				p := f32.Pt(
					act.Point.X+(act.Point2.X-act.Point.X)*t,
					act.Point.Y+(act.Point2.Y-act.Point.Y)*t,
				)
				router.Queue(pointer.Event{
					Kind: pointer.Move, Position: p,
					Buttons: pointer.ButtonPrimary, Source: pointer.Mouse,
				})
				if err := frame(); err != nil {
					return err
				}
			}
			router.Queue(pointer.Event{
				Kind: pointer.Release, Position: act.Point2,
				Source: pointer.Mouse,
			})
		}
		if err := frame(); err != nil {
			return err
		}
	}

	if err := frame(); err != nil {
		return err
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	if err := hw.Screenshot(img); err != nil {
		return fmt.Errorf("截屏失败: %w", err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// loadForHeadless 同步从本地缓存装入数据，跳过 GUI 抓取流程。
func (a *App) loadForHeadless() error {
	r, err := wiki.LoadResult(a.cacheRoot)
	if err != nil {
		return fmt.Errorf("加载本地缓存失败（请先运行 -cmd fetch）: %w", err)
	}
	a.store.SetData(r)
	a.cache.OnTileReady = func() {}
	a.icons.OnReady = func() {}
	a.mapView = NewMapView(a.Theme, a.store, a.cache, a.icons)
	a.mapView.Editor = a.editor
	a.mapView.Routes = a.routes
	a.applySettingsToMapView()
	a.mapView.SetDebug(true)
	a.refreshDefaultVisibleTypes()
	a.mode = ModeMain
	a.fetchCtx, a.fetchCancel = context.WithCancel(context.Background())
	return nil
}
