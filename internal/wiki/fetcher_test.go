package wiki

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// TestFetchTileOnDemand_404ShortCircuit 验证当瓦片第一次被服务器返回 404 后，
// 落盘的 .404 marker 在后续调用中会短路 fetchOneTile，避免重复 HTTP 请求。
func TestFetchTileOnDemand_404ShortCircuit(t *testing.T) {
	var calls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	dir := t.TempDir()
	f := NewFetcher(dir, nil)
	ly := Layer{Name: "G", TileURL: ts.URL + "/{z}/{x}/{y}.png", X1: 4, X2: 4, Y1: 4, Y2: 4}

	_, err := f.FetchTileOnDemand(context.Background(), ly, 5, 100, 100)
	if err != ErrTileOutOfBounds {
		t.Fatalf("首次调用应返回 ErrTileOutOfBounds，实际: %v", err)
	}
	if c := atomic.LoadInt64(&calls); c != 1 {
		t.Fatalf("首次应发出 1 次 HTTP，实际 %d", c)
	}
	markerPath := TilePath(dir, ly.Name, 5, 100, 100) + ".404"
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf(".404 marker 应落盘: %v", err)
	}

	_, err = f.FetchTileOnDemand(context.Background(), ly, 5, 100, 100)
	if err != ErrTileOutOfBounds {
		t.Fatalf("第二次调用应仍返回 ErrTileOutOfBounds，实际: %v", err)
	}
	if c := atomic.LoadInt64(&calls); c != 1 {
		t.Fatalf("第二次应被 .404 marker 短路，总请求数应仍为 1，实际 %d", c)
	}
}

// TestFetchTileOnDemand_PngCache 验证下载成功后 .png 落盘；重复调用命中缓存不再请求。
func TestFetchTileOnDemand_PngCache(t *testing.T) {
	var calls int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "PNG-PAYLOAD")
	}))
	defer ts.Close()

	dir := t.TempDir()
	f := NewFetcher(dir, nil)
	ly := Layer{Name: "G", TileURL: ts.URL + "/{z}/{x}/{y}.png", X1: 4, X2: 4, Y1: 4, Y2: 4}

	p1, err := f.FetchTileOnDemand(context.Background(), ly, 5, 1, 2)
	if err != nil {
		t.Fatalf("首次下载失败: %v", err)
	}
	if p1 == "" {
		t.Fatalf("首次应返回非空 path")
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf(".png 未落盘: %v", err)
	}

	p2, err := f.FetchTileOnDemand(context.Background(), ly, 5, 1, 2)
	if err != nil || p2 != p1 {
		t.Fatalf("第二次调用应命中缓存返回同一路径，实际: %v %q", err, p2)
	}
	if c := atomic.LoadInt64(&calls); c != 1 {
		t.Fatalf("命中缓存后不应再发 HTTP，总请求数应为 1，实际 %d", c)
	}
}
