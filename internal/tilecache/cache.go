// Package tilecache 负责瓦片的两级缓存：磁盘 (PNG 文件) + 内存 (解码后的 image.RGBA)。
//
// 上层的 Gio 渲染器需要解码后的位图来上传 GPU 纹理，因此我们做一个 LRU 内存缓存
// 限制内存上限。视口需要某瓦片但磁盘没有时，触发后台 Fetcher 下载。
package tilecache

import (
	"container/list"
	"context"
	"image"
	"image/png"
	"os"
	"sync"

	"github.com/where-to-go/app/internal/wiki"
)

// MaxInMemoryTiles 内存中最多保留的瓦片张数（每张 256*256*4 ≈ 256KB → 800 张约 200MB 上限）。
const MaxInMemoryTiles = 800

// State 描述一张瓦片的当前状态。
type State int

const (
	StateUnknown    State = iota // 未尝试加载
	StateLoading                 // 正在下载中
	StateLoaded                  // 已解码到内存
	StateOutOfBounds             // 已确认超出有效范围
	StateError                   // 加载失败
)

// Tile 一张瓦片的内存表示。
type Tile struct {
	Layer string
	Z, X, Y int
	State State
	Image *image.RGBA
}

// Cache 瓦片缓存，线程安全。
type Cache struct {
	mu        sync.Mutex
	cacheRoot string
	fetcher   *wiki.Fetcher
	lru       *list.List
	index     map[string]*list.Element // key = layer/z/x/y
	loading   map[string]chan struct{} // 正在下载的 key

	// 触发重绘的回调（UI 注册）
	OnTileReady func()
}

// NewCache 创建瓦片缓存。fetcher 用于下载磁盘上没有的瓦片。
func NewCache(cacheRoot string, fetcher *wiki.Fetcher) *Cache {
	return &Cache{
		cacheRoot: cacheRoot,
		fetcher:   fetcher,
		lru:       list.New(),
		index:     make(map[string]*list.Element),
		loading:   make(map[string]chan struct{}),
	}
}

func tileKey(layer string, z, x, y int) string {
	return layer + "/" + itoa(z) + "/" + itoa(x) + "_" + itoa(y)
}
func itoa(n int) string {
	// 局部小工具，避免引 strconv
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Get 同步取一张已经加载到内存的瓦片。
//
// 如果磁盘上有 → 解码并缓存进内存后返回。
// 如果磁盘上没有 → 返回 StateLoading 并启动后台下载，下载完成后 OnTileReady 触发 UI 重绘。
func (c *Cache) Get(layer wiki.Layer, z, x, y int) *Tile {
	key := tileKey(layer.Name, z, x, y)
	c.mu.Lock()
	if el, ok := c.index[key]; ok {
		c.lru.MoveToFront(el)
		t := el.Value.(*Tile)
		c.mu.Unlock()
		return t
	}
	if _, ok := c.loading[key]; ok {
		c.mu.Unlock()
		return &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateLoading}
	}
	c.mu.Unlock()

	// 先看磁盘
	path := wiki.TilePath(c.cacheRoot, layer.Name, z, x, y)
	if _, err := os.Stat(path); err == nil {
		img, err := loadPNG(path)
		if err != nil {
			return &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateError}
		}
		t := &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateLoaded, Image: img}
		c.put(key, t)
		return t
	}
	if _, err := os.Stat(path + ".404"); err == nil {
		t := &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateOutOfBounds}
		c.put(key, t)
		return t
	}

	// 异步下载
	c.startLoad(layer, z, x, y)
	return &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateLoading}
}

func (c *Cache) startLoad(layer wiki.Layer, z, x, y int) {
	key := tileKey(layer.Name, z, x, y)
	c.mu.Lock()
	if _, ok := c.loading[key]; ok {
		c.mu.Unlock()
		return
	}
	done := make(chan struct{})
	c.loading[key] = done
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.loading, key)
			close(done)
			c.mu.Unlock()
			if c.OnTileReady != nil {
				c.OnTileReady()
			}
		}()
		ctx := context.Background()
		path, err := c.fetcher.FetchTileOnDemand(ctx, layer, z, x, y)
		if err != nil {
			if err == wiki.ErrTileOutOfBounds {
				t := &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateOutOfBounds}
				c.put(key, t)
			}
			return
		}
		img, err := loadPNG(path)
		if err != nil {
			return
		}
		c.put(key, &Tile{Layer: layer.Name, Z: z, X: x, Y: y, State: StateLoaded, Image: img})
	}()
}

func (c *Cache) put(key string, t *Tile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[key]; ok {
		el.Value = t
		c.lru.MoveToFront(el)
		return
	}
	el := c.lru.PushFront(t)
	c.index[key] = el
	for c.lru.Len() > MaxInMemoryTiles {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.lru.Remove(back)
		bt := back.Value.(*Tile)
		delete(c.index, tileKey(bt.Layer, bt.Z, bt.X, bt.Y))
	}
}

func loadPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	b := img.Bounds()
	rgba := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba, nil
}
