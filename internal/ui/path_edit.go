package ui

import (
	"github.com/where-to-go/app/internal/mapdata"
)

// pathEditOp 描述一次"在现有已保存路径上编辑某个节点"的进行中操作。
// 由右键菜单发起；MapView 在接下来的左键事件里读取并应用。
//
// Action 语义：
//   - ""            未进行编辑（零值）
//   - "move"        下一次左键落点处把 AnchorIdx 节点移到新世界坐标
//   - "delete"      立即删除（不需要后续点击；在菜单回调里直接执行即可）
//   - "insertBefore" 下一次左键落点处在 AnchorIdx 之前插入新节点
//   - "insertAfter"  下一次左键落点处在 AnchorIdx 之后插入新节点
type pathEditOp struct {
	Active    bool
	RouteID   string
	AnchorIdx int
	Action    string
}

// Clear 清零操作状态。
func (p *pathEditOp) Clear() { *p = pathEditOp{} }

// 特殊 RouteID 字符串：表示节点位于编辑器草稿中（包括 chain / freedraw 阶段）。
// applyPathEdit 收到这种 routeID 时直接在 PathEditor 的 draft / freedrawSegments
// 上动手，不走 RouteStore.Save。
const (
	editTargetDraft     = "__draft__"
	editTargetFreedraw  = "__freedraw__"
)

// nearestPathNode 在 routes 中所有可见路径上找最靠近 (wx, wy) 的节点；
// hitR2 为命中阈值（世界单位平方）。无命中返回 routeID="" / idx=-1。
func nearestPathNode(routes *RouteStore, wx, wy, hitR2 float64) (routeID string, idx int, node mapdata.PathNode) {
	if routes == nil {
		return "", -1, mapdata.PathNode{}
	}
	bestID := ""
	bestIdx := -1
	bestD2 := hitR2
	var bestNode mapdata.PathNode
	for _, p := range routes.All() {
		if !p.Visible {
			continue
		}
		for i, n := range p.Nodes {
			dx := n.X - wx
			dy := n.Y - wy
			d2 := dx*dx + dy*dy
			if d2 <= bestD2 {
				bestD2 = d2
				bestID = p.ID
				bestIdx = i
				bestNode = n
			}
		}
	}
	return bestID, bestIdx, bestNode
}

// nearestEditableNode 与 nearestPathNode 类似，但额外把当前 PathEditor 的
// draft / freedrawSegments 也加入候选 —— 用户在"绘制 / 连接 / 自由绘制"
// 过程中右键可直接编辑当前正在构造的路径节点。
//
// editor.draft 优先；草稿不存在时退回 freedrawSegments；都没有就只看已保存路径。
func nearestEditableNode(editor *PathEditor, routes *RouteStore, wx, wy, hitR2 float64) (routeID string, idx int, node mapdata.PathNode) {
	bestID, bestIdx, bestNode := nearestPathNode(routes, wx, wy, hitR2)
	bestD2 := hitR2
	if bestIdx >= 0 {
		dx := bestNode.X - wx
		dy := bestNode.Y - wy
		bestD2 = dx*dx + dy*dy
	}
	if editor != nil {
		if d := editor.draft; d != nil {
			for i, n := range d.Nodes {
				dx := n.X - wx
				dy := n.Y - wy
				d2 := dx*dx + dy*dy
				if d2 <= bestD2 {
					bestD2 = d2
					bestID = editTargetDraft
					bestIdx = i
					bestNode = n
				}
			}
		}
		// freedrawSegments：用户已松开但未 flush 的累计段
		for i, n := range editor.freedrawSegments {
			dx := n.X - wx
			dy := n.Y - wy
			d2 := dx*dx + dy*dy
			if d2 <= bestD2 {
				bestD2 = d2
				bestID = editTargetFreedraw
				bestIdx = i
				bestNode = n
			}
		}
	}
	return bestID, bestIdx, bestNode
}

// EditDraftNode 在 draft 上应用一次节点编辑（move / delete / insertBefore /
// insertAfter）。delete 时若节点数 < 3 直接返回 false。
func (e *PathEditor) EditDraftNode(idx int, action string, px, py float64) bool {
	if e.draft == nil || idx < 0 || idx >= len(e.draft.Nodes) {
		return false
	}
	layer := e.draft.Nodes[idx].Layer
	e.snapshot()
	switch action {
	case "delete":
		if len(e.draft.Nodes) <= 2 {
			// 仅 2 节点时删除会让路径失去意义；让上层回滚
			e.undoStack = e.undoStack[:len(e.undoStack)-1]
			return false
		}
		e.draft.Nodes = append(e.draft.Nodes[:idx], e.draft.Nodes[idx+1:]...)
	case "move":
		e.draft.Nodes[idx].X = px
		e.draft.Nodes[idx].Y = py
	case "insertBefore":
		n := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		e.draft.Nodes = append(e.draft.Nodes[:idx], append([]mapdata.PathNode{n}, e.draft.Nodes[idx:]...)...)
	case "insertAfter":
		n := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		ins := idx + 1
		e.draft.Nodes = append(e.draft.Nodes[:ins], append([]mapdata.PathNode{n}, e.draft.Nodes[ins:]...)...)
	default:
		e.undoStack = e.undoStack[:len(e.undoStack)-1]
		return false
	}
	return true
}

// EditFreedrawNode 在 freedrawSegments 上应用一次节点编辑（同 EditDraftNode 语义）。
func (e *PathEditor) EditFreedrawNode(idx int, action string, px, py float64) bool {
	if idx < 0 || idx >= len(e.freedrawSegments) {
		return false
	}
	layer := e.freedrawSegments[idx].Layer
	e.snapshot()
	switch action {
	case "delete":
		if len(e.freedrawSegments) <= 2 {
			e.undoStack = e.undoStack[:len(e.undoStack)-1]
			return false
		}
		e.freedrawSegments = append(e.freedrawSegments[:idx], e.freedrawSegments[idx+1:]...)
	case "move":
		e.freedrawSegments[idx].X = px
		e.freedrawSegments[idx].Y = py
	case "insertBefore":
		n := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		e.freedrawSegments = append(e.freedrawSegments[:idx], append([]mapdata.PathNode{n}, e.freedrawSegments[idx:]...)...)
	case "insertAfter":
		n := mapdata.PathNode{X: px, Y: py, Layer: layer, Source: "user"}
		ins := idx + 1
		e.freedrawSegments = append(e.freedrawSegments[:ins], append([]mapdata.PathNode{n}, e.freedrawSegments[ins:]...)...)
	default:
		e.undoStack = e.undoStack[:len(e.undoStack)-1]
		return false
	}
	return true
}

