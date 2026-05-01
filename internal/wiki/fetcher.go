package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) where-to-go/0.1"
	defaultBaseURL  = "https://wiki.biligame.com"
	mapPagePath     = "/rocom/%E5%A4%A7%E5%9C%B0%E5%9B%BE" // /rocom/大地图
	gameName        = "rocom"
	httpTimeout     = 30 * time.Second
)

// ProgressEvent 抓取过程中产生的事件，UI 据此驱动加载页。
type ProgressEvent struct {
	Stage   string  // "metadata" / "categories" / "points" / "tiles"
	Message string  // 一行文字日志
	Done    int64   // 当前 stage 已完成数量
	Total   int64   // 当前 stage 总数（0 表示未知）
	Failed  int64   // 失败数量
	Err     error   // 致命错误
}

// FetchResult 资源抓取最终产物（不含瓦片，瓦片在磁盘缓存）。
type FetchResult struct {
	Meta       *MapMeta
	Categories *CategoryData
	Areas      []AreaItem
	TextLayer  json.RawMessage
	Points     map[string][]PointItem // markType -> points
}

// Fetcher 负责按需 / 完整地从 Wiki 抓取资源到本地缓存。
type Fetcher struct {
	BaseURL    string        // 默认 https://wiki.biligame.com
	HTTP       *http.Client
	CacheRoot  string        // 缓存根（瓦片、原始 HTML 都在此下）
	OnProgress func(ProgressEvent)
	Concurrency int          // 瓦片并发数（默认 16）

	httpClient *http.Client
}

// NewFetcher 构造默认 Fetcher。
func NewFetcher(cacheRoot string, onProgress func(ProgressEvent)) *Fetcher {
	return &Fetcher{
		BaseURL:     defaultBaseURL,
		CacheRoot:   cacheRoot,
		OnProgress:  onProgress,
		Concurrency: 16,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
	}
}

func (f *Fetcher) emit(ev ProgressEvent) {
	if f.OnProgress != nil {
		f.OnProgress(ev)
	}
}

// FetchAll 主入口：抓取元数据 + 全部点位 + z=4 全部瓦片。
//
// 调用一次后本地缓存即可让 UI 完整离线展示低缩放视图；
// 高缩放瓦片由 tilecache 在视口需要时按需获取。
func (f *Fetcher) FetchAll(ctx context.Context) (*FetchResult, error) {
	res := &FetchResult{Points: map[string][]PointItem{}}

	// 1) 抓元数据页
	f.emit(ProgressEvent{Stage: "metadata", Message: "正在拉取大地图元数据页…"})
	html, err := f.fetchString(ctx, f.BaseURL+mapPagePath)
	if err != nil {
		return nil, fmt.Errorf("拉取大地图页失败: %w", err)
	}
	// 落盘以备调试
	_ = os.WriteFile(filepath.Join(f.CacheRoot, "_dadetu.html"), []byte(html), 0o644)

	res.Meta, err = ParseMapMeta(html)
	if err != nil {
		return nil, err
	}
	f.emit(ProgressEvent{Stage: "metadata", Message: fmt.Sprintf("解析到 %d 个图层、缩放 %d~%d", len(res.Meta.Layers), res.Meta.MinZoom, res.Meta.MaxZoom)})

	res.Categories, err = ParseCategoryData(html)
	if err != nil {
		return nil, err
	}
	f.emit(ProgressEvent{Stage: "categories", Message: fmt.Sprintf("解析到 %d 个点位类别", len(res.Categories.Data))})

	if areas, err := ParseAreaData(html); err == nil {
		res.Areas = areas
		f.emit(ProgressEvent{Stage: "categories", Message: fmt.Sprintf("解析到 %d 个区域", len(areas))})
	} else {
		f.emit(ProgressEvent{Stage: "categories", Message: "区域数据缺失或为空（可忽略）"})
	}
	if tl, err := ParseTextLayer(html); err == nil {
		res.TextLayer = tl
	}

	// 2) 逐个 markType 拉点位
	if err := f.fetchAllPoints(ctx, res); err != nil {
		return nil, err
	}

	// 3) 抓 z=4 全瓦片（极小，作为离线 baseline）
	if err := f.fetchBaselineTiles(ctx, res.Meta); err != nil {
		return nil, err
	}

	return res, nil
}

// fetchAllPoints 通过 Data:Mapnew/point.json 一次性拉所有类别的点位。
//
// 返回的 HTML 含 <div id="mapPointData">，文本是 JS 对象字面量（数字键未加引号）。
// 我们清洗 HTML、规范键名为 JSON 字符串，再统一解析。
func (f *Fetcher) fetchAllPoints(ctx context.Context, res *FetchResult) error {
	u := fmt.Sprintf("%s/%s/%s/point.json", f.BaseURL, gameName, res.Meta.DataPrefix)
	f.emit(ProgressEvent{Stage: "points", Message: "正在拉取批量点位数据…"})

	html, err := f.fetchString(ctx, u)
	if err != nil {
		return fmt.Errorf("拉取批量点位失败: %w", err)
	}
	// 落盘备查
	pointsDir := filepath.Join(f.CacheRoot, "points")
	_ = os.MkdirAll(pointsDir, 0o755)
	_ = os.WriteFile(filepath.Join(pointsDir, "_raw_point.html"), []byte(html), 0o644)

	parsed, err := ParseAllPoints(html)
	if err != nil {
		return err
	}
	res.Points = parsed
	totalPoints := 0
	for _, lst := range parsed {
		totalPoints += len(lst)
	}
	f.emit(ProgressEvent{Stage: "points", Message: fmt.Sprintf("点位拉取完成：%d 个 markType，共 %d 个点位", len(parsed), totalPoints), Done: int64(len(parsed)), Total: int64(len(parsed))})
	return nil
}

// fetchBaselineTiles 抓取所有图层在 minZoom（默认 z=4）下的全部有效瓦片。
//
// z=4 时 refer = ceil(8/2) = 4，每层共 8x8=64 张，三层 192 张。
func (f *Fetcher) fetchBaselineTiles(ctx context.Context, meta *MapMeta) error {
	z := meta.MinZoom
	type job struct {
		layer Layer
		x, y  int
	}
	var jobs []job
	for _, ly := range meta.Layers {
		refer := tileRefer(z)
		for x := -refer * ly.X1; x < refer*ly.X2; x++ {
			for y := -refer * ly.Y1; y < refer*ly.Y2; y++ {
				jobs = append(jobs, job{ly, x, y})
			}
		}
	}
	total := int64(len(jobs))
	var done, failed int64
	f.emit(ProgressEvent{Stage: "tiles", Message: fmt.Sprintf("开始下载 z=%d 基础瓦片，共 %d 张…", z, total), Total: total})

	jobCh := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < f.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				select {
				case <-ctx.Done():
					return
				default:
				}
				err := f.fetchOneTile(ctx, j.layer, z, j.x, j.y)
				if err != nil {
					atomic.AddInt64(&failed, 1)
				}
				d := atomic.AddInt64(&done, 1)
				if d%16 == 0 || d == total {
					f.emit(ProgressEvent{Stage: "tiles", Message: fmt.Sprintf("瓦片 %d/%d (失败 %d)", d, total, atomic.LoadInt64(&failed)), Done: d, Total: total, Failed: atomic.LoadInt64(&failed)})
				}
			}
		}()
	}
	for _, j := range jobs {
		select {
		case <-ctx.Done():
			close(jobCh)
			wg.Wait()
			return ctx.Err()
		case jobCh <- j:
		}
	}
	close(jobCh)
	wg.Wait()
	f.emit(ProgressEvent{Stage: "tiles", Message: fmt.Sprintf("基础瓦片下载完成：%d/%d 成功，失败 %d", done-failed, total, failed), Done: done, Total: total, Failed: failed})
	return nil
}

// tileRefer 见源 JS：refer = ceil((1 << (z-1)) / 2)。
func tileRefer(z int) int {
	if z <= 0 {
		return 1
	}
	v := 1 << (z - 1)
	return (v + 1) / 2
}

// fetchOneTile 下载单张瓦片到磁盘缓存。已存在则跳过。
func (f *Fetcher) fetchOneTile(ctx context.Context, ly Layer, z, x, y int) error {
	dst := TilePath(f.CacheRoot, ly.Name, z, x, y)
	if _, err := os.Stat(dst); err == nil {
		return nil // 已缓存
	}
	url := tileURL(ly.TileURL, z, x, y)
	tmp := dst + ".part"
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", defaultUA)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// 边界外瓦片（OSS 通常返回 404），写入空标记避免重复请求。
		return os.WriteFile(dst+".404", nil, 0o644)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

// TilePath 返回瓦片在缓存中的本地路径。
//
//	{cache}/tiles/{layer}/{z}/{x}_{y}.png
func TilePath(cacheRoot, layer string, z, x, y int) string {
	return filepath.Join(cacheRoot, "tiles", layer, fmt.Sprint(z), fmt.Sprintf("%d_%d.png", x, y))
}

// FetchTileOnDemand 用于 P2 阶段的视口懒加载。
// 返回本地路径（成功）或错误。404 时返回特殊的 ErrTileOutOfBounds。
func (f *Fetcher) FetchTileOnDemand(ctx context.Context, layer Layer, z, x, y int) (string, error) {
	if err := f.fetchOneTile(ctx, layer, z, x, y); err != nil {
		return "", err
	}
	dst := TilePath(f.CacheRoot, layer.Name, z, x, y)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if _, err := os.Stat(dst + ".404"); err == nil {
		return "", ErrTileOutOfBounds
	}
	return "", fmt.Errorf("瓦片下载后仍不存在: %s", dst)
}

// ErrTileOutOfBounds 表示请求的瓦片超出地图有效范围。
var ErrTileOutOfBounds = fmt.Errorf("瓦片超出有效范围")

func tileURL(template string, z, x, y int) string {
	r := strings.NewReplacer("{z}", fmt.Sprint(z), "{x}", fmt.Sprint(x), "{y}", fmt.Sprint(y))
	return r.Replace(template)
}

// fetchString GET 一个 URL 并返回正文字符串。
func (f *Fetcher) fetchString(ctx context.Context, raw string) (string, error) {
	b, err := f.fetchBytes(ctx, raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// fetchBytes GET 一个 URL 并返回正文字节。
func (f *Fetcher) fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	if _, err := url.Parse(raw); err != nil {
		return nil, fmt.Errorf("URL 不合法 %q: %w", raw, err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUA)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)
	}
	return io.ReadAll(resp.Body)
}

// SaveResult 把元数据 / 类别 / 点位 序列化到缓存目录，便于之后离线启动直接读。
func SaveResult(cacheRoot string, r *FetchResult) error {
	dir := filepath.Join(cacheRoot, "manifest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	type compact struct {
		Meta       *MapMeta                  `json:"meta"`
		Categories *CategoryData             `json:"categories"`
		Areas      []AreaItem                `json:"areas"`
		Points     map[string][]PointItem    `json:"points"`
	}
	c := compact{Meta: r.Meta, Categories: r.Categories, Areas: r.Areas, Points: r.Points}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)
}

// LoadResult 从缓存反序列化（与 SaveResult 对应）。
func LoadResult(cacheRoot string) (*FetchResult, error) {
	p := filepath.Join(cacheRoot, "manifest", "manifest.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	type compact struct {
		Meta       *MapMeta                  `json:"meta"`
		Categories *CategoryData             `json:"categories"`
		Areas      []AreaItem                `json:"areas"`
		Points     map[string][]PointItem    `json:"points"`
	}
	var c compact
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &FetchResult{Meta: c.Meta, Categories: c.Categories, Areas: c.Areas, Points: c.Points}, nil
}
