package ui

import (
	"fmt"
	"image"
	"image/color"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// SettingsView 设置页。访问 a.settings 并在用户操作后保存。
type SettingsView struct {
	app *App

	styleBubble widget.Clickable
	styleIcon   widget.Clickable
	styleDot    widget.Clickable

	selHalo widget.Clickable
	selDot  widget.Clickable

	debugChk widget.Bool

	iconSlider widget.Float

	// 导航相关
	roiX        widget.Editor
	roiY        widget.Editor
	roiW        widget.Editor
	roiH        widget.Editor
	roiApply    widget.Clickable
	roiPick    widget.Clickable
	navTrackChk widget.Bool
	navPredictChk widget.Bool
	autoCenter   widget.Bool // 已废弃，保留以避免删字段时其它代码引用；不再读取
	centerOff    widget.Clickable
	centerAlways widget.Clickable
	centerNavOnly widget.Clickable
	fbStay      widget.Clickable
	fbLast      widget.Clickable
	fbLost      widget.Clickable
	zoom4       widget.Clickable
	zoom6       widget.Clickable
	zoom7       widget.Clickable
	zoom8       widget.Clickable
	kEd         widget.Editor
	kApply      widget.Clickable
	kAutoCalib  widget.Clickable

	// 悬浮窗
	ovToggle      widget.Clickable
	ovAlpha       widget.Float
	ovClickThru   widget.Bool
	ovProgressBar widget.Bool

	scroll      widget.List
	navMsg      string

	refetchBtn widget.Clickable
	dlAllBtn   widget.Clickable // 全量下载所有 tilemap
	dlCancelBtn widget.Clickable // 取消全量下载
	backBtn    widget.Clickable

	confirmRefetch time.Time // 第一次点击的时间；在 3 秒内再次点击才真正触发
	confirmMsg     string
}

// NewSettingsView 构造。
func NewSettingsView(a *App) *SettingsView {
	v := &SettingsView{app: a}
	cur := a.settings.Get()
	v.debugChk.Value = cur.DebugLog
	v.iconSlider.Value = iconSizeToSlider(cur.IconSize)
	v.navTrackChk.Value = cur.NavTracking
	v.navPredictChk.Value = cur.NavPredict
	v.autoCenter.Value = cur.NavAutoCenter
	_ = v.autoCenter // 已不用
	v.scroll.Axis = layout.Vertical
	v.roiX.SingleLine = true
	v.roiY.SingleLine = true
	v.roiW.SingleLine = true
	v.roiH.SingleLine = true
	v.roiX.SetText(itoa(cur.MinimapROI.X))
	v.roiY.SetText(itoa(cur.MinimapROI.Y))
	v.roiW.SetText(itoa(cur.MinimapROI.W))
	v.roiH.SetText(itoa(cur.MinimapROI.H))
	v.kEd.SingleLine = true
	v.kEd.SetText(ftoa(cur.WorldUnitsPerMinimapPx, 3))
	a0 := cur.OverlayAlpha
	if a0 == 0 {
		a0 = 0xC0
	}
	v.ovAlpha.Value = float32(a0) / 255.0
	v.ovClickThru.Value = cur.OverlayClickThrough
	v.ovProgressBar.Value = cur.OverlayProgressBarEnabled()
	return v
}

// iconSizeToSlider / sliderToIconSize 在 [12..64] 与 [0..1] 之间映射。
func iconSizeToSlider(sz int) float32 {
	if sz < 12 {
		sz = 12
	}
	if sz > 64 {
		sz = 64
	}
	return float32(sz-12) / 52.0
}

func sliderToIconSize(v float32) int {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	return 12 + int(v*52+0.5)
}

// Layout 渲染设置页。返回前会处理控件事件并把变更写回 settings store。
func (v *SettingsView) Layout(gtx layout.Context) layout.Dimensions {
	v.handleEvents(gtx)

	paint.FillShape(gtx.Ops, v.app.Theme.BG, clip.Rect{Max: gtx.Constraints.Max}.Op())

	rows := []layout.Widget{
		v.titleRow,
		spacer(20),
		v.section("外观"),
		spacer(8),
		v.markerStyleRow,
		spacer(12),
		v.iconSizeRow,
		spacer(12),
		v.selectionStyleRow,
		spacer(20),
		v.section("导航 / 追踪"),
		spacer(8),
		v.navTrackRow,
		spacer(6),
		v.navPredictRow,
		spacer(6),
		v.autoCenterRow,
		spacer(8),
		v.zoomRow,
		spacer(8),
		v.calibKRow,
		spacer(8),
		v.fallbackRow,
		spacer(10),
		v.roiRow,
		spacer(20),
		v.section("调试"),
		spacer(8),
		v.debugRow,
		spacer(20),
		v.section("悬浮窗"),
		spacer(8),
		v.overlayToggleRow,
		spacer(8),
		v.overlayAlphaRow,
		spacer(8),
		v.overlayClickThruRow,
		spacer(8),
		v.overlayProgressBarRow,
		spacer(20),
		v.section("数据"),
		spacer(8),
		v.dataSourceRow,
		spacer(8),
		v.dataPathRow,
		spacer(8),
		v.refetchRow,
		spacer(8),
		v.dlAllTilesRow,
		spacer(24),
	}

	return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		list := material.List(v.app.Theme.Material, &v.scroll)
		return list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
			return rows[i](gtx)
		})
	})
}

// spacer 一行垂直间隔。
func spacer(h int) layout.Widget {
	return layout.Spacer{Height: unit.Dp(float32(h))}.Layout
}

func (v *SettingsView) handleEvents(gtx layout.Context) {
	cur := v.app.settings.Get()

	if v.styleBubble.Clicked(gtx) && cur.MarkerStyle != MarkerBubble {
		v.app.settings.Update(func(s *Settings) { s.MarkerStyle = MarkerBubble })
	}
	if v.styleIcon.Clicked(gtx) && cur.MarkerStyle != MarkerIcon {
		v.app.settings.Update(func(s *Settings) { s.MarkerStyle = MarkerIcon })
		v.app.prefetchIcons()
	}
	if v.styleDot.Clicked(gtx) && cur.MarkerStyle != MarkerDot {
		v.app.settings.Update(func(s *Settings) { s.MarkerStyle = MarkerDot })
	}
	if v.selHalo.Clicked(gtx) && cur.SelectionStyle != SelectionHalo {
		v.app.settings.Update(func(s *Settings) { s.SelectionStyle = SelectionHalo })
	}
	if v.selDot.Clicked(gtx) && cur.SelectionStyle != SelectionDot {
		v.app.settings.Update(func(s *Settings) { s.SelectionStyle = SelectionDot })
	}
	if v.debugChk.Update(gtx) {
		v.app.settings.Update(func(s *Settings) { s.DebugLog = v.debugChk.Value })
		if v.app.mapView != nil {
			v.app.mapView.SetDebug(v.debugChk.Value)
		}
	}
	if v.iconSlider.Update(gtx) {
		nv := sliderToIconSize(v.iconSlider.Value)
		if nv != cur.IconSize {
			v.app.settings.Update(func(s *Settings) { s.IconSize = nv })
		}
	}
	if v.navPredictChk.Update(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavPredict = v.navPredictChk.Value })
	}
	if v.navTrackChk.Update(gtx) {
		// 切换追踪开关：写入 settings + 启动/停止追踪
		v.app.settings.Update(func(s *Settings) { s.NavTracking = v.navTrackChk.Value })
		if v.navTrackChk.Value {
			v.app.StartTracking()
		} else {
			v.app.StopNavigation()
		}
	}
	if v.zoom4.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavSearchZoom = 4 })
	}
	if v.zoom6.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavSearchZoom = 6 })
	}
	if v.zoom7.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavSearchZoom = 7 })
	}
	if v.zoom8.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavSearchZoom = 8 })
	}
	if v.kApply.Clicked(gtx) {
		k := atof(v.kEd.Text())
		if k <= 0 {
			v.navMsg = "K 必须 > 0"
		} else {
			v.app.settings.Update(func(s *Settings) { s.WorldUnitsPerMinimapPx = k })
			v.navMsg = "已保存校准 K = " + ftoa(k, 3)
		}
	}
	if v.kAutoCalib.Clicked(gtx) {
		v.app.AutoCalibrateKAsync()
		v.navMsg = "正在自动校准 K…"
	}
	if v.autoCenter.Update(gtx) {
		// 兼容旧勾选框，任何变动同步到新模式
		if v.autoCenter.Value {
			v.app.settings.Update(func(s *Settings) { s.NavAutoCenter = true; s.NavCenterMode = CenterAlways })
		} else {
			v.app.settings.Update(func(s *Settings) { s.NavAutoCenter = false; s.NavCenterMode = CenterOff })
		}
	}
	if v.centerOff.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavCenterMode = CenterOff; s.NavAutoCenter = false })
	}
	if v.centerAlways.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavCenterMode = CenterAlways; s.NavAutoCenter = true })
	}
	if v.centerNavOnly.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavCenterMode = CenterNavOnly; s.NavAutoCenter = true })
	}
	if v.fbStay.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavFallback = NavFallbackStay })
	}
	if v.fbLast.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavFallback = NavFallbackLast })
	}
	if v.fbLost.Clicked(gtx) {
		v.app.settings.Update(func(s *Settings) { s.NavFallback = NavFallbackLost })
	}
	if v.roiApply.Clicked(gtx) {
		x := atoi(v.roiX.Text())
		y := atoi(v.roiY.Text())
		w := atoi(v.roiW.Text())
		h := atoi(v.roiH.Text())
		v.app.settings.Update(func(s *Settings) {
			s.MinimapROI = MinimapROI{X: x, Y: y, W: w, H: h}
		})
		v.navMsg = "已保存：" + itoa(x) + "," + itoa(y) + " " + itoa(w) + "×" + itoa(h)
	}
	if v.roiPick.Clicked(gtx) {
		if v.app.roiView != nil {
			if err := v.app.roiView.Capture(); err != nil {
				v.navMsg = err.Error()
			} else {
				v.app.mode = ModeROISelect
			}
		}
	}
	if v.backBtn.Clicked(gtx) {
		v.app.mode = ModeMain
	}
	if v.ovToggle.Clicked(gtx) {
		v.app.ToggleOverlay()
	}
	if v.ovAlpha.Update(gtx) {
		a := byte(v.ovAlpha.Value*255 + 0.5)
		if a < 0x20 {
			a = 0x20
		}
		v.app.SetOverlayAlpha(a)
	}
	if v.ovClickThru.Update(gtx) {
		v.app.SetOverlayClickThrough(v.ovClickThru.Value)
	}
	if v.ovProgressBar.Update(gtx) {
		val := v.ovProgressBar.Value
		v.app.settings.Update(func(s *Settings) { s.OverlayProgressBar = &val })
	}
	if v.refetchBtn.Clicked(gtx) {
		now := time.Now()
		if now.Sub(v.confirmRefetch) <= 3*time.Second {
			v.confirmRefetch = time.Time{}
			v.confirmMsg = ""
			v.app.triggerRefetch()
		} else {
			v.confirmRefetch = now
			v.confirmMsg = "再次点击以确认（3 秒内）"
		}
	}
	if !v.confirmRefetch.IsZero() && time.Since(v.confirmRefetch) > 3*time.Second {
		v.confirmRefetch = time.Time{}
		v.confirmMsg = ""
	}
	if v.dlAllBtn.Clicked(gtx) {
		running, _, _, _, _, _, _, _ := v.app.TilesDLSnapshot()
		if !running {
			v.app.DownloadAllTilesAsync()
		}
	}
	if v.dlCancelBtn.Clicked(gtx) {
		v.app.CancelDownloadAllTiles()
	}
}

func (v *SettingsView) titleRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.H4(t.Material, "设置")
			lbl.Color = t.Text
			lbl.Font.Weight = font.Bold
			return lbl.Layout(gtx)
		}),
		layout.Flexed(1, layout.Spacer{}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.backBtn, "返回")
			b.Background = t.Panel
			b.Color = t.Text
			return b.Layout(gtx)
		}),
	)
}

func (v *SettingsView) section(title string) layout.Widget {
	t := v.app.Theme
	return func(gtx layout.Context) layout.Dimensions {
		lbl := material.H6(t.Material, title)
		lbl.Color = t.Accent
		lbl.Font.Weight = font.Bold
		return lbl.Layout(gtx)
	}
}

func (v *SettingsView) markerStyleRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().MarkerStyle
	mk := func(style MarkerStyle, label string, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, btn, label)
			if cur == style {
				b.Background = t.Accent
				b.Color = color.NRGBA{A: 0xff}
			} else {
				b.Background = t.Panel
				b.Color = t.Text
			}
			return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, b.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("标记样式")),
		mk(MarkerBubble, "气泡", &v.styleBubble),
		mk(MarkerIcon, "图标（默认）", &v.styleIcon),
		mk(MarkerDot, "圆点", &v.styleDot),
	)
}

func (v *SettingsView) iconSizeRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().IconSize
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("图标大小")),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(280))
			sl := material.Slider(t.Material, &v.iconSlider)
			return sl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, itoa(cur)+" px")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) debugRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cb := material.CheckBox(t.Material, &v.debugChk, "打印渲染调试日志（写到 stderr）")
	cb.Color = t.Text
	return cb.Layout(gtx)
}

func (v *SettingsView) selectionStyleRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().SelectionStyle
	mk := func(style SelectionStyle, label string, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, btn, label)
			if cur == style {
				b.Background = t.Accent
				b.Color = color.NRGBA{A: 0xff}
			} else {
				b.Background = t.Panel
				b.Color = t.Text
			}
			return layout.Inset{Right: unit.Dp(8)}.Layout(gtx, b.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("选中样式")),
		mk(SelectionHalo, "高亮描边（默认）", &v.selHalo),
		mk(SelectionDot, "白色圆环", &v.selDot),
	)
}

// dataSourceRow 显示 wiki 来源 + 开源协议（CC BY-NC-SA 4.0）。
func (v *SettingsView) dataSourceRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, "数据来源：洛克王国世界wiki")
			lbl.Color = t.Text
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(t.Material, "https://wiki.biligame.com/rocom")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(t.Material, "wiki 内容遵循 CC BY-NC-SA 4.0；本程序仅作个人辅助使用，不存储 / 转载原始 wiki 文本。")
			lbl.Color = t.TextDim
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) dataPathRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, "缓存目录：")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, v.app.cacheRoot)
			lbl.Color = t.Text
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) refetchRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.refetchBtn, "从 Wiki 重新抓取数据")
			b.Background = t.Warn
			b.Color = color.NRGBA{A: 0xff}
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			msg := v.confirmMsg
			if msg == "" {
				msg = "将清空缓存并联网重新下载所有元数据 / 点位 / 基础瓦片。"
			}
			lbl := material.Caption(t.Material, msg)
			if v.confirmMsg != "" {
				lbl.Color = t.Warn
			} else {
				lbl.Color = t.TextDim
			}
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) fieldLabel(s string) layout.Widget {
	t := v.app.Theme
	return func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Dp(unit.Dp(120))
		lbl := material.Body1(t.Material, s)
		lbl.Color = t.Text
		return layout.Inset{Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return lbl.Layout(gtx)
		})
	}
}

// dlAllTilesRow 全量下载按钮 + 进度条。running 时显示 done/total + 已写字节。
func (v *SettingsView) dlAllTilesRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	running, done, total, skipped, failed, bytes, finished, errMsg := v.app.TilesDLSnapshot()
	var statusTxt string
	switch {
	case errMsg != "":
		statusTxt = "错误：" + errMsg
	case running:
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total) * 100
		}
		statusTxt = fmt.Sprintf("%d / %d (%.1f%%)  已失败 %d  已写入 %.1f MB",
			done, total, pct, failed, float64(bytes)/(1024*1024))
	case finished:
		statusTxt = fmt.Sprintf("完成：成功 %d / %d，失败 %d，跳过 %d，共 %.1f MB",
			done-failed-skipped, total, failed, skipped, float64(bytes)/(1024*1024))
	default:
		statusTxt = "尚未开始。按父层 .png 覆盖自适应枚举 z=5..8 有效瓦片，约几万张、1~3 GB；可中途取消。"
	}
	btnLabel := "下载所有瓦片 (z=5..8)"
	btnBg := t.Accent
	if running {
		btnLabel = "下载中…"
		btnBg = t.Panel
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.dlAllBtn, btnLabel)
			b.Background = btnBg
			b.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			b.TextSize = unit.Sp(12)
			b.Font.Typeface = "CJK"
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !running {
				return layout.Dimensions{}
			}
			b := material.Button(t.Material, &v.dlCancelBtn, "取消")
			b.Background = t.Err
			b.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			b.TextSize = unit.Sp(12)
			b.Font.Typeface = "CJK"
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(t.Material, statusTxt)
			lbl.Color = t.TextDim
			lbl.Font.Typeface = "CJK"
			lbl.MaxLines = 2
			return lbl.Layout(gtx)
		}),
	)
}

// 防止某些 import 在裁掉特定分支时产生未使用警告
var _ = image.Point{}

func (v *SettingsView) autoCenterRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().NavCenterMode
	mk := func(mode CenterMode, label string, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, btn, label)
			if cur == mode {
				b.Background = t.Accent
				b.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			} else {
				b.Background = t.Panel
				b.Color = t.Text
			}
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			b.Font.Typeface = "CJK"
			return layout.Inset{Right: unit.Dp(6)}.Layout(gtx, b.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("自动居中")),
		mk(CenterOff, "关闭", &v.centerOff),
		mk(CenterAlways, "总是居中（默认）", &v.centerAlways),
		mk(CenterNavOnly, "仅导航时", &v.centerNavOnly),
	)
}

func (v *SettingsView) fallbackRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().NavFallback
	mk := func(mode NavFallback, label string, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, btn, label)
			if cur == mode {
				b.Background = t.Accent
				b.Color = color.NRGBA{A: 0xff}
			} else {
				b.Background = t.Panel
				b.Color = t.Text
			}
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			return layout.Inset{Right: unit.Dp(6)}.Layout(gtx, b.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("匹配失败")),
		mk(NavFallbackStay, "原地停留（默认）", &v.fbStay),
		mk(NavFallbackLast, "保持上次位置", &v.fbLast),
		mk(NavFallbackLost, "标记已丢失", &v.fbLost),
	)
}

func (v *SettingsView) roiRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	mkField := func(label string, ed *widget.Editor) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Body2(t.Material, label)
					lbl.Color = t.TextDim
					return lbl.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(72))
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(72))
					ee := material.Editor(t.Material, ed, "0")
					ee.Color = t.Text
					return ee.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
			)
		})
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, "小地图屏幕区域 (绝对屏幕像素)")
			lbl.Color = t.TextDim
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					b := material.Button(t.Material, &v.roiPick, "在屏幕上框选…")
					b.Background = t.Accent
					b.TextSize = unit.Sp(12)
					b.Inset = layout.UniformInset(unit.Dp(6))
					b.Font.Typeface = "CJK"
					return b.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Caption(t.Material, "推荐：拖拽框选小地图区域，自动写入下方坐标。")
					lbl.Color = t.TextDim
					lbl.Font.Typeface = "CJK"
					return lbl.Layout(gtx)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				mkField("X ", &v.roiX),
				mkField("Y ", &v.roiY),
				mkField("W ", &v.roiW),
				mkField("H ", &v.roiH),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					b := material.Button(t.Material, &v.roiApply, "应用")
					b.Background = t.Accent
					b.TextSize = unit.Sp(12)
					b.Inset = layout.UniformInset(unit.Dp(6))
					return b.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if v.navMsg == "" {
						return layout.Dimensions{}
					}
					lbl := material.Caption(t.Material, v.navMsg)
					lbl.Color = t.TextDim
					return lbl.Layout(gtx)
				}),
			)
		}),
	)
}

// atoi 简单整数解析；非法返回 0。
func atoi(s string) int {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n
}

// atof 解析浮点（兼容简单 "1.5" / "0.5" / "-2.0"），非法返回 0。
func atof(s string) float64 {
	if s == "" {
		return 0
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	} else if s[0] == '+' {
		i = 1
	}
	intPart := 0.0
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			i++
			break
		}
		if c < '0' || c > '9' {
			return 0
		}
		intPart = intPart*10 + float64(c-'0')
	}
	frac := 0.0
	div := 1.0
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0
		}
		frac = frac*10 + float64(c-'0')
		div *= 10
	}
	v := intPart + frac/div
	if neg {
		v = -v
	}
	return v
}

func (v *SettingsView) navTrackRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cb := material.CheckBox(v.app.Theme.Material, &v.navTrackChk, "实时追踪玩家位置（独立于路径导航；先框选小地图 ROI）")
	cb.Color = t.Text
	cb.Font.Typeface = "CJK"
	return cb.Layout(gtx)
}

func (v *SettingsView) navPredictRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cb := material.CheckBox(v.app.Theme.Material, &v.navPredictChk, "启用位置预测（按最近速度外推，两次匹配之间画面更流畅）")
	cb.Color = t.Text
	cb.Font.Typeface = "CJK"
	return cb.Layout(gtx)
}

func (v *SettingsView) zoomRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cur := v.app.settings.Get().NavSearchZoom
	mk := func(z int, label string, btn *widget.Clickable) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, btn, label)
			if cur == z {
				b.Background = t.Accent
				b.Color = color.NRGBA{A: 0xff}
			} else {
				b.Background = t.Panel
				b.Color = t.Text
			}
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			return layout.Inset{Right: unit.Dp(6)}.Layout(gtx, b.Layout)
		})
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("匹配 zoom")),
		mk(4, "z=4", &v.zoom4),
		mk(6, "z=6", &v.zoom6),
		mk(7, "z=7", &v.zoom7),
		mk(8, "z=8 (最大放大)", &v.zoom8),
	)
}

func (v *SettingsView) calibKRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("K 校准")),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(96))
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(96))
			ee := material.Editor(t.Material, &v.kEd, "0.5")
			ee.Color = t.Text
			return ee.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.kApply, "应用")
			b.Background = t.BG
			b.Color = t.Text
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.kAutoCalib, "自动校准")
			b.Background = t.Accent
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			b.Font.Typeface = "CJK"
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(t.Material, "推荐：在地图上右键玩家所在位置 → \"校准：玩家在这里（精确）\"")
			lbl.Color = t.TextDim
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) overlayToggleRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	open := v.app.overlay != nil && v.app.overlay.IsOpen()
	label := "打开悬浮窗"
	bg := t.Accent
	if open {
		label = "关闭悬浮窗"
		bg = t.Err
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("悬浮窗")),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			b := material.Button(t.Material, &v.ovToggle, label)
			b.Background = bg
			b.Color = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
			b.TextSize = unit.Sp(12)
			b.Inset = layout.UniformInset(unit.Dp(6))
			b.Font.Typeface = "CJK"
			return b.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(10)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Caption(t.Material, "悬浮窗显示朝向 + 导航状态。穿透模式下需关闭穿透才能拖动")
			lbl.Color = t.TextDim
			lbl.Font.Typeface = "CJK"
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) overlayAlphaRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	alpha := byte(v.ovAlpha.Value*255 + 0.5)
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(v.fieldLabel("透明度")),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(280))
			sl := material.Slider(t.Material, &v.ovAlpha)
			return sl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(t.Material, itoa(int(alpha))+"/255")
			lbl.Color = t.TextDim
			return lbl.Layout(gtx)
		}),
	)
}

func (v *SettingsView) overlayClickThruRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cb := material.CheckBox(t.Material, &v.ovClickThru, "鼠标穿透（悬浮窗不再接收鼠标事件，可点击下方游戏窗口）")
	cb.Color = t.Text
	cb.Font.Typeface = "CJK"
	return cb.Layout(gtx)
}

func (v *SettingsView) overlayProgressBarRow(gtx layout.Context) layout.Dimensions {
	t := v.app.Theme
	cb := material.CheckBox(t.Material, &v.ovProgressBar, "导航时在悬浮窗顶部显示进度条（默认开）")
	cb.Color = t.Text
	cb.Font.Typeface = "CJK"
	return cb.Layout(gtx)
}
