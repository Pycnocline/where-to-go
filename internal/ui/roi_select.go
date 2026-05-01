package ui

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/winutil"
)

// ROISelectView 全屏截图 + 拖拽框选 → 写回 Settings.MinimapROI。
//
// 工作流：
//   1. 进入此视图前，Capture() 抓一张全屏截图保存到 view.shot
//   2. 渲染时把 shot 等比缩放到窗口可用区域，居中
//   3. 用户在显示图上左键拖拽 → 记录矩形（显示坐标）
//   4. 显示坐标 → 屏幕坐标（除以 scale，加上 virtualScreen 原点）
//   5. 点 "保存" 写回 settings.MinimapROI；"重新截屏" 再来一次；"取消" 返回设置页
type ROISelectView struct {
	app *App

	shot       *image.RGBA // 整张虚拟桌面截图（屏幕原始像素）
	shotOp     paint.ImageOp
	originX    int // 虚拟屏左上角对应的"屏幕坐标系"
	originY    int

	// 拖拽状态（显示坐标系：相对于显示图左上角的像素）
	tag      int
	dragging bool
	dragSx   float32
	dragSy   float32
	curSx    float32
	curSy    float32
	hasRect  bool
	rectX0   float32
	rectY0   float32
	rectX1   float32
	rectY1   float32

	saveBtn   widget.Clickable
	retakeBtn widget.Clickable
	cancelBtn widget.Clickable

	// 上一帧 drawImageArea 内部计算出的显示参数（事件→屏幕坐标换算用）
	lastDispOrigin image.Point
	lastDispSize   image.Point

	msg string
}

// NewROISelectView 构造。
func NewROISelectView(a *App) *ROISelectView {
	return &ROISelectView{app: a}
}

// Capture 抓全屏截图，准备进入 ROI 选择模式。
func (v *ROISelectView) Capture() error {
	x, y, w, h := winutil.VirtualScreen()
	img, err := winutil.CaptureScreenRect(image.Rect(x, y, x+w, y+h))
	if err != nil {
		return fmt.Errorf("截屏失败：%w", err)
	}
	v.shot = img
	v.shotOp = paint.NewImageOp(img)
	v.originX = x
	v.originY = y
	v.dragging = false
	v.hasRect = false
	v.msg = "在图上拖拽鼠标框选小地图区域"
	return nil
}

// Layout 渲染并处理事件。
func (v *ROISelectView) Layout(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0x10, G: 0x12, B: 0x18, A: 0xff},
		clip.Rect{Max: gtx.Constraints.Max}.Op())

	// 先处理按钮事件：放在 Layout 顶端，避免任何后续 ops 抢占。
	if v.cancelBtn.Clicked(gtx) {
		v.app.mode = ModeSettings
		if v.app.Window != nil {
			v.app.Window.Invalidate()
		}
	}
	if v.retakeBtn.Clicked(gtx) {
		if err := v.Capture(); err != nil {
			v.msg = err.Error()
		}
		if v.app.Window != nil {
			v.app.Window.Invalidate()
		}
	}
	if v.saveBtn.Clicked(gtx) && v.hasRect {
		v.commitROI()
		if v.app.Window != nil {
			v.app.Window.Invalidate()
		}
	}

	if v.shot == nil {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body1(t.Material, "尚未截屏。点 重新截屏。")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		})
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		// 顶部说明栏
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = gtx.Dp(unit.Dp(48))
			gtx.Constraints.Max.Y = gtx.Dp(unit.Dp(48))
			rect := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, gtx.Constraints.Max.Y)}
			paint.FillShape(gtx.Ops, t.Panel, clip.Rect(rect).Op())
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Body1(t.Material, "校准小地图位置：在下方截图上拖动鼠标框选游戏内的小地图区域")
				lbl.Color = t.Text
				lbl.Font.Typeface = "CJK"
				lbl.MaxLines = 1
				return lbl.Layout(gtx)
			})
		}),
		// 中间显示截图
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Max
			v.handleEvents(gtx, size, 0)
			v.drawImageArea(gtx, size)
			return layout.Dimensions{Size: size}
		}),
		// 底部按钮栏
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = gtx.Dp(unit.Dp(56))
			gtx.Constraints.Max.Y = gtx.Dp(unit.Dp(56))
			rect := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, gtx.Constraints.Max.Y)}
			paint.FillShape(gtx.Ops, t.Panel, clip.Rect(rect).Op())
			return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16), Top: unit.Dp(10), Bottom: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Caption(t.Material, v.msg)
						lbl.Color = t.TextDim
						lbl.Font.Typeface = "CJK"
						return lbl.Layout(gtx)
					}),
					layout.Flexed(1, layout.Spacer{}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						b := material.Button(t.Material, &v.cancelBtn, "取消")
						b.Background = t.BG
						b.Color = t.Text
						b.TextSize = unit.Sp(13)
						b.Font.Typeface = "CJK"
						return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, b.Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						b := material.Button(t.Material, &v.retakeBtn, "重新截屏")
						b.Background = t.BG
						b.Color = t.Text
						b.TextSize = unit.Sp(13)
						b.Font.Typeface = "CJK"
						return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, b.Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						b := material.Button(t.Material, &v.saveBtn, "保存框选")
						if v.hasRect {
							b.Background = t.Accent
						} else {
							b.Background = t.BG
							b.Color = t.TextDim
						}
						b.TextSize = unit.Sp(13)
						b.Font.Typeface = "CJK"
						return b.Layout(gtx)
					}),
				)
			})
		}),
	)
}

// drawImageArea 渲染缩放截图 + 当前框选矩形，居中在 (0, 0) 到 size 的区域。
func (v *ROISelectView) drawImageArea(gtx layout.Context, size image.Point) {
	src := v.shot.Bounds()
	srcW := float32(src.Dx())
	srcH := float32(src.Dy())
	if srcW == 0 || srcH == 0 {
		return
	}
	// 等比缩放
	scaleX := float32(size.X) / srcW
	scaleY := float32(size.Y) / srcH
	scale := scaleX
	if scaleY < scale {
		scale = scaleY
	}
	dispW := int(srcW * scale)
	dispH := int(srcH * scale)
	offX := (size.X - dispW) / 2
	offY := (size.Y - dispH) / 2

	// 画截图
	imgOff := op.Offset(image.Pt(offX, offY)).Push(gtx.Ops)
	cstack := clip.Rect(image.Rect(0, 0, dispW, dispH)).Push(gtx.Ops)
	aff := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops)
	v.shotOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	aff.Pop()
	cstack.Pop()
	imgOff.Pop()

	// 暗化未选区域（仅在 hasRect 时）
	rectFill := color.NRGBA{R: 0, G: 0, B: 0, A: 0x80}
	imgRect := image.Rect(offX, offY, offX+dispW, offY+dispH)
	if v.dragging || v.hasRect {
		var x0, y0, x1, y1 float32
		if v.dragging {
			x0, y0 = v.dragSx, v.dragSy
			x1, y1 = v.curSx, v.curSy
		} else {
			x0, y0 = v.rectX0, v.rectY0
			x1, y1 = v.rectX1, v.rectY1
		}
		if x0 > x1 {
			x0, x1 = x1, x0
		}
		if y0 > y1 {
			y0, y1 = y1, y0
		}
		// 转成显示像素位置（dispOrigin offset 已包含）
		ix0, iy0 := int(x0), int(y0)
		ix1, iy1 := int(x1), int(y1)
		// 暗化四条带
		paint.FillShape(gtx.Ops, rectFill, clip.Rect(image.Rect(imgRect.Min.X, imgRect.Min.Y, imgRect.Max.X, iy0)).Op())
		paint.FillShape(gtx.Ops, rectFill, clip.Rect(image.Rect(imgRect.Min.X, iy1, imgRect.Max.X, imgRect.Max.Y)).Op())
		paint.FillShape(gtx.Ops, rectFill, clip.Rect(image.Rect(imgRect.Min.X, iy0, ix0, iy1)).Op())
		paint.FillShape(gtx.Ops, rectFill, clip.Rect(image.Rect(ix1, iy0, imgRect.Max.X, iy1)).Op())
		// 矩形边框
		edge := color.NRGBA{R: 0x4a, G: 0xff, B: 0x6e, A: 0xff}
		drawRectBorder(gtx.Ops, image.Rect(ix0, iy0, ix1, iy1), 2, edge)

		// 在矩形上方画一行尺寸提示
		w := ix1 - ix0
		h := iy1 - iy0
		// 反算屏幕坐标供提示
		sx, sy, sw, sh := v.displayToScreen(x0, y0, x1, y1, offX, offY, dispW, dispH)
		txt := fmt.Sprintf("框选: 显示 %dx%d  →  屏幕 (%d,%d) %dx%d", w, h, sx, sy, sw, sh)
		stack := op.Offset(image.Pt(ix0, iy0-18)).Push(gtx.Ops)
		gtx2 := gtx
		gtx2.Constraints = layout.Exact(image.Pt(400, 18))
		lbl := material.Caption(v.app.Theme.Material, txt)
		lbl.Color = color.NRGBA{R: 0x4a, G: 0xff, B: 0x6e, A: 0xff}
		lbl.Alignment = text.Start
		lbl.Layout(gtx2)
		stack.Pop()
	}

	// 注册整个图像区域为 pointer 事件目标（在最上层，确保接收到拖拽）
	full := image.Rect(imgRect.Min.X, imgRect.Min.Y, imgRect.Max.X, imgRect.Max.Y)
	cstack2 := clip.Rect(full).Push(gtx.Ops)
	event.Op(gtx.Ops, &v.tag)
	cstack2.Pop()

	// 保留 dispRect 信息
	v.lastDispOrigin = image.Pt(offX, offY)
	v.lastDispSize = image.Pt(dispW, dispH)
}

// 缓存最近一次显示矩形（事件处理用）—— 见 ROISelectView.lastDispOrigin/Size。

// handleEvents 处理图像区域上的拖拽。headerOffsetY 是图像区域在窗口里的 Y 偏移。
func (v *ROISelectView) handleEvents(gtx layout.Context, areaSize image.Point, headerOffsetY int) {
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target: &v.tag,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		// pe.Position 是相对于 event.Op 注册时的 clip.Rect 原点（即图像区域内）
		switch pe.Kind {
		case pointer.Press:
			if !pe.Buttons.Contain(pointer.ButtonPrimary) {
				continue
			}
			v.dragging = true
			v.dragSx = pe.Position.X
			v.dragSy = pe.Position.Y
			v.curSx = pe.Position.X
			v.curSy = pe.Position.Y
			v.hasRect = false
		case pointer.Drag:
			if v.dragging {
				v.curSx = pe.Position.X
				v.curSy = pe.Position.Y
			}
		case pointer.Release:
			if v.dragging {
				v.dragging = false
				x0, y0 := v.dragSx, v.dragSy
				x1, y1 := pe.Position.X, pe.Position.Y
				if x0 > x1 {
					x0, x1 = x1, x0
				}
				if y0 > y1 {
					y0, y1 = y1, y0
				}
				if x1-x0 < 4 || y1-y0 < 4 {
					v.hasRect = false
					v.msg = "矩形太小，请重新拖拽"
					continue
				}
				v.rectX0, v.rectY0 = x0, y0
				v.rectX1, v.rectY1 = x1, y1
				v.hasRect = true
				v.msg = "已框选；点 保存框选 应用"
			}
		case pointer.Cancel:
			v.dragging = false
		}
	}
	_ = areaSize
	_ = headerOffsetY
}

// 这两个字段供 handleEvents → drawImageArea 协作（保存上一帧的显示参数）

func (v *ROISelectView) commitROI() {
	if !v.hasRect {
		return
	}
	// 显示坐标 (rectX0..rectY1) 是相对于"图像区域"左上的。
	// drawImageArea 已经把 dispOrigin/Size 写到 v.lastDispOrigin/Size。
	x0, y0 := v.rectX0, v.rectY0
	x1, y1 := v.rectX1, v.rectY1
	sx, sy, sw, sh := v.displayToScreen(x0, y0, x1, y1,
		v.lastDispOrigin.X, v.lastDispOrigin.Y, v.lastDispSize.X, v.lastDispSize.Y)
	v.app.settings.Update(func(s *Settings) {
		s.MinimapROI = MinimapROI{X: sx, Y: sy, W: sw, H: sh}
	})
	if sv := v.app.settingsView; sv != nil {
		sv.roiX.SetText(itoa(sx))
		sv.roiY.SetText(itoa(sy))
		sv.roiW.SetText(itoa(sw))
		sv.roiH.SetText(itoa(sh))
		sv.navMsg = fmt.Sprintf("已框选：(%d,%d) %dx%d", sx, sy, sw, sh)
	}
	v.msg = fmt.Sprintf("已保存：屏幕 (%d,%d) %dx%d", sx, sy, sw, sh)
	v.app.mode = ModeSettings
}

// displayToScreen 把显示坐标系下的矩形 (x0,y0)-(x1,y1) 转成绝对屏幕坐标。
// dispOriginX/Y 是缩放截图在显示区域内的左上角偏移。
func (v *ROISelectView) displayToScreen(x0, y0, x1, y1 float32, dispOriginX, dispOriginY, dispW, dispH int) (sx, sy, sw, sh int) {
	if v.shot == nil || dispW == 0 || dispH == 0 {
		return 0, 0, 0, 0
	}
	srcW := float32(v.shot.Bounds().Dx())
	srcH := float32(v.shot.Bounds().Dy())
	scaleX := float32(dispW) / srcW
	scaleY := float32(dispH) / srcH
	// x0,y0 是绝对窗口坐标系（在 drawImageArea 注册的 clip.Rect 是从 imgRect.Min
	// 起算）—— 我们用的 event.Op 的 Position 等于 absolute - imgRect.Min。
	// 即 x0 已经在以图像区域左上为原点的坐标里。需要再扣掉 dispOriginX/Y 才是
	// 截图缩放后的偏移；除以 scale 得到原始截图像素；最后加 originX/Y 得到屏幕坐标。
	rx0 := (x0 - float32(dispOriginX)) / scaleX
	ry0 := (y0 - float32(dispOriginY)) / scaleY
	rx1 := (x1 - float32(dispOriginX)) / scaleX
	ry1 := (y1 - float32(dispOriginY)) / scaleY
	if rx0 < 0 {
		rx0 = 0
	}
	if ry0 < 0 {
		ry0 = 0
	}
	if rx1 > srcW {
		rx1 = srcW
	}
	if ry1 > srcH {
		ry1 = srcH
	}
	sx = v.originX + int(rx0)
	sy = v.originY + int(ry0)
	sw = int(rx1 - rx0)
	sh = int(ry1 - ry0)
	return
}
