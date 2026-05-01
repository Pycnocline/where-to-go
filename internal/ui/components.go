package ui

// components.go 集中放置自绘的精致 UI 控件 —— 主要是 iconButton 配套的
// labelLayout，以及之后会用到的 toolbarSection 分隔。

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget/material"
)

// materialLabel 用主题的 material.Theme 渲染一个文本，应用 labelOp 的字段。
func materialLabel(gtx layout.Context, l labelOp) layout.Dimensions {
	lbl := material.Label(l.theme.Material, l.size, l.text)
	lbl.Color = l.color
	lbl.MaxLines = 1
	if l.typeface != "" {
		lbl.Font.Typeface = font.Typeface(l.typeface)
	}
	if l.bold {
		lbl.Font.Weight = font.Bold
	}
	return lbl.Layout(gtx)
}
