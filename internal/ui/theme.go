// Package ui 是基于 Gio 的全部界面层。
package ui

import (
	"image/color"
	"os"

	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/text"
	"gioui.org/widget/material"

	"github.com/where-to-go/app/internal/winutil"
)

// Theme 应用全局主题（颜色 + 字体）。
type Theme struct {
	Material *material.Theme
	BG       color.NRGBA // 主背景
	Panel    color.NRGBA // 面板背景
	Text     color.NRGBA // 主文字
	TextDim  color.NRGBA // 次文字
	Accent   color.NRGBA // 强调色（绿色）
	Warn     color.NRGBA // 警告色（黄色）
	Err      color.NRGBA // 错误色（红色）
}

// NewTheme 装载中文字体并创建一套配色。
func NewTheme() (*Theme, error) {
	collection, err := loadCJKFont()
	if err != nil {
		// 字体加载失败不致命，使用默认 ASCII 字体（中文会显示豆腐块）
		collection = nil
	}
	mat := material.NewTheme()
	mat.Shaper = text.NewShaper(text.WithCollection(collection))

	t := &Theme{
		Material: mat,
		BG:       color.NRGBA{R: 0x1a, G: 0x1c, B: 0x22, A: 0xff},
		Panel:    color.NRGBA{R: 0x24, G: 0x27, B: 0x30, A: 0xff},
		Text:     color.NRGBA{R: 0xea, G: 0xea, B: 0xea, A: 0xff},
		TextDim:  color.NRGBA{R: 0x99, G: 0xa1, B: 0xae, A: 0xff},
		Accent:   color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e, A: 0xff},
		Warn:     color.NRGBA{R: 0xf2, G: 0xb0, B: 0x4f, A: 0xff},
		Err:      color.NRGBA{R: 0xe2, G: 0x6a, B: 0x6a, A: 0xff},
	}
	t.Material.Palette.Bg = t.BG
	t.Material.Palette.Fg = t.Text
	t.Material.Palette.ContrastBg = t.Accent
	t.Material.Palette.ContrastFg = color.NRGBA{R: 0, G: 0, B: 0, A: 0xff}
	return t, nil
}

// loadCJKFont 从系统字体目录加载一个中文字体并返回 Gio 字体集合。
func loadCJKFont() ([]font.FontFace, error) {
	path, err := winutil.FindChineseFont()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// 微软雅黑是 ttc 集合，使用 ParseCollection
	faces, err := opentype.ParseCollection(data)
	if err != nil {
		// 单 ttf 走 Parse
		face, perr := opentype.Parse(data)
		if perr != nil {
			return nil, perr
		}
		return []font.FontFace{{Font: font.Font{Typeface: "CJK"}, Face: face}}, nil
	}
	out := make([]font.FontFace, 0, len(faces))
	for _, f := range faces {
		out = append(out, font.FontFace{Font: font.Font{Typeface: "CJK"}, Face: f.Face})
	}
	return out, nil
}
