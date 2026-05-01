package ui

// 图标系统：把 Material Design Icons 的 IconVG 字节缓存成 *widget.Icon，方便
// 在按钮 / 列表项里快速复用。所有图标都用统一的渲染辅助 paintIcon 在指定
// 尺寸下绘制，保证在不同 DPI 下视觉一致。

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	mdicons "golang.org/x/exp/shiny/materialdesign/icons"
)

// IconKey 命名图标。新增图标时在这里加常量并在 iconRegistry 注册。
type IconKey string

const (
	IconSettings    IconKey = "settings"
	IconLayers      IconKey = "layers"
	IconBrush       IconKey = "brush"
	IconChain       IconKey = "chain"
	IconNavigate    IconKey = "navigate"
	IconCompass     IconKey = "compass"
	IconDirections  IconKey = "directions"
	IconPin         IconKey = "pin"
	IconImport      IconKey = "import"
	IconExport      IconKey = "export"
	IconOverlay     IconKey = "overlay"
	IconUndo        IconKey = "undo"
	IconSave        IconKey = "save"
	IconClear       IconKey = "clear"
	IconSelect      IconKey = "select"
	IconMenu        IconKey = "menu"
	IconClose       IconKey = "close"
	IconChevronL    IconKey = "chevron_left"
	IconChevronR    IconKey = "chevron_right"
	IconCheck       IconKey = "check"
	IconAdd         IconKey = "add"
	IconRefresh     IconKey = "refresh"
	IconWaypoints   IconKey = "waypoints"
	IconColor       IconKey = "color"
	IconFolder      IconKey = "folder"
	IconVisibility  IconKey = "visibility"
	IconArrowDown   IconKey = "arrow_drop_down"
	IconArrowRight  IconKey = "arrow_right"
)

var iconRegistry = map[IconKey][]byte{
	IconSettings:   mdicons.ActionSettings,
	IconLayers:     mdicons.MapsLayers,
	IconBrush:      mdicons.ImageBrush,
	IconChain:      mdicons.EditorInsertComment, // 折线连接：用一个折线状的图标
	IconNavigate:   mdicons.MapsNavigation,
	IconCompass:    mdicons.MapsMyLocation,
	IconDirections: mdicons.MapsDirections,
	IconPin:        mdicons.MapsPinDrop,
	IconImport:     mdicons.FileFileDownload,
	IconExport:     mdicons.FileFileUpload,
	IconOverlay:    mdicons.ActionVisibility,
	IconUndo:       mdicons.ContentUndo,
	IconSave:       mdicons.ContentSave,
	IconClear:      mdicons.ContentClear,
	IconSelect:     mdicons.EditorShortText,
	IconMenu:       mdicons.NavigationMenu,
	IconClose:      mdicons.NavigationClose,
	IconChevronL:   mdicons.NavigationArrowBack,
	IconChevronR:   mdicons.NavigationArrowForward,
	IconCheck:      mdicons.NavigationCheck,
	IconAdd:        mdicons.ContentAdd,
	IconRefresh:    mdicons.NavigationRefresh,
	IconWaypoints:  mdicons.MapsPinDrop,
	IconColor:      mdicons.ImageBrush,
	IconFolder:     mdicons.FileFolderOpen,
	IconVisibility: mdicons.ActionVisibility,
	IconArrowDown:  mdicons.NavigationArrowDropDown,
	IconArrowRight: mdicons.NavigationArrowForward,
}

// iconCache 把 IconKey 解码后的 *widget.Icon 缓存起来。Gio 的 widget.NewIcon
// 会做一次 IconVG 解析，每次新建有少量开销。
var iconCache = map[IconKey]*widget.Icon{}

// Icon 获取（并缓存）指定 key 的 widget.Icon。未注册返回 nil。
func Icon(key IconKey) *widget.Icon {
	if ic, ok := iconCache[key]; ok {
		return ic
	}
	data, ok := iconRegistry[key]
	if !ok {
		return nil
	}
	ic, err := widget.NewIcon(data)
	if err != nil {
		return nil
	}
	iconCache[key] = ic
	return ic
}

// paintIcon 在 size dp 的尺寸内绘制图标 ic。color 控制 tint。
// 用 layout.Constraints 强制方框，避免不同图标 SVG 视口大小不一造成视觉跳动。
func paintIcon(gtx layout.Context, ic *widget.Icon, size unit.Dp, col color.NRGBA) layout.Dimensions {
	if ic == nil {
		// 占位：画一个透明方块，保持尺寸
		sz := gtx.Dp(size)
		return layout.Dimensions{Size: image.Pt(sz, sz)}
	}
	sz := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(sz, sz))
	return ic.Layout(gtx, col)
}

// iconButtonStyle 一个紧凑的"图标 + 文字"按钮，只用 widget.Clickable 不依赖
// material.Button —— 这样我们能完全控制 padding / hover 反馈 / 圆角。
//
// 字段语义：
//   - Label  按钮文字；为空时只显示图标
//   - Icon   图标 key；为空时只显示文字
//   - Active 高亮态（true → 背景用 bg 高亮色）
//   - bg / fg 普通态颜色
//   - bgActive / fgActive 高亮态颜色
//   - PadH / PadV 内边距（dp）
type iconButtonStyle struct {
	Label    string
	Icon     IconKey
	IconSize unit.Dp
	TextSize unit.Sp
	Bold     bool
	Active   bool
	Disabled bool

	BG       color.NRGBA
	FG       color.NRGBA
	BGActive color.NRGBA
	FGActive color.NRGBA

	PadH unit.Dp
	PadV unit.Dp
}

// defaultIconButton 用主题色填充常用字段。
func defaultIconButton(t *Theme, label string, icon IconKey) iconButtonStyle {
	return iconButtonStyle{
		Label:    label,
		Icon:     icon,
		IconSize: 16,
		TextSize: 12,
		BG:       t.BG,
		FG:       t.Text,
		BGActive: t.Accent,
		FGActive: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		PadH:     10,
		PadV:     6,
	}
}

// Layout 渲染按钮（不含 Clickable —— 调用方传入）。
func (s iconButtonStyle) Layout(gtx layout.Context, t *Theme, btn *widget.Clickable) layout.Dimensions {
	bg := s.BG
	fg := s.FG
	if s.Active {
		bg = s.BGActive
		fg = s.FGActive
	}
	if s.Disabled {
		fg = t.TextDim
	}
	return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		// 测量内容尺寸
		return layout.Background{}.Layout(gtx,
			func(gtx layout.Context) layout.Dimensions {
				// 背景：圆角矩形
				r := gtx.Dp(unit.Dp(6))
				rect := image.Rectangle{Max: gtx.Constraints.Min}
				paint.FillShape(gtx.Ops, bg, clip.UniformRRect(rect, r).Op(gtx.Ops))
				// hover / pressed 反馈：在背景上叠一层半透明白
				if btn.Hovered() && !s.Disabled {
					paint.FillShape(gtx.Ops,
						color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x14},
						clip.UniformRRect(rect, r).Op(gtx.Ops))
				}
				if btn.Pressed() && !s.Disabled {
					paint.FillShape(gtx.Ops,
						color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x28},
						clip.UniformRRect(rect, r).Op(gtx.Ops))
				}
				return layout.Dimensions{Size: gtx.Constraints.Min}
			},
			func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{
					Left: s.PadH, Right: s.PadH, Top: s.PadV, Bottom: s.PadV,
				}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					children := []layout.FlexChild{}
					if s.Icon != "" {
						ic := Icon(s.Icon)
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return paintIcon(gtx, ic, s.IconSize, fg)
						}))
						if s.Label != "" {
							children = append(children, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
						}
					}
					if s.Label != "" {
						children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := s.labelStyle(t, fg)
							return lbl.Layout(gtx)
						}))
					}
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, children...)
				})
			},
		)
	})
}

// labelStyle 构造文本子组件。
func (s iconButtonStyle) labelStyle(t *Theme, col color.NRGBA) labelOp {
	return labelOp{
		text:     s.Label,
		size:     s.TextSize,
		color:    col,
		bold:     s.Bold,
		typeface: "CJK",
		theme:    t,
	}
}

type labelOp struct {
	text     string
	size     unit.Sp
	color    color.NRGBA
	bold     bool
	typeface string
	theme    *Theme
}

func (l labelOp) Layout(gtx layout.Context) layout.Dimensions {
	return labelLayout(gtx, l)
}

// labelLayout 把 labelOp 转成 material.Label 调用；从 icons.go 单独抽出来是
// 为了避免在每个按钮里 import widget/material。
func labelLayout(gtx layout.Context, l labelOp) layout.Dimensions {
	if l.theme == nil || l.theme.Material == nil {
		return layout.Dimensions{}
	}
	return materialLabel(gtx, l)
}

// 实现细节移到 components.go 的 materialLabel，保持本文件聚焦于图标。
// 但为了避免循环 import 我们在这里做内联：
//
// 注：因为 Theme.Material 是 *material.Theme，不需要 import material 二次，
// 我们直接调用 material.Label。
//
// （实际实现见同包内 components.go 的 materialLabel。）

// op 占位，避免 import "op" 被裁掉。
var _ = op.Offset
