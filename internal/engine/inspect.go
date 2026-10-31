package engine

// PixelInfo 是一个像素的全部可查看数据，供前端悬停/点击显示
type PixelInfo struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	T      int16   `json:"t"`      // 当前高度
	T0     int16   `json:"t0"`     // 初始高度(当前放大尺度下)
	Eroded int16   `json:"eroded"` // t0 - t，正数为侵蚀，负数为沉积
	C      int16   `json:"c"`      // 积水
	Level  int16   `json:"level"`  // 水面高度 t + c
	Q      int     `json:"q"`      // 历史流量
	Sea    bool    `json:"sea"`    // 低于海平面
	XPower float32 `json:"xPower"` // 场向量
	YPower float32 `json:"yPower"`
	PreX   int     `json:"preX"` // 上游坐标，无则为 -1
	PreY   int     `json:"preY"`
	OutX   int     `json:"outX"` // 下游坐标，无则为 -1
	OutY   int     `json:"outY"`

	ErodeCnt uint16 `json:"erodeCnt"` // 累计侵蚀次数
	SediCnt  uint16 `json:"sediCnt"`  // 累计沉积次数
	Drops    int    `json:"drops"`    // 当前停在该像素的水滴数
	Sand     int    `json:"sand"`     // 这些水滴携带的沙量
	// 以该像素为中心的 5x5 高度，按行排列，越界为 -1
	Neighborhood [25]int16 `json:"neighborhood"`
}

// Pixel 返回 (x,y) 的详细数据，坐标越界时 ok 为 false
func (s *Sim) Pixel(x, y int) (info PixelInfo, ok bool) {
	m, w := &s.m, &s.w
	if x < 0 || y < 0 || x >= m.Width || y >= m.Length {
		return info, false
	}
	idx := x + y*m.Width
	dot := &w.Data[idx]
	info = PixelInfo{
		X: x, Y: y,
		T: m.Data[idx], T0: s.t0[idx], Eroded: s.t0[idx] - m.Data[idx],
		C: dot.C, Level: m.Data[idx] + dot.C, Q: dot.Q, Sea: m.Data[idx] < SeaLevel,
		XPower: dot.XPower, YPower: dot.YPower,
		PreX: -1, PreY: -1, OutX: -1, OutY: -1,
		ErodeCnt: s.erodeCnt[idx], SediCnt: s.sediCnt[idx],
	}
	// 旧版用 0 表示没有上下游
	if dot.PrePos > 0 {
		info.PreX, info.PreY = dot.PrePos%m.Width, dot.PrePos/m.Width
	}
	if dot.OutPos > 0 {
		info.OutX, info.OutY = dot.OutPos%m.Width, dot.OutPos/m.Width
	}
	for i := range s.drops {
		d := &s.drops[i]
		if int(d.X) == x && int(d.Y) == y {
			info.Drops++
			info.Sand += d.TakeSand
		}
	}
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			v := int16(-1)
			if nx, ny := x+dx, y+dy; nx >= 0 && ny >= 0 && nx < m.Width && ny < m.Length {
				v = m.Data[nx+ny*m.Width]
			}
			info.Neighborhood[(dy+2)*5+dx+2] = v
		}
	}
	return info, true
}

type Stats struct {
	Tick       int64 `json:"tick"`
	TotalTicks int64 `json:"totalTicks"`
	Stage      int   `json:"stage"`
	Stages     int   `json:"stages"`
	Page       int   `json:"page"`
	Pages      int   `json:"pages"`
	Step       int   `json:"step"`
	Times      int   `json:"times"`
	Done       bool  `json:"done"`

	Width  int `json:"width"`
	Length int `json:"length"`
	Drops  int `json:"drops"`

	MaxLv int16 `json:"maxLv"`
	MinLv int16 `json:"minLv"`
	MaxC  int16 `json:"maxC"`
	MinC  int16 `json:"minC"`
	MaxQ  int   `json:"maxQ"`

	TopoEvents  int   `json:"topoEvents"`
	Sediments   int   `json:"sediments"`
	Erosions    int   `json:"erosions"`
	WaterEvents int   `json:"waterEvents"`
	WaterPlus   int   `json:"waterPlus"`
	WaterMinus  int   `json:"waterMinus"`
	Exchanges   int   `json:"exchanges"`
	QCount      int   `json:"qCount"`
	StayLong    int64 `json:"stayLong"`
}

// Stats 遍历整张地图计算极值，适合低频调用
func (s *Sim) Stats() Stats {
	maxLv, minLv := extremes(s.m.Data)
	maxC, minC, maxQ := s.w.CalcMaxC()
	return Stats{
		Tick: s.tick, TotalTicks: s.TotalTicks(),
		Stage: s.stage, Stages: s.p.Enlarge + 1,
		Page: s.page, Pages: pagesOf(len(s.drops)),
		Step: s.step, Times: s.p.Times, Done: s.done,
		Width: s.m.Width, Length: s.m.Length, Drops: len(s.drops),
		MaxLv: maxLv, MinLv: minLv, MaxC: maxC, MinC: minC, MaxQ: maxQ,
		TopoEvents: s.m.EvtIdx, Sediments: s.m.PlusCnt, Erosions: s.m.MinusCnt,
		WaterEvents: s.w.EvtIdx, WaterPlus: s.w.PlusCnt, WaterMinus: s.w.MinusCnt,
		Exchanges: s.w.ExchCnt, QCount: s.w.QCount, StayLong: s.stayLongCount,
	}
}

type DropStats struct {
	MaxJourney int
	AvgJourney int
	UpDotNum   int // 结束时比出生时高的水滴
	DownDotNum int
}

// DropStats 统计当前阶段水滴的路程与高度变化
func (s *Sim) DropStats() (ds DropStats) {
	sum := 0
	for i := range s.drops {
		d := &s.drops[i]
		sum += d.Journey
		ds.MaxJourney = max(ds.MaxJourney, d.Journey)
		p := int(d.X) + int(d.Y)*s.m.Width
		if p < 0 || p >= len(s.m.Data) {
			continue
		}
		if s.m.Data[p] > d.BirthH {
			ds.UpDotNum++
		} else {
			ds.DownDotNum++
		}
	}
	ds.AvgJourney = sum / (len(s.drops) + 1)
	return
}
