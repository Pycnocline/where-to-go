// Package winutil 中的文件对话框封装。
//
// 直接使用 comdlg32.dll 的 GetOpenFileNameW / GetSaveFileNameW，
// 无需引入 CGo / 第三方 GUI 框架。在 GUI 进程下调用会同步弹出系统文件对话框，
// 用户取消时返回空字符串和 nil。
package winutil

import (
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	maxPath        = 32768 // 留足够大的缓冲，避免长路径被截断
	ofnExplorer    = 0x00080000
	ofnPathMustExist = 0x00000800
	ofnFileMustExist = 0x00001000
	ofnHideReadOnly  = 0x00000004
	ofnOverwritePrompt = 0x00000002
)

type openFileNameW struct {
	StructSize      uint32
	HwndOwner       uintptr
	Instance        uintptr
	Filter          *uint16
	CustomFilter    *uint16
	MaxCustFilter   uint32
	FilterIndex     uint32
	File            *uint16
	MaxFile         uint32
	FileTitle       *uint16
	MaxFileTitle    uint32
	InitialDir      *uint16
	Title           *uint16
	Flags           uint32
	FileOffset      uint16
	FileExtension   uint16
	DefExt          *uint16
	CustData        uintptr
	FnHook          uintptr
	TemplateName    *uint16
	PvReserved      uintptr
	DwReserved      uint32
	FlagsEx         uint32
}

// OpenFileDialog 弹出"打开文件"对话框。
//
//   - title 对话框标题（如 "导入路径"）
//   - filterDesc 过滤器描述（如 "JSON 文件"）
//   - filterPat 过滤器通配（如 "*.json"）
//   - initialDir 初始目录（可空）
//
// 返回用户选择的绝对路径，取消时返回 ""。
func OpenFileDialog(title, filterDesc, filterPat, initialDir string) (string, error) {
	return showFileDialog(title, filterDesc, filterPat, initialDir, false, "")
}

// SaveFileDialog 弹出"保存文件"对话框。
// defaultName 是建议文件名（不含目录）。
func SaveFileDialog(title, filterDesc, filterPat, initialDir, defaultName string) (string, error) {
	return showFileDialog(title, filterDesc, filterPat, initialDir, true, defaultName)
}

func showFileDialog(title, filterDesc, filterPat, initialDir string, save bool, defaultName string) (string, error) {
	comdlg32 := windows.NewLazySystemDLL("comdlg32.dll")
	var proc *windows.LazyProc
	if save {
		proc = comdlg32.NewProc("GetSaveFileNameW")
	} else {
		proc = comdlg32.NewProc("GetOpenFileNameW")
	}

	// 构建 filter 字符串：双 0 结尾，"描述\0模式\0\0"
	filter := buildFilter(filterDesc, filterPat)

	titleP, _ := windows.UTF16PtrFromString(title)
	var initP *uint16
	if initialDir != "" {
		initP, _ = windows.UTF16PtrFromString(initialDir)
	}

	buf := make([]uint16, maxPath)
	if defaultName != "" {
		nm := utf16.Encode([]rune(defaultName))
		copy(buf, nm)
	}

	var ext *uint16
	if i := strings.LastIndex(filterPat, "."); i >= 0 {
		ext, _ = windows.UTF16PtrFromString(filterPat[i+1:])
	}

	flags := uint32(ofnExplorer | ofnPathMustExist | ofnHideReadOnly)
	if save {
		flags |= ofnOverwritePrompt
	} else {
		flags |= ofnFileMustExist
	}

	ofn := openFileNameW{
		StructSize: uint32(unsafe.Sizeof(openFileNameW{})),
		Filter:     &filter[0],
		File:       &buf[0],
		MaxFile:    uint32(len(buf)),
		InitialDir: initP,
		Title:      titleP,
		Flags:      flags,
		DefExt:     ext,
	}

	r1, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn)))
	if r1 == 0 {
		// 用户取消或出错（忽略具体错误码：CommDlgExtendedError 多数对终端用户没意义）
		return "", nil
	}
	// 找到第一个 0 终止符
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return syscall.UTF16ToString(buf[:end]), nil
}

func buildFilter(desc, pat string) []uint16 {
	if desc == "" {
		desc = "All files"
	}
	if pat == "" {
		pat = "*.*"
	}
	s := desc + "\x00" + pat + "\x00\x00"
	return utf16.Encode([]rune(s))
}
