package locator

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
)

// CalibrationResult Calibrate 的输出。
type CalibrationResult struct {
	K             float64   `json:"k"`
	BestSharpness float64   `json:"bestSharpness"`
	WorldX        float64   `json:"worldX"`
	WorldY        float64   `json:"worldY"`
	AllScores     []float64 `json:"-"`
	AllKs         []float64 `json:"-"`
	ZoomUsed      int       `json:"zoomUsed"`
}

// CalibrateK 在给定 ROI 和种子世界坐标下，在 K ∈ [kMin, kMax] 步长 kStep
// 扫描，选尖锐度最高的 K。
//
// 关键区别于运行期 Match：
//   - 搜索半径更大（初始位置不一定准），默认 80
//   - 允许所有 K 候选，不因边界命中而早停
//   - 不更新 matcher 内部 failStreak（单次评估）
//
// 输出写到 data/calibration.json。
func CalibrateK(mp *MosaicProvider, roi *image.RGBA, seedX, seedY, kMin, kMax, kStep float64, zoom, searchRadius int, progress func(k float64, score float64)) (CalibrationResult, error) {
	if mp == nil {
		return CalibrationResult{}, fmt.Errorf("calibrate: mosaic nil")
	}
	if kMin <= 0 || kMax <= kMin || kStep <= 0 {
		return CalibrationResult{}, fmt.Errorf("calibrate: invalid K range [%.2f, %.2f] step %.2f", kMin, kMax, kStep)
	}
	if zoom <= 0 {
		zoom = 8
	}
	if searchRadius <= 0 {
		searchRadius = 60
	}

	best := CalibrationResult{BestSharpness: -math.MaxFloat64, ZoomUsed: zoom}
	allK := []float64{}
	allScore := []float64{}
	for k := kMin; k <= kMax+1e-9; k += kStep {
		m := NewEdgeMatcher(mp, k)
		m.SearchZoom = zoom
		m.SearchRadiusMinPx = searchRadius
		m.SearchRadiusMaxPx = searchRadius
		m.MinSharpness = -1 // 评估阶段不拒绝
		m.HeadingDetect = false
		m.AllowMissingCircle = true
		m.SetSeed(seedX, seedY)
		_, _ = m.Match(roi)
		dbg := m.LastDebug
		sharp := dbg.Sharpness
		// 用 fix 数据；如果 LastDebug 没填（前置错误），跳过
		fixX := seedX + dbg.WorldDX
		fixY := seedY + dbg.WorldDY
		allK = append(allK, k)
		allScore = append(allScore, sharp)
		if progress != nil {
			progress(k, sharp)
		}
		if sharp > best.BestSharpness {
			best = CalibrationResult{
				K:             k,
				BestSharpness: sharp,
				WorldX:        fixX,
				WorldY:        fixY,
				ZoomUsed:      zoom,
			}
		}
	}
	best.AllKs = allK
	best.AllScores = allScore
	return best, nil
}

// SaveCalibration 把校准结果写入 path（建议 data/calibration.json）。
func SaveCalibration(path string, r CalibrationResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadCalibration 从 path 读取。不存在返回 (zero, false, nil)。
func LoadCalibration(path string) (CalibrationResult, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CalibrationResult{}, false, nil
		}
		return CalibrationResult{}, false, err
	}
	var r CalibrationResult
	if err := json.Unmarshal(b, &r); err != nil {
		return CalibrationResult{}, false, err
	}
	return r, r.K > 0, nil
}
