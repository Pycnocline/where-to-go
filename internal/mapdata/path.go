// Package mapdata 中的路径定义。
package mapdata

import "github.com/where-to-go/app/internal/wiki"

// PathNode 路径上的一个节点。
type PathNode struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Layer   string  `json:"layer,omitempty"`
	PointID string  `json:"pointId,omitempty"` // 来自地图官方点位则记录 ID
	Source  string  `json:"source,omitempty"`  // "wiki" / "user"，便于反向区分
	Note    string  `json:"note,omitempty"`    // 用户备注（仅 user 节点有意义）
}

// Path 一条用户定义的路径。
type Path struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Color    string     `json:"color"` // "#RRGGBB"
	Nodes    []PathNode `json:"nodes"`
	Closed   bool       `json:"closed,omitempty"`
	Freeform bool       `json:"freeform,omitempty"` // 自由绘制（画笔）路径，不显示节点编号
	Visible  bool       `json:"visible"`            // UI：当前是否可见
}

// PathFromPoints 把地图上的官方点位（按 markType 过滤）转成 PathNode 列表。
func PathFromPoints(items []wiki.PointItem) []PathNode {
	out := make([]PathNode, 0, len(items))
	for _, p := range items {
		out = append(out, PathNode{
			X: p.Point.Lng, Y: p.Point.Lat,
			Layer: p.Layer, PointID: p.ID, Source: "wiki",
		})
	}
	return out
}
