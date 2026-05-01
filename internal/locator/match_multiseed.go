package locator

import (
	"fmt"
	"image"
	"sort"
)

// MultiSeedCandidate 一个候选 seed 与匹配结果。
type MultiSeedCandidate struct {
	SeedX, SeedY float64
	Fix          Fix
	Score        float64
	Err          error
}

// MatchMultiSeed 针对多个候选 seed 各跑一次"广域 NCC + 多 scale"匹配，
// 返回得分最高的 Fix；用于：
//   - 重启后玩家可能在世界任何地方
//   - 玩家传送后旧 seed 完全失效
//   - patch-vote 之后想真正确认位置
//
// 调用方应给一个相对宽的搜索半径（例如 200 模板像素）和宽 scale 表。
// 该函数计算开销大，应在后台 goroutine 中调用。
//
// 注意：会临时修改 m.LastFix / m.SearchRadiusPx / m.WikiHalfCap，调用前后由
// 调用方负责保护并发（建议每个调用者用自己专属的 *Matcher 实例）。
func (m *Matcher) MatchMultiSeed(roi *image.RGBA, seeds []Fix, searchRadiusPx, wikiHalfCap int) (Fix, []MultiSeedCandidate, error) {
	if len(seeds) == 0 {
		return Fix{}, nil, fmt.Errorf("multi-seed: no seeds")
	}
	saveRadius := m.SearchRadiusPx
	saveCap := m.WikiHalfCap
	saveStreak := m.failStreak
	saveSeed := m.LastFix
	defer func() {
		m.SearchRadiusPx = saveRadius
		m.WikiHalfCap = saveCap
		m.failStreak = saveStreak
		m.LastFix = saveSeed
	}()

	if searchRadiusPx > 0 {
		m.SearchRadiusPx = searchRadiusPx
	}
	if wikiHalfCap > 0 {
		m.WikiHalfCap = wikiHalfCap
	}

	cands := make([]MultiSeedCandidate, 0, len(seeds))
	for _, s := range seeds {
		seed := s
		m.LastFix = &seed
		m.failStreak = 0
		fix, err := m.Match(roi)
		dbg := m.LastDebug
		cand := MultiSeedCandidate{
			SeedX: s.WorldX,
			SeedY: s.WorldY,
			Fix:   fix,
			Score: dbg.BestScore,
			Err:   err,
		}
		cands = append(cands, cand)
	}

	// 按 score 降序
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	if cands[0].Score <= 0 {
		return Fix{}, cands, fmt.Errorf("multi-seed: no candidate matched")
	}
	return cands[0].Fix, cands, nil
}
