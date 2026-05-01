package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/wiki"
)

// CustomMarkType 用户自定义点位的合成 markType。
const (
	CustomMarkType    = "user_custom"
	CustomTypeName    = "自定义"
	CustomMarkTypeLbl = "自定义点位"
)

// CustomStore 在 data/custom_points.json 中持久化用户在地图上添加的"自定义点位"。
//
// 这些点位会以普通 wiki 点位的形式注入 mapdata.Store，从而无缝复用现有的
// 渲染 / 选区 / 路径流程。
type CustomStore struct {
	mu    sync.Mutex
	path  string
	store *mapdata.Store
}

type customFile struct {
	Points []wiki.PointItem `json:"points"`
}

// NewCustomStore 加载磁盘上的自定义点位并注入 store。
func NewCustomStore(path string, store *mapdata.Store) (*CustomStore, error) {
	c := &CustomStore{path: path, store: store}
	c.ensureCategory()
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *CustomStore) ensureCategory() {
	c.store.EnsureCategory(wiki.CategoryItem{
		MarkType:     CustomMarkType,
		MarkTypeName: CustomMarkTypeLbl,
		Type:         CustomTypeName,
		Icon:         "",
		DefaultShow:  true,
	})
}

func (c *CustomStore) load() error {
	b, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var f customFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	for i := range f.Points {
		f.Points[i].MarkType = CustomMarkType
	}
	c.store.SetPoints(CustomMarkType, f.Points)
	return nil
}

func (c *CustomStore) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *CustomStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	pts := c.store.PointsOf(CustomMarkType)
	out := customFile{Points: pts}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o644)
}

// Add 在 (worldX, worldY) 添加一个自定义点位，标题为 title。返回新建的 PointItem。
func (c *CustomStore) Add(worldX, worldY float64, layer, title string) (wiki.PointItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if title == "" {
		title = "自定义点位"
	}
	p := wiki.PointItem{
		MarkType: CustomMarkType,
		Title:    title,
		ID:       fmt.Sprintf("custom-%d", time.Now().UnixNano()),
		Point:    wiki.Point{Lng: worldX, Lat: worldY},
		Layer:    layer,
	}
	c.store.AddPoint(p)
	if err := c.saveLocked(); err != nil {
		return p, err
	}
	return p, nil
}

// Remove 按 ID 删除一个自定义点位。
func (c *CustomStore) Remove(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store.RemovePoint(CustomMarkType, id)
	return c.saveLocked()
}

// UpdateTitle 修改某个自定义点位的标题。
func (c *CustomStore) UpdateTitle(id, title string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	pts := c.store.PointsOf(CustomMarkType)
	for _, p := range pts {
		if p.ID == id {
			p.Title = title
			c.store.UpdatePoint(p)
			return c.saveLocked()
		}
	}
	return fmt.Errorf("not found: %s", id)
}

// Find 按 ID 查找。
func (c *CustomStore) Find(id string) (wiki.PointItem, bool) {
	pts := c.store.PointsOf(CustomMarkType)
	for _, p := range pts {
		if p.ID == id {
			return p, true
		}
	}
	return wiki.PointItem{}, false
}
