package ui

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// ContextMenuItem 一项菜单条目。Sep=true 代表分隔线（Label / Action 忽略）。
type ContextMenuItem struct {
	Label   string
	Action  func()
	Sep     bool
	Disable bool
}

// ContextMenuState 上下文菜单当前状态。X/Y 是相对应用根容器的屏幕坐标。
type ContextMenuState struct {
	Open   bool
	X      float32
	Y      float32
	Items  []ContextMenuItem
	clicks []widget.Clickable
	bgTag  int
	keyTag int
	// menuRect 记录上一帧菜单本体的实际矩形（在容器坐标系里），
	// 用于判断 press 是否落在菜单外（→ 关闭）。
	menuRect image.Rectangle
}

// Show 重置状态并打开菜单。x,y 屏幕坐标（应用容器内）。
func (s *ContextMenuState) Show(x, y float32, items []ContextMenuItem) {
	s.Open = true
	s.X = x
	s.Y = y
	s.Items = items
	s.menuRect = image.Rectangle{}
	if cap(s.clicks) < len(items) {
		s.clicks = make([]widget.Clickable, len(items))
	} else {
		s.clicks = s.clicks[:len(items)]
		for i := range s.clicks {
			s.clicks[i] = widget.Clickable{}
		}
	}
}

// Close 关闭菜单。
func (s *ContextMenuState) Close() { s.Open = false }

// Layout 渲染上下文菜单（如果打开）。
func (s *ContextMenuState) Layout(gtx layout.Context, t *Theme) layout.Dimensions {
	if !s.Open {
		return layout.Dimensions{}
	}

	// 键盘焦点 + Esc
	gtx.Execute(key.FocusCmd{Tag: &s.keyTag})
	for {
		ev, ok := gtx.Source.Event(key.Filter{Focus: &s.keyTag, Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			s.Close()
			return layout.Dimensions{Size: gtx.Constraints.Max}
		}
	}

	// 读上一帧之前注册的全屏 bg pointer.Filter 的 press 事件：
	// 落点在 menuRect 之外 → 关闭。落点在内 → 让菜单项 widget.Clickable 自己处理。
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{
			Target: &s.bgTag,
			Kinds:  pointer.Press,
		})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		if pe.Kind != pointer.Press {
			continue
		}
		if !s.menuRect.Empty() {
			x, y := int(pe.Position.X), int(pe.Position.Y)
			inside := x >= s.menuRect.Min.X && x < s.menuRect.Max.X &&
				y >= s.menuRect.Min.Y && y < s.menuRect.Max.Y
			if !inside {
				s.Close()
				return layout.Dimensions{Size: gtx.Constraints.Max}
			}
		}
	}

	// 菜单项点击
	for i := range s.clicks {
		if s.clicks[i].Clicked(gtx) {
			it := s.Items[i]
			s.Close()
			if it.Action != nil && !it.Disable && !it.Sep {
				it.Action()
			}
		}
	}

	// 全屏 pointer 区域，注册到 bgTag。注意：要先注册再画菜单项，
	// 这样菜单项的 widget.Clickable 在它之上（z 更高）能正常 hover / 接收 release。
	full := image.Rectangle{Max: gtx.Constraints.Max}
	bgStack := clip.Rect(full).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.bgTag)
	bgStack.Pop()

	// 菜单本体：估算宽 / 高，避免溢出右下边界
	const (
		w  = 180
		hi = 28
	)
	height := 0
	for _, it := range s.Items {
		if it.Sep {
			height += 8
		} else {
			height += hi
		}
	}
	height += 8

	x := int(s.X)
	y := int(s.Y)
	if x+w > gtx.Constraints.Max.X {
		x = gtx.Constraints.Max.X - w
	}
	if y+height > gtx.Constraints.Max.Y {
		y = gtx.Constraints.Max.Y - height
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	// 记录本帧的菜单矩形供下一帧 press 判定使用。
	s.menuRect = image.Rect(x, y, x+w, y+height)

	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	defer off.Pop()

	// 阴影 + 面板
	rect := image.Rect(0, 0, w, height)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 0x80},
		clip.UniformRRect(image.Rect(2, 2, w+2, height+2), 6).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, t.Panel, clip.UniformRRect(rect, 6).Op(gtx.Ops))

	cy := 4
	for i, it := range s.Items {
		if it.Sep {
			line := image.Rect(8, cy+3, w-8, cy+5)
			paint.FillShape(gtx.Ops, t.TextDim, clip.Rect(line).Op())
			cy += 8
			continue
		}
		btnOff := op.Offset(image.Pt(0, cy)).Push(gtx.Ops)
		gtx2 := gtx
		gtx2.Constraints = layout.Exact(image.Pt(w, hi))
		ck := &s.clicks[i]
		ck.Layout(gtx2, func(gtx layout.Context) layout.Dimensions {
			if ck.Hovered() && !it.Disable {
				paint.FillShape(gtx.Ops, t.BG, clip.Rect(image.Rect(0, 0, w, hi)).Op())
			}
			return layout.Inset{Left: unit.Dp(12), Right: unit.Dp(12), Top: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(t.Material, unit.Sp(12), it.Label)
				if it.Disable {
					lbl.Color = t.TextDim
				} else {
					lbl.Color = t.Text
				}
				lbl.Font.Typeface = "CJK"
				lbl.Alignment = text.Start
				lbl.MaxLines = 1
				return lbl.Layout(gtx)
			})
		})
		btnOff.Pop()
		cy += hi
	}

	return layout.Dimensions{Size: gtx.Constraints.Max}
}
