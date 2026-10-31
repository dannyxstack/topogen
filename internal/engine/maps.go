package engine

type pos struct{ x, y int }

type Topomap struct {
	Data   []int16 // 对应坐标只保存高度
	Width  int
	Length int
	EvtIdx int   // 已应用的地形事件数，同时决定 FindNeiDots 的遍历方向
	MaxLv  int16 // 只在生成地形后计算一次，侵蚀过程中保持不变(与旧版一致)
	MinLv  int16

	PlusCnt  int
	MinusCnt int
}

// 将变成固定不移动 记录场 被流动水雕刻
type WaterDot struct {
	X      float32 // 将不变化 =Topomap[x,y] +(0.5, 0.5)
	Y      float32
	XPower float32 // x方向速度增益 场强度 根据地形得出 范围(-1,1)
	YPower float32
	C      int16 // 积水高度 catchment
	Q      int   // 历史流量 0=无

	PrePos int // 上一个位置
	OutPos int // 预期下游位置
}

type WaterMap struct {
	Data   []WaterDot
	Width  int
	Length int
	EvtIdx int // 已应用的积水事件数
	QCount int

	ExchCnt  int
	PlusCnt  int
	MinusCnt int
}

func (m *Topomap) Init(width, length int) {
	m.Data = make([]int16, width*length)
	m.Width = width
	m.Length = length
}

func (w *WaterMap) Init(width, length int) {
	w.Data = make([]WaterDot, width*length)
	w.Width = width
	w.Length = length
	// 把点的实际基点摆在中间
	for y := 0; y < length; y++ {
		for x := 0; x < width; x++ {
			w.Data[x+y*width].X = float32(x) + 0.5
			w.Data[x+y*width].Y = float32(y) + 0.5
		}
	}
}

// 8 种遍历顺序，消除固定遍历方向带来的 45 度倾向性
var neiOrders = [8]struct{ xOuter, outerAsc, innerAsc bool }{
	{true, true, true},    // NE
	{true, true, false},   // SE
	{true, false, true},   // NW
	{true, false, false},  // SW
	{false, false, true},  // WN
	{false, true, true},   // WS
	{false, false, false}, // EN
	{false, true, false},  // ES
}

// FindNeiDots 圆形区域搜索，遍历方向由 EvtIdx 决定。ring=1 时有明显的 45 度倾向性，需要调用者处理
// traversal neighbors. maxRing: 1=nei*8 2=nei*16 3=nei*24
func (m *Topomap) FindNeiDots(x, y, maxRing int, iterFunc func(nx, ny, distSq int)) (iterCount int) {
	order := neiOrders[m.EvtIdx&0x7]
	limit := maxRing*maxRing + 1
	for oi := 0; oi <= 2*maxRing; oi++ {
		o := maxRing - oi
		if order.outerAsc {
			o = oi - maxRing
		}
		for ii := 0; ii <= 2*maxRing; ii++ {
			in := maxRing - ii
			if order.innerAsc {
				in = ii - maxRing
			}
			dx, dy := in, o
			if order.xOuter {
				dx, dy = o, in
			}
			nx, ny := x+dx, y+dy
			if nx < 0 || ny < 0 || nx >= m.Width || ny >= m.Length {
				continue
			}
			distSq := dx*dx + dy*dy
			if distSq == 0 || distSq > limit {
				continue
			}
			iterFunc(nx, ny, distSq)
			iterCount++
		}
	}
	return
}

// 获取某个点周围的最高值和最低值
func (m *Topomap) CountNeighbors(x, y int) (low, high, higherCount int16) {
	low = MaxHeight
	myHigh := m.Data[y*m.Width+x]
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			nx, ny := x+dx, y+dy
			if dx == 0 && dy == 0 || nx < 0 || nx >= m.Width || ny < 0 || ny >= m.Length {
				continue
			}
			v := m.Data[nx+ny*m.Width]
			low = min(low, v)
			high = max(high, v)
			if myHigh < v {
				higherCount++
			}
		}
	}
	return
}

func (m *Topomap) CalcMaxMin() (maxLv, minLv int16) {
	m.MaxLv, m.MinLv = extremes(m.Data)
	return m.MaxLv, m.MinLv
}

func extremes(data []int16) (maxLv, minLv int16) {
	maxLv, minLv = 0, 32767
	for _, v := range data {
		maxLv = max(maxLv, v)
		minLv = min(minLv, v)
	}
	return
}

func (w *WaterMap) CalcMaxC() (maxC, minC int16, maxQ int) {
	minC = 32767
	for i := range w.Data {
		d := &w.Data[i]
		maxC = max(maxC, d.C)
		minC = min(minC, d.C)
		maxQ = max(maxQ, d.Q)
	}
	return
}

// 放大地图为 2x2 倍，高度同时翻倍
func (m *Topomap) Enlarge() {
	for i := range m.Data {
		m.Data[i] *= 2
	}
	w2 := m.Width * 2
	newData := make([]int16, len(m.Data)*4)
	quad := func(x, y, dst int, neis [3]pos) {
		c := m.Data[x+y*m.Width]
		v := int16(0)
		for _, n := range neis {
			if n.x >= 0 && n.y >= 0 && n.x < m.Width && n.y < m.Length {
				v = (v + m.Data[n.x+n.y*m.Width]) / 2
			}
		}
		// 效果挺好，但是有加剧45度倾向性的嫌疑 某些方向的线条锯齿厉害
		newData[dst] = min((v+c)/2+1, c)
	}
	for y := 0; y < m.Length; y++ {
		for x := 0; x < m.Width; x++ {
			base := x*2 + y*2*w2
			quad(x, y, base, [3]pos{{x - 1, y}, {x - 1, y - 1}, {x, y - 1}})      // nw
			quad(x, y, base+1, [3]pos{{x, y - 1}, {x + 1, y - 1}, {x + 1, y}})    // ne
			quad(x, y, base+1+w2, [3]pos{{x + 1, y}, {x + 1, y + 1}, {x, y + 1}}) // se
			quad(x, y, base+w2, [3]pos{{x, y + 1}, {x - 1, y + 1}, {x - 1, y}})   // sw
		}
	}
	m.Data = newData
	m.Width, m.Length = m.Width*2, m.Length*2
}

// 放大后积水与流量全部清零
func (w *WaterMap) Enlarge() {
	w.Init(w.Width*2, w.Length*2)
}

// 按最近邻放大 2x2，用于 T0 和逐像素计数
func upsample[T any](data []T, width, length int, scale func(T) T) []T {
	out := make([]T, len(data)*4)
	w2 := width * 2
	for y := 0; y < length; y++ {
		for x := 0; x < width; x++ {
			v := scale(data[x+y*width])
			base := x*2 + y*2*w2
			out[base], out[base+1], out[base+w2], out[base+w2+1] = v, v, v, v
		}
	}
	return out
}

func calcBit[T int | int16 | int32 | int64](x T) (cnt T) {
	if x < 0 {
		x = -x
	}
	for x > 0 {
		x >>= 1
		cnt++
	}
	return
}
