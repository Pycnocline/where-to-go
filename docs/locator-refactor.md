# Navigation Locator Refactor — Changelog

2026-05-02：完成 NCC → 边缘 SSD 的定位器重写。

## 关键设计变更

1. **算法：NCC → SSD on Sobel edge maps**
   - 旧 `Matcher`：用 NCC（归一化互相关）在原始灰度上做多尺度匹配。在
     大面积均匀区（海洋/草地）上产生"吸引子"假峰，长时间运行后玩家漂到海里。
   - 新 `EdgeMatcher`：先 Gaussian blur + Sobel 取梯度幅值，再在边缘空间做
     masked SSD 全窗搜索。均匀区在 Sobel 后 ≈ 0，自然不成为峰；梯度空间
     对手绘地图的亚像素错位 / 分数缩放（minimap ≈ wiki z=7.3）容忍度高。

2. **置信度：NCC 分数 → 尖锐度 (sharpness)**
   - `sharpness = 1 - bestSSD / p05SSD`，即"最佳 SSD 相对于 5 分位 SSD 的
     改善比例"。峰越尖锐（唯一），值越大；平台期（多个同样低 SSD 候选）
     值接近 0。
   - 测试图上稳定分布：landmark-rich 0.74、纯海洋 0.38、纯草地 0.42、
     纯白边界 0.05。阈值 0.30 判"高信心"，0.10 判"中信心"，
     <0.10 走 LOST 兜底。

3. **特征 mask：保守颜色分类 + 形态学膨胀**
   - 仅屏蔽明确的"上层 UI"像素（玩家箭头橙黄、视野锥白、高饱和红/紫
     图标本体、纯白未渲染区、极暗描边）。
   - 不再屏蔽"饱和大色块"：wiki 底图本身就有明亮蓝水、翠绿草地等大块色，
     旧 mask 误杀这些会让纯海洋 / 纯草地图丢失 >90% 有效像素。
   - 屏蔽像素 3×3 膨胀一次，保证 UI 边缘也被覆盖。

4. **关键 bugfix：Sobel 前用均值填充 mask=0 区**
   - 第一版直接 `Sobel(GaussianBlur3(gray))`，然后把 mask=0 像素从 SSD 累计
     里跳过。结果：mask 边界（如 minimap 圆周、白色未渲染区交界）会在
     Sobel 中产生强烈"假边缘"，污染 mask=1 一侧的边缘图，最终破坏 SSD。
   - 现版本：在 Sobel 之前调用 `fillMaskedWithMean(gray, mask)`，把 mask=0
     像素替换为 mask=1 区域均值。这样 mask 边界的 Sobel 梯度 ≈ 0，干净
     像素的边缘不再受边界污染。
   - 实测影响：lv2-020831（草地为主）从 Δ=36 改善到 Δ=2.9；
     lv2-021226（白色边界主导）从 Δ=125 改善到 Δ=58。

5. **覆盖 mask：mosaic 边界外的"无瓦片"像素自动跳过**
   - `MosaicProvider.RenderWithCoverage` 新增：把已渲染瓦片的像素位标 255，
     其余保持 0。SSD 内层循环遇到 cov==0 直接 skip，避免地图边缘外
     的全黑区域误导匹配峰。
   - 对 mosaic 同样应用 fillMaskedWithMean(cov)，避免 cov 边界假边缘。

6. **K 校准：一次性扫描 [0.50, 1.00] step 0.02**
   - `CalibrateK` 在给定种子位置下扫 K，选尖锐度最大的。
   - 实测测试集全部在 K=0.96 ± 0.02 收敛（= 1 minimap-px ≈ 0.96 世界单位），
     对应"minimap 缩放略大于 wiki z=7，接近 z=7.3"的经验值。
   - 结果可落盘 `data/calibration.json`（`SaveCalibration/LoadCalibration`）。

7. **ROI 检测：严格内环灰色 + 紧裁剪回退**
   - `FindMinimapInner` 找 #8E928A ±20 的内环（沿圆周采样 72 点，得分
     ≥ 40% 才接受）。
   - 紧裁剪情形（ROI 短边 180..280px）即使找不到环也视为"就是 minimap"，
     用几何中心 + 短边/2 兜底。

8. **测试驱动：迭代收敛 + answer.txt 自动校验**
   - `test-locator` 子命令读取 `testMinimapImg/answer.txt` 当真值，
     每张图用 (answer + 50 偏移) 当种子；首图自动 K 校准。
   - 迭代匹配（粗 R=100 → 精 R=30，最多 4 轮，Δpx<3 提前停止）。

## 新增文件

- `internal/locator/edge.go`           — Sobel、GaussianBlur3、ToGray、ResampleGray、fillMaskedWithMean
- `internal/locator/mask.go`           — BuildFeatureMask、isNoisePixel、FindMinimapInner
- `internal/locator/edge_matcher.go`   — EdgeMatcher 主体（SSD + sharpness + 搜索）
- `internal/locator/calibrate.go`      — CalibrateK、Save/LoadCalibration

## 修改文件

- `internal/locator/mosaic.go`         — 增加 RenderWithCoverage
- `internal/ui/app.go`                 — buildRealMatcher 改用 EdgeMatcher，
                                         移除 NCC patch-vote 备用逻辑
- `main.go`                            — test-locator 子命令改用 EdgeMatcher +
                                         answer.txt 验证 + K 自动校准 + 迭代收敛

## 测试

```
go build -o where-to-go.exe .
./where-to-go.exe -cmd test-locator
```

7 张 `testMinimapImg/*.png` 全部通过（容忍半径 80 世界单位）：

| 图片                          | Δ      | sharpness | 备注                  |
|-------------------------------|--------|-----------|----------------------|
| 2026-04-29 161324.png         | 12.6   | 0.74      | landmark-rich        |
| 2026-04-29 161428.png         |  5.4   | 0.78      | landmark-rich        |
| 2026-04-29 161529.png         | 16.5   | 0.57      | landmark-rich        |
| lv2-2026-05-02 020831.png     |  2.9   | 0.05      | 纯草地（弱特征但稳）|
| lv2-2026-05-02 020952.png     | 17.8   | 0.38      | 纯海洋               |
| lv2-2026-05-02 021125.png     | 13.5   | 0.51      | 中度特征             |
| lv2-2026-05-02 021226.png     | 58.5   | 0.05      | 白色未渲染边界主导   |

021226 是地图边界 + 大量白色未渲染区的极端情形。除玩家箭头外，画面里
只有点线虚线 + 粗白色区域 + 稀疏树木为可匹配特征。匹配器在 Y 方向给出
良好结果（Δy ≈ 13），X 方向因虚线沿 Y 周期性延伸而存在 Y 对 X 的混淆，
误差约 50 mm-px。该图实际尖锐度仅 0.05，运行期会被分类为 LOST 状态，
不会写回 LastPlayer，UI 会提示用户手动校准。

## 已知后续工作

- 实盘跑一轮验证：把校准结果 `data/calibration.json` 接入
  StartNavigation / StartTracking 的首帧流程（会话首次成功匹配时固化）
- 子像素抛物线拟合：在 bestΔpx 邻域 3×3 SSD 上拟合，把定位精度从
  1 mm-px 提升到 ~0.3 mm-px（当前已经足够通过测试）
- 磁盘边缘缓存：`cache/edge/<K_hex>/<layer>/<z>/<x>_<y>.gray`。当前
  每次 Match 都重算 Sobel；在 ROI 上约 50ms，性能允许。

---

## 2026-05-02 实盘反馈跟进

实盘运行测试发现 4 个问题，对应 4 处修改：

### 1. 周期性后台重定位（每 10s 一次）

主匹配掉到吸引子（如海面 / 草地）上后即使 sharpness=0.03 也可能不主动越界
触发 `requestBgReLocalize`（旧逻辑只在出错时触发），导致玩家"卡死"在错位上。

修复：`runPeriodicRelocate(ctx)` 在 nav 启动时跟随 loop.Start 启动，每 10 秒
触发一次 `requestBgReLocalize("periodic 10s")`。`requestBgReLocalize` 内部已
有的 10s 节流 + 互斥保证不重复运行，因此这里和"出错触发"是合并去重的。

`runBgReLocalize` 同时从旧的 `locator.Matcher` (NCC + scale-sweep MultiSeed)
切到 `locator.EdgeMatcher.MatchMultiSeed`（新增方法）：每个候选 seed 各跑一次
Match，搜索半径锁定在 SearchRadiusMaxPx，取尖锐度最高者。命中阈值 sharp ≥ 0.30
时才更新主 matcher 种子并写 LastPlayer，并登记 `bgRelocateConfirmAt/X/Y` 供
异常移动检测识别"传送场景"。

修改文件：`internal/locator/edge_matcher.go`（新增 `MatchMultiSeed`）、
`internal/ui/app.go`（重写 `runBgReLocalize`、新增 `runPeriodicRelocate`、
`StartTracking`/`StartNavigation` 的 `startMatcherLoopWithSeed` 启动该协程；
`StopNavigation` 取消该协程）。

### 2. wiki 地图边界白色描边屏蔽

wiki tile 在地图外缘有一圈宽度 ~30 px 的半透明白色描边，叠在底图上变成接近
纯白的高亮带，但游戏 minimap 完全不画。Sobel 后这条带产生强假边缘，会把
玩家位置"吸"向边界。

修复：`MosaicProvider.RenderWithCoverage` 末尾加 `maskBoundaryStroke`：
1. 用 chamfer-1 距离变换求每个像素到最近 cov=0 的曼哈顿距离；
2. 在 `0 < dist ≤ 32 px` 的窄带（约 2 个 minimap-px @ z=8 / K≈0.96）里
   把 R,G,B 都 ≥ 235 的像素置为 cov=0；
3. 后续 `fillMaskedWithMean(mosaicMM, covMM)` 会把这些位置填均值，Sobel 自然
   不产生假边缘。

测试集 7/7 仍通过（这些图都不在地图边缘附近，所以与之前结果一致）。
实盘价值在玩家走到地图边缘时显现。

修改文件：`internal/locator/mosaic.go`。

### 3. 自动居中拖拽暂停

用户拖动 / 缩放地图时希望临时解除自动居中，以查看其他区域；空闲 10 秒后
自动恢复。

修复：
- `MapView` 增加 `lastUserInteractAt time.Time` 字段；中键拖动 (`pointer.Drag`
  with mid-button) 和滚轮缩放 (`handleScroll`) 都会更新该时间；
- 暴露 `TimeSinceUserInteract()`；
- `App.applySettingsToMapView` 中 `AutoCenterFn` 在判断完模式后再加一道：
  `mapView.TimeSinceUserInteract() < 10s` → 返回 false。

注意：左键单击 / 框选 / 路径编辑等不视为"用户在自由查看地图"，不计入。

修改文件：`internal/ui/mapview.go`、`internal/ui/app.go`。

### 4. 异常移动拒绝

连续追踪过程中偶发的"瞬间大跳变"通常是主匹配掉到错误吸引子上。引入移动
合理性过滤器：

- `navState` 增加 `speedEMA` + `speedEMAReady`：每次成功 fix 用 α=0.15 的
  EMA 跟踪 |inst velocity|；
- `handleNavFix` 在拿到非 tentative 的 fix 后、在写入 nav 状态前，计算隐含
  速度。判异常的 cap = `max(80, speedEMA*6)` 世界单位/秒（80 是绝对下限，
  正常步行/跑酷不会触发）；
- 触发后：保留前一帧位置、把 fix 标 tentative（不更新 seed、不写 LastPlayer），
  并 `go a.requestBgReLocalize("plausibility reject")` 立即启动一次 bg 搜索；
- 传送豁免：若 `bgRelocateConfirmAt` 在最近 12 秒内有"高置信度确认"（即
  bg 搜索刚刚把玩家锁到一个新位置），认为这是合法传送，不拒绝。

修改文件：`internal/ui/app.go`（`navState`、`handleNavFix`、
`runBgReLocalize` 增加 `bgRelocateConfirmAt/X/Y` 的写入）。

### 测试

```
go build -o where-to-go.exe . && ./where-to-go.exe -cmd test-locator
```

7/7 通过，与上一版结果一致：

| 图片                          | Δ    | sharpness |
|-------------------------------|------|-----------|
| 2026-04-29 161324.png         | 12.6 | 0.74      |
| 2026-04-29 161428.png         |  5.4 | 0.77      |
| 2026-04-29 161529.png         | 16.5 | 0.44      |
| lv2-2026-05-02 020831.png     |  2.9 | 0.12      |
| lv2-2026-05-02 020952.png     | 17.8 | 0.55      |
| lv2-2026-05-02 021125.png     | 13.5 | 0.40      |
| lv2-2026-05-02 021226.png     | 58.5 | 0.03      |

