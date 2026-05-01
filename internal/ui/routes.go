package ui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/where-to-go/app/internal/mapdata"
)

// RouteStore 管理保存到磁盘的路径集合。
//
// 文件布局：
//
//	{routesDir}/<id>.json    保存的路径，每条一个文件
//
// id 由保存时生成（时间戳 + 序号），文件名安全；
// Path.Name 是用户友好名，可重复。
type RouteStore struct {
	mu     sync.RWMutex
	dir    string
	routes []*mapdata.Path
}

// NewRouteStore 打开 / 创建路径目录。
func NewRouteStore(dir string) (*RouteStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建路径目录失败: %w", err)
	}
	r := &RouteStore{dir: dir}
	_ = r.LoadAll()
	return r, nil
}

// LoadAll 从磁盘装载所有路径文件。
func (r *RouteStore) LoadAll() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = r.routes[:0]
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		full := filepath.Join(r.dir, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var p mapdata.Path
		if err := json.Unmarshal(b, &p); err != nil {
			continue
		}
		if p.ID == "" {
			p.ID = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		r.routes = append(r.routes, &p)
	}
	sort.Slice(r.routes, func(i, j int) bool { return r.routes[i].ID < r.routes[j].ID })
	return nil
}

// All 返回当前所有路径副本。
func (r *RouteStore) All() []*mapdata.Path {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*mapdata.Path, len(r.routes))
	copy(out, r.routes)
	return out
}

// Find 按 id 查找；不存在返回 nil。
func (r *RouteStore) Find(id string) *mapdata.Path {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.routes {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Save 把 path 写入磁盘并更新内存。
func (r *RouteStore) Save(p *mapdata.Path) error {
	if p.ID == "" {
		p.ID = newRouteID()
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	full := filepath.Join(r.dir, sanitize(p.ID)+".json")
	if err := os.WriteFile(full, b, 0o644); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ex := range r.routes {
		if ex.ID == p.ID {
			r.routes[i] = p
			return nil
		}
	}
	r.routes = append(r.routes, p)
	return nil
}

// Delete 按 id 删除。
func (r *RouteStore) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ex := range r.routes {
		if ex.ID == id {
			r.routes = append(r.routes[:i], r.routes[i+1:]...)
			break
		}
	}
	full := filepath.Join(r.dir, sanitize(id)+".json")
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ImportFile 从外部 .json 文件导入一条路径并保存进 routes/。
func (r *RouteStore) ImportFile(path string) (*mapdata.Path, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p mapdata.Path
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	// 给一个新 ID，避免与既有冲突
	p.ID = newRouteID()
	if p.Name == "" {
		p.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if err := r.Save(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ExportFile 把指定路径写到任意位置（用户选择的目标文件）。
func (r *RouteStore) ExportFile(p *mapdata.Path, targetPath string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(targetPath, b, 0o644)
}

func newRouteID() string {
	return fmt.Sprintf("rt-%d", time.Now().UnixNano())
}

func sanitize(s string) string {
	bad := []rune{'/', '\\', ':', '*', '?', '"', '<', '>', '|'}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		isBad := false
		for _, b := range bad {
			if r == b {
				isBad = true
				break
			}
		}
		if isBad {
			out = append(out, '_')
		} else {
			out = append(out, r)
		}
	}
	return string(out)
}

// 颜色调色板：路径默认颜色循环
var defaultPathColors = []color.NRGBA{
	{R: 0x4a, G: 0xc6, B: 0x6e, A: 0xff}, // 绿
	{R: 0xe2, G: 0x6a, B: 0x6a, A: 0xff}, // 红
	{R: 0x4f, G: 0x9d, B: 0xe5, A: 0xff}, // 蓝
	{R: 0xf2, G: 0xb0, B: 0x4f, A: 0xff}, // 橙
	{R: 0xb1, G: 0x7b, B: 0xe0, A: 0xff}, // 紫
}

func nextPathColor(idx int) color.NRGBA {
	return defaultPathColors[idx%len(defaultPathColors)]
}

func colorToHex(c color.NRGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func parseHexColor(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	var rgb [3]uint8
	for i := 0; i < 3; i++ {
		var hi, lo uint8
		if !hex1(s[i*2], &hi) || !hex1(s[i*2+1], &lo) {
			return color.NRGBA{}, false
		}
		rgb[i] = hi<<4 | lo
	}
	return color.NRGBA{R: rgb[0], G: rgb[1], B: rgb[2], A: 0xff}, true
}

func hex1(c byte, out *uint8) bool {
	switch {
	case c >= '0' && c <= '9':
		*out = c - '0'
	case c >= 'a' && c <= 'f':
		*out = c - 'a' + 10
	case c >= 'A' && c <= 'F':
		*out = c - 'A' + 10
	default:
		return false
	}
	return true
}
