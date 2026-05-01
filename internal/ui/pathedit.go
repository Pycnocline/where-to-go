package ui

import (
	"fmt"
	"image/color"
	"math"

	"github.com/where-to-go/app/internal/mapdata"
	"github.com/where-to-go/app/internal/pathfind"
)

// Tool 当前工具。鼠标在 MapView 上的左键行为完全由工具决定；
// 中键拖拽（平移地图）和滚轮缩放在所有工具下都生效，
// 右键在所有工具下都唤出"功能列表"上下文菜单。
type Tool int

const (
	ToolPan      Tool = iota // 默认：左键单击 = 选中 / 框选；左键拖拽 = 框选
	ToolPlace                // 加点：左键单击 = 在该处放置 user 路径点
	ToolFreedraw             // 画笔：左键拖拽 = 自由绘制连续路径
	ToolChain                // 连接：依次单击点位 / 空白 → 串联成路径
)

// ToolMarquee 兼容旧代码：等同 ToolPan（默认工具已经统一处理框选）。
const ToolMarquee = ToolPan

// EndpointRole 选区中某个节点的端点角色。
type EndpointRole int

const (
	RoleNone  EndpointRole = iota
	RoleStart              // 起点（绿色环）
	RoleEnd                // 终点（红色环）
)

// SelectionItem 选区里的一项。"wiki" 来源用 PointID 唯一标识，
// "user" 来源 PointID 为 "" 且按 (X,Y) 在容器内唯一。
type SelectionItem struct {
	Source  string  // "wiki" / "user"
	PointID string  // 仅 wiki
	X, Y    float64 // 世界坐标
	Layer   string
	MarkType string // 仅 wiki，方便回查图标 / 颜色
	Role    EndpointRole
	Note    string // 仅 user：用户备注
}

// PathEditor 路径与选区的核心可变状态。
//
// 不绑定任何 Gio 控件 / 事件源 —— 由 MapView 和工具栏在事件回调中调用方法，
// 由 MapView 在 Layout 内调用渲染。
type PathEditor struct {
	tool Tool

	selection []SelectionItem

	// 正在构造的草稿路径（连接 / 画笔 / TSP 计算结果）。
	// 已保存（写到磁盘）的路径在 RouteStore 里。
	draft *mapdata.Path

	// 框选过程中的临时矩形（屏幕坐标）。Active 为 false 时不渲染。
	marquee marqueeState

	// 自由绘制过程中正在累计的世界坐标点。
	freedraw freedrawState

	// 自由绘制的"分段累计"：用户按下 → 拖拽 → 松开为一段，松开后点位
	// 转存到这里；下一次按下会继续往里追加（段间用直线连接）。
	// FlushFreedraw / SaveDraft / Undo / 切换工具时清空。
	freedrawSegments []mapdata.PathNode

	// 撤销栈：每次会"提交一个用户可见的变更"前压一份当前 (selection, draft) 快照，
	// Ctrl+Z 弹出最近一份并恢复。
	undoStack []editorSnapshot

	// 由用户（界面层）注入的依赖
	store     dataLookup // 查询 wiki 点位
	routes    *RouteStore
	pathCount int // 用于颜色循环

	// 最近一次 Place 操作的选区索引；调用 ConsumePendingPlace 后清零。
	// UI 用它在加点后自动弹出"备注编辑"。
	pendingPlaceIdx int
}

// editorSnapshot 撤销栈中一帧。
type editorSnapshot struct {
	selection []SelectionItem
	draft     *mapdata.Path
}

const undoStackLimit = 100

// snapshot 把当前 selection / draft 的深拷贝压入撤销栈。
// 调用方应在 *任何修改用户可见状态前* 调用。
func (e *PathEditor) snapshot() {
	snap := editorSnapshot{
		selection: append([]SelectionItem(nil), e.selection...),
	}
	if e.draft != nil {
		c := *e.draft
		c.Nodes = append([]mapdata.PathNode(nil), e.draft.Nodes...)
		snap.draft = &c
	}
	e.undoStack = append(e.undoStack, snap)
	if len(e.undoStack) > undoStackLimit {
		e.undoStack = e.undoStack[len(e.undoStack)-undoStackLimit:]
	}
}

// Undo 弹出最近一份快照恢复。
func (e *PathEditor) Undo() bool {
	n := len(e.undoStack)
	if n == 0 {
		return false
	}
	snap := e.undoStack[n-1]
	e.undoStack = e.undoStack[:n-1]
	e.selection = snap.selection
	e.draft = snap.draft
	// 撤销时取消进行中的临时操作
	e.marquee = marqueeState{}
	e.freedraw = freedrawState{}
	e.freedrawSegments = nil
	return true
}

type marqueeState struct {
	Active   bool
	StartX   float32
	StartY   float32
	CurX     float32
	CurY     float32
	Modifier int // 0=replace 1=add 2=remove
}

type freedrawState struct {
	Active bool
	Points []mapdata.PathNode
}

// dataLookup 是 PathEditor 需要的最小 Store 接口（避免循环依赖）。
type dataLookup interface {
	WikiPointAt(worldX, worldY, screenRadius2 float64) (mt, id string, x, y float64, ok bool)
}

// NewPathEditor 构造。
func NewPathEditor(store dataLookup, routes *RouteStore) *PathEditor {
	return &PathEditor{
		tool:            ToolPan,
		store:           store,
		routes:          routes,
		pendingPlaceIdx: -1,
	}
}

// Tool 返回当前工具。
func (e *PathEditor) Tool() Tool { return e.tool }

// SetTool 切换工具。切换时取消任何半成品操作。
func (e *PathEditor) SetTool(t Tool) {
	if e.tool == t {
		return
	}
	// 切到非自由绘制时，把累计的分段固化为 draft（避免静默丢失）。
	if e.tool == ToolFreedraw && t != ToolFreedraw {
		e.FlushFreedraw()
	}
	e.tool = t
	e.marquee = marqueeState{}
	e.freedraw = freedrawState{}
	// 链式构造的 draft 在工具切换时不立即丢弃 —— 用户可能想换工具继续添加；
	// 真正想清空时可点工具栏的"清空草稿"。
}

// Selection 返回当前选区副本（只读）。
func (e *PathEditor) Selection() []SelectionItem {
	out := make([]SelectionItem, len(e.selection))
	copy(out, e.selection)
	return out
}

// ClearSelection 清空选区与端点角色。
func (e *PathEditor) ClearSelection() {
	if len(e.selection) == 0 {
		return
	}
	e.snapshot()
	e.selection = nil
}

// Draft 返回当前草稿路径（可能为 nil）。
func (e *PathEditor) Draft() *mapdata.Path { return e.draft }

// ClearDraft 清空草稿（不影响选区）。
func (e *PathEditor) ClearDraft() {
	if e.draft == nil {
		return
	}
	e.snapshot()
	e.draft = nil
}

// FinalizeChain 把当前 ToolChain 模式下正在串的草稿"封存"。
// 现实意义：让 UI 知道用户结束了这一轮 Chain 操作；之后再用连接工具
// 单击会重新创建一条新草稿（而不是接着原草稿继续追加）。
//
// 实现：把 draft 保留，但在外层切到非连接工具，下次进 Chain 单击时
// 由 ChainClick 内部判断 "draft.Nodes 非空且 tool 刚切回 chain" 时新建。
//
// 这里采用更直接的语义：直接把 draft 移交（不清空）并返回，
// 之后用户可以选择保存或继续编辑；同时清空 selection 让流程简洁。
func (e *PathEditor) FinalizeChain() *mapdata.Path {
	if e.draft == nil || len(e.draft.Nodes) < 2 {
		return nil
	}
	e.snapshot()
	d := e.draft
	// 逻辑上结束了，下一次 ChainClick 会新建 draft。
	// 这里通过把 selection 清掉来给用户一个视觉反馈。
	e.selection = nil
	return d
}

// 内部工具：判定 (x,y) 是否已在选区中（按 source/id 或坐标完全相等）。
func (e *PathEditor) selectionIndex(it SelectionItem) int {
	for i, s := range e.selection {
		if s.Source != it.Source {
			continue
		}
		if s.Source == "wiki" && s.PointID == it.PointID {
			return i
		}
		if s.Source == "user" && s.X == it.X && s.Y == it.Y {
			return i
		}
	}
	return -1
}

func (e *PathEditor) addSelection(it SelectionItem) {
	if e.selectionIndex(it) >= 0 {
		return
	}
	e.snapshot()
	e.selection = append(e.selection, it)
}

func (e *PathEditor) removeSelection(it SelectionItem) {
	if i := e.selectionIndex(it); i >= 0 {
		e.snapshot()
		e.selection = append(e.selection[:i], e.selection[i+1:]...)
	}
}

// addSelectionRaw / removeSelectionRaw 不记录撤销，由调用者一次性 snapshot。
func (e *PathEditor) addSelectionRaw(it SelectionItem) {
	if e.selectionIndex(it) < 0 {
		e.selection = append(e.selection, it)
	}
}

func (e *PathEditor) removeSelectionRaw(it SelectionItem) {
	if i := e.selectionIndex(it); i >= 0 {
		e.selection = append(e.selection[:i], e.selection[i+1:]...)
	}
}

// CycleEndpoint 在 (worldX, worldY) 附近寻找选区节点（屏幕半径^2 给出距离阈值），
// 找到则在 None → Start → End → None 之间循环切换其角色。
// 同一时刻选区中只允许一个 Start / End，因此切换时会清掉同角色的其它节点。
//
// 返回 true 表示命中并修改。
func (e *PathEditor) CycleEndpoint(worldX, worldY float64, hitR2World float64) bool {
	idx := e.nearestSelected(worldX, worldY, hitR2World)
	if idx == -1 {
		return false
	}
	e.snapshot()
	cur := e.selection[idx].Role
	next := RoleNone
	switch cur {
	case RoleNone:
		next = RoleStart
	case RoleStart:
		next = RoleEnd
	case RoleEnd:
		next = RoleNone
	}
	e.applyRole(idx, next)
	return true
}

// SetEndpointRoleAt 把 (worldX, worldY) 附近最近的选区节点设为 role；
// 用于上下文菜单 "设为起点 / 设为终点 / 取消角色"。
func (e *PathEditor) SetEndpointRoleAt(worldX, worldY, hitR2World float64, role EndpointRole) bool {
	idx := e.nearestSelected(worldX, worldY, hitR2World)
	if idx == -1 {
		return false
	}
	e.snapshot()
	e.applyRole(idx, role)
	return true
}

// RemoveSelectionAt 把 (worldX, worldY) 附近最近的选区节点移出选区。
func (e *PathEditor) RemoveSelectionAt(worldX, worldY, hitR2World float64) bool {
	idx := e.nearestSelected(worldX, worldY, hitR2World)
	if idx == -1 {
		return false
	}
	e.snapshot()
	e.selection = append(e.selection[:idx], e.selection[idx+1:]...)
	return true
}

// IsSelectedAt 判断 (worldX, worldY) 附近是否有任意选中节点。
func (e *PathEditor) IsSelectedAt(worldX, worldY, hitR2World float64) bool {
	return e.nearestSelected(worldX, worldY, hitR2World) != -1
}

func (e *PathEditor) nearestSelected(worldX, worldY, r2 float64) int {
	best := -1
	bestD := r2
	for i, s := range e.selection {
		dx := s.X - worldX
		dy := s.Y - worldY
		d := dx*dx + dy*dy
		if d <= bestD {
			bestD = d
			best = i
		}
	}
	return best
}

func (e *PathEditor) applyRole(idx int, role EndpointRole) {
	if role != RoleNone {
		for i := range e.selection {
			if i != idx && e.selection[i].Role == role {
				e.selection[i].Role = RoleNone
			}
		}
	}
	e.selection[idx].Role = role
}

// ====== Marquee ======

func (e *PathEditor) BeginMarquee(sx, sy float32, modifier int) {
	e.marquee = marqueeState{Active: true, StartX: sx, StartY: sy, CurX: sx, CurY: sy, Modifier: modifier}
}
func (e *PathEditor) UpdateMarquee(sx, sy float32) {
	if e.marquee.Active {
		e.marquee.CurX = sx
		e.marquee.CurY = sy
	}
}
func (e *PathEditor) EndMarquee(applied []SelectionItem) {
	if !e.marquee.Active {
		return
	}
	mod := e.marquee.Modifier
	e.marquee = marqueeState{}
	if mod == 0 && len(applied) == 0 && len(e.selection) == 0 {
		return // 无变化
	}
	e.snapshot()
	switch mod {
	case 0: // replace
		e.selection = nil
		fallthrough
	case 1: // add
		for _, it := range applied {
			e.addSelectionRaw(it)
		}
	case 2: // remove
		for _, it := range applied {
			e.removeSelectionRaw(it)
		}
	}
}
func (e *PathEditor) MarqueeRect() (active bool, x0, y0, x1, y1 float32) {
	m := e.marquee
	if !m.Active {
		return false, 0, 0, 0, 0
	}
	x0, x1 = m.StartX, m.CurX
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	y0, y1 = m.StartY, m.CurY
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	return true, x0, y0, x1, y1
}

// ====== Place ======

// PlaceUserWaypoint 在 (worldX, worldY) 创建一个用户路径点并加入选区。
// 返回插入后的选区索引；如果同坐标的点已存在则返回该已存在项的索引。
func (e *PathEditor) PlaceUserWaypoint(worldX, worldY float64, layer string) int {
	it := SelectionItem{
		Source: "user",
		X:      worldX, Y: worldY,
		Layer: layer,
	}
	if i := e.selectionIndex(it); i >= 0 {
		e.pendingPlaceIdx = i
		return i
	}
	e.snapshot()
	e.selection = append(e.selection, it)
	idx := len(e.selection) - 1
	e.pendingPlaceIdx = idx
	return idx
}

// ConsumePendingPlace 返回最近一次 Place 操作的选区索引并清零。
// 没有未消费的 Place 时返回 -1。
func (e *PathEditor) ConsumePendingPlace() int {
	idx := e.pendingPlaceIdx
	e.pendingPlaceIdx = -1
	return idx
}

// SetNoteAt 把指定选区项的备注更新为 note。返回是否成功。
func (e *PathEditor) SetNoteAt(idx int, note string) bool {
	if idx < 0 || idx >= len(e.selection) {
		return false
	}
	if e.selection[idx].Note == note {
		return true
	}
	e.snapshot()
	e.selection[idx].Note = note
	return true
}

// SelectionIndexAt 返回 (worldX, worldY) 附近最近的选区项索引；无命中返回 -1。
func (e *PathEditor) SelectionIndexAt(worldX, worldY, hitR2World float64) int {
	return e.nearestSelected(worldX, worldY, hitR2World)
}

// SelectionAt 返回索引位置的选区项副本；越界返回零值与 false。
func (e *PathEditor) SelectionAt(idx int) (SelectionItem, bool) {
	if idx < 0 || idx >= len(e.selection) {
		return SelectionItem{}, false
	}
	return e.selection[idx], true
}

// AddWikiToSelection 加入一个 wiki 已有点位到选区。
func (e *PathEditor) AddWikiToSelection(markType, pointID string, worldX, worldY float64, layer string) {
	it := SelectionItem{
		Source:   "wiki",
		PointID:  pointID,
		MarkType: markType,
		X:        worldX, Y: worldY,
		Layer: layer,
	}
	e.addSelection(it)
}

// ====== Chain ======

// ChainClick 处理 ToolChain 下的一次点击（命中 wiki 点位则附带 ID）。
// 内部把节点加到草稿路径末尾。
func (e *PathEditor) ChainClick(worldX, worldY float64, layer, wikiID, wikiMT string) {
	e.snapshot()
	if e.draft == nil {
		e.draft = e.newDraft("手动连接")
	}
	source := "user"
	if wikiID != "" {
		source = "wiki"
	}
	e.draft.Nodes = append(e.draft.Nodes, mapdata.PathNode{
		X: worldX, Y: worldY,
		Layer: layer, PointID: wikiID, Source: source,
	})
	// 同步加到选区便于可视化（不再额外 snapshot —— 与本次链点操作合并）
	if wikiID != "" {
		it := SelectionItem{Source: "wiki", PointID: wikiID, MarkType: wikiMT, X: worldX, Y: worldY, Layer: layer}
		e.addSelectionRaw(it)
	} else {
		it := SelectionItem{Source: "user", X: worldX, Y: worldY, Layer: layer}
		e.addSelectionRaw(it)
	}
}

// ====== Freedraw ======
//
// 自由绘制支持"分段"：
//
//   - 用户第一次按下 → BeginFreedraw 开新笔触；松开后 EndFreedraw 把已有点
//     固化进 freedrawSegments 暂存（不立即写 draft）。
//   - 用户在 ToolFreedraw 下再次按下 → BeginFreedraw 检测到 freedrawSegments
//     非空，继续往里追加；本次按下点与上一段最后一个点之间用直线连接（即
//     直接 append 一个起点 PathNode，渲染时按相邻两点绘线即可）。
//   - 用户切换工具 / 撤销 / 调用 FlushFreedraw / SaveDraft → 把累计的所有
//     段（含直线连接）固化为 draft。
//
// 为了让"实时绘制中"也能看到先前段，FreedrawPoints 返回的是 segments 的所有
// 点 + 当前未结束段的实时点。

func (e *PathEditor) BeginFreedraw(worldX, worldY float64, layer string) {
	// 已有累计段：本次按下点视为下一段起点，与上一段最后一个点之间会被
	// 渲染成直线。
	e.freedraw.Active = true
	e.freedraw.Points = []mapdata.PathNode{{X: worldX, Y: worldY, Layer: layer, Source: "user"}}
}

// AppendFreedraw 在 freedraw 过程中追加一个采样点；采样点距上一个 < minStep（世界单位）则丢弃。
func (e *PathEditor) AppendFreedraw(worldX, worldY float64, layer string, minStep float64) {
	if !e.freedraw.Active {
		return
	}
	pts := e.freedraw.Points
	if n := len(pts); n > 0 {
		dx := worldX - pts[n-1].X
		dy := worldY - pts[n-1].Y
		if dx*dx+dy*dy < minStep*minStep {
			return
		}
	}
	e.freedraw.Points = append(e.freedraw.Points, mapdata.PathNode{X: worldX, Y: worldY, Layer: layer, Source: "user"})
}

// EndFreedraw 结束当前一段笔触；点位累计到 freedrawSegments 暂存，等待
// FlushFreedraw 真正写入 draft。这样允许用户分多次按下 / 松开，逐段拼出
// 一条复杂路径，相邻段之间自动用直线连接。
func (e *PathEditor) EndFreedraw() {
	if !e.freedraw.Active {
		return
	}
	if len(e.freedraw.Points) >= 1 {
		e.freedrawSegments = append(e.freedrawSegments, e.freedraw.Points...)
	}
	e.freedraw = freedrawState{}
}

// FlushFreedraw 把累计的所有段（含分段间的直线段）固化为 draft。
// 由切换工具 / 保存草稿 / 撤销 / 用户显式"完成绘制"时调用。
func (e *PathEditor) FlushFreedraw() {
	if len(e.freedrawSegments) < 2 {
		e.freedrawSegments = nil
		return
	}
	e.snapshot()
	d := e.newDraft("自由绘制")
	d.Freeform = true
	d.Nodes = append([]mapdata.PathNode(nil), e.freedrawSegments...)
	e.draft = d
	e.freedrawSegments = nil
}

// FreedrawPoints 返回当前 freedraw 笔触的临时节点（用于实时渲染）。
// 包括已完成的所有段 + 当前未结束段的点；段间相邻点的直线连接由渲染器自然处理。
func (e *PathEditor) FreedrawPoints() []mapdata.PathNode {
	if len(e.freedrawSegments) == 0 && !e.freedraw.Active {
		return nil
	}
	out := make([]mapdata.PathNode, 0, len(e.freedrawSegments)+len(e.freedraw.Points))
	out = append(out, e.freedrawSegments...)
	if e.freedraw.Active {
		out = append(out, e.freedraw.Points...)
	}
	return out
}

// HasFreedrawSegments 是否有未 flush 的累计段（用于工具栏 / 保存逻辑判断）。
func (e *PathEditor) HasFreedrawSegments() bool {
	return len(e.freedrawSegments) > 0 || e.freedraw.Active
}

// ====== TSP ======

// ComputeTSPOrder 仅做求解，不修改 draft；可在 goroutine 中调用。
// 返回顺序数组（指向 selection 的下标）；selection 为空或 <2 时返回 nil。
func (e *PathEditor) ComputeTSPOrder() ([]int, []SelectionItem, error) {
	sel := append([]SelectionItem(nil), e.selection...)
	if len(sel) < 2 {
		return nil, nil, fmt.Errorf("至少需要 2 个选中点")
	}
	nodes := make([]pathfind.TSPNode, len(sel))
	startIdx, endIdx := -1, -1
	for i, s := range sel {
		nodes[i] = pathfind.TSPNode{X: s.X, Y: s.Y, Tag: s.PointID}
		if s.Role == RoleStart {
			startIdx = i
		} else if s.Role == RoleEnd {
			endIdx = i
		}
	}
	order := pathfind.SolveTSPPath(nodes, startIdx, endIdx)
	if len(order) == 0 {
		return nil, nil, fmt.Errorf("求解失败")
	}
	return order, sel, nil
}

// ApplyTSPOrder 在 UI 线程把求解结果写入 draft。
func (e *PathEditor) ApplyTSPOrder(order []int, sel []SelectionItem) string {
	e.snapshot()
	d := e.newDraft("最短路径")
	d.Nodes = make([]mapdata.PathNode, len(order))
	for i, idx := range order {
		s := sel[idx]
		d.Nodes[i] = mapdata.PathNode{
			X: s.X, Y: s.Y,
			Layer: s.Layer, PointID: s.PointID, Source: s.Source,
		}
	}
	e.draft = d
	return fmt.Sprintf("已计算 %d 个节点的最短路径，总长度约 %.0f", len(order), tourLengthNodes(d.Nodes))
}

// SolveTSPDraft 把当前选区按 "包含全部节点的最短开放路径" 求解为 draft。
//
// 规则：
//   - 选区为空 / 仅 1 个 → no-op
//   - 选区中标记为 Start / End 的节点对应固定端点；都未标记则由算法决定
//   - 距离用欧氏距离（世界坐标系下）
//
// 注意：此为同步版本，可能在 N 较大时长时间阻塞。UI 应使用 ComputeTSPOrder +
// ApplyTSPOrder 的拆分版本以保持响应。
func (e *PathEditor) SolveTSPDraft() (string, error) {
	order, sel, err := e.ComputeTSPOrder()
	if err != nil {
		return "", err
	}
	return e.ApplyTSPOrder(order, sel), nil
}

func tourLengthNodes(ns []mapdata.PathNode) float64 {
	s := 0.0
	for i := 0; i+1 < len(ns); i++ {
		dx := ns[i+1].X - ns[i].X
		dy := ns[i+1].Y - ns[i].Y
		s += math.Sqrt(dx*dx + dy*dy)
	}
	return s
}

// SaveDraft 把当前 draft 保存到 RouteStore，并清空 draft。
func (e *PathEditor) SaveDraft(name string) (*mapdata.Path, error) {
	// 自由绘制有未 flush 的累计段时，先把它们 flush 为 draft（用户期望"保存"
	// 时所见即所得，不论中途松开了多少次）。
	if len(e.freedrawSegments) > 0 || e.freedraw.Active {
		e.FlushFreedraw()
	}
	if e.draft == nil || len(e.draft.Nodes) < 2 {
		return nil, fmt.Errorf("没有可保存的草稿（至少 2 个节点）")
	}
	e.snapshot()
	if name != "" {
		e.draft.Name = name
	}
	e.draft.Visible = true
	if err := e.routes.Save(e.draft); err != nil {
		return nil, err
	}
	e.pathCount++
	saved := e.draft
	e.draft = nil
	return saved, nil
}

func (e *PathEditor) newDraft(name string) *mapdata.Path {
	c := nextPathColor(e.pathCount)
	return &mapdata.Path{
		Name:    name,
		Color:   colorToHex(c),
		Visible: true,
	}
}

// 颜色辅助：从 Path.Color 解析到 NRGBA，失败则用默认绿色。
func resolvePathColor(p *mapdata.Path) color.NRGBA {
	if c, ok := parseHexColor(p.Color); ok {
		return c
	}
	return color.NRGBA{R: 0x4a, G: 0xc6, B: 0x6e, A: 0xff}
}
