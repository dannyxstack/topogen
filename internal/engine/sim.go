package engine

import (
	"log"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
)

type eventKind uint8

const (
	evTopo  eventKind = iota // 地形高度 += plus，plus>0 沉淀 plus<0 侵蚀
	evWater                  // 积水 c += plus
	evFlow                   // 水滴从 a 流到 b
	evQ                      // a 处流量额外 +1
)

// 地形事件是相对当前高度的 ±1。旧版几乎立即应用"设为发出时高度∓1"，
// 延迟到 tick 末尾后，若仍按绝对值应用，同一 tick 落在同一格的多个水滴只有第一个生效，
// 而每个水滴都已 takeSand++，沙子凭空增多。按增量应用更接近旧版，也保持守恒。
type event struct {
	kind eventKind
	plus int16
	a, b int32
}

// Sim 是一次地形生成 + 侵蚀模拟。不是并发安全的：Step、Pixel、Snapshot 等方法需在同一个 goroutine 中调用。
//
// 侵蚀按 tick 推进。一个 tick 是当前页(最多 PageSize 个水滴)的所有水滴各走一步：
// 先并行计算每个水滴的移动，修改写进各自的事件缓冲；再按水滴顺序串行应用。
// 所以任意 tick 边界都可以暂停，结果与 Workers 数无关。
type Sim struct {
	p       Params
	workers int
	logFn   func(format string, args ...any)

	src *countingSource
	rng *rand.Rand

	m Topomap
	w WaterMap

	t0       []int16  // 当前阶段尺度下的初始高度，侵蚀量 = t0 - t
	erodeCnt []uint16 // 逐像素累计侵蚀次数
	sediCnt  []uint16 // 逐像素累计沉积次数

	drops        []Droplet
	dropMaxLevel int // 生成水滴时用的最高海拔，放大阶段沿用初始值(与旧版一致)

	stage int // 0 = 初始地图，k = 第 k 次放大后
	page  int
	step  int // 当前页内下一步，1..Times
	tick  int64
	done  bool

	stayLongCount int64

	tilesX, tilesY int
	dirty          []bool

	bufs  [][]event
	cwBuf []event
}

func New(p Params) (*Sim, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	s := &Sim{p: p}
	s.setup()
	s.src = newCountingSource(p.Seed)
	s.rng = rand.New(s.src)

	s.generateTerrain()
	s.t0 = append([]int16(nil), s.m.Data...)
	s.erodeCnt = make([]uint16, len(s.m.Data))
	s.sediCnt = make([]uint16, len(s.m.Data))
	s.resetDirty()
	s.dropMaxLevel = int(s.m.MaxLv)

	if p.DropNum > 0 {
		s.assignAllVectors(2)
		s.rng.Seed(int64(p.Times)) // 与旧版一致：水滴的随机序列只取决于 times
	}
	s.startStage(0)
	return s, nil
}

func (s *Sim) setup() {
	s.workers = s.p.Workers
	if s.workers <= 0 {
		s.workers = runtime.GOMAXPROCS(0)
	}
	if s.logFn == nil {
		s.logFn = s.p.Logf
	}
	if s.logFn == nil {
		s.logFn = log.Printf
	}
	s.bufs = make([][]event, PageSize)
}

// SetLogger 替换日志输出，nil 表示丢弃
func (s *Sim) SetLogger(fn func(format string, args ...any)) {
	if fn == nil {
		fn = func(string, ...any) {}
	}
	s.logFn = fn
}

func (s *Sim) logf(format string, args ...any) { s.logFn(format, args...) }

func (s *Sim) debugf(format string, args ...any) {
	if s.p.Verbose {
		s.logFn(format, args...)
	}
}

func (s *Sim) Params() Params      { return s.p }
func (s *Sim) Size() (w, l int)    { return s.m.Width, s.m.Length }
func (s *Sim) Done() bool          { return s.done }
func (s *Sim) Tick() int64         { return s.tick }
func (s *Sim) Heights() []int16    { return s.m.Data } // 只读视图
func (s *Sim) Initial() []int16    { return s.t0 }     // 只读视图
func (s *Sim) Water() []WaterDot   { return s.w.Data } // 只读视图
func (s *Sim) Droplets() []Droplet { return s.drops }  // 只读视图

func (s *Sim) dropsInStage(stage int) int {
	if stage == 0 {
		return s.p.DropNum
	}
	k := 2 << (stage - 1)
	return s.p.DropNum * k * k
}

func pagesOf(drops int) int { return (drops + PageSize - 1) / PageSize }

func (s *Sim) ticksPerPage() int64 { return int64(max(s.p.Times, 1)) }

// TotalTicks 是整个模拟需要的 tick 数
func (s *Sim) TotalTicks() int64 {
	var n int64
	for st := 0; st <= s.p.Enlarge; st++ {
		n += int64(pagesOf(s.dropsInStage(st))) * s.ticksPerPage()
	}
	return n
}

func (s *Sim) startStage(stage int) {
	s.stage, s.page, s.step = stage, 0, 1
	if stage > 0 {
		s.m.Enlarge()
		s.w.Enlarge()
		width, length := s.m.Width/2, s.m.Length/2
		// t0 用与地形相同的插值放大，侵蚀量 t0-t 就不会混入插值本身造成的高度变化
		t0 := Topomap{Data: s.t0, Width: width, Length: length}
		t0.Enlarge()
		s.t0 = t0.Data
		s.erodeCnt = upsample(s.erodeCnt, width, length, func(v uint16) uint16 { return v })
		s.sediCnt = upsample(s.sediCnt, width, length, func(v uint16) uint16 { return v })
		s.resetDirty()
		s.assignAllVectors(2)
		s.logf("enlarge %dth time, h,w=%d,%d, will make %d droplets", stage, s.m.Width, s.m.Length, s.dropsInStage(stage))
	}
	s.drops = makeDroplets(s.rng, &s.m, &s.w, s.dropsInStage(stage), s.dropMaxLevel)
	if len(s.drops) == 0 {
		s.finishStage()
	}
}

func (s *Sim) finishStage() {
	if s.stage < s.p.Enlarge {
		s.startStage(s.stage + 1)
		return
	}
	s.done = true
}

// Step 推进最多 n 个 tick，返回实际执行的 tick 数
func (s *Sim) Step(n int) int {
	i := 0
	for ; i < n && !s.done; i++ {
		s.oneTick()
	}
	return i
}

// Run 一直执行到结束
func (s *Sim) Run() {
	for !s.done {
		s.oneTick()
	}
}

func (s *Sim) oneTick() {
	lo := s.page * PageSize
	hi := min(lo+PageSize, len(s.drops))
	if s.step <= s.p.Times {
		s.moveDrops(s.drops[lo:hi], s.step)
	}
	s.tick++
	s.step++
	if s.step <= s.p.Times {
		return
	}
	// 一页水滴走完，收集积水
	s.collectWater()
	s.step = 1
	s.page++
	if s.page*PageSize >= len(s.drops) {
		s.finishStage()
	}
}

func (s *Sim) moveDrops(drops []Droplet, step int) {
	run := func(i int) {
		c := mover{s: s, m: &s.m, w: &s.w, buf: s.bufs[i][:0]}
		drops[i].move(&c, step)
		s.bufs[i] = c.buf
	}
	if s.workers == 1 || len(drops) < 32 {
		for i := range drops {
			run(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range s.workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					start := int(next.Add(16)) - 16
					if start >= len(drops) {
						return
					}
					for i := start; i < min(start+16, len(drops)); i++ {
						run(i)
					}
				}
			}()
		}
		wg.Wait()
	}
	for i := range drops {
		for _, e := range s.bufs[i] {
			s.apply(e)
		}
	}
}

func (s *Sim) apply(e event) {
	switch e.kind {
	case evTopo:
		s.applyTopo(int(e.a), e.plus)
	case evWater:
		s.applyWater(int(e.a), e.plus)
	case evFlow:
		from, to := int(e.a), int(e.b)
		s.w.Data[from].Q++
		s.w.QCount++
		s.w.Data[from].OutPos = to
		s.w.Data[to].PrePos = from
		s.markDirty(from)
	case evQ:
		s.w.Data[e.a].Q++
		s.markDirty(int(e.a))
	}
}

// plus>0 表示沉淀 plus<0 表示侵蚀，侵蚀不低于 0
func (s *Sim) applyTopo(idx int, plus int16) {
	m := &s.m
	m.EvtIdx++
	if plus > 0 {
		s.debugf("evt sediment idx:%d,%d h+%d: %d", idx%m.Width, idx/m.Width, plus, m.Data[idx])
		m.Data[idx] += plus
		m.PlusCnt++
		s.sediCnt[idx] = satInc(s.sediCnt[idx])
		s.markDirty(idx)
		return
	}
	if m.Data[idx] <= 0 {
		return
	}
	s.debugf("evt eroding idx:%d,%d h%d: %d", idx%m.Width, idx/m.Width, plus, m.Data[idx])
	m.Data[idx] = max(m.Data[idx]+plus, 0)
	expectLv := m.Data[idx]
	m.MinusCnt++
	s.erodeCnt[idx] = satInc(s.erodeCnt[idx])
	s.markDirty(idx)
	// 25% 的几率侵蚀周围 加宽河谷
	if m.EvtIdx&0x3 == 1 && m.Data[idx] > SeaLevel {
		m.FindNeiDots(idx%m.Width, idx/m.Width, 2, func(nx, ny, distQ int) {
			nIdx := nx + ny*m.Width
			if (distQ < 2 || m.EvtIdx&0xF < 4) && m.Data[nIdx] > expectLv+1 { // 解决45度倾向性问题
				m.Data[nIdx]--
				s.erodeCnt[nIdx] = satInc(s.erodeCnt[nIdx])
				s.markDirty(nIdx)
			}
		})
	}
}

func (s *Sim) applyWater(idx int, plus int16) {
	w := &s.w
	w.EvtIdx++
	w.Data[idx].C += plus
	if plus > 0 {
		w.PlusCnt++
	} else {
		w.MinusCnt++
	}
	if s.m.Data[idx] <= SeaLevel {
		w.Data[idx].C -= 2 // 减掉太多会导致w.minC太小
	}
	if w.EvtIdx&0x1 == 1 {
		w.Data[idx].assignVector(w, &s.m, 2)
	}
	s.markDirty(idx)
}

func satInc(v uint16) uint16 {
	if v == 0xFFFF {
		return v
	}
	return v + 1
}

func (s *Sim) resetDirty() {
	s.tilesX = (s.m.Width + TileSize - 1) / TileSize
	s.tilesY = (s.m.Length + TileSize - 1) / TileSize
	s.dirty = make([]bool, s.tilesX*s.tilesY)
	for i := range s.dirty {
		s.dirty[i] = true
	}
}

func (s *Sim) markDirty(idx int) {
	s.dirty[(idx/s.m.Width)/TileSize*s.tilesX+(idx%s.m.Width)/TileSize] = true
}

// TakeDirtyTiles 返回自上次调用以来有变化的区块编号(ty*tilesX+tx)并清空标记。
// 放大地图后全部区块都会被标记。
func (s *Sim) TakeDirtyTiles() (tiles []int, tilesX, tilesY int) {
	for i, d := range s.dirty {
		if d {
			tiles = append(tiles, i)
			s.dirty[i] = false
		}
	}
	return tiles, s.tilesX, s.tilesY
}

// countingSource 记录随机数调用次数，使随机数状态可以写进快照
type countingSource struct {
	src  rand.Source64
	seed int64
	n    uint64
}

func newCountingSource(seed int64) *countingSource {
	return &countingSource{src: rand.NewSource(seed).(rand.Source64), seed: seed}
}

func (c *countingSource) Int63() int64   { c.n++; return c.src.Int63() }
func (c *countingSource) Uint64() uint64 { c.n++; return c.src.Uint64() }
func (c *countingSource) Seed(seed int64) {
	c.src.Seed(seed)
	c.seed, c.n = seed, 0
}

func (c *countingSource) replay(seed int64, n uint64) {
	c.Seed(seed)
	for range n {
		c.src.Uint64()
	}
	c.n = n
}
