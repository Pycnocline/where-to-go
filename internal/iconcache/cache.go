// Package iconcache 负责类别图标的两级缓存：磁盘 PNG + 内存 *image.RGBA。
//
// 图标 URL 来自 wiki 的 categoryData.icon 字段（例如
// https://patchwiki.biligame.com/images/rocom/.../<sha>.png）。
// 文件名按 URL 的 SHA1 编码到 {cacheRoot}/icons/<sha>.png。
package iconcache

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/op/paint"

	_ "image/gif"
	_ "image/jpeg"
)

const (
	defaultUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) where-to-go/0.1"
	httpTimeout = 20 * time.Second
)

// Cache 图标缓存。线程安全。
type Cache struct {
	mu        sync.Mutex
	dir       string
	images    map[string]*image.RGBA   // url -> 已解码图像
	ops       map[string]*paint.ImageOp // url -> 预生成的 GPU 上传句柄（避免每帧重建纹理）
	loading   map[string]bool          // 正在下载的 url
	negative  map[string]bool          // 已确认失败的 url（避免重复重试）
	OnReady   func()                   // 任意一张新图就绪时触发（UI Invalidate）
	client    *http.Client
}

// New 创建 / 打开图标缓存目录。
func New(cacheRoot string) (*Cache, error) {
	dir := filepath.Join(cacheRoot, "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 icons 目录失败: %w", err)
	}
	return &Cache{
		dir:      dir,
		images:   map[string]*image.RGBA{},
		ops:      map[string]*paint.ImageOp{},
		loading:  map[string]bool{},
		negative: map[string]bool{},
		client:   &http.Client{Timeout: httpTimeout},
	}, nil
}

// Get 返回 url 对应的解码后图像。
//   - 内存命中 → 直接返回
//   - 磁盘命中 → 异步装载到内存
//   - 都不命中 → 异步下载
//
// 调用方在加载完成前会得到 nil；OnReady 回调用来触发重绘。
func (c *Cache) Get(url string) *image.RGBA {
	if url == "" {
		return nil
	}
	c.mu.Lock()
	if img, ok := c.images[url]; ok {
		c.mu.Unlock()
		return img
	}
	if c.loading[url] || c.negative[url] {
		c.mu.Unlock()
		return nil
	}
	c.loading[url] = true
	c.mu.Unlock()

	go c.fetch(url)
	return nil
}

// GetOp 与 Get 类似，但返回预生成、可复用的 paint.ImageOp。
// 同一张图在不同帧 / 不同位置共用同一个 ImageOp，渲染端能够复用 GPU 纹理，
// 避免重叠点位之间因为重复 NewImageOp 而出现纹理上传抖动 → 视觉闪烁。
func (c *Cache) GetOp(url string) *paint.ImageOp {
	if url == "" {
		return nil
	}
	c.mu.Lock()
	if op, ok := c.ops[url]; ok {
		c.mu.Unlock()
		return op
	}
	if img, ok := c.images[url]; ok {
		op := paint.NewImageOp(img)
		op.Filter = paint.FilterLinear
		c.ops[url] = &op
		c.mu.Unlock()
		return &op
	}
	if c.loading[url] || c.negative[url] {
		c.mu.Unlock()
		return nil
	}
	c.loading[url] = true
	c.mu.Unlock()
	go c.fetch(url)
	return nil
}

// Clear 清空内存缓存（磁盘文件保留）。重抓后调用以强制重新装载。
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.images = map[string]*image.RGBA{}
	c.ops = map[string]*paint.ImageOp{}
	c.negative = map[string]bool{}
}

func (c *Cache) cachePath(url string) string {
	h := sha1.Sum([]byte(url))
	name := hex.EncodeToString(h[:]) + ".png"
	return filepath.Join(c.dir, name)
}

func (c *Cache) fetch(url string) {
	defer func() {
		c.mu.Lock()
		delete(c.loading, url)
		c.mu.Unlock()
	}()

	path := c.cachePath(url)
	// 先看磁盘
	if img, err := loadImage(path); err == nil {
		c.commit(url, img)
		return
	}
	// 下载
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", defaultUA)
	resp, err := c.client.Do(req)
	if err != nil {
		c.markNegative(url)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		c.markNegative(url)
		return
	}
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		c.markNegative(url)
		return
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		c.markNegative(url)
		return
	}
	out.Close()
	if err := os.Rename(tmp, path); err != nil {
		c.markNegative(url)
		return
	}
	img, err := loadImage(path)
	if err != nil {
		c.markNegative(url)
		return
	}
	c.commit(url, img)
}

func (c *Cache) commit(url string, img *image.RGBA) {
	c.mu.Lock()
	c.images[url] = img
	cb := c.OnReady
	c.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (c *Cache) markNegative(url string) {
	c.mu.Lock()
	c.negative[url] = true
	c.mu.Unlock()
}

func loadImage(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// 优先按扩展名走 png 解码（更快），失败则回落 image.Decode。
	if strings.EqualFold(filepath.Ext(path), ".png") {
		if img, err := png.Decode(f); err == nil {
			return toRGBA(img), nil
		}
		if _, err := f.Seek(0, 0); err != nil {
			return nil, err
		}
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return toRGBA(img), nil
}

func toRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok {
		return r
	}
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			rgba.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return rgba
}
