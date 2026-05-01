# where-to-go 开发文档

> 《洛克王国：世界》辅助工具 —— 内部架构与开发指南。
> 适用对象：本项目维护者 / 后续迭代的 AI / 想读源码的好奇用户。

## 一、项目目标

构建一个 **纯 Go、单文件 Windows 可执行程序** 的游戏辅助工具：

1. 完整大地图查看 / 编辑器，对齐 https://wiki.biligame.com/rocom/大地图 上的 Leaflet 工具。
2. 资源（瓦片 + 点位 JSON）**不打包进二进制**，首次启动从 Wiki 抓取并缓存到 `<exe 同级目录>/cache/`。
3. 支持手绘 / 链接 / TSP 多种路径生成方式。
4. 屏幕小地图识别：每 ~600ms 截图 + NCC 匹配出玩家世界坐标 + 朝向。
5. 路径导航：进度感知的双色管道线 + 流动箭头 + 节点圆形过渡。
6. 透明置顶悬浮窗 + 全局热键 + 鼠标穿透切换；悬浮窗与主窗口共享 MapView 状态。

## 二、目录结构

```
where-to-go/
├── go.mod / go.sum                 # 依赖：gioui.org v0.7.1 + x/image + x/sys
├── main.go                         # 入口、命令行参数、内部子命令分发
├── README.md                       # 用户文档
├── config.json                     # 运行期配置（Settings 持久化）
├── docs/DEVELOPMENT.md             # 本文件
├── data/routes/*.json              # 用户保存的路径
├── cache/                          # Wiki 抓取缓存（瓦片 / 点位 / 元数据）
├── testMinimapImg/                 # P4 单元测试输入
├── where-to-go.exe                 # 发布产物
└── internal/
    ├── wiki/                       # Wiki 资源抓取 + HTML/JSON 解析
    ├── mapdata/                    # 内存数据模型 + 坐标变换 + 路径模型
    ├── tilecache/                  # 瓦片磁盘缓存 + LRU 内存解码缓存
    ├── iconcache/                  # 点位图标下载 + 内存缓存（paint.ImageOp）
    ├── pathfind/                   # A* 最短路 + TSP（K-NN + 2-opt）
    ├── tracker/                    # Win32 BitBlt 截屏 + 朝向预处理
    ├── locator/                    # NCC 模板匹配 + 多种子兜底 + 模拟器
    ├── overlay/                    # Win32 layered 悬浮窗（无 GUI 框架依赖）
    ├── hotkey/                     # RegisterHotKey 全局热键
    ├── screenarea/                 # 屏幕框选（已合并进 ROI 选择 UI）
    ├── ui/                         # Gio 主界面 + 悬浮窗内容
    └── winutil/                    # 通用：缓存路径 / 字体加载 / wstring
```

## 三、关键技术决策

### 3.1 GUI 框架 — Gio v0.7.1

- **理由**：纯 Go，Windows 用 D3D11 后端，无 CGO；自带高性能 2D 矢量绘制；编译产物为单 exe。
- **替代方案权衡**：Fyne 需要 OpenGL；Walk 不擅长 canvas 绘制；都不及 Gio 适合本项目。

### 3.2 Wiki 资源抓取

- **元数据**：解析 `https://wiki.biligame.com/rocom/大地图` 的 HTML，从内嵌 `<script>mapData = {...}</script>` 与 `<div id="categoryData|textLayerData|mapAreaData">…</div>` 抽 JSON。
- **点位**：从 `/rocom/Data:Mapnew/point.json` 抽 `<div id="mapPointData">…</div>`。
- **瓦片**：直链 OSS。有效范围公式：

  ```
  refer = ceil((1 << (z-1)) / 2)
  有效 x ∈ [-refer*x1, refer*x2)，y ∈ [-refer*y1, refer*y2)
  其中 x1 = x2 = y1 = y2 = 4
  ```

- **首次抓取**：仅 z=4 全部 + 元数据。z=5..8 在视口需要时按需抓取。
- **缓存路径**：`cache/tiles/{layer}/{z}/{x}_{y}.png`。

### 3.3 坐标系

照搬 Leaflet 的 `L.CRS.Simple` + `L.Transformation(0.0078125, 0, 0.0078125, 0)`：

```
worldX = mapX * 0.0078125     // 1/128
worldY = mapY * 0.0078125
tile_pixel(z) = world * 2^(z - 4) * 256
```

`mapdata.WorldToPixel` / `PixelToWorld` / `WorldUnitsPerPixel` 是所有屏幕↔世界转换的唯一入口。

### 3.4 小地图识别 (`internal/locator`)

1. **采样**：`tracker.CaptureScreenRect` 每 600ms（追踪）/ 400ms（导航）截屏指定 `image.Rectangle`。
2. **预处理**：圆形遮罩 + 灰度 + 边缘抑制。
3. **匹配**：NCC（归一化互相关）。多分辨率金字塔 + 多种子（路径节点 / 上次 fix / 视图中心 / 全图扫描）。`match_patch.go` 用积分图加速；`match_multiseed.go` 在置信度低时跑兜底搜索。
4. **朝向**：箭头颜色阈值化 → PCA 主方向 → 角度（弧度，0=北，顺时针为正）。
5. **模拟器** (`simulator.go`)：`NavSimulator=true` 时用合成路径生成 fix，便于无游戏环境调试视觉。

输出统一为 `locator.Fix{ WorldX, WorldY, Heading, HasHeading, Confidence }`，`locator.OnFix` 把每次成功的匹配回调到 `App.handleNavFix`。

### 3.5 路径与导航

- **数据**：`mapdata.Path` { Name, Layer, Nodes []PathNode { X, Y, Type }, Freeform, Color, Visible }。
- **生成**：手动链接 → `pathfind/astar.go` 在点位 K-NN 图上跑 A\*；TSP → `pathfind/tsp.go` 用 K-NN 减少候选 + 2-opt。
- **存储**：`internal/ui/routes.go` 的 `RouteStore` 持久化到 `data/routes/*.json`。
- **导航状态**：`App.nav` (`navState`) 保护字段：active / routeID / fix / hasFix / lost / progress / velX,velY / lastFixAt。所有写都在 `a.nav.mu` 锁内完成；`snapshotWithVelocity()` 一次性读取避免分次取值导致的不一致。
- **进度计算**：每次 fix 时把玩家点投影到当前路径，记录 `SegmentIndex` + 投影点 (ProjX,ProjY)，用于 drawNavRoute 切分 "已走 / 未走"。
- **统一匹配管线**：`startMatcherLoopAsync` 是追踪 / 导航共用入口，仅匹配频率不同 + 是否把路径节点塞进种子表不同。`StartTracking` / `StartNavigation` 都委托它，避免逻辑分叉。

### 3.6 渲染（MapView）

- 单一 `MapView.Layout` 入口画背景 / 瓦片 / 点位 / 路径 / 选区 / 框选 / 导航。
- **视口剔除**：所有点位绘制都跳过 `< -64 || > size+64` 的项；`drawPathLine` 跳过两端在同一外侧的段、节点编号、箭头；`drawNavRoute` 同样剔除整段不可见的段。
- **路径样式（导航）**：双层 "管道" — 13px 描边 + 9px 主色芯。已走段灰色（深灰描边 + 浅灰芯），未走段亮绿。每个内部节点画一个 jointR=描边/2 的填充圆，把折线尖角填成圆滑过渡，避免视觉断裂。流动白色箭头 5px 嵌在主色芯之上，仅在未走 + 可见段动起来。
- **平滑插值**：玩家显示坐标对最新 fix 做指数衰减 (α=0.18)；朝向用最近角度路径 (α=0.18)。
- **预测**：`Settings.NavPredict=true` 时，drawPathOverlay 在 `|v|>2 && dt<=1.5s` 范围按速度外推 playerX/Y，仅影响显示，不修改 navState。
- **MapView 与悬浮窗共享状态**：`OverlayWindow` 直接调用 `o.app.mapView.Layout(gtx)`，与主窗口共享 centerX/Y、layerName、smoothPX/Y、Editor、Routes、Icons 等。`MapView.layoutMu` 串行化两条 Layout 调用线，**避免主窗口与悬浮窗 renderLoop 同时进入造成的 race / 偶发崩溃**。

### 3.7 悬浮窗架构 (`internal/overlay` + `internal/ui/overlay_window.go`)

之前尝试过的失败方案：

- **方案 A**：开第二个 Gio `app.Window` —— 在某些显卡上 D3D11 + WS_EX_LAYERED 黑屏 / 不可见。
- **方案 B**：纯 GDI 直绘 —— 重复造轮子且没有矢量裁剪。
- **方案 C**：Gio 镜像绘制 —— 与 A 同病。

最终方案（已稳定 + 测试覆盖）：

```
[winLoop goroutine, LockOSThread]
  overlay.New() → Win32 layered popup（WS_EX_LAYERED|TOPMOST|TOOLWINDOW，
                  WS_POPUP|WS_THICKFRAME，自定义 WndProc）
  overlay.Run() → GetMessage / TranslateMessage / DispatchMessage 直到 WM_DESTROY

[renderLoop goroutine, LockOSThread]
  每 100ms：
    headless.NewWindow(W, H)  ← Gio 离屏 D3D11 渲染
    layoutOverlay(gtx) → mapView.Layout(gtx)  ← 与主窗口同一 MapView
    hw.Frame(ops); hw.Screenshot(img)
    frameRGBA = img
    win.Invalidate() → 通知 WM_PAINT

[WndProc, winLoop 线程]
  WM_PAINT: BeginPaint → onPaint(hdc, w, h)
            → toBGRA(frameRGBA) → StretchDIBits(top-down DIB) → EndPaint
  WM_NCCALCSIZE (wparam!=0): return 0  → 抑制 WS_THICKFRAME 默认 NC 边框
                                         （即"顶部白边"）
  WM_NCHITTEST: 边缘 6px 走 HTLEFT/RIGHT/TOP/...，其余走 HTCAPTION
  WM_SIZE: 通过 onSize 更新 clientW/H，renderLoop 下次 tick 重建 headless
  WM_DESTROY: 注销 + PostQuitMessage
```

要点：

- **PostClose() 跨线程关闭**：DestroyWindow 不是线程安全的，旧版本从外部协程直接调它会卡住。`PostClose()` 投递 WM_CLOSE，由 WndProc 走 DefWindowProc → DestroyWindow（在 winLoop 线程上执行）。
- **多协程访问 MapView**：靠 `MapView.layoutMu` 串行化（fix 1 of this iteration）。
- **顶部白边**：靠 WM_NCCALCSIZE 抑制 + 创建后立即 SetWindowPos(SWP_FRAMECHANGED) 应用一次（fix 2 of this iteration）。

### 3.8 全局热键 (`internal/hotkey`)

`RegisterHotKey(NULL, id, MOD_*, vk)` + 消息循环里捕获 `WM_HOTKEY`。默认绑定（可在 UI 改）：

- `Ctrl+Alt+M`：切换悬浮窗显示
- `Ctrl+Alt+T`：切换悬浮窗鼠标穿透
- `Ctrl+Alt+S`：开始/停止屏幕追踪
- `Ctrl+Alt+R`：框选小地图区域

## 四、构建与运行

### 4.1 开发态

```powershell
go run .
go run . -reset      # 强制重抓 Wiki 数据
go run . -no-fetch   # 跳过抓取（仅当本地缓存已就绪）
go test ./...        # 单元测试（含 overlay 窗口创建 / locator NCC / pathfind TSP）
go test ./... -race  # 数据竞争检测
```

### 4.2 发布态（单 exe）

```powershell
$env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags "-s -w -H windowsgui" -trimpath -o where-to-go.exe .
```

产出物 `where-to-go.exe` 不依赖任何外部 DLL（只依赖系统自带的 user32 / gdi32 / kernel32）。

### 4.3 内部子命令（开发自检）

```text
-cmd fetch          仅执行 Wiki 抓取流程（无 GUI）
-cmd verify-cache   打印当前缓存内容
-cmd test-recog     用 testMinimapImg 跑一遍小地图识别
-cmd test-overlay   仅打开悬浮窗供调试（验证 Win32 + headless 管线）
-cmd test-hotkey    仅注册热键并打印事件
```

## 五、Settings & 持久化

- 文件：`<exe 同级目录>/config.json`，由 `internal/ui/settings.go::SettingsStore` 维护。
- 字段（`Settings` 结构体）：

  | 字段 | 含义 | 默认 |
  |---|---|---|
  | MarkerStyle | 点位渲染：bubble / icon / dot | icon |
  | IconSize | 图标尺寸 px | 24 |
  | SelectionStyle | 选中点高亮：halo / dot | halo |
  | DebugLog | 把 drawPoints / 匹配统计打到 stderr | false |
  | MinimapROI | 小地图屏幕区域 | 空 |
  | NavSimulator | 用模拟器代替真截屏 | true |
  | NavTracking | 启动后自动恢复追踪 | false |
  | WorldUnitsPerMinimapPx | 校准：mm-px → 世界单位 | 0.5 |
  | NavCenterMode | off / always / navonly | always |
  | NavFallback | 定位失败兜底：stay / last / lost | stay |
  | NavSearchZoom | NCC 搜索 wiki zoom | 8 |
  | NavPredict | 速度外推开关 | true |
  | OverlayAlpha | 悬浮窗 alpha (0..255) | 220 |
  | OverlayClickThrough | 鼠标穿透 | false |
  | LastPlayer\* | 最近一次定位坐标，重启恢复种子 | 0 |

- 读：`store.Get()` 返回副本（值类型）。
- 写：`store.Update(func(*Settings))` 在 mu 内修改并立即落盘；`store.Set(v)` 整体替换。

## 六、并发模型与锁

| 字段 | 锁 | 写者 | 读者 |
|---|---|---|---|
| `App.nav.*` | `nav.mu` | `handleNavFix` (locator 后台) / `StartTracking` / `StartNavigation` / `Stop*` | `NavStateFn` snapshot（每帧） |
| `MapView` 内部状态（smooth* / center* / Editor 引用 …） | `MapView.layoutMu` | Layout 自己 | Layout 自己（主窗口 + 悬浮窗 renderLoop） |
| `RouteStore` | 内部 mu | UI 主协程 | UI 主协程 + Layout |
| `iconcache.Cache` | 内部 mu | 下载协程 | Layout |
| `tilecache.Cache` | 内部 mu | 抓取协程 | Layout |
| `SettingsStore` | 内部 mu | 任意 | 任意 |
| `OverlayWindow.frameRGBA` | `frameMu` (RWMutex) | renderLoop | onPaint |
| `OverlayWindow.win` | `OverlayWindow.mu` | winLoop start/exit | Close / SetAlpha / SetClickThrough |

新加的关键不变量：**`MapView.Layout` 全程持 `layoutMu`**。这是修复 "悬浮窗打开后开启路径显示偶发崩溃" 的根因 —— 主 Gio 协程与悬浮窗 renderLoop 并发进入 Layout 会在 smooth* / Editor / Routes / Icons 上 race。

## 七、性能要点

- 视口剔除：`drawPoints`、`drawPathLine`、`drawNavRoute`、`drawSelectionRings` 都对屏幕外项做 `inView` 早返回，pad ≈ 32~64 px。
- `drawPathLine` 节点 > 200 时短段 (< 12 屏幕像素) 跳过箭头与编号；当前默认是任意长度都做基础剔除。
- 渲染线宽通过仿射变换 + 填充矩形（`drawLine`），避免某些 D3D 后端 `clip.Stroke` 的开销。
- `MarkerStyle=icon` + `iconcache` 复用 `paint.ImageOp`，每帧不重新解码。
- 悬浮窗渲染是 100ms 一帧，主窗口受 Gio 自身节流（事件驱动 + 显式 InvalidateCmd）。

## 八、测试

| 包 | 测试 | 覆盖 |
|---|---|---|
| `internal/overlay` | TestCreateShowDestroy / TestAlphaAndClickThrough | Win32 窗口创建、WM_PAINT / WM_SIZE 回调、PostClose 跨线程关闭 |
| `internal/ui` | TestOverlayWindowLifecycle | OverlayWindow.Start / Close 状态机 + headless 渲染管线 |
| `internal/locator` | TestNCCBasic / TestPatchVote / TestMultiSeed | NCC 在合成图上的匹配 + 兜底逻辑 |
| `internal/pathfind` | TestAStar / TestTSP | 最短路 + TSP 可解 |

发布前建议：

```powershell
go test ./... -race -count=1 -timeout 120s
go vet ./...
```

## 九、最近变更（2026-05）

- `MapView.layoutMu`：串行化 Layout，修复并发崩溃。
- `WM_NCCALCSIZE`：抑制 WS_THICKFRAME 在悬浮窗顶部留下的白边。
- `drawNavRoute`：已走 / 未走线宽统一 (双层 13/9)，新增节点 joint 圆形过渡，所有段做视口剔除。
- `drawPathLine`：默认对任意长度路径做视口剔除（不再仅 > 200 节点才剔）。
- `Settings.NavPredict` + 速度外推：在两次 NCC 匹配之间按 `vel * dt` 外推显示位置 (cap 1.5s)。
- `startMatcherLoopAsync`：把追踪 / 导航的匹配管线统一到一个函数，仅频率与种子注入不同。

## 十、目录结构概览（与代码同步）

```
where-to-go/
├── main.go                      # 命令行 + UI 启动 + 抓取调度
├── internal/wiki/               # 抓取与解析（parser / fetcher / types）
├── internal/mapdata/            # 坐标变换 + 数据仓库 + 路径模型
├── internal/tilecache/          # 瓦片磁盘 + LRU 内存缓存
├── internal/iconcache/          # 图标下载 + paint.ImageOp 缓存
├── internal/pathfind/           # A* + TSP（含单测）
├── internal/tracker/            # capture.go (BitBlt) + recognize.go (预处理)
├── internal/locator/            # NCC + 朝向 + 多种子 + simulator
├── internal/overlay/            # Win32 layered window + WndProc（独立可测）
├── internal/hotkey/             # 全局热键
├── internal/screenarea/         # 屏幕框选（已合入 ROISelectView）
├── internal/ui/                 # Gio 主界面 + OverlayWindow + Settings + Routes …
└── internal/winutil/            # 通用：缓存路径 / 字体加载 / wstring
```
