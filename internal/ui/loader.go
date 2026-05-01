package ui

import (
	"fmt"
	"image"
	"image/color"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/wiki"
)

// LoaderState 加载页内部可变状态（线程安全）。
type LoaderState struct {
	mu       sync.Mutex
	stage    string
	message  string
	done     int64
	total    int64
	failed   int64
	logs     []logLine
	finished bool
	err      error
}

type logLine struct {
	t    time.Time
	text string
	kind string // info/warn/err
}

// NewLoaderState 空状态。
func NewLoaderState() *LoaderState { return &LoaderState{} }

// HandleProgress 是 Fetcher 的 OnProgress 回调。
func (s *LoaderState) HandleProgress(ev wiki.ProgressEvent, w *app.Window) {
	s.mu.Lock()
	if ev.Stage != "" {
		s.stage = ev.Stage
	}
	if ev.Message != "" {
		s.message = ev.Message
		kind := "info"
		if ev.Failed > 0 && ev.Done == ev.Total {
			kind = "warn"
		}
		s.logs = append(s.logs, logLine{t: time.Now(), text: ev.Message, kind: kind})
		// 限制日志条数，避免无限增长
		if len(s.logs) > 500 {
			s.logs = s.logs[len(s.logs)-500:]
		}
	}
	if ev.Done > 0 {
		s.done = ev.Done
	}
	if ev.Total > 0 {
		s.total = ev.Total
	}
	if ev.Failed > 0 {
		s.failed = ev.Failed
	}
	if ev.Err != nil {
		s.err = ev.Err
		s.logs = append(s.logs, logLine{t: time.Now(), text: "[致命错误] " + ev.Err.Error(), kind: "err"})
	}
	s.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

// MarkFinished 标记加载完成，UI 主循环根据此切到主视图。
func (s *LoaderState) MarkFinished(err error, w *app.Window) {
	s.mu.Lock()
	s.finished = true
	if err != nil {
		s.err = err
		s.logs = append(s.logs, logLine{t: time.Now(), text: "[致命错误] " + err.Error(), kind: "err"})
	} else {
		s.logs = append(s.logs, logLine{t: time.Now(), text: "全部资源加载完成。", kind: "info"})
	}
	s.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

// IsFinished 主循环每帧检查。
func (s *LoaderState) IsFinished() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished, s.err
}

// LoaderView 加载页视图组件。
type LoaderView struct {
	State    *LoaderState
	Theme    *Theme
	logsList widget.List
}

// NewLoaderView 构造。
func NewLoaderView(t *Theme, st *LoaderState) *LoaderView {
	v := &LoaderView{Theme: t, State: st}
	v.logsList.Axis = layout.Vertical
	// 让日志面板默认跟随最新项滚动到底部。Gio 的 List 在 ScrollToEnd=true
	// 且新条目不断追加时会保持视口贴底；用户主动向上滚动时会脱离贴底（取决于
	// Gio 内部对 ScrollToEnd 的处理）。
	v.logsList.ScrollToEnd = true
	return v
}

// Layout 渲染加载页一帧。
func (v *LoaderView) Layout(gtx layout.Context) layout.Dimensions {
	v.State.mu.Lock()
	stage := v.State.stage
	msg := v.State.message
	done := v.State.done
	total := v.State.total
	failed := v.State.failed
	logs := append([]logLine(nil), v.State.logs...)
	hasErr := v.State.err != nil
	v.State.mu.Unlock()

	// 全屏背景
	paint.FillShape(gtx.Ops, v.Theme.BG, clip.Rect{Max: gtx.Constraints.Max}.Op())

	return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceEnd}.Layout(gtx,
			// 标题
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.H4(v.Theme.Material, "where-to-go · 资源加载中")
				lbl.Color = v.Theme.Text
				lbl.Font.Weight = font.Bold
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body2(v.Theme.Material, "首次启动需要从 wiki.biligame.com/rocom 拉取地图元数据、点位与基础瓦片，仅需一次。")
				lbl.Color = v.Theme.TextDim
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Caption(v.Theme.Material,
					"数据来源：洛克王国世界wiki (https://wiki.biligame.com/rocom)  ·  内容遵循 CC BY-NC-SA 4.0")
				lbl.Color = v.Theme.TextDim
				lbl.Font.Typeface = "CJK"
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),

			// 当前阶段 + 消息
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				stageZh := stageLabel(stage)
				lbl := material.Body1(v.Theme.Material, fmt.Sprintf("阶段：%s", stageZh))
				lbl.Color = v.Theme.Text
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body2(v.Theme.Material, msg)
				lbl.Color = v.Theme.TextDim
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),

			// 进度条
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return v.layoutProgressBar(gtx, done, total, failed)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),

			// 日志面板（占余下空间）
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return v.layoutLogPanel(gtx, logs, hasErr)
			}),
		)
	})
}

func (v *LoaderView) layoutProgressBar(gtx layout.Context, done, total, failed int64) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(unit.Dp(8))
	// 背景
	bg := clip.UniformRRect(image.Rectangle{Max: image.Point{X: w, Y: h}}, h/2)
	paint.FillShape(gtx.Ops, v.Theme.Panel, bg.Op(gtx.Ops))
	// 前景
	if total > 0 {
		filled := int(int64(w) * done / total)
		if filled > w {
			filled = w
		}
		if filled > 0 {
			fg := clip.UniformRRect(image.Rectangle{Max: image.Point{X: filled, Y: h}}, h/2)
			paint.FillShape(gtx.Ops, v.Theme.Accent, fg.Op(gtx.Ops))
		}
	}
	dim := layout.Dimensions{Size: image.Point{X: w, Y: h}}
	// 进度数字
	op.Offset(image.Point{Y: h + gtx.Dp(unit.Dp(4))}).Add(gtx.Ops)
	txt := ""
	if total > 0 {
		txt = fmt.Sprintf("%d / %d  失败 %d", done, total, failed)
	} else {
		txt = "（统计中…）"
	}
	lbl := material.Caption(v.Theme.Material, txt)
	lbl.Color = v.Theme.TextDim
	d2 := lbl.Layout(gtx)
	dim.Size.Y += gtx.Dp(unit.Dp(4)) + d2.Size.Y
	return dim
}

func (v *LoaderView) layoutLogPanel(gtx layout.Context, logs []logLine, hasErr bool) layout.Dimensions {
	// 背景框
	rect := image.Rectangle{Max: gtx.Constraints.Max}
	paint.FillShape(gtx.Ops, v.Theme.Panel, clip.UniformRRect(rect, gtx.Dp(unit.Dp(6))).Op(gtx.Ops))

	return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(v.Theme.Material, &v.logsList).Layout(gtx, len(logs), func(gtx layout.Context, i int) layout.Dimensions {
			// 默认从前往后绘制，但日志希望最新在底，所以倒序索引
			line := logs[i]
			c := v.Theme.TextDim
			switch line.kind {
			case "warn":
				c = v.Theme.Warn
			case "err":
				c = v.Theme.Err
			case "info":
				c = v.Theme.Text
			}
			t := line.t.Format("15:04:05")
			lbl := material.Label(v.Theme.Material, unit.Sp(12), fmt.Sprintf("[%s] %s", t, line.text))
			lbl.Color = c
			lbl.Font.Typeface = "CJK"
			lbl.Alignment = text.Start
			return lbl.Layout(gtx)
		})
	})
}

func stageLabel(stage string) string {
	switch stage {
	case "metadata":
		return "拉取地图元数据"
	case "categories":
		return "解析点位类别"
	case "points":
		return "下载点位坐标"
	case "tiles":
		return "下载基础瓦片"
	case "":
		return "准备中"
	default:
		return stage
	}
}

// 防止未引用 color 警告（保留供拓展）
var _ = color.NRGBA{}
