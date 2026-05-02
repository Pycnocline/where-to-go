# 开发文档

面向源码维护与二次开发。读完本文应当对项目目录、构建方式、关键模块的职责和并发模型有完整概念。

## 范围与目标

- **范围**：纯 Go、单文件 Windows 可执行，提供 BWiki《洛克王国：世界》大地图浏览器、路径规划器、和基于屏幕截图的玩家定位与导航。
- **不做的事**：不读写游戏内存、不调用游戏 API、不分发 BWiki 数据（运行时按需抓取）。
- **平台**：仅 Windows。`overlay` / `hotkey` / `tracker` 直接调 Win32 API；其他纯 Go 包跨平台无障碍。

## 目录

```
where-to-go/
├── go.mod / go.sum                 # 主要依赖：gioui.org v0.7.x、x/image、x/sys/windows
├── main.go                         # 入口、CLI flag 解析、子命令分发
├── README.md                       # 用户文档
├── LICENSE
├── docs/
│   ├── DEVELOPMENT.md              # 本文件
│   └── locator-refactor.md         # 定位器演进记录（NCC → 边缘 SSD）
├── internal/
│   ├── wiki/                       # BWiki 抓取与 HTML/JSON 解析
│   ├── mapdata/                    # 内存数据模型 + 坐标变换 + 路径模型
│   ├── tilecache/                  # 瓦片磁盘缓存 + LRU 内存缓存
│   ├── iconcache/                  # 点位图标下载 + paint.ImageOp 缓存
│   ├── pathfind/                   # A* + TSP（K-NN + 2-opt）
│   ├── tracker/                    # Win32 BitBlt 截屏 + 朝向预处理
│   ├── locator/                    # 边缘 SSD 匹配 + 多种子兜底 + 校准
│   ├── overlay/                    # Win32 layered popup（无 GUI 框架依赖）
│   ├── hotkey/                     # RegisterHotKey 全局热键
│   ├── ui/                         # Gio 主界面 + OverlayWindow + Settings + Routes
│   ├── crashlog/                   # panic 捕获、日志重定向、控制台保留
│   └── winutil/                    # 缓存路径解析、字体加载、wstring 等
└── data/                           # 运行时生成（不入库）：路径、calibration.json
```

`cache/`、`data/`、`config.json`、`testMinimapImg/` 都被 `.gitignore` 排除。仓库里只保留源码、文档和 `LICENSE`。

## 构建

```
# 开发态
go run .                         # 启动 GUI（需要联网或本地已有缓存）
go run . -reset                  # 强制重抓
go run . -no-fetch               # 禁止联网（要求本地缓存完整）
go test ./... -count=1           # 跑全部单元测试
go test ./... -race -count=1     # 数据竞争检测

# 发布态：单文件 exe
$env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags "-s -w -H windowsgui" -trimpath -o where-to-go.exe .
```

构建结果只依赖系统自带的 `user32 / gdi32 / kernel32`，无外部 DLL。

### 子命令（`-cmd ...`）

| 子命令 | 用途 |
|---|---|
| `fetch` | 仅执行 BWiki 抓取流程，不开 GUI |
| `verify-cache` | 列出当前缓存内容 |
| `test-recog` | 用 `testMinimapImg/` 跑旧 NCC 识别（保留对照） |
| `test-locator` | 用 `testMinimapImg/` + `answer.txt` 跑边缘 SSD 定位回归 |
| `test-overlay` | 仅打开悬浮窗，验证 Win32 + headless 渲染管线 |
| `test-hotkey` | 注册默认热键并打印事件 |
| `ui-snapshot` | UI 自动截屏自检 |

`test-locator` 当前回归基线是 `testMinimapImg/` 7 张图全部通过（位置误差 ≤ 80 世界单位）。跑完会把扫到的最佳 K 写到 `data/calibration.json`，下次启动 `App.New` 自动加载覆盖默认值。

## 关键技术决策

### GUI：Gio v0.7.x

纯 Go，Windows 默认 D3D11 后端，无 CGO；自带 2D 矢量图形 API。替代方案 Fyne / Walk 都不擅长大量自定义绘制，弃用。

### BWiki 资源抓取

- 元数据：解析 `https://wiki.biligame.com/rocom/大地图` 的 HTML，从内嵌 `<script>mapData = {...}</script>` 与几个 `<div id="...Data">…</div>` 抽 JSON。
- 点位：解析 `/rocom/Data:Mapnew/point.json` 页面中 `<div id="mapPointData">` 的 JSON。
- 瓦片：直链 OSS `wiki-dev-patch-oss.oss-cn-hangzhou.aliyuncs.com`。瓦片有效范围

  ```
  refer = ceil((1 << (z-1)) / 2)
  x ∈ [-refer*x1, refer*x2)，y ∈ [-refer*y1, refer*y2)
  其中 x1 = x2 = y1 = y2 = 4
  ```

- 首次抓取仅 z=4 全图（最低细节）+ 元数据。z=5..8 在视口或 locator 匹配需要时按需抓取。
- 缓存路径：`cache/tiles/{layer}/{z}/{x}_{y}.png`。

### 坐标系

照搬 BWiki 页面的 Leaflet `L.CRS.Simple` + `L.Transformation(0.0078125, 0, 0.0078125, 0)`：

```
worldX = mapX * 0.0078125     # 1/128
worldY = mapY * 0.0078125
tile_pixel(z) = world * 2^(z - 4) * 256
```

`mapdata.WorldToPixel` / `PixelToWorld` / `WorldUnitsPerPixel` 是所有屏幕↔世界转换的唯一入口，禁止在别处复制公式。

### 玩家定位（`internal/locator`）

历史上用过 NCC + 多尺度模板匹配，在大片均匀区（海面 / 草地）会形成"吸引子"假峰。当前实现见 `edge_matcher.go`：

1. `tracker.CaptureScreenRect`：每 600 ms（追踪）/ 400 ms（导航）截一次小地图 ROI。
2. `mask.go::BuildFeatureMask`：按颜色识别玩家箭头、视野锥、UI 描边等，把它们从模板中屏蔽。
3. `edge.go`：Gaussian 模糊 + Sobel 取边缘幅值；`fillMaskedWithMean` 把屏蔽区填均值，避免 Sobel 在 mask 边界产生假边。
4. `MosaicProvider.RenderWithCoverage`：以种子位置为中心从 `tilecache` 拼出 wiki 底图，未覆盖的瓦片由 cov mask 标 0；地图外缘的白色描边由 `maskBoundaryStroke`（距离变换 + 颜色阈值）置 cov=0，避免成为吸引子。
5. `EdgeMatcher.Match`：在 [-radius, +radius] × [-radius, +radius] 滑窗做带 mask 的 SSD，得到最佳偏移与置信度 `sharpness = 1 - bestSSD / p05SSD`。
6. `MatchMultiSeed`：多个候选 seed 各跑一次 Match 取最佳，用于首次定位与失联时的全图兜底。
7. `CalibrateK`：以用户提供的真值位置为中心，扫 K ∈ [0.5, 1.0] step 0.02，取 sharp 最高者。结果写到 `data/calibration.json`。

### 性能优化

主匹配 wrapper 与周期 bg 重定位都做以下优化：

- **截图哈希跳过**（`imgHashFNV64`）：玩家完全静止时连续帧 minimap 字节级一致，hash 命中直接复用上次成功 fix，跳过整个 Match。任何 UI 或位置变化都会改变字节序列，hash 不命中走正常路径。
- **SSD 内核多核并行**：外层 dy 维度按 `GOMAXPROCS` 分块，每个 worker 处理连续一段 dy；合并阶段稳定，输出与串行版字节级一致。8 核机器上 r=200 的 Match 从 ~80 ms 降到 ~20 ms。
- **悬浮窗 BGRA 转换 buffer 复用**：`sync.Pool` 复用 ~200 KB 输出 slice，避免每帧分配。

### 主匹配与后台重定位

```
[locator.Locator]                          [周期 ticker]
  每 400~600 ms                              每 6 s
   ↓                                          ↓
 buildRealMatcher 的 wrapper             requestBgReLocalize
   ↓                                          ↓
 EdgeMatcher.Match (r=60..200, 单帧)      runBgReLocalize
   ↓                                       ├─ Phase 1: MatchMultiSeed
 sharp >= 0.30 ?                          │      r=200/400/600 自适应（按 bgFailStreak）
   ├ yes → OnFix → handleNavFix           │      Downsample=2 + EarlyStopSharp=0.55
   │       UpdateSeed                     │
   │       写 LastPlayer                  ├─ 重新截图 img2
   │                                      │
   └ no  → OnErr → applyNavFallback       └─ Phase 2: 单 seed 全分辨率 verify
           不污染 nav.fix                         成功 → 热替换主 wrapper 的 seed
```

主匹配只跑小半径快速跟踪（r=60..200，~100 ms），bg 独立 goroutine 跑大半径（r=200..600）。两者不互抢 CPU。曾经尝试让 EdgeMatcher 内置 failStreak 在主匹配里扩到 600，结果单帧 Match 1~2 秒堵死整条 loop，已回滚——大半径搜索一律由 bg 承担。

### 路径与导航

- 数据：`mapdata.Path { Name, Layer, Nodes []PathNode { X, Y, Type }, Freeform, Color, Visible }`。
- 生成：`pathfind/astar.go` 在点位 K-NN 图上跑 A*；`pathfind/tsp.go` 用 K-NN + 2-opt。
- 持久化：`internal/ui/routes.go::RouteStore` 写到 `data/routes/<id>.json`。
- 进度：`internal/ui/navigation.go::ProjectOnPathLocked`。每帧把玩家投影限制在 `[lockedSeg, lockedSeg+1]`，T ≥ 0.95 时推进锁。这避免了路径自交 / 平行段处把玩家投影"串"到后续段。首帧用全局 `ProjectOnPath` 给锁一个合理初值；`StopNavigation` / `CalibrateAtWorld` / 切换 session 时都会清零锁。
- 异常移动拦截：`handleNavFix` 用速度 EMA + 6× cap + 80 单位/秒下限做 plausibility，突然大跳变会被降级为 tentative，不污染 LastPlayer，并触发 bg 重定位。

### 渲染（MapView）

- 单一 `MapView.Layout` 入口画背景 / 瓦片 / 点位 / 路径 / 选区 / 导航。
- 视口剔除：所有 draw 函数对屏外项做早返回（pad 32~64 px）。
- 路径样式（导航）：双层管道 13 px 描边 + 9 px 主色芯。已走段灰色，未走段亮绿。每个内部节点画 `jointR=描边/2` 的填充圆做圆滑过渡。流动白色箭头 5 px 嵌在主色芯上，仅未走 + 可见段动起来。
- 平滑：玩家显示坐标对最新 fix 做指数衰减（α=0.18）；朝向用最近角度路径同 α。
- 预测：`Settings.NavPredict=true` 时按速度外推 fix 之间的位置，cap 1.5 秒，仅影响显示不修改 navState。默认关闭，需要在设置里开。

### 悬浮窗

走过几次架构调整后定型为：

```
[winLoop goroutine, LockOSThread]
  overlay.New() → Win32 layered popup
  overlay.Run() → GetMessage → DispatchMessage 直到 WM_DESTROY

[renderLoop goroutine, LockOSThread]
  每 ~120 ms：
    headless.NewWindow(W, H)         # Gio 离屏 D3D11
    layoutOverlay → MapView.LayoutAs(gtx, overlay=true)
    hw.Frame; hw.Screenshot → frameRGBA
    win.Invalidate                    # 通知 WM_PAINT

[WndProc, winLoop 线程]
  WM_PAINT: BeginPaint → onPaint(hdc) → toBGRA + StretchDIBits
  WM_NCCALCSIZE (wparam!=0): return 0   # 抑制 WS_THICKFRAME 顶部白边
  WM_NCHITTEST: 边缘 6 px 走 HT*，其余 HTCAPTION（可拖）
  WM_SIZE: 更新 clientW/H，下次 tick 重建 headless
  WM_DESTROY: 注销 + PostQuitMessage
```

要点：

- 关闭走 `PostClose()`：`DestroyWindow` 不是线程安全的，从外部协程直接调会卡。`PostClose` 投递 WM_CLOSE 让 WndProc 在 winLoop 线程上执行 DestroyWindow。
- MapView 共享：`MapView.layoutMu` 串行化主窗口与悬浮窗 renderLoop 的两条 Layout 调用线，避免 race。
- 文本：`overlayMode=true` 时 MapView 跳过所有 `material.Label`，避免两窗口并发触发 Gio text.Shaper 内部 map 写。

### 全局热键

`RegisterHotKey(NULL, id, MOD_*, vk)` + 自有消息循环捕获 `WM_HOTKEY`。默认绑定见 README。

## Settings

`internal/ui/settings.go`：

- 文件：`<exe 同级目录>/config.json`，由 `SettingsStore` 维护。
- 读：`Get()` 返回值副本（无锁副作用）。
- 写：`Update(func(*Settings))` 在 mu 内修改并立即落盘；`Set(v)` 整体替换。
- `DefaultSettings()` 列出所有默认值；`load()` 做 schema 校验，越界值会回退到默认。

## 并发模型

| 字段 | 锁 | 写者 | 读者 |
|---|---|---|---|
| `App.nav.*` | `nav.mu` | `handleNavFix`（locator 后台）/ `Start*` / `Stop*` / `runBgReLocalize` / `CalibrateAtWorld` | `NavStateFn` snapshot（每帧） |
| `MapView` 内部状态 | `MapView.layoutMu` | Layout（主 + overlay） | Layout |
| `RouteStore` | 内部 mu | UI 主协程 | UI + Layout |
| `iconcache.Cache` | 内部 mu | 下载协程 | Layout |
| `tilecache.Cache` | 内部 mu | 抓取协程 | Layout / locator |
| `SettingsStore` | 内部 RWMutex | 任意 | 任意 |
| `OverlayWindow.frameRGBA` | `frameMu` (RWMutex) | renderLoop | onPaint |
| `OverlayWindow.win` | `OverlayWindow.mu` | winLoop | Close / SetAlpha / SetClickThrough |

关键不变量：**`MapView.Layout` 全程持 `layoutMu`**。这是 "悬浮窗 + 主窗口同时显示路径偶发崩溃" 的根因 —— 没有这把锁两条 goroutine 会并发写 `smoothPX/Y`、`Editor`、`Routes`、`Icons` 等共享字段。

## 测试

| 包 | 主要用例 |
|---|---|
| `internal/overlay` | Win32 窗口创建、WM_PAINT / WM_SIZE 回调、`PostClose` 跨线程关闭、alpha / 穿透切换 |
| `internal/ui` | `OverlayWindow.Start / Close` 状态机 + headless 渲染管线 |
| `internal/locator` | 边缘 SSD 在合成图上的匹配、多种子兜底 |
| `internal/pathfind` | A* 与 TSP 在小图上可解 |

发布前建议：

```
go test ./... -race -count=1 -timeout 120s
go vet ./...
./where-to-go.exe -cmd test-locator   # 7 张样本图 + answer.txt 真值
```

## 演进历史

参见 `docs/locator-refactor.md`：从 NCC + 多尺度到边缘 SSD + 自适应 bg 半径的完整路径，包括失败方案与回滚。

## 贡献注意事项

- 不要在仓库里提交瓦片、点位 JSON、`config.json`、调试截图（`.gitignore` 已排除）。
- 修改 `Settings` 结构时同步更新 `load()` 的 schema 校验和 `DefaultSettings()`。
- 任何对 `MapView` 共享字段的修改都要确认在 `layoutMu` 保护下完成。
- 涉及 `nav.*` 的改动最好通过 `snapshot()` / `snapshotWithVelocity()` 一次性读出再用，避免分次取值导致字段不一致。
