package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// PathListView 侧边栏的"路径"管理面板。
//
// 功能：
//   - 列出 RouteStore 中所有路径，每行：可见性勾选 / 颜色块 / 名称（可改名）
//     / 删除按钮
//   - 改名通过点击名字打开内联编辑器；按 Enter / 失焦保存
type PathListView struct {
	app *App

	// 每条路径的控件按 ID 缓存
	rowsByID map[string]*pathRow

	listScroll widget.List
}

type pathRow struct {
	visChk   widget.Bool
	delBtn   widget.Clickable
	nameBtn  widget.Clickable
	navBtn   widget.Clickable // ▶ 开始导航 / ■ 停止导航
	colorBtn widget.Clickable // 点击颜色块打开 ColorPickerView
	editor   widget.Editor
	editing  bool
}

func NewPathListView(a *App) *PathListView {
	v := &PathListView{
		app:      a,
		rowsByID: map[string]*pathRow{},
	}
	v.listScroll.Axis = layout.Vertical
	return v
}

func (v *PathListView) Layout(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	paths := v.app.routes.All()

	// 处理草稿可见性 / 删除事件
	changed := false
	_, navRouteID, _, _, _ := v.app.nav.snapshot()
	for _, p := range paths {
		r := v.rowFor(p.ID)
		// 同步 widget 状态 ↔ Path 字段
		if r.visChk.Update(gtx) {
			p.Visible = r.visChk.Value
			_ = v.app.routes.Save(p)
		} else {
			r.visChk.Value = p.Visible
		}
		if r.navBtn.Clicked(gtx) {
			if navRouteID == p.ID {
				v.app.StopNavigation()
			} else {
				v.app.StartNavigation(p.ID)
			}
		}
		if r.delBtn.Clicked(gtx) {
			_ = v.app.routes.Delete(p.ID)
			delete(v.rowsByID, p.ID)
			changed = true
			break
		}
		if r.colorBtn.Clicked(gtx) && v.app.colorPicker != nil {
			v.app.colorPicker.Open(p)
		}
		if r.nameBtn.Clicked(gtx) {
			r.editing = !r.editing
			if r.editing {
				r.editor.SetText(p.Name)
				r.editor.SetCaret(len(p.Name), len(p.Name))
				gtx.Execute(key.FocusCmd{Tag: &r.editor})
			} else {
				p.Name = r.editor.Text()
				_ = v.app.routes.Save(p)
			}
		}
		// Submit on Enter
		for {
			ev, ok := r.editor.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				p.Name = r.editor.Text()
				_ = v.app.routes.Save(p)
				r.editing = false
			}
		}
	}
	if changed {
		paths = v.app.routes.All()
	}

	if len(paths) == 0 {
		lbl := material.Caption(t.Material, "暂无保存的路径。绘制后点工具栏 \"保存\"。")
		lbl.Color = t.TextDim
		return lbl.Layout(gtx)
	}

	return material.List(t.Material, &v.listScroll).Layout(gtx, len(paths), func(gtx layout.Context, i int) layout.Dimensions {
		p := paths[i]
		r := v.rowFor(p.ID)
		col := resolvePathColor(p)
		isNav := navRouteID == p.ID
		return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					cb := material.CheckBox(t.Material, &r.visChk, "")
					cb.Color = t.Text
					return cb.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return r.colorBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						sz := gtx.Dp(unit.Dp(14))
						rect := image.Rect(0, 0, sz, sz)
						paint.FillShape(gtx.Ops, col, clip.UniformRRect(rect, sz/2).Op(gtx.Ops))
						if r.colorBtn.Hovered() {
							drawRectBorder(gtx.Ops, rect, 1, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xc0})
						}
						return layout.Dimensions{Size: image.Pt(sz, sz)}
					})
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					if r.editing {
						ed := material.Editor(t.Material, &r.editor, "路径名")
						ed.Color = t.Text
						ed.Font.Typeface = "CJK"
						r.editor.SingleLine = true
						r.editor.Submit = true
						return ed.Layout(gtx)
					}
					return r.nameBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						lbl := material.Body2(t.Material, p.Name)
						lbl.Color = t.Text
						lbl.Font.Typeface = "CJK"
						lbl.MaxLines = 1
						return lbl.Layout(gtx)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := "导航"
					bg := t.BG
					col := t.Accent
					if isNav {
						label = "停止"
						bg = t.Accent
						col = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
					}
					b := material.Button(t.Material, &r.navBtn, label)
					b.TextSize = unit.Sp(10)
					b.Background = bg
					b.Color = col
					b.Font.Typeface = "CJK"
					b.Inset = layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}
					return b.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(2)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					b := material.Button(t.Material, &r.delBtn, "删除")
					b.TextSize = unit.Sp(11)
					b.Background = color.NRGBA{R: 0xc0, G: 0x30, B: 0x30, A: 0xff}
					b.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
					b.Font.Typeface = "CJK"
					b.Font.Weight = font.Bold
					b.Inset = layout.Inset{Top: unit.Dp(3), Bottom: unit.Dp(3), Left: unit.Dp(8), Right: unit.Dp(8)}
					return b.Layout(gtx)
				}),
			)
		})
	})
}

func (v *PathListView) rowFor(id string) *pathRow {
	if r, ok := v.rowsByID[id]; ok {
		return r
	}
	r := &pathRow{}
	r.editor.SingleLine = true
	r.editor.Submit = true
	v.rowsByID[id] = r
	return r
}

// CategoryGroupsView 按 wiki.CategoryItem.Type 把点位类别折叠分组的列表。
type CategoryGroupsView struct {
	app *App

	expanded   map[string]bool
	groupClick map[string]*widget.Clickable
	groupAll   map[string]*widget.Clickable

	scroll widget.List
}

func NewCategoryGroupsView(a *App) *CategoryGroupsView {
	v := &CategoryGroupsView{
		app:        a,
		expanded:   map[string]bool{},
		groupClick: map[string]*widget.Clickable{},
		groupAll:   map[string]*widget.Clickable{},
	}
	v.scroll.Axis = layout.Vertical
	return v
}

func (v *CategoryGroupsView) Layout(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme

	// 把类别按 Type 分桶，保持类别原顺序
	groups := []string{}
	groupItems := map[string][]int{} // type -> indices into cats
	cats := v.app.store.Categories()
	for i, c := range cats {
		typeName := c.Type
		if typeName == "" {
			typeName = "其它"
		}
		if _, ok := groupItems[typeName]; !ok {
			groups = append(groups, typeName)
		}
		groupItems[typeName] = append(groupItems[typeName], i)
	}

	// 展开 / 折叠点击
	for _, g := range groups {
		btn := v.groupClickFor(g)
		if btn.Clicked(gtx) {
			v.expanded[g] = !v.expanded[g]
		}
		// 全选 / 取消按钮
		all := v.groupAllFor(g)
		if all.Clicked(gtx) {
			// 看当前是否所有同组类别都选中：是 → 全部取消；否 → 全部选中
			anyOff := false
			for _, idx := range groupItems[g] {
				mt := cats[idx].MarkType
				if !v.app.mapView.IsTypeVisible(mt) {
					anyOff = true
					break
				}
			}
			turnOn := anyOff
			for _, idx := range groupItems[g] {
				mt := cats[idx].MarkType
				if mt == "" {
					continue
				}
				chk := v.app.typeChks[mt]
				if chk == nil {
					chk = &widget.Bool{}
					v.app.typeChks[mt] = chk
				}
				chk.Value = turnOn
				v.app.mapView.SetVisibleType(mt, turnOn)
			}
		}
	}

	// 把分组按 (header, [items]...) 平铺到一个虚拟列表，再交给 material.List 滚动渲染。
	type rowKind int
	const (
		rowHeader rowKind = iota
		rowItem
	)
	type vrow struct {
		Kind  rowKind
		Group string
		Idx   int
	}
	rows := []vrow{}
	for _, g := range groups {
		rows = append(rows, vrow{Kind: rowHeader, Group: g})
		if v.expanded[g] {
			for _, idx := range groupItems[g] {
				rows = append(rows, vrow{Kind: rowItem, Group: g, Idx: idx})
			}
		}
	}

	return material.List(t.Material, &v.scroll).Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		r := rows[i]
		switch r.Kind {
		case rowHeader:
			isExp := v.expanded[r.Group]
			arrow := "▶"
			if isExp {
				arrow = "▼"
			}
			n := len(groupItems[r.Group])
			return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return v.groupClickFor(r.Group).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Body1(t.Material, arrow+"  "+r.Group+"  ("+itoa(n)+")")
							lbl.Color = t.Text
							lbl.Font.Weight = font.SemiBold
							lbl.Font.Typeface = "CJK"
							return lbl.Layout(gtx)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						b := material.Button(t.Material, v.groupAllFor(r.Group), "全选")
						b.TextSize = unit.Sp(10)
						b.Background = t.BG
						b.Color = t.TextDim
						b.Inset = layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2), Left: unit.Dp(6), Right: unit.Dp(6)}
						return b.Layout(gtx)
					}),
				)
			})
		case rowItem:
			c := cats[r.Idx]
			chk := v.app.typeChks[c.MarkType]
			if chk == nil {
				chk = &widget.Bool{}
				v.app.typeChks[c.MarkType] = chk
			}
			lbl := c.MarkTypeName + " (" + c.Type + ")"
			pts := v.app.store.PointsOf(c.MarkType)
			if len(pts) > 0 {
				lbl += "  " + countTag(len(pts))
			}
			iconURL := c.Icon
			return layout.Inset{Left: unit.Dp(20), Top: unit.Dp(1), Bottom: unit.Dp(1)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					// 复选框（无文字）
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						cb := material.CheckBox(t.Material, chk, "")
						cb.Color = t.Text
						return cb.Layout(gtx)
					}),
					// 文字（占据剩余空间，单行省略）
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						lblW := material.Body2(t.Material, lbl)
						lblW.Color = t.Text
						lblW.Font.Typeface = "CJK"
						lblW.MaxLines = 1
						return lblW.Layout(gtx)
					}),
					// 类别图标（固定方形，最右）
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return v.layoutCategoryIcon(gtx, iconURL)
					}),
				)
			})
		}
		return layout.Dimensions{}
	})
}

// layoutCategoryIcon 在类别条目右侧画一个固定方形（18dp）的类别 PNG 图标。
// 未下载 / 缺失时画一个浅灰占位方块，避免行高跳动。
func (v *CategoryGroupsView) layoutCategoryIcon(gtx layout.Context, url string) layout.Dimensions {
	t := v.app.Theme
	size := gtx.Dp(unit.Dp(18))
	box := image.Pt(size, size)
	if url == "" || v.app.icons == nil {
		// 占位：浅色描边小方框
		paint.FillShape(gtx.Ops, t.Panel, clip.UniformRRect(image.Rectangle{Max: box}, gtx.Dp(unit.Dp(3))).Op(gtx.Ops))
		return layout.Dimensions{Size: box}
	}
	imgOp := v.app.icons.GetOp(url)
	if imgOp == nil {
		paint.FillShape(gtx.Ops, t.Panel, clip.UniformRRect(image.Rectangle{Max: box}, gtx.Dp(unit.Dp(3))).Op(gtx.Ops))
		return layout.Dimensions{Size: box}
	}
	src := imgOp.Size()
	if src.X == 0 || src.Y == 0 {
		return layout.Dimensions{Size: box}
	}
	// 等比缩放到 size，最长边贴边，内置剪裁框保证宽高严格 = size。
	maxDim := src.X
	if src.Y > maxDim {
		maxDim = src.Y
	}
	scale := float32(size) / float32(maxDim)
	dispW := int(float32(src.X)*scale + 0.5)
	dispH := int(float32(src.Y)*scale + 0.5)
	off := op.Offset(image.Point{X: (size - dispW) / 2, Y: (size - dispH) / 2}).Push(gtx.Ops)
	aff := op.Affine(f32.Affine2D{}.Scale(f32.Pt(0, 0), f32.Pt(scale, scale))).Push(gtx.Ops)
	cstack := clip.Rect{Max: src}.Push(gtx.Ops)
	imgOp.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cstack.Pop()
	aff.Pop()
	off.Pop()
	return layout.Dimensions{Size: box}
}

func (v *CategoryGroupsView) groupClickFor(g string) *widget.Clickable {
	if c, ok := v.groupClick[g]; ok {
		return c
	}
	c := &widget.Clickable{}
	v.groupClick[g] = c
	return c
}

func (v *CategoryGroupsView) groupAllFor(g string) *widget.Clickable {
	if c, ok := v.groupAll[g]; ok {
		return c
	}
	c := &widget.Clickable{}
	v.groupAll[g] = c
	return c
}

// 防止 image / text / color 被裁剪掉
var (
	_ = image.Rectangle{}
	_ = text.Start
	_ = color.NRGBA{}
)
