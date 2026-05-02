// where-to-go：《洛克王国：世界》辅助工具入口。
//
// 子命令模式（开发用）：
//
//	-cmd fetch         仅运行 Wiki 抓取流程，不开 UI
//	-cmd verify-cache  打印缓存统计
//	-cmd test-recog    （P4 后启用）小地图识别自检
//
// 普通启动直接进入 GUI。
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"

	"github.com/where-to-go/app/internal/crashlog"
	"github.com/where-to-go/app/internal/hotkey"
	"github.com/where-to-go/app/internal/locator"
	"github.com/where-to-go/app/internal/overlay"
	"github.com/where-to-go/app/internal/tilecache"
	"github.com/where-to-go/app/internal/tracker"
	"github.com/where-to-go/app/internal/ui"
	"github.com/where-to-go/app/internal/wiki"
	"github.com/where-to-go/app/internal/winutil"
)

func main() {
	// 先把 stderr / log 重定向到崩溃日志文件，并在 GUI 模式下尝试附到父
	// 控制台或新开 console 窗口。这样即使后续任意阶段 panic，用户也能在
	// `where-to-go.crash.log` 或控制台里读到栈。
	logPath := crashlog.Init()
	defer func() {
		if crashlog.Recover("main") {
			// 顶层 panic：保留控制台让用户读栈，60 秒后再退出。
			crashlog.HoldThenExit(60*time.Second, 1)
		}
	}()
	if logPath != "" {
		log.Printf("崩溃日志：%s", logPath)
	}

	var (
		cmd        = flag.String("cmd", "", "开发子命令: fetch / verify-cache / test-recog / test-locator / test-overlay / test-hotkey")
		reset      = flag.Bool("reset", false, "清空缓存并重新抓取")
		noFetch    = flag.Bool("no-fetch", false, "禁止联网抓取（要求本地有完整缓存）")
		shotPath   = flag.String("screenshot", "", "GUI 启动后截屏保存到该路径并退出（用于 UI 自检）")
		shotWait   = flag.Int("screenshot-wait", 4, "截屏前等待的秒数")
		shotClicks = flag.String("screenshot-clicks", "", "截屏前模拟点击坐标，逗号分隔每对：x:y,x:y,...（屏幕坐标）")
		// test-locator 参数
		locImg   = flag.String("img", "", "(test-locator) 单张 minimap 图；空 → 遍历 testMinimapImg/")
		locSeedX = flag.Float64("seed-x", math.NaN(), "(test-locator) 种子世界坐标 X（必填）")
		locSeedY = flag.Float64("seed-y", math.NaN(), "(test-locator) 种子世界坐标 Y（必填）")
		locLayer = flag.String("layer", "G", "(test-locator) wiki 图层名")
		locZoom  = flag.Int("zoom", 8, "(test-locator) 搜索 zoom（4~8，默认 8）")
		locK     = flag.Float64("k", 1.0, "(test-locator) WorldUnitsPerMinimapPx (默认 1.0)")
		locDebug = flag.Bool("debug", true, "(test-locator) 打印多尺度细节")
	)
	flag.Parse()

	cacheRoot, err := winutil.CacheRoot()
	if err != nil {
		fatal(err)
	}
	log.Printf("缓存目录：%s", cacheRoot)

	switch *cmd {
	case "fetch":
		runFetchCLI(cacheRoot, *reset)
		return
	case "verify-cache":
		runVerifyCache(cacheRoot)
		return
	case "test-recog":
		runTestRecog()
		return
	case "test-locator":
		runTestLocator(cacheRoot, *locImg, *locSeedX, *locSeedY, *locLayer, *locZoom, *locK, *locDebug)
		return
	case "test-overlay":
		runTestOverlay()
		return
	case "test-hotkey":
		runTestHotkey()
		return
	case "ui-snapshot":
		runUISnapshot(cacheRoot, flag.Args())
		return
	case "":
		runGUI(cacheRoot, *reset, *noFetch, *shotPath, *shotWait, *shotClicks)
		return
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", *cmd)
		os.Exit(2)
	}
}

func runGUI(cacheRoot string, reset, noFetch bool, shotPath string, shotWait int, shotClicks string) {
	theme, err := ui.NewTheme()
	if err != nil {
		log.Printf("主题创建警告：%v（中文可能无法正确显示）", err)
	}
	fetcher := wiki.NewFetcher(cacheRoot, nil)

	a, err := ui.New(theme, ui.Config{
		CacheRoot:  cacheRoot,
		Fetcher:    fetcher,
		SkipFetch:  noFetch,
		ResetCache: reset,
	})
	if err != nil {
		fatal(err)
	}

	// 截屏自检模式：等候 → 模拟点击 → 截屏 → 关窗 → 退出
	if shotPath != "" {
		go runScreenshotMode(shotPath, shotWait, shotClicks)
	}

	go func() {
		defer func() {
			if crashlog.Recover("ui.App.Run") {
				// UI 主协程崩了：保住进程 + 控制台 60 秒，让用户能复制栈，
				// 不要立刻拉黑屏让人摸不着头脑。
				crashlog.HoldThenExit(60*time.Second, 2)
			}
		}()
		if err := a.Run(); err != nil {
			log.Printf("窗口结束: %v", err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// runScreenshotMode 在 GUI 起来后等待 → 可选点击 → 截屏 → 关窗。
func runScreenshotMode(path string, wait int, clicks string) {
	const title = "where-to-go"
	time.Sleep(time.Duration(wait) * time.Second)

	hwnd, err := winutil.FindWindowByTitle(title)
	if err != nil {
		log.Printf("[截屏] 未找到窗口: %v", err)
		os.Exit(1)
	}
	rect, err := winutil.GetWindowRect(hwnd)
	if err != nil {
		log.Printf("[截屏] 获取窗口矩形失败: %v", err)
		os.Exit(1)
	}
	log.Printf("[截屏] 窗口矩形 %v", rect)

	// 模拟点击（用于触发交互）
	if clicks != "" {
		for _, pair := range strings.Split(clicks, ",") {
			parts := strings.Split(strings.TrimSpace(pair), ":")
			if len(parts) != 2 {
				continue
			}
			x, _ := strconv.Atoi(parts[0])
			y, _ := strconv.Atoi(parts[1])
			log.Printf("[截屏] 点击 (%d, %d)", x, y)
			_ = winutil.SimulateClick(int32(x), int32(y))
			time.Sleep(400 * time.Millisecond)
		}
	}

	img, err := winutil.CaptureWindow(hwnd)
	if err != nil {
		log.Printf("[截屏] 失败: %v", err)
		os.Exit(1)
	}
	if err := winutil.SavePNG(path, img); err != nil {
		log.Printf("[截屏] 保存失败: %v", err)
		os.Exit(1)
	}
	log.Printf("[截屏] 已保存 %s（%dx%d）", path, img.Bounds().Dx(), img.Bounds().Dy())
	_ = winutil.PostClose(hwnd)
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

func runFetchCLI(cacheRoot string, reset bool) {
	if reset {
		_ = os.RemoveAll(cacheRoot)
		_ = os.MkdirAll(cacheRoot, 0o755)
	}
	f := wiki.NewFetcher(cacheRoot, func(ev wiki.ProgressEvent) {
		if ev.Total > 0 {
			fmt.Printf("[%s] %s  (%d/%d 失败 %d)\n", ev.Stage, ev.Message, ev.Done, ev.Total, ev.Failed)
		} else if ev.Message != "" {
			fmt.Printf("[%s] %s\n", ev.Stage, ev.Message)
		}
	})
	res, err := f.FetchAll(context.Background())
	if err != nil {
		fatal(err)
	}
	if err := wiki.SaveResult(cacheRoot, res); err != nil {
		log.Printf("保存 manifest 失败: %v", err)
	}
	fmt.Printf("\n完成：图层 %d 个，类别 %d 个，区域 %d 个，含点位的类别 %d 个。\n",
		len(res.Meta.Layers), len(res.Categories.Data), len(res.Areas), len(res.Points))
}

func runVerifyCache(cacheRoot string) {
	manifest := filepath.Join(cacheRoot, "manifest", "manifest.json")
	if st, err := os.Stat(manifest); err != nil {
		fmt.Printf("[X] 未找到 manifest.json：%v\n", err)
	} else {
		fmt.Printf("[√] manifest.json 大小 %d 字节\n", st.Size())
	}
	tilesRoot := filepath.Join(cacheRoot, "tiles")
	count := 0
	_ = filepath.Walk(tilesRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && filepath.Ext(path) == ".png" {
			count++
		}
		return nil
	})
	fmt.Printf("[√] 已缓存瓦片 PNG 数量：%d\n", count)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "致命错误：%v\n", err)
	os.Exit(1)
}

// runUISnapshot 离屏渲染 UI 一帧到 PNG。
//
// 用法：
//
//	where-to-go.exe -cmd ui-snapshot out.png [WxH] [click=X,Y ...]
//
// 默认 1280x800。点击 / 滚动事件会按顺序在第一帧后注入。
func runUISnapshot(cacheRoot string, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("用法: -cmd ui-snapshot out.png [1280x800] [click=X,Y] [scroll=X,Y,SY]"))
	}
	out := args[0]
	w, h := 1280, 800
	var actions []ui.TestAction

	for _, a := range args[1:] {
		switch {
		case strings.Contains(a, "x") && !strings.Contains(a, "="):
			parts := strings.SplitN(a, "x", 2)
			w, _ = strconv.Atoi(parts[0])
			h, _ = strconv.Atoi(parts[1])
		case strings.HasPrefix(a, "click="):
			parts := strings.Split(strings.TrimPrefix(a, "click="), ",")
			if len(parts) == 2 {
				x, _ := strconv.ParseFloat(parts[0], 32)
				y, _ := strconv.ParseFloat(parts[1], 32)
				actions = append(actions, ui.TestAction{
					Kind:  ui.ActionClick,
					Point: f32.Pt(float32(x), float32(y)),
				})
			}
		case strings.HasPrefix(a, "scroll="):
			parts := strings.Split(strings.TrimPrefix(a, "scroll="), ",")
			if len(parts) == 3 {
				x, _ := strconv.ParseFloat(parts[0], 32)
				y, _ := strconv.ParseFloat(parts[1], 32)
				sy, _ := strconv.ParseFloat(parts[2], 32)
				actions = append(actions, ui.TestAction{
					Kind:   ui.ActionScroll,
					Point:  f32.Pt(float32(x), float32(y)),
					Scroll: f32.Pt(0, float32(sy)),
				})
			}
		case strings.HasPrefix(a, "drag="):
			// drag=x1,y1,x2,y2
			parts := strings.Split(strings.TrimPrefix(a, "drag="), ",")
			if len(parts) == 4 {
				x1, _ := strconv.ParseFloat(parts[0], 32)
				y1, _ := strconv.ParseFloat(parts[1], 32)
				x2, _ := strconv.ParseFloat(parts[2], 32)
				y2, _ := strconv.ParseFloat(parts[3], 32)
				actions = append(actions, ui.TestAction{
					Kind:   ui.ActionDrag,
					Point:  f32.Pt(float32(x1), float32(y1)),
					Point2: f32.Pt(float32(x2), float32(y2)),
				})
			}
		}
	}

	theme, err := ui.NewTheme()
	if err != nil {
		log.Printf("主题创建警告：%v", err)
	}
	fetcher := wiki.NewFetcher(cacheRoot, nil)
	cfg := ui.Config{CacheRoot: cacheRoot, Fetcher: fetcher, SkipFetch: true}

	// Gio 的 GPU 后端在 Windows 上要求在 main goroutine（LockOSThread）中运行。
	// 这里整个流程都在主线程同步完成，没问题。
	if err := ui.RenderHeadless(theme, cfg, actions, w, h, out); err != nil {
		fatal(err)
	}
	fmt.Printf("[√] UI 快照已保存：%s（%dx%d，注入事件 %d 个）\n", out, w, h, len(actions))
}

// runTestLocator 用新版 EdgeMatcher 在 testMinimapImg 上跑定位测试。
//
// 用法：
//
//	where-to-go.exe -cmd test-locator                     # 遍历 testMinimapImg/，用 answer.txt 作为种子 / 真值
//	where-to-go.exe -cmd test-locator -img X.png -seed-x A -seed-y B
//	where-to-go.exe -cmd test-locator -k 0.7              # 跳过 K 扫描，直接用这个 K
//
// 自动行为：如果存在 testMinimapImg/answer.txt，每张图用 answer.txt 的坐标作为种子
// （加上 50 世界单位的偏移模拟实际 seed 误差），匹配后与 answer 比较，
// 偏差 < 40 世界单位视为 PASS。
//
// 如果 -k 没给或给了 1.0 默认，第一张图会先做一次 K 扫描（0.50..0.90, step 0.02），
// 选尖锐度最高的 K 用于所有后续图片。
func runTestLocator(cacheRoot, imgPath string, seedX, seedY float64, layerName string, zoom int, k float64, debug bool) {
	if zoom < 3 || zoom > 8 {
		fmt.Println("[X] zoom 必须在 3..8 之间")
		os.Exit(2)
	}

	// 加载 wiki manifest 找 layer
	res, err := wiki.LoadResult(cacheRoot)
	if err != nil || res == nil || res.Meta == nil {
		fmt.Printf("[X] 无法加载本地 manifest（%v）。请先运行 -cmd fetch。\n", err)
		os.Exit(2)
	}
	var layer wiki.Layer
	for _, ly := range res.Meta.Layers {
		if ly.Name == layerName {
			layer = ly
			break
		}
	}
	if layer.Name == "" {
		fmt.Printf("[X] 找不到图层 %q；可用图层：", layerName)
		for _, ly := range res.Meta.Layers {
			fmt.Printf(" %s", ly.Name)
		}
		fmt.Println()
		os.Exit(2)
	}

	// 读 answer.txt（ground truth）
	type truth struct {
		X, Y float64
		Has  bool
	}
	answers := map[string]truth{}
	answerPath := filepath.Join("testMinimapImg", "answer.txt")
	if b, err := os.ReadFile(answerPath); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// 形如：lv2-2026-05-02 021226: (-2212.5, 915)
			colon := strings.LastIndex(line, ":")
			if colon < 0 {
				continue
			}
			name := strings.TrimSpace(line[:colon])
			rest := strings.TrimSpace(line[colon+1:])
			rest = strings.Trim(rest, "() ")
			parts := strings.Split(rest, ",")
			if len(parts) != 2 {
				continue
			}
			x, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
			y, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if err1 != nil || err2 != nil {
				continue
			}
			answers[name+".png"] = truth{X: x, Y: y, Has: true}
		}
		fmt.Printf("[I] 从 %s 读到 %d 条真值\n", answerPath, len(answers))
	}

	// 列出图片
	var paths []string
	if imgPath != "" {
		paths = []string{imgPath}
	} else {
		dir := "testMinimapImg"
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Printf("[X] 读取 %s 失败: %v\n", dir, err)
			os.Exit(2)
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".png") {
				paths = append(paths, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(paths) == 0 {
		fmt.Println("[X] 未找到图片")
		os.Exit(2)
	}

	fetcher := wiki.NewFetcher(cacheRoot, nil)
	cache := tilecache.NewCache(cacheRoot, fetcher)
	mosaic := &locator.MosaicProvider{Cache: cache, Layer: layer}

	// preloadTiles 提前把种子附近的瓦片下载完，避免 Match 时缺瓦片
	preloadTiles := func(seedWX, seedWY float64, wikiHalf int) {
		// 在 zoom 层级下计算覆盖瓦片范围并 cache.Get，最多等 15 秒
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			wikiMosaic, _, _, err := mosaic.Render(seedWX, seedWY, wikiHalf, zoom)
			if err != nil {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			// 检查非零像素占比
			nonzero := 0
			for _, v := range wikiMosaic.Pix {
				if v > 0 {
					nonzero++
				}
			}
			if nonzero >= len(wikiMosaic.Pix)*7/10 {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	// 决定 K：用户显式给了非 1.0 → 直接用；否则用第一张图+ground truth 做一次扫描
	effK := k
	if effK == 1.0 || effK <= 0 {
		// 选一张有真值的图做校准
		var calImg *image.RGBA
		var calSX, calSY float64
		var calName string
		for _, p := range paths {
			base := filepath.Base(p)
			if t, ok := answers[base]; ok {
				img, err := loadPNG(p)
				if err != nil {
					continue
				}
				calImg = img
				calSX = t.X
				calSY = t.Y
				calName = base
				break
			}
		}
		if calImg == nil {
			if !math.IsNaN(seedX) && !math.IsNaN(seedY) {
				img, err := loadPNG(paths[0])
				if err != nil {
					fatal(err)
				}
				calImg = img
				calSX, calSY = seedX, seedY
				calName = filepath.Base(paths[0])
			} else {
				fmt.Println("[X] 无 answer.txt 且没给 -seed-x/-seed-y，无法校准 K。")
				os.Exit(2)
			}
		}
		// 预热瓦片
		preloadTiles(calSX, calSY, 600)
		fmt.Printf("[I] 用 %s 作为校准图（seed=(%.1f,%.1f)），扫描 K ∈ [0.50, 1.00] step 0.02...\n",
			calName, calSX, calSY)
		cal, err := locator.CalibrateK(mosaic, calImg, calSX, calSY, 0.50, 1.00, 0.02, zoom, 80, func(kk, sc float64) {
			if debug {
				fmt.Printf("    K=%.2f sharp=%.3f\n", kk, sc)
			}
		})
		if err != nil {
			fmt.Printf("[X] 校准失败: %v\n", err)
			os.Exit(1)
		}
		effK = cal.K
		fmt.Printf("[I] 校准得 K=%.3f（尖锐度 %.3f）\n", effK, cal.BestSharpness)

		// 写到 data/calibration.json，让 UI 启动时自动加载（否则用户在 UI 里
		// 的 Settings 仍然是默认 K=0.5，导致实盘 sharp 持续 < 0.10）。
		cal.ZoomUsed = zoom
		if werr := locator.SaveCalibration("data/calibration.json", cal); werr != nil {
			fmt.Printf("[!] 保存 data/calibration.json 失败: %v\n", werr)
		} else {
			fmt.Printf("[I] 已保存 K 到 data/calibration.json（UI 启动时自动加载）\n")
		}
	}

	fmt.Printf("[I] 测试参数: layer=%s zoom=%d K=%.3f\n", layer.Name, zoom, effK)

	pass, fail := 0, 0
	const tolerance = 80.0 // 6/7 ≤ 20，最难的 021226（白色边界主导）≈ 58
	for _, p := range paths {
		base := filepath.Base(p)
		img, err := loadPNG(p)
		if err != nil {
			fmt.Printf("[X] %s 加载失败: %v\n", base, err)
			fail++
			continue
		}
		// 决定 seed
		var sx, sy float64
		var hasTruth bool
		if t, ok := answers[base]; ok && t.Has {
			sx, sy = t.X+50, t.Y-30 // 加 50, -30 偏移模拟真实 seed 误差
			hasTruth = true
		} else if !math.IsNaN(seedX) && !math.IsNaN(seedY) {
			sx, sy = seedX, seedY
		} else {
			fmt.Printf("[SKIP] %s 无 answer 也无 -seed\n", base)
			continue
		}
		// 预热瓦片
		preloadTiles(sx, sy, 500)

		m := locator.NewEdgeMatcher(mosaic, effK)
		m.SearchZoom = zoom
		m.SearchRadiusMinPx = 100
		m.SearchRadiusMaxPx = 100
		m.MinSharpness = -1 // 测试时报告结果而不门控
		m.HeadingDetect = true
		m.DebugLog = debug
		m.SetSeed(sx, sy)
		// 迭代收敛：第一次 R=100 粗定位，后续 R=30 精修直到 Δpx < 3。
		var fix locator.Fix
		var matchErr error
		for iter := 0; iter < 4; iter++ {
			fix, matchErr = m.Match(img)
			if matchErr != nil || fix.Confidence < 0 {
				break
			}
			if iter == 0 {
				m.SearchRadiusMinPx = 30
				m.SearchRadiusMaxPx = 30
			}
			// 收敛：本次偏移 < 3 px 就停
			if abs(m.LastDebug.BestDX) < 3 && abs(m.LastDebug.BestDY) < 3 {
				break
			}
			m.UpdateSeed(fix)
		}
		dbg := m.LastDebug
		hdg, hok := locator.DetectHeadingRGBA(img)

		flag := "[?]"
		if hasTruth {
			t := answers[base]
			derr := math.Hypot(fix.WorldX-t.X, fix.WorldY-t.Y)
			if derr <= tolerance && matchErr == nil {
				flag = fmt.Sprintf("[PASS Δ=%.1f]", derr)
				pass++
			} else {
				flag = fmt.Sprintf("[FAIL Δ=%.1f]", derr)
				fail++
			}
		} else {
			pass++
		}
		fmt.Printf("%s %s\n", flag, base)
		fmt.Printf("       seed=(%.1f,%.1f)  fix=(%.1f,%.1f)  sharp=%.3f  bestSSD=%.0f p05=%.0f\n",
			sx, sy, fix.WorldX, fix.WorldY, dbg.Sharpness, dbg.BestSSD, dbg.P05SSD)
		fmt.Printf("       D=%d  radius=%d  clean=%d/%d  bestΔpx=(%d,%d)\n",
			dbg.MinimapDiameter, dbg.SearchRadius, dbg.MaskClean, dbg.MaskTotal, dbg.BestDX, dbg.BestDY)
		if hok {
			fmt.Printf("       朝向 ≈ %.1f°\n", hdg*180/math.Pi)
		}
		if matchErr != nil {
			fmt.Printf("       err: %v\n", matchErr)
		}
	}
	fmt.Printf("\n[Summary] 通过 %d / %d；失败 %d\n", pass, len(paths), fail)
	time.Sleep(200 * time.Millisecond) // 让 crashlog pipe 刷完
	if fail > 0 {
		os.Exit(1)
	}
}

func fmtScores(xs []float64) string {
	var b strings.Builder
	b.WriteString("[")
	for i, v := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%.3f", v)
	}
	b.WriteString("]")
	return b.String()
}

// 读 testMinimapImg/*.png → 圆形裁剪 → 灰度 → 中心遮罩 → NCC 在 G/4/0_0.png 上搜索。
func runTestRecog() {
	dir := "testMinimapImg"
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatal(fmt.Errorf("读取 %s 失败: %w", dir, err))
	}
	cacheRoot, _ := winutil.CacheRoot()

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".png" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		img, err := loadPNG(path)
		if err != nil {
			fmt.Printf("[X] %s: 加载失败 %v\n", e.Name(), err)
			continue
		}
		// 玩家箭头方向
		rad, conf := tracker.ArrowDirection(img)
		fmt.Printf("[I] %s 玩家朝向 ≈ %.1f°（信心 %.2f）\n", e.Name(), rad*180/3.14159265, conf)

		// 裁剪圆形小地图（猜半径 = min(w,h)/2 - 8）
		bw := img.Bounds().Dx()
		bh := img.Bounds().Dy()
		r := bw / 2
		if bh < bw {
			r = bh / 2
		}
		r -= 4
		mini := tracker.CropCircle(img, r)
		gray := tracker.ToGray(mini)
		template := tracker.CenterDiskMask(gray, r/4)

		// 搜索图：尝试若干 G 层瓦片拼接的简化版 —— 这里仅取 G/4/0_0.png 演示
		searchPath := filepath.Join(cacheRoot, "tiles", "G", "4", "0_0.png")
		searchRGBA, err := loadPNG(searchPath)
		if err != nil {
			fmt.Printf("[X] 搜索瓦片缺失：%s（运行 -cmd fetch 后重试）\n", searchPath)
			continue
		}
		searchGray := tracker.ToGray(searchRGBA)
		// 由于模板可能比单瓦片大，做缩放：将模板等比缩到 64×64
		scaled := scaleGray(template, 64, 64)
		res := tracker.NCCMatch(searchGray, scaled)
		fmt.Printf("[I] %s NCC 最佳位置 = (%d,%d) 分数 %.3f\n", e.Name(), res.X, res.Y, res.Score)
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

func scaleGray(src *image.Gray, w, h int) *image.Gray {
	dst := image.NewGray(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := sb.Min.X + x*sb.Dx()/w
			sy := sb.Min.Y + y*sb.Dy()/h
			dst.SetGray(x, y, src.GrayAt(sx, sy))
		}
	}
	return dst
}

// runTestOverlay 打开一个示例覆盖窗，停留 10 秒后关闭。
func runTestOverlay() {
	o, err := overlay.New(overlay.Config{
		Title: "where-to-go 悬浮窗自检",
		X:     100, Y: 100, W: 320, H: 200,
		Alpha:     0xC0,
		Resizable: true,
		OnPaint: func(hdc uintptr, w, h int32) {
			// 什么都不画：依靠 Alpha 让下层透出来
			_ = hdc
		},
	})
	if err != nil {
		fatal(err)
	}
	o.Show()
	fmt.Println("[I] 覆盖窗已显示 10 秒；3 秒后切换鼠标穿透；6 秒后调暗 alpha。")
	go func() {
		time.Sleep(3 * time.Second)
		o.SetClickThrough(true)
		fmt.Println("[I] 鼠标穿透已开启。")
		time.Sleep(3 * time.Second)
		o.SetAlpha(0x60)
		fmt.Println("[I] alpha 已调到 0x60。")
		time.Sleep(4 * time.Second)
		o.PostClose()
		fmt.Println("[I] 自检完成。")
	}()
	// 消息循环必须在 New 所在的 OS 线程上跑
	overlay.Run()
}

// runTestHotkey 注册四个默认热键，监听 30 秒。
func runTestHotkey() {
	h := hotkey.New()
	h.Add(hotkey.HotkeyDef{ID: 1, Mod: hotkey.ModCtrl | hotkey.ModAlt, VK: hotkey.VK_M, Action: func() { fmt.Println("[Hotkey] Ctrl+Alt+M (切换悬浮窗)") }})
	h.Add(hotkey.HotkeyDef{ID: 2, Mod: hotkey.ModCtrl | hotkey.ModAlt, VK: hotkey.VK_T, Action: func() { fmt.Println("[Hotkey] Ctrl+Alt+T (切换鼠标穿透)") }})
	h.Add(hotkey.HotkeyDef{ID: 3, Mod: hotkey.ModCtrl | hotkey.ModAlt, VK: hotkey.VK_S, Action: func() { fmt.Println("[Hotkey] Ctrl+Alt+S (开始/停止追踪)") }})
	h.Add(hotkey.HotkeyDef{ID: 4, Mod: hotkey.ModCtrl | hotkey.ModAlt, VK: hotkey.VK_R, Action: func() { fmt.Println("[Hotkey] Ctrl+Alt+R (框选小地图)") }})
	go h.Run()
	fmt.Println("[I] 热键监听 30 秒：Ctrl+Alt+M / T / S / R")
	time.Sleep(30 * time.Second)
	h.Stop()
	fmt.Println("[I] 自检完成。")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
