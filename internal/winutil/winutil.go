// Package winutil 提供跨子系统使用的小工具：缓存目录、Win32 字符串转换、字体加载等。
package winutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppRoot 返回应用根目录：优先 exe 同级目录（便于用户管理 / 整体迁移），
// 当该目录不可写（例如安装在 Program Files）时回落到 %LOCALAPPDATA%\where-to-go。
func AppRoot() (string, error) {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if dir != "" && writable(dir) {
			return dir, nil
		}
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}
	if base == "" {
		return "", fmt.Errorf("无法确定可写的应用目录")
	}
	root := filepath.Join(base, "where-to-go")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建应用目录失败: %w", err)
	}
	return root, nil
}

// writable 探测目录是否可写。
func writable(dir string) bool {
	probe := filepath.Join(dir, ".where-to-go-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// CacheRoot 返回应用缓存根目录 {AppRoot}/cache。
func CacheRoot() (string, error) {
	app, err := AppRoot()
	if err != nil {
		return "", err
	}
	root := filepath.Join(app, "cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建缓存目录失败: %w", err)
	}
	return root, nil
}

// DataRoot 返回用户数据目录（路径、配置等可写数据），与缓存分离。
func DataRoot() (string, error) {
	app, err := AppRoot()
	if err != nil {
		return "", err
	}
	root := filepath.Join(app, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

// FindChineseFont 尝试在系统字体目录中找到一个能完整渲染简体中文的字体文件。
// 顺序优先级：微软雅黑 → 微软正黑（备选）→ 宋体 → 黑体。
func FindChineseFont() (string, error) {
	winDir := os.Getenv("WINDIR")
	if winDir == "" {
		winDir = `C:\Windows`
	}
	fontDir := filepath.Join(winDir, "Fonts")
	candidates := []string{
		"msyh.ttc", "msyh.ttf", "msyhl.ttc",
		"simhei.ttf", "simsun.ttc", "simfang.ttf",
		"Deng.ttf", "Dengl.ttf",
	}
	for _, c := range candidates {
		p := filepath.Join(fontDir, c)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("未在 %s 找到可用中文字体", fontDir)
}
