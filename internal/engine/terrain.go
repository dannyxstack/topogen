package engine

import (
	"math"
	"math/rand"
	"sync"
)

type Hill struct {
	x       int
	y       int
	r       int
	h       int
	tiltDir float64 // 倾斜方向
	tiltLen int     // 倾斜长度 todo tilt: 未生效
}

// get radius 一个点(x,y)看hill的边距离 不同视角看到的距离不一样 返回的R不能比hill.r大
// 返回花瓣状距离 花瓣hill产生的高原效果特别好
func (h *Hill) R(x, y int, petal *PetalFlag) int {
	switch petal.Shape {
	case 1:
		// 尖角瓣状 需要加大hill-wide 否则都是细线  1-abs(sin(dir))
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir
		dist := 1.0 - math.Abs(math.Sin(diffDir*petal.PetalNum/2.0))*petal.Sharp
		return int(dist * float64(h.r))
	case 2:
		// 圆花瓣状 细腰长叶花瓣 1-sin(dir)
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir
		dist := 1.0 - (math.Sin(diffDir*petal.PetalNum)+1.0)/2.0*petal.Sharp
		return int(dist * float64(h.r))
	case 3:
		// 圆形瓣状 圆润丰满 1-abs(sin(dir))
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir
		dist := 1.0 - (1.0-math.Abs(math.Sin(diffDir*petal.PetalNum/2.0)))*petal.Sharp
		return int(dist * float64(h.r))
	default:
		// 圆形 相同hill-wide，比其他2种占用面积大
		return h.r
	}
}

// 随机n个圆圈 1/3 是反向海拔，成为盆地
func makeHills(rng *rand.Rand, width, length, hillWide, num int) []Hill {
	widthEdge := width / 9
	lengthEdge := length / 9
	hills := make([]Hill, num)
	for ri := range hills {
		r := &hills[ri]
		r.x, r.y, r.h = (rng.Int()%(width-widthEdge*2))+widthEdge, (rng.Int()%(length-lengthEdge*2))+lengthEdge, rng.Int()%HillHeightMedian+HillHeightMedian/2
		r.tiltDir, r.tiltLen, r.r = rng.Float64()*math.Pi*2, (rng.Int()%20)+1, int(math.Sqrt(float64(rng.Int()%(hillWide*hillWide+1))))
		if ri%3 == 1 {
			r.h *= -1
		}
	}
	return hills
}

// ridgeLen = Hill 个数, ridgeWide = Hill wide
func makeRidge(rng *rand.Rand, ridgeLen, ridgeWide, mWidth, mLength int) []Hill {
	ridgeHills := make([]Hill, ridgeLen)
	widthEdge := mWidth / 8
	lengthEdge := mLength / 8
	baseTowardX1, baseTowardY1 := randomDir(rng)
	baseTowardX, baseTowardY := int(baseTowardX1*float32(ridgeWide)), int(baseTowardY1*float32(ridgeWide))
	for ri := 0; ri < ridgeLen; ri++ {
		r := &ridgeHills[ri]
		r.h = rng.Int()%RidgeHeightMedian + RidgeHeightMedian/2
		r.tiltDir, r.tiltLen = rng.Float64()*math.Pi*2, (rng.Int()%20)+1
		if ri == 0 {
			r.x, r.y, r.r = (rng.Int()%(mWidth-widthEdge*2))+widthEdge, (rng.Int()%(mLength-lengthEdge*2))+lengthEdge, (rng.Int()%ridgeWide)/2+ridgeWide/2
		} else {
			// 基础方向: 上一个 + baseToward 摆动: wave
			waveX, waveY := 0, 0
			if baseTowardX != 0 {
				waveY = (rng.Int() % baseTowardX) - (rng.Int() % baseTowardX)
			}
			if baseTowardY != 0 {
				waveX = (rng.Int() % baseTowardY) - (rng.Int() % baseTowardY)
			}
			r.x, r.y, r.r = ridgeHills[ri-1].x+baseTowardX/2+waveX, ridgeHills[ri-1].y+baseTowardY/2+waveY, (rng.Int()%ridgeWide)/2+ridgeWide/2
		}
	}
	return ridgeHills
}

// 返回随机方向 x,y 取值范围 [-1,1]
func randomDir(rng *rand.Rand) (x, y float32) {
	thedir := rng.Float64() * math.Pi * 2
	return float32(math.Cos(thedir)), float32(math.Sin(thedir))
}

// 从底图像素取高度 r,g,b 取值 0-255
func basemapHeight(tone string, r, g, b int16) int16 {
	switch tone {
	case "1": // light is high
		return (r + g + b) / 4
	case "0": // dark is high
		return (256 - (r+g+b)/3) / 4
	case "r":
		return r / 4
	case "g":
		return g / 4
	case "b":
		return b / 4
	case "r+":
		if r*2-b-g > 0 {
			return (r - b + r - g) / 4
		}
	case "g+":
		if g*2-r-b > 0 {
			return (g - r + g - b) / 4
		}
	case "b+":
		if b*2-r-g > 0 {
			return (b - r + b - g) / 4
		}
	case "rs": // redness_simple = R - max(G, B) 深红色值更高
		if r*2-b-g > 0 {
			return (r-max(g, b))/2 + 128
		}
	case "gs":
		if g*2-r-b > 0 {
			return (g-max(r, b))/2 + 128
		}
	case "bs":
		if b*2-r-g > 0 {
			return (b-max(r, g))/2 + 128
		}
	case "rh": // redness_simple2 = R - (G + B) / 2
		if r*2-b-g > 0 {
			return (r - (g+b)/2 + 128) / 2
		}
	case "gh":
		if g*2-r-b > 0 {
			return (g - (r+b)/2 + 128) / 2
		}
	case "bh":
		if b*2-r-g > 0 {
			return (b - (g+r)/2 + 128) / 2
		}
	case "rg":
		if r+g > 2*b {
			return (r + g) / 4
		}
	case "gb":
		if g+b > 2*r {
			return (g + b) / 4
		}
	case "rb":
		if r+b > 2*g {
			return (r + b) / 4
		}
	case "rg+":
		if r+g > 2*b {
			return g/16 + r/4 // poor effect
		}
	case "t+": // terrain
		return (r*4 + g*2 + b) / 28
	}
	return 0
}

// generateTerrain 生成初始地形到 s.m，并初始化 s.w
func (s *Sim) generateTerrain() {
	p := &s.p
	width, length := p.Width, p.Length

	var baseM Topomap
	if p.Basemap != nil {
		img := p.Basemap.Image
		bounds := img.Bounds()
		width, length = bounds.Dx(), bounds.Dy()
		zoomOut := 1
		for width > 600 || length > 600 {
			width /= 2
			length /= 2
			zoomOut *= 2
		}
		baseM.Init(width, length)
		for y := 0; y < length; y++ {
			for x := 0; x < width; x++ {
				ru, gu, bu, _ := img.At(bounds.Min.X+x*zoomOut, bounds.Min.Y+y*zoomOut).RGBA()
				baseM.Data[x+y*width] = basemapHeight(p.Basemap.Tone, int16(ru>>8), int16(gu>>8), int16(bu>>8))
			}
		}
		s.logf("basemap width:%d length:%d zoomOut:%d", width, length, zoomOut)
		s.m = baseM
		s.w.Init(width, length)
	}

	if p.Basemap == nil || p.Basemap.Cover != "" {
		s.w.Init(width, length)
		s.m.Init(width, length)
		s.fillHills(width, length)
	}

	if p.Basemap != nil {
		switch p.Basemap.Cover {
		case "sum":
			for i := range s.m.Data {
				s.m.Data[i] += baseM.Data[i]
			}
		case "shape":
			for i := range s.m.Data {
				if baseM.Data[i] <= SeaLevel {
					s.m.Data[i] = 0
				}
			}
		case "avg":
			for i := range s.m.Data {
				s.m.Data[i] = (s.m.Data[i] + baseM.Data[i]) / 2
			}
		case "max":
			for i := range s.m.Data {
				s.m.Data[i] = max(s.m.Data[i], baseM.Data[i])
			}
		}
	}

	m := &s.m
	if p.LowerWaterLevel > 0 {
		for i := range m.Data {
			m.Data[i] = max(m.Data[i]-int16(p.LowerWaterLevel), 0)
		}
	}
	if p.Blur > 0 {
		for li := 0; li < m.Length; li++ {
			for wi := 0; wi < m.Width; wi++ {
				m.EvtIdx += 13
				sum := int16(0)
				cnt := m.FindNeiDots(wi, li, p.Blur, func(nx, ny, distSq int) {
					sum += m.Data[nx+ny*m.Width]
				})
				if cnt > 0 {
					m.Data[wi+li*m.Width] = sum / int16(cnt)
				}
			}
		}
		m.EvtIdx = 0
	}

	// 抬升到 MaxHeight 的一半左右。旧版在 maxLevel > 500 时倍数为 0 会把地图抹平，这里跳过
	maxLevel, _ := m.CalcMaxMin()
	if maxLevel > 1 && maxLevel < MaxHeight {
		if uplift := MaxHeight / maxLevel / 2; uplift > 1 {
			for i := range m.Data {
				m.Data[i] *= uplift
			}
		}
	}
	maxLevel, minLevel := m.CalcMaxMin()
	s.logf("terrain makes ok. min:%d max:%d", minLevel, maxLevel)
}

// 随机 hills、ridges、stuck 叠加出地形
func (s *Sim) fillHills(width, length int) {
	p := &s.p
	s.logf("will make hills(n:%d,wide:%d)", p.Hills, p.HillWide)
	hills := makeHills(s.rng, width, length, p.HillWide, p.Hills)
	// elevate the first hills
	for hi := range hills {
		if hills[hi].h > 0 {
			hills[hi].h *= 4
			break
		}
	}

	s.logf("will make ridge(n:%d, wide:%d)", p.RidgeLen, p.RidgeWide)
	var ridgeHills []Hill
	for ri := 0; ri < p.Ridges; ri++ {
		ridgeHills = append(ridgeHills, makeRidge(s.rng, p.RidgeLen, p.RidgeWide, width, length)...)
	}

	// stuck 区域内的 hill 被压低(偶数)或抬高(奇数)
	stuckHills := makeHills(s.rng, width, length, width/2, p.Stuck)
	for sti := range stuckHills {
		st := &stuckHills[sti]
		press := func(hs []Hill, div int) (cnt int) {
			for hi := range hs {
				distM := (hs[hi].x-st.x)*(hs[hi].x-st.x) + (hs[hi].y-st.y)*(hs[hi].y-st.y)
				stuckR := st.R(hs[hi].x, hs[hi].y, &p.Petal)
				if distM < stuckR*stuckR {
					cnt++
					if sti&0x1 == 0 {
						hs[hi].h /= div
					} else {
						hs[hi].h += 2
					}
				}
			}
			return
		}
		stuckedCnt := press(hills, 4) + press(ridgeHills, 2)
		s.logf("hills of stucked shield:%d/%d", stuckedCnt, len(hills)+len(ridgeHills))
	}

	s.logf("will fill hills and ridges to TopoMap(all times:%d)", width*length*(len(hills)+len(ridgeHills)))
	// 每行互不影响，按行并行
	rows := make(chan int, length)
	for y := 0; y < length; y++ {
		rows <- y
	}
	close(rows)
	var wg sync.WaitGroup
	for range s.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for y := range rows {
				s.fillRow(y, width, hills, ridgeHills)
			}
		}()
	}
	wg.Wait()
}

func (s *Sim) fillRow(y, width int, hills, ridgeHills []Hill) {
	petal := &s.p.Petal
	for x := 0; x < width; x++ {
		var tmpLevel float32
		for _, r := range ridgeHills {
			distM := (x-r.x)*(x-r.x) + (y-r.y)*(y-r.y)
			rn := r.R(x, y, petal)
			if distM <= rn*rn {
				tmpLevel += float32(r.h) - float32(float64(r.h)*math.Sqrt(math.Sqrt(float64(distM)/float64(rn*rn))))
			}
		}
		for _, r := range hills {
			distM := (x-r.x)*(x-r.x) + (y-r.y)*(y-r.y)
			rn := r.R(x, y, petal) // 使用花瓣半径效果好
			if distM < rn*rn {
				// 产生的ring中间隆起
				tmpLevel += float32(r.h) - float32(float64(r.h)*math.Sqrt(math.Sqrt(float64(distM)/float64(rn*rn))))
			}
		}
		tmpLevel = max(tmpLevel, 0)
		idx := x + y*width
		s.m.Data[idx] = max(s.m.Data[idx], int16(tmpLevel))
	}
}
