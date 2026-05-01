package locator

import (
	"image"
	"math"
	"sync"
	"time"
)

// SimulatorTrack 让模拟器沿一条折线运行。
type SimulatorTrack struct {
	Nodes []SimNode // 至少 2 个节点
	Speed float64   // 世界单位 / 秒
}

// SimNode 一个折线节点（世界坐标）。
type SimNode struct{ X, Y float64 }

// Simulator 一个用于调试的"假定位器"匹配函数：忽略截图，
// 沿配置好的折线匀速移动并返回坐标 + 沿路径切线的朝向。
//
// 用法：
//   sim := locator.NewSimulator(track)
//   loc := locator.New(locator.Config{ Match: sim.Match, ROI: ... })
//   loc.Start()
type Simulator struct {
	mu    sync.Mutex
	track SimulatorTrack
	start time.Time
	totalLen float64
	segLens  []float64
}

// NewSimulator 构造。
func NewSimulator(track SimulatorTrack) *Simulator {
	if track.Speed <= 0 {
		track.Speed = 50 // 世界单位 / 秒
	}
	s := &Simulator{track: track, start: time.Now()}
	s.precompute()
	return s
}

// SetTrack 替换轨迹（保持时间起点）。
func (s *Simulator) SetTrack(track SimulatorTrack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.track = track
	if s.track.Speed <= 0 {
		s.track.Speed = 50
	}
	s.start = time.Now()
	s.precompute()
}

func (s *Simulator) precompute() {
	s.segLens = s.segLens[:0]
	s.totalLen = 0
	for i := 0; i+1 < len(s.track.Nodes); i++ {
		dx := s.track.Nodes[i+1].X - s.track.Nodes[i].X
		dy := s.track.Nodes[i+1].Y - s.track.Nodes[i].Y
		l := math.Sqrt(dx*dx + dy*dy)
		s.segLens = append(s.segLens, l)
		s.totalLen += l
	}
}

// Match 实现 MatchFn —— 忽略截图。
func (s *Simulator) Match(_ *image.RGBA) (Fix, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.track.Nodes) < 2 || s.totalLen == 0 {
		return Fix{}, nil
	}
	elapsed := time.Since(s.start).Seconds()
	dist := s.track.Speed * elapsed
	// 在轨迹上来回（往返）：dist 折叠到 [0, totalLen]。
	cycle := s.totalLen * 2
	d := math.Mod(dist, cycle)
	if d < 0 {
		d += cycle
	}
	if d > s.totalLen {
		d = cycle - d // 折返
	}
	// 找 d 所在的段。
	acc := 0.0
	segIdx := 0
	for i, sl := range s.segLens {
		if acc+sl >= d {
			segIdx = i
			break
		}
		acc += sl
		segIdx = i
	}
	a := s.track.Nodes[segIdx]
	b := s.track.Nodes[segIdx+1]
	t := 0.0
	if s.segLens[segIdx] > 0 {
		t = (d - acc) / s.segLens[segIdx]
	}
	x := a.X + (b.X-a.X)*t
	y := a.Y + (b.Y-a.Y)*t
	heading := math.Atan2(b.X-a.X, -(b.Y - a.Y)) // 朝路径切线，0 = 北
	return Fix{
		WorldX:     x,
		WorldY:     y,
		Heading:    heading,
		HasHeading: true,
		Confidence: 1.0,
	}, nil
}
