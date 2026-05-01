package ui

// 路径自定义颜色：调色盘 + HSV / Hex 编辑器。从 PathListView 的"颜色块"
// 点击触发，整窗口浮层（modal-ish）显示，应用 / 取消后关闭。
//
// 设计要点：
//   - 12 个预设色（覆盖常用语义色 + 8 个调和色），方便一键选中。
//   - 一个 200x200 的 HSV 调色板（X = 饱和度，Y = 亮度），右侧 16dp 宽度的
//     色相条（点击/拖拽更新 H）。点击 / 拖拽 → 更新当前色。
//   - 底部一个 6 字符 Hex 输入框 + 一个色块预览。
//   - "应用" / "取消"按钮。
//
// 不依赖外部 SVG / 图片，全部 paint.FillShape + clip.Path 自绘。

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/mapdata"
)

// presetColors 12 个常用色（hex）。前 4 个是常见路径色（绿 / 黄 / 红 / 蓝）。
var presetColors = []string{
	"#4ac66e", "#f2b04f", "#e26a6a", "#5aa9e6",
	"#9c6cdc", "#e07b9a", "#3cd2c8", "#f97e36",
	"#a3d36b", "#fcd34d", "#f87171", "#94a3b8",
}

// ColorPickerView 一个覆盖在主区域上的调色盘弹窗。
//
// app.colorPicker 持有一个实例；当 Active=true 时主 Layout 会盖在地图之上。
// Apply 时由它自己写回 path 并 Save，关闭弹窗。
type ColorPickerView struct {
	app *App

	Active   bool
	pathID   string

	// 当前选中的颜色（HSV）
	h float64 // 0..1
	s float64 // 0..1
	v float64 // 0..1

	// 控件
	presetBtns []widget.Clickable
	hexEd      widget.Editor
	applyBtn   widget.Clickable
	cancelBtn  widget.Clickable

	// 自绘控件的事件 tag
	svTag    int
	hueTag   int
	svDragging bool
	hueDragging bool
}

// NewColorPickerView 构造。预先生成 preset 按钮缓存。
func NewColorPickerView(a *App) *ColorPickerView {
	v := &ColorPickerView{
		app:        a,
		presetBtns: make([]widget.Clickable, len(presetColors)),
	}
	v.hexEd.SingleLine = true
	v.hexEd.Submit = true
	return v
}

// Open 打开 picker 编辑指定 path 的颜色。
func (v *ColorPickerView) Open(p *mapdata.Path) {
	v.Active = true
	v.pathID = p.ID
	c := resolvePathColor(p)
	v.h, v.s, v.v = rgbToHSV(c)
	v.hexEd.SetText(strings.ToLower(colorToHex(c)))
}

// Close 关闭 picker。
func (v *ColorPickerView) Close() { v.Active = false; v.pathID = "" }

// currentColor 当前 HSV 对应的 NRGBA。
func (v *ColorPickerView) currentColor() color.NRGBA {
	return hsvToRGB(v.h, v.s, v.v)
}

// applyAndClose 写回 path 并关闭。
func (v *ColorPickerView) applyAndClose() {
	if v.app == nil || v.app.routes == nil || v.pathID == "" {
		v.Close()
		return
	}
	p := v.app.routes.Find(v.pathID)
	if p != nil {
		p.Color = colorToHex(v.currentColor())
		_ = v.app.routes.Save(p)
	}
	v.Close()
	if v.app.Window != nil {
		v.app.Window.Invalidate()
	}
}

// Layout 渲染整个浮层。覆盖 gtx.Constraints.Max 全区域，背景半透明遮罩。
func (v *ColorPickerView) Layout(gtx layout.Context) layout.Dimensions {
	if !v.Active {
		return layout.Dimensions{}
	}
	t := v.app.Theme

	// 处理控件事件
	for i := range v.presetBtns {
		if v.presetBtns[i].Clicked(gtx) {
			c, _ := parseHexColor(presetColors[i])
			v.h, v.s, v.v = rgbToHSV(c)
			v.hexEd.SetText(strings.ToLower(presetColors[i]))
		}
	}
	if v.applyBtn.Clicked(gtx) {
		v.applyAndClose()
		return layout.Dimensions{Size: gtx.Constraints.Max}
	}
	if v.cancelBtn.Clicked(gtx) {
		v.Close()
		return layout.Dimensions{Size: gtx.Constraints.Max}
	}
	for {
		ev, ok := v.hexEd.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			if c, ok := parseHexColor(strings.TrimSpace(v.hexEd.Text())); ok {
				v.h, v.s, v.v = rgbToHSV(c)
			}
		}
	}
	// Esc 关闭
	for {
		ev, ok := gtx.Source.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			v.Close()
			return layout.Dimensions{Size: gtx.Constraints.Max}
		}
	}

	// 半透明遮罩 + 全屏点击屏蔽（防止点击穿透到地图）
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 0xa0}, clip.Rect{Max: gtx.Constraints.Max}.Op())

	// 居中放置面板（自适应宽度但有上下界）
	panelW := gtx.Dp(unit.Dp(420))
	panelH := gtx.Dp(unit.Dp(440))
	if panelW > gtx.Constraints.Max.X-32 {
		panelW = gtx.Constraints.Max.X - 32
	}
	if panelH > gtx.Constraints.Max.Y-32 {
		panelH = gtx.Constraints.Max.Y - 32
	}
	x0 := (gtx.Constraints.Max.X - panelW) / 2
	y0 := (gtx.Constraints.Max.Y - panelH) / 2

	// 面板背景 + 阴影
	shadowR := image.Rect(x0+2, y0+4, x0+panelW+2, y0+panelH+4)
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0x80}, clip.UniformRRect(shadowR, gtx.Dp(unit.Dp(10))).Op(gtx.Ops))
	panelRect := image.Rect(x0, y0, x0+panelW, y0+panelH)
	paint.FillShape(gtx.Ops, t.Panel, clip.UniformRRect(panelRect, gtx.Dp(unit.Dp(10))).Op(gtx.Ops))

	stack := op.Offset(image.Pt(x0, y0)).Push(gtx.Ops)
	defer stack.Pop()
	gtx.Constraints = layout.Exact(image.Pt(panelW, panelH))

	return layout.UniformInset(unit.Dp(16)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			// 标题
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.H6(t.Material, "选择路径颜色")
				lbl.Color = t.Text
				lbl.Font.Typeface = "CJK"
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(10)}.Layout),
			// 调色板 + 色相条
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return v.layoutPaletteRow(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			// 预设色
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body2(t.Material, "预设")
				lbl.Color = t.TextDim
				lbl.Font.Typeface = "CJK"
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return v.layoutPresetRow(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			// Hex 输入 + 当前色块
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return v.layoutHexRow(gtx)
			}),
			layout.Flexed(1, layout.Spacer{}.Layout),
			// 应用 / 取消
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						s := defaultIconButton(t, "取消", IconClose)
						return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return s.Layout(gtx, t, &v.cancelBtn)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						s := defaultIconButton(t, "应用", IconCheck)
						s.Active = true
						return s.Layout(gtx, t, &v.applyBtn)
					}),
				)
			}),
		)
	})
}

// layoutPaletteRow SV 调色板（左）+ 色相条（右）。
func (v *ColorPickerView) layoutPaletteRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	// 整行高度
	rowH := gtx.Dp(unit.Dp(180))
	hueW := gtx.Dp(unit.Dp(20))
	gap := gtx.Dp(unit.Dp(8))
	svW := gtx.Constraints.Max.X - hueW - gap
	if svW < 100 {
		svW = 100
	}
	gtx.Constraints = layout.Exact(image.Pt(svW+gap+hueW, rowH))

	// SV 矩形：底色 = 当前 hue 的纯色 + 渐变叠加
	// 由于 Gio 没有内置 gradient，我们用固定步长（16x16 cell）来 fake，
	// 视觉上够"调色板"的效果。每个 cell 中心计算 (s, val) → 颜色。
	const cellsX = 24
	const cellsY = 16
	cellW := svW / cellsX
	cellH := rowH / cellsY
	for j := 0; j < cellsY; j++ {
		for i := 0; i < cellsX; i++ {
			s := float64(i) / float64(cellsX-1)
			val := 1.0 - float64(j)/float64(cellsY-1)
			c := hsvToRGB(v.h, s, val)
			rx := i * cellW
			ry := j * cellH
			rw := cellW
			rh := cellH
			if i == cellsX-1 {
				rw = svW - rx
			}
			if j == cellsY-1 {
				rh = rowH - ry
			}
			paint.FillShape(gtx.Ops, c, clip.Rect{
				Min: image.Pt(rx, ry),
				Max: image.Pt(rx+rw, ry+rh),
			}.Op())
		}
	}
	// 当前 (s, v) 的指示圆
	cx := int(v.s * float64(svW))
	cy := int((1 - v.v) * float64(rowH))
	v.drawIndicator(gtx, cx, cy)

	// SV 区域指针事件
	v.handleSVEvents(gtx, image.Pt(svW, rowH))

	// 色相条
	hueOff := op.Offset(image.Pt(svW+gap, 0)).Push(gtx.Ops)
	const hueSteps = 32
	stepH := rowH / hueSteps
	for j := 0; j < hueSteps; j++ {
		c := hsvToRGB(float64(j)/float64(hueSteps-1), 1, 1)
		ry := j * stepH
		rh := stepH
		if j == hueSteps-1 {
			rh = rowH - ry
		}
		paint.FillShape(gtx.Ops, c, clip.Rect{Min: image.Pt(0, ry), Max: image.Pt(hueW, ry+rh)}.Op())
	}
	hueY := int(v.h * float64(rowH))
	v.drawIndicator(gtx, hueW/2, hueY)
	hueOff.Pop()

	// 色相条事件（在 svW+gap 偏移上）
	hueArea := op.Offset(image.Pt(svW+gap, 0)).Push(gtx.Ops)
	v.handleHueEvents(gtx, image.Pt(hueW, rowH))
	hueArea.Pop()

	_ = t
	return layout.Dimensions{Size: image.Pt(svW+gap+hueW, rowH)}
}

// drawIndicator 在 (cx, cy) 画一个白边黑芯小圆，作为选中位置标记。
func (v *ColorPickerView) drawIndicator(gtx layout.Context, cx, cy int) {
	r := gtx.Dp(unit.Dp(6))
	outer := image.Rect(cx-r-1, cy-r-1, cx+r+1, cy+r+1)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, clip.Ellipse(outer).Op(gtx.Ops))
	inner := image.Rect(cx-r+1, cy-r+1, cx+r-1, cy+r-1)
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0xff}, clip.Ellipse(inner).Op(gtx.Ops))
}

// handleSVEvents 处理 SV 调色板的点击 / 拖拽 → 更新 v.s / v.v。
func (v *ColorPickerView) handleSVEvents(gtx layout.Context, size image.Point) {
	// 注册点击区域 + 事件 tag
	cs := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &v.svTag)
	cs.Pop()
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target: &v.svTag,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			if pe.Buttons.Contain(pointer.ButtonPrimary) {
				v.svDragging = true
				v.updateSVFrom(pe.Position, size)
			}
		case pointer.Drag:
			if v.svDragging {
				v.updateSVFrom(pe.Position, size)
			}
		case pointer.Release:
			v.svDragging = false
		}
	}
}

func (v *ColorPickerView) updateSVFrom(p f32.Point, size image.Point) {
	x := float64(p.X) / float64(size.X)
	y := float64(p.Y) / float64(size.Y)
	if x < 0 {
		x = 0
	} else if x > 1 {
		x = 1
	}
	if y < 0 {
		y = 0
	} else if y > 1 {
		y = 1
	}
	v.s = x
	v.v = 1 - y
	v.hexEd.SetText(strings.ToLower(colorToHex(v.currentColor())))
}

func (v *ColorPickerView) handleHueEvents(gtx layout.Context, size image.Point) {
	cs := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &v.hueTag)
	cs.Pop()
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target: &v.hueTag,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			if pe.Buttons.Contain(pointer.ButtonPrimary) {
				v.hueDragging = true
				v.updateHueFrom(pe.Position, size)
			}
		case pointer.Drag:
			if v.hueDragging {
				v.updateHueFrom(pe.Position, size)
			}
		case pointer.Release:
			v.hueDragging = false
		}
	}
}

func (v *ColorPickerView) updateHueFrom(p f32.Point, size image.Point) {
	y := float64(p.Y) / float64(size.Y)
	if y < 0 {
		y = 0
	} else if y > 1 {
		y = 1
	}
	v.h = y
	v.hexEd.SetText(strings.ToLower(colorToHex(v.currentColor())))
}

// layoutPresetRow 预设色快捷选。
func (v *ColorPickerView) layoutPresetRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	children := make([]layout.FlexChild, 0, len(presetColors))
	for i := range presetColors {
		i := i
		c, _ := parseHexColor(presetColors[i])
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return v.presetBtns[i].Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				sz := gtx.Dp(unit.Dp(28))
				rect := image.Rect(0, 0, sz, sz)
				paint.FillShape(gtx.Ops, c, clip.UniformRRect(rect, gtx.Dp(unit.Dp(6))).Op(gtx.Ops))
				// hover / press 时画白边
				if v.presetBtns[i].Hovered() || v.presetBtns[i].Pressed() {
					drawRectBorder(gtx.Ops, rect, 2, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xc0})
				}
				return layout.Dimensions{Size: image.Pt(sz, sz)}
			})
		}))
		children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
	}
	_ = t
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
}

// layoutHexRow Hex 输入 + 大色块预览。
func (v *ColorPickerView) layoutHexRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.currentColor()
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			sz := gtx.Dp(unit.Dp(36))
			rect := image.Rect(0, 0, sz, sz)
			paint.FillShape(gtx.Ops, cur, clip.UniformRRect(rect, gtx.Dp(unit.Dp(6))).Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(sz, sz)}
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, "Hex")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(140))
			ed := material.Editor(t.Material, &v.hexEd, "#rrggbb")
			ed.Color = t.Text
			return ed.Layout(gtx)
		}),
	)
}

// hsvToRGB H/S/V ∈ [0, 1] → NRGBA。
func hsvToRGB(h, s, v float64) color.NRGBA {
	h = math.Mod(h, 1)
	if h < 0 {
		h += 1
	}
	if s < 0 {
		s = 0
	}
	if s > 1 {
		s = 1
	}
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	hh := h * 6
	i := int(hh)
	f := hh - float64(i)
	p := v * (1 - s)
	q := v * (1 - s*f)
	t := v * (1 - s*(1-f))
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	case 5:
		r, g, b = v, p, q
	}
	return color.NRGBA{
		R: byte(r*255 + 0.5),
		G: byte(g*255 + 0.5),
		B: byte(b*255 + 0.5),
		A: 0xff,
	}
}

// rgbToHSV NRGBA → H/S/V ∈ [0, 1]。
func rgbToHSV(c color.NRGBA) (h, s, v float64) {
	r := float64(c.R) / 255
	g := float64(c.G) / 255
	b := float64(c.B) / 255
	max := r
	if g > max {
		max = g
	}
	if b > max {
		max = b
	}
	min := r
	if g < min {
		min = g
	}
	if b < min {
		min = b
	}
	v = max
	d := max - min
	if max == 0 {
		s = 0
	} else {
		s = d / max
	}
	if d == 0 {
		h = 0
		return
	}
	switch max {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	case b:
		h = (r-g)/d + 4
	}
	h /= 6
	return
}

// debug helper
var _ = fmt.Sprintf
