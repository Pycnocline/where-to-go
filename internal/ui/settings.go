package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// MarkerStyle 点位渲染样式。
type MarkerStyle string

const (
	MarkerBubble MarkerStyle = "bubble" // 默认：白底气泡 + 类别名
	MarkerIcon   MarkerStyle = "icon"   // 直接画 wiki 提供的彩色图标
	MarkerDot    MarkerStyle = "dot"    // 旧的彩色圆点
)

// SelectionStyle 选中点位的高亮样式。
type SelectionStyle string

const (
	SelectionHalo SelectionStyle = "halo" // 默认：图标背后的彩色光晕（不遮挡）
	SelectionDot  SelectionStyle = "dot"  // 旧：在图标上叠加大白色圆环
)

// NavFallback 定位失败（NCC 置信度低 / 截屏失败 / 矩阵奇异）时的处理策略。
type NavFallback string

const (
	NavFallbackStay NavFallback = "stay" // 默认：原地停留，不更新位置
	NavFallbackLast NavFallback = "last" // 用上一次 Fix（外推一帧）
	NavFallbackLost NavFallback = "lost" // 显式标记"已丢失定位"，UI 弹出提示
)

// CenterMode 自动居中模式。
type CenterMode string

const (
	CenterOff    CenterMode = "off"     // 不自动居中
	CenterAlways CenterMode = "always"  // 任何时候都把玩家保持在视野中
	CenterNavOnly CenterMode = "navonly" // 仅在导航某条路径时居中；纯追踪不居中
)

// Settings 用户可调的全局选项。
type Settings struct {
	MarkerStyle    MarkerStyle    `json:"markerStyle"`
	IconSize       int            `json:"iconSize"`
	SelectionStyle SelectionStyle `json:"selectionStyle"`
	DebugLog       bool           `json:"debugLog"`

	// 导航相关
	MinimapROI               MinimapROI  `json:"minimapRoi"`
	NavSimulator             bool        `json:"navSimulator"`             // true = 用模拟器代替真截屏匹配
	NavTracking              bool        `json:"navTracking"`              // true = 启用实时追踪玩家位置（独立于路径导航）
	WorldUnitsPerMinimapPx   float64     `json:"worldUnitsPerMinimapPx"`   // 校准：游戏小地图 1px = 多少世界单位
	NavAutoCenter            bool        `json:"navAutoCenter"`            // 已废弃保留兼容；用 NavCenterMode 代替
	NavCenterMode            CenterMode  `json:"navCenterMode"`            // 自动居中模式
	NavFallback              NavFallback `json:"navFallback"`              // 定位失败兜底策略
	NavSearchZoom            int         `json:"navSearchZoom"`            // NCC 搜索使用的 wiki zoom (4~8)，默认 8

	// 悬浮窗
	OverlayAlpha        int  `json:"overlayAlpha"`        // 0..255，0 表示用默认 0xC0
	OverlayClickThrough bool `json:"overlayClickThrough"` // 鼠标穿透
	// OverlayProgressBar 在悬浮窗顶部显示一条与窗等宽的导航进度条；仅在导航
	// 激活时绘制。默认 true。pointer 类型方便区分"未设置 = 默认开"和"显式关"，
	// 避免 JSON 反序列化把 false 当成默认。
	OverlayProgressBar *bool `json:"overlayProgressBar,omitempty"`

	// 追踪位置预测：在 fix 之间按速度外推显示位置，减少滞后感。
	// 由于 NCC 每 600ms 一次，预测能让画面在两次 fix 之间继续平滑前进。
	NavPredict bool `json:"navPredict"`

	// 最近一次成功定位到的世界坐标（用于重启后自动恢复追踪的种子）。
	// 0/0 视为未设置，退化为 mapView 视图中心。
	LastPlayerX float64 `json:"lastPlayerX"`
	LastPlayerY float64 `json:"lastPlayerY"`
	LastPlayerSet bool  `json:"lastPlayerSet"`
}

// MinimapROI 屏幕上小地图区域（绝对屏幕像素坐标）。
type MinimapROI struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Empty 是否未配置。
func (r MinimapROI) Empty() bool { return r.W <= 0 || r.H <= 0 }

// OverlayProgressBarEnabled 返回悬浮窗顶部进度条是否启用（默认 true）。
func (s Settings) OverlayProgressBarEnabled() bool {
	if s.OverlayProgressBar == nil {
		return true
	}
	return *s.OverlayProgressBar
}

// DefaultSettings 默认配置。
func DefaultSettings() Settings {
	return Settings{
		MarkerStyle:            MarkerIcon,
		IconSize:               24,
		SelectionStyle:         SelectionHalo,
		DebugLog:               false,
		NavSimulator:           false, // 默认走真实截屏；模拟器仅供无游戏环境调试视觉
		NavTracking:            false,
		WorldUnitsPerMinimapPx: 0.5, // 经验默认；首次手动校准后会被实测值替换
		NavAutoCenter:          true,
		NavCenterMode:          CenterAlways,
		NavFallback:            NavFallbackStay,
		NavSearchZoom:          8, // wiki 最大 zoom，对应游戏内"最大放大"小地图
		OverlayAlpha:           0xFF,
		OverlayClickThrough:    false,
		NavPredict:             false, // 默认关闭：预测在弱信号下会放大漂移；用户可在设置里开启
	}
}

// SettingsStore 维护设置的持久化、加载和并发访问。
type SettingsStore struct {
	mu   sync.RWMutex
	path string
	cur  Settings
}

// NewSettingsStore 从 path 读取设置；不存在或解析失败时使用默认。
func NewSettingsStore(path string) *SettingsStore {
	s := &SettingsStore{path: path, cur: DefaultSettings()}
	s.load()
	return s
}

func (s *SettingsStore) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var v Settings
	if err := json.Unmarshal(b, &v); err != nil {
		return
	}
	// 校验
	switch v.MarkerStyle {
	case MarkerBubble, MarkerIcon, MarkerDot:
	default:
		v.MarkerStyle = MarkerIcon
	}
	if v.IconSize < 12 || v.IconSize > 64 {
		v.IconSize = 24
	}
	switch v.SelectionStyle {
	case SelectionHalo, SelectionDot:
	default:
		v.SelectionStyle = SelectionHalo
	}
	if v.WorldUnitsPerMinimapPx <= 0 || v.WorldUnitsPerMinimapPx < 0.1 || v.WorldUnitsPerMinimapPx > 5.0 {
		// 0.1..5.0 是合理 K 范围；之前因校准 bug 累积出来的 ≈0.05 自动修复到默认 0.5
		v.WorldUnitsPerMinimapPx = 0.5
	}
	switch v.NavFallback {
	case NavFallbackStay, NavFallbackLast, NavFallbackLost:
	default:
		v.NavFallback = NavFallbackStay
	}
	if v.NavSearchZoom < 3 || v.NavSearchZoom > 8 {
		v.NavSearchZoom = 8
	}
	switch v.NavCenterMode {
	case CenterOff, CenterAlways, CenterNavOnly:
	default:
		// 旧字段兼容：没 NavCenterMode 就根据旧的 NavAutoCenter 推导
		if v.NavAutoCenter {
			v.NavCenterMode = CenterAlways
		} else {
			v.NavCenterMode = CenterOff
		}
	}
	s.cur = v
}

// Get 返回当前配置的副本。
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Set 整体替换配置并立即落盘。
func (s *SettingsStore) Set(v Settings) {
	s.mu.Lock()
	s.cur = v
	path := s.path
	cur := s.cur
	s.mu.Unlock()
	_ = saveSettings(path, cur)
}

// Update 在锁内部分修改配置并落盘。
func (s *SettingsStore) Update(fn func(*Settings)) {
	s.mu.Lock()
	fn(&s.cur)
	path := s.path
	cur := s.cur
	s.mu.Unlock()
	_ = saveSettings(path, cur)
}

func saveSettings(path string, v Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
