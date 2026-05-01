// Package wiki 负责从 Wiki 站点抓取地图相关资源。
//
// 数据源（同一个游戏 wiki：rocom）：
//
//	元数据页：
//	  https://wiki.biligame.com/rocom/大地图
//	点位 JSON 合集（含全部 markType）：
//	  https://wiki.biligame.com/rocom/Data:Mapnew/point.json
//	瓦片：
//	  https://wiki-dev-patch-oss.oss-cn-hangzhou.aliyuncs.com/res/lkwg/map-{V}/...
package wiki

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Layer 描述一个地图底图图层。
type Layer struct {
	Name    string `json:"name"`
	Index   int    `json:"index"`
	TileURL string `json:"tileUrl"`
	X1      int    `json:"x1"`
	X2      int    `json:"x2"`
	Y1      int    `json:"y1"`
	Y2      int    `json:"y2"`
}

// MapMeta 大地图全局元数据。
type MapMeta struct {
	DataPrefix string     `json:"dataPrefix"`
	MinZoom    int        `json:"minZoom"`
	MaxZoom    int        `json:"maxZoom"`
	Layers     []Layer    `json:"layers"`
	MaxBounds  [4]float64 `json:"maxBounds"`
}

// CategoryItem 一个点位类别。
//
// 序列化输出强类型字段；反序列化时使用自定义 UnmarshalJSON 同时兼容
// 来自 Wiki 的原始格式（数字键 markType / 字符串布尔等）与本地 manifest
// 的标准化格式。
type CategoryItem struct {
	MarkType     string `json:"markType"`
	MarkTypeName string `json:"markTypeName"`
	Type         string `json:"type"`
	Icon         string `json:"icon"`
	DefaultShow  bool   `json:"defaultShow"`
	Collectible  bool   `json:"collectible"`
}

// UnmarshalJSON 兼容多形态字段。
func (c *CategoryItem) UnmarshalJSON(data []byte) error {
	var aux struct {
		MarkType     interface{} `json:"markType"`
		MarkTypeName string      `json:"markTypeName"`
		Type         string      `json:"type"`
		Icon         string      `json:"icon"`
		DefaultShow  interface{} `json:"defaultShow"`
		Collectible  interface{} `json:"collectible"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	c.MarkTypeName = aux.MarkTypeName
	c.Type = aux.Type
	c.Icon = aux.Icon
	c.MarkType = anyToString(aux.MarkType)
	c.DefaultShow = anyToBool(aux.DefaultShow)
	c.Collectible = anyToBool(aux.Collectible)
	return nil
}

// CategoryData 类别集合。
type CategoryData struct {
	Data []CategoryItem `json:"data"`
}

// Point 一个具体点位坐标。
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// PointItem 一个点位。
type PointItem struct {
	MarkType string  `json:"markType"`
	Title    string  `json:"title"`
	ID       string  `json:"id"`
	Point    Point   `json:"point"`
	UID      string  `json:"uid"`
	Layer    string  `json:"layer"`
	Version  int     `json:"version"`
}

// UnmarshalJSON 兼容 markType 为整型 / 字符串。
func (p *PointItem) UnmarshalJSON(data []byte) error {
	var aux struct {
		MarkType interface{} `json:"markType"`
		Title    string      `json:"title"`
		ID       string      `json:"id"`
		Point    Point       `json:"point"`
		UID      string      `json:"uid"`
		Layer    string      `json:"layer"`
		Version  int         `json:"version"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	p.MarkType = anyToString(aux.MarkType)
	p.Title = aux.Title
	p.ID = aux.ID
	p.Point = aux.Point
	p.UID = aux.UID
	p.Layer = aux.Layer
	p.Version = aux.Version
	return nil
}

// AreaItem 区域定义。
type AreaItem struct {
	Name    string                 `json:"name"`
	GeoJSON map[string]interface{} `json:"geojson"`
}

// anyToString 把任意 JSON 标量转成字符串。
func anyToString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	}
	return ""
}

// anyToBool 把任意 JSON 标量转成布尔。
func anyToBool(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || strings.EqualFold(x, "TRUE") || x == "1"
	case float64:
		return x != 0
	case json.Number:
		return x.String() != "0" && x.String() != ""
	}
	return false
}
