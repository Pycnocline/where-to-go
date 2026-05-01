package mapdata

import (
	"sync"

	"github.com/where-to-go/app/internal/wiki"
)

// Store 是 UI 持有的全局只读数据视图（通过 SetData 整体替换）。
//
// 所有读操作都通过 RLock 保护；UI 可在主线程频繁读，抓取协程
// 完成后只在最后一次性写入。
type Store struct {
	mu sync.RWMutex

	meta       *wiki.MapMeta
	categories []wiki.CategoryItem
	categoryByMarkType map[string]*wiki.CategoryItem
	areas      []wiki.AreaItem
	points     map[string][]wiki.PointItem
}

// NewStore 空仓库。
func NewStore() *Store {
	return &Store{
		categoryByMarkType: map[string]*wiki.CategoryItem{},
		points:             map[string][]wiki.PointItem{},
	}
}

// SetData 把抓取结果一次性灌入。
func (s *Store) SetData(r *wiki.FetchResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = r.Meta
	if r.Categories != nil {
		s.categories = r.Categories.Data
		s.categoryByMarkType = make(map[string]*wiki.CategoryItem, len(r.Categories.Data))
		for i := range s.categories {
			s.categoryByMarkType[s.categories[i].MarkType] = &s.categories[i]
		}
	}
	s.areas = r.Areas
	if r.Points != nil {
		s.points = r.Points
	}
}

// Meta 返回当前元数据。可能为 nil（数据未加载）。
func (s *Store) Meta() *wiki.MapMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.meta
}

// Layers 返回所有图层（按 index 从大到小排序后的副本，地表 G 排第一）。
func (s *Store) Layers() []wiki.Layer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.meta == nil {
		return nil
	}
	out := make([]wiki.Layer, len(s.meta.Layers))
	copy(out, s.meta.Layers)
	// 排序：index 从大到小（0 在前）
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Index > out[j-1].Index; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Categories 返回类别列表副本（按 type 大类聚合后展开）。
func (s *Store) Categories() []wiki.CategoryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]wiki.CategoryItem, len(s.categories))
	copy(out, s.categories)
	return out
}

// CategoryOf 按 markType 查类别（可能返回 nil）。
func (s *Store) CategoryOf(markType string) *wiki.CategoryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.categoryByMarkType[markType]
}

// PointsOf 返回某 markType 的全部点位（直接返回内部切片，调用方不要修改）。
func (s *Store) PointsOf(markType string) []wiki.PointItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.points[markType]
}

// AllPoints 遍历所有点位（用于全量绘制）。
func (s *Store) AllPoints(visit func(markType string, p wiki.PointItem)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for mt, lst := range s.points {
		for _, p := range lst {
			visit(mt, p)
		}
	}
}

// PointInWorldRect 返回所有 markType ∈ allowedTypes（key 为 markType）
// 且坐标落在世界矩形 [x0,x1]×[y0,y1] 内的点位。
func (s *Store) PointInWorldRect(allowedTypes map[string]bool, x0, y0, x1, y1 float64) []HitPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	var out []HitPoint
	for mt, lst := range s.points {
		if allowedTypes != nil && !allowedTypes[mt] {
			continue
		}
		for _, p := range lst {
			if p.Point.Lng >= x0 && p.Point.Lng <= x1 && p.Point.Lat >= y0 && p.Point.Lat <= y1 {
				out = append(out, HitPoint{
					MarkType: mt,
					ID:       p.ID,
					X:        p.Point.Lng,
					Y:        p.Point.Lat,
					Layer:    p.Layer,
				})
			}
		}
	}
	return out
}

// NearestVisibleWiki 在 (worldX, worldY) 附近搜索最近的、属于 allowedTypes 的 wiki 点。
// 仅当世界距离^2 ≤ maxR2World 时才认为命中。
func (s *Store) NearestVisibleWiki(worldX, worldY, maxR2World float64, allowedTypes map[string]bool) (HitPoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bestD := maxR2World
	var best HitPoint
	found := false
	for mt, lst := range s.points {
		if allowedTypes != nil && !allowedTypes[mt] {
			continue
		}
		for _, p := range lst {
			dx := p.Point.Lng - worldX
			dy := p.Point.Lat - worldY
			d := dx*dx + dy*dy
			if d <= bestD {
				bestD = d
				best = HitPoint{MarkType: mt, ID: p.ID, X: p.Point.Lng, Y: p.Point.Lat, Layer: p.Layer}
				found = true
			}
		}
	}
	return best, found
}

// HitPoint 简化的 wiki 点位命中信息。
type HitPoint struct {
	MarkType string
	ID       string
	X, Y     float64
	Layer    string
}

// EnsureCategory 注册一个合成 / 用户自定义类别。已存在则覆盖元信息。
// 用于 "user_custom" 这类不来自 wiki 的虚拟类别。
func (s *Store) EnsureCategory(c wiki.CategoryItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.categoryByMarkType[c.MarkType]; ok && existing != nil {
		*existing = c
		return
	}
	s.categories = append(s.categories, c)
	s.categoryByMarkType[c.MarkType] = &s.categories[len(s.categories)-1]
}

// SetPoints 整体替换某 markType 的点位列表（用于持久化的自定义点位整体灌入）。
func (s *Store) SetPoints(markType string, pts []wiki.PointItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pts == nil {
		delete(s.points, markType)
		return
	}
	cp := make([]wiki.PointItem, len(pts))
	copy(cp, pts)
	s.points[markType] = cp
}

// AddPoint 追加一个点位。
func (s *Store) AddPoint(p wiki.PointItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.points[p.MarkType] = append(s.points[p.MarkType], p)
}

// RemovePoint 按 (markType, id) 移除一个点位。返回是否删除成功。
func (s *Store) RemovePoint(markType, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lst := s.points[markType]
	for i, p := range lst {
		if p.ID == id {
			s.points[markType] = append(lst[:i], lst[i+1:]...)
			return true
		}
	}
	return false
}

// UpdatePoint 按 (markType, id) 替换一个点位条目。
func (s *Store) UpdatePoint(p wiki.PointItem) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lst := s.points[p.MarkType]
	for i, q := range lst {
		if q.ID == p.ID {
			lst[i] = p
			return true
		}
	}
	return false
}
