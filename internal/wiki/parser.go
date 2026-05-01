package wiki

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseMapMeta 从大地图页 HTML 中抽取 mapData 配置块，
// 解析出图层信息、缩放范围、bounds、dataPrefix 等。
//
// 来源是嵌入在 <script>var mapData = { ... };</script> 中的 JS 对象字面量，
// 我们只需要其中确定的几个字段，因此用正则取值即可，无需 JS 解释器。
func ParseMapMeta(html string) (*MapMeta, error) {
	meta := &MapMeta{
		MinZoom: 4,
		MaxZoom: 8,
	}

	// dataPrefix: "Data:Mapnew"
	if m := regexp.MustCompile(`dataPrefix:\s*"([^"]+)"`).FindStringSubmatch(html); m != nil {
		meta.DataPrefix = m[1]
	} else {
		meta.DataPrefix = "Data:Mapnew"
	}

	// minZoom / maxZoom：可能不在 mapData 顶层（用默认即可）
	if m := regexp.MustCompile(`maxZoom:\s*(\d+)`).FindStringSubmatch(html); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			meta.MaxZoom = v
		}
	}
	if m := regexp.MustCompile(`minZoom:\s*(\d+)`).FindStringSubmatch(html); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil {
			meta.MinZoom = v
		}
	}

	// maxBounds: [[-256 * 32, -256 * 60], [256 * 32, 256 * 32]]
	if m := regexp.MustCompile(`maxBounds:\s*\[\s*\[\s*([\-+\d\s\*\.]+),\s*([\-+\d\s\*\.]+)\s*\],\s*\[\s*([\-+\d\s\*\.]+),\s*([\-+\d\s\*\.]+)\s*\]`).FindStringSubmatch(html); m != nil {
		meta.MaxBounds[0] = evalArith(m[1])
		meta.MaxBounds[1] = evalArith(m[2])
		meta.MaxBounds[2] = evalArith(m[3])
		meta.MaxBounds[3] = evalArith(m[4])
	}

	// 解析 mapLayers 数组：直接全局扫描每一个图层对象（更鲁棒）。
	// 每一项的捕获：name / index / tileUrl / myBounds(x1,x2,y1,y2)
	itemRe := regexp.MustCompile(`(?s)\{\s*name:\s*"([^"]+)",\s*index:\s*(-?\d+),\s*layerOption:\s*\{\s*tileUrl:\s*"([^"]+)",\s*myBounds:\s*\{\s*x1:\s*(\d+),\s*x2:\s*(\d+),\s*y1:\s*(\d+),\s*y2:\s*(\d+)\s*\}`)
	for _, m := range itemRe.FindAllStringSubmatch(html, -1) {
		idx, _ := strconv.Atoi(m[2])
		x1, _ := strconv.Atoi(m[4])
		x2, _ := strconv.Atoi(m[5])
		y1, _ := strconv.Atoi(m[6])
		y2, _ := strconv.Atoi(m[7])
		meta.Layers = append(meta.Layers, Layer{
			Name:    m[1],
			Index:   idx,
			TileURL: m[3],
			X1:      x1, X2: x2, Y1: y1, Y2: y2,
		})
	}
	if len(meta.Layers) == 0 {
		return nil, fmt.Errorf("解析 mapLayers 失败：未匹配到任何图层")
	}
	return meta, nil
}

// evalArith 解析形如 "-256 * 32" / "256*60" 的简单算术表达式（仅 *）。
func evalArith(s string) float64 {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, "*")
	v := 1.0
	for _, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0
		}
		v *= f
	}
	return v
}

// ParseCategoryData 从大地图 HTML 中抽取 <div id="categoryData">...</div> 的 JSON 文本。
// 该 div 中的 JSON 实际上还嵌入了 <p>、<a> 等 HTML 标签，需要先清洗再解析。
// 解析依赖 CategoryItem.UnmarshalJSON 处理类型差异。
func ParseCategoryData(html string) (*CategoryData, error) {
	raw, err := extractDivText(html, "categoryData")
	if err != nil {
		return nil, fmt.Errorf("抽取 categoryData 失败: %w", err)
	}
	cleaned := cleanEmbeddedJSON(raw)
	var c CategoryData
	if err := json.Unmarshal([]byte(cleaned), &c); err != nil {
		return nil, fmt.Errorf("解析 categoryData JSON 失败: %w (前 200 字符: %s)", err, truncate(cleaned, 200))
	}
	return &c, nil
}

// cleanEmbeddedJSON 清除 Wiki 注入到 JSON 文本中的 HTML 残骸：
//   - <p>、</p>、<br/> 标签
//   - <a ... href="URL">URL</a> 形态的链接（保留 URL 文本）
func cleanEmbeddedJSON(s string) string {
	aRe := regexp.MustCompile(`(?s)<a[^>]*>(.*?)</a>`)
	s = aRe.ReplaceAllString(s, "$1")
	tagRe := regexp.MustCompile(`<\s*/?\s*[a-zA-Z][a-zA-Z0-9_-]*[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ParseAreaData 抽取 #mapAreaData。
func ParseAreaData(html string) ([]AreaItem, error) {
	raw, err := extractDivText(html, "mapAreaData")
	if err != nil {
		return nil, err
	}
	var areas []AreaItem
	if err := json.Unmarshal([]byte(raw), &areas); err != nil {
		return nil, fmt.Errorf("解析 mapAreaData JSON 失败: %w", err)
	}
	return areas, nil
}

// ParseTextLayer 抽取 #textLayerData。返回原始 JSON 文本（按图层分组的对象）。
func ParseTextLayer(html string) (json.RawMessage, error) {
	raw, err := extractDivText(html, "textLayerData")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(strings.TrimSpace(raw)), nil
}

// ParseAllPoints 解析 /rocom/Data:Mapnew/point.json 返回的 HTML。
//
// 预期 HTML 中含 <div id="mapPointData">{ 1001:[…],1002:[…],…}</div>，
// 内部还会被 Wiki 注入 <p> 标签等噪声，键名是未加引号的数字。
func ParseAllPoints(html string) (map[string][]PointItem, error) {
	raw, err := extractDivText(html, "mapPointData")
	if err != nil {
		return nil, err
	}
	cleaned := cleanEmbeddedJSON(raw)
	// 复刻 wiki 前端：未展开的模板会以 ":Data:Mapnew/type/N/json" 形式出现，
	// 在 JS 中被替换成 ":[]"。
	dataRefRe := regexp.MustCompile(`:Data:[^"]{0,60}/json`)
	cleaned = dataRefRe.ReplaceAllString(cleaned, ":[]")
	// 数字键未加引号 → 加引号。匹配 `{` 或 `,` 后紧跟 [可空格] [数字 / 中文键] : 的形态
	keyRe := regexp.MustCompile(`([\{,])\s*([A-Za-z0-9_\p{Han}]+)\s*:`)
	cleaned = keyRe.ReplaceAllString(cleaned, `$1"$2":`)
	// 清理可能的尾随逗号 ",}" 或 ",]"
	tailRe := regexp.MustCompile(`,(\s*[\}\]])`)
	cleaned = tailRe.ReplaceAllString(cleaned, "$1")

	var raw2 map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cleaned), &raw2); err != nil {
		return nil, fmt.Errorf("解析批量点位失败: %w (前 200 字符: %s)", err, truncate(cleaned, 200))
	}
	out := make(map[string][]PointItem, len(raw2))
	for mt, arr := range raw2 {
		items, err := ParsePointJSON([]byte(arr), mt)
		if err != nil {
			return nil, fmt.Errorf("markType %s 点位解析失败: %w", mt, err)
		}
		if len(items) > 0 {
			out[mt] = items
		}
	}
	return out, nil
}

// ParsePointJSON 解析单个点位 JSON 数组。利用 PointItem.UnmarshalJSON 处理类型差异。
// 若解析出来的 markType 为空（极少数缺字段情况），用调用方提供的 fallback。
func ParsePointJSON(raw []byte, markType string) ([]PointItem, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "[]" {
		return nil, nil
	}
	var items []PointItem
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, fmt.Errorf("解析点位 JSON 失败: %w", err)
	}
	for i := range items {
		if items[i].MarkType == "" {
			items[i].MarkType = markType
		}
	}
	return items, nil
}

// extractDivText 抽取 <div id="ID">...</div> 内的纯文本。
// 注：Wiki 页面里这些 div 是用来塞 JSON 文本的，class/style 等属性可能有，但 id 唯一。
func extractDivText(html, id string) (string, error) {
	pat := regexp.MustCompile(fmt.Sprintf(`(?s)<div[^>]*id="%s"[^>]*>(.*?)</div>`, regexp.QuoteMeta(id)))
	m := pat.FindStringSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("未找到 #%s 元素", id)
	}
	// HTML 实体解码：& &gt; &lt; &quot;
	t := m[1]
	t = strings.ReplaceAll(t, "&quot;", `"`)
	t = strings.ReplaceAll(t, "&amp;", "&")
	t = strings.ReplaceAll(t, "&lt;", "<")
	t = strings.ReplaceAll(t, "&gt;", ">")
	t = strings.ReplaceAll(t, "&#39;", "'")
	t = strings.ReplaceAll(t, "&nbsp;", " ")
	return strings.TrimSpace(t), nil
}
