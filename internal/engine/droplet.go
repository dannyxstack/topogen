package engine

import (
	"math"
	"math/rand"
	"sync/atomic"
)

type Droplet struct {
	X        float32
	Y        float32
	VX       float32 // 滑行速度
	VY       float32
	Journey  int // 走过的路程
	BirthH   int16
	TakeSand int // 冲掉的沙子，带在身上，走到下坡的时候有可能放下
	StayLong uint8
}

func makeDroplet(rng *rand.Rand, w *WaterMap, m *Topomap) Droplet {
	idx := rng.Int() % len(w.Data)
	d := Droplet{
		X:      float32(idx%w.Width) + 0.5,
		Y:      float32(idx/w.Width) + 0.5,
		BirthH: m.Data[idx],
	}
	w.Data[idx].C++
	thedir := rng.Float64() * math.Pi * 2
	d.VX, d.VY = float32(math.Cos(thedir))/2, float32(math.Sin(thedir))/2
	return d
}

// 西风水滴：从左边沿一行向右吹，碰到足够高的地方才降落
func makeWindDroplet(rng *rand.Rand, w *WaterMap, m *Topomap, maxLevel int) Droplet {
	rnd := rng.Int()
	rndY0 := (rnd % w.Length) * w.Width
	d := Droplet{
		X: float32(rndY0%w.Width) + 0.5,
		Y: float32(rndY0/w.Width) + 0.5,
	}
	rnd2 := (rnd / 19) % 37
	for xi := 1; xi < m.Width-1; xi++ {
		if rndY0+xi < m.Width*m.Length && m.Data[rndY0+xi] >= int16(maxLevel/5+rnd2) {
			d.X += float32(xi)
			break
		}
	}
	// fix if they did not find a high enough point
	if d.X < 1 {
		d.X = float32((rng.Int()/29)%w.Width) + 0.5
	}
	idx := int(d.X) + int(d.Y)*w.Width
	d.BirthH = m.Data[idx]
	w.Data[idx].C++
	thedir := rng.Float64() * math.Pi * 2
	d.VX, d.VY = float32(math.Cos(thedir))/2, float32(math.Sin(thedir))/2
	return d
}

func makeDroplets(rng *rand.Rand, m *Topomap, w *WaterMap, dropNum, maxLevel int) []Droplet {
	drops := make([]Droplet, dropNum)
	for di := range drops {
		if di&0xF >= 2 {
			drops[di] = makeDroplet(rng, w, m)
		} else {
			drops[di] = makeWindDroplet(rng, w, m, maxLevel) // west wind drops
		}
	}
	return drops
}

func (d *Droplet) reduceSpeed() {
	for d.VX*d.VX+d.VY*d.VY < 0.5 {
		d.VX, d.VY = d.VX*1.05, d.VY*1.05
	}
	for d.VX*d.VX+d.VY*d.VY > MinDistToReduce {
		d.VX, d.VY = d.VX*0.95, d.VY*0.95
	}
}

// mover 是一个水滴走一步时的上下文：只读共享地图，所有修改写进 buf，
// 由 Sim 在本步所有水滴算完之后按水滴顺序串行应用。
type mover struct {
	s   *Sim
	m   *Topomap
	w   *WaterMap
	buf []event
}

func (c *mover) topo(x, y int, plus int16) {
	c.buf = append(c.buf, event{kind: evTopo, a: int32(x + y*c.m.Width), plus: plus})
}

func (c *mover) water(x, y int, plus int16) {
	c.buf = append(c.buf, event{kind: evWater, a: int32(x + y*c.w.Width), plus: plus})
}

// 流出才算流量
func (c *mover) flow(from, to int) {
	c.buf = append(c.buf, event{kind: evFlow, a: int32(from), b: int32(to)})
}

func (c *mover) qinc(idx int) {
	c.buf = append(c.buf, event{kind: evQ, a: int32(idx)})
}

func (c *mover) debugf(format string, args ...any) {
	if c.s.p.Verbose {
		c.s.logf(format, args...)
	}
}

// 改变地形的修改都通过事件延迟应用
// 如果下游有落差，并且和上游也有落差，才侵蚀上游
// 先找邻居中具有最低流出预计高度的邻居；如果邻居平等，则按照场强度选择
func (d *Droplet) move(c *mover, step int) {
	m, w := c.m, c.w
	oldX, oldY := int(d.X), int(d.Y)
	oldIdx := oldX + oldY*w.Width
	if oldIdx >= len(w.Data) || oldIdx < 0 {
		c.debugf("oldIdx(%d) out of w.data. stop it.", oldIdx) // never happen
		return
	}

	// check sea level
	if m.Data[oldIdx] < SeaLevel {
		return
	}

	if d.StayLong > MaxStayLong {
		atomic.AddInt64(&c.s.stayLongCount, 1)
		if d.TakeSand > 0 && d.StayLong&0x1 == 0 {
			c.topo(oldX, oldY, 1)
			c.debugf("make sediment for stayLong exceeded (%d,%d) stayLong:%d.", oldX, oldY, d.StayLong)
			d.TakeSand--
		}
		return
	}

	if d.StayLong&0x7 == 2 {
		c.water(oldX, oldY, 2+int16(step&0x1)) // 增加积水
		if d.TakeSand > 1 && w.Data[oldIdx].Q < 2 {
			c.debugf("pos(%d,%d) stay too long:%d SEDIMENT. t:%d", oldX, oldY, d.StayLong, m.Data[oldIdx])
			c.topo(oldX, oldY, 1) // 增加沉淀
			d.TakeSand--
			d.StayLong++
			return
		}
	}

	d.StayLong++
	newIdx, newX, newY := 0, 0, 0

	if step&0x7 <= 5 && d.hasLowestNei(m, w, 1) {
		// 从邻居中寻找潜在的流出最低的下游; 无45度倾向性
		lowestLv, lowerNeis := d.findNeiLowerDots(m, w, 2)
		if len(lowerNeis) == 0 {
			c.debugf("cannot find lowest neighbor, stay. t:%d oldPos:%d,%d", m.Data[oldIdx], oldX, oldY)
			return
		}

		newPos := lowerNeis[(step*7)%len(lowerNeis)]
		newX, newY = newPos.x, newPos.y
		newIdx = newX + newY*w.Width

		if lowestLv == m.Data[oldIdx] {
			// find out the neighbor with lowest out level
			lowestNeiOutLv := int16(MaxHeight)
			lowestNeiOut := -1
			for _, nei := range lowerNeis {
				out := w.Data[nei.x+nei.y*m.Width].OutPos
				if out > 0 && m.Data[out] < lowestNeiOutLv {
					lowestNeiOutLv = m.Data[out]
					lowestNeiOut = nei.x + nei.y*m.Width
				}
			}
			if lowestNeiOut > 0 {
				newIdx = lowestNeiOut
				newX, newY = newIdx%w.Width, newIdx/w.Width
			}
		}

		// 如果旧的地形低，但是积水高，则给旧地形沉淀
		catchDiff := w.Data[oldIdx].C - w.Data[newIdx].C
		if m.Data[oldIdx] < m.Data[newIdx] && w.Data[oldIdx].C > 0 && w.Data[oldIdx].C > w.Data[newIdx].C {
			c.debugf("pos(%d,%d to %d,%d) SEDIMENT. stay by nei is too high, t+c:%d+%d->%d+%d stay:%d", oldX, oldY, newX, newY, m.Data[oldIdx], w.Data[oldIdx].C, m.Data[newIdx], w.Data[newIdx].C, d.StayLong)
			c.water(oldX, oldY, -(catchDiff+1)/2)
			c.water(newX, newY, 1)
			c.topo(oldX, oldY, 1)
			return
		}

		// check if the newIdx is too high
		if int(m.Data[newIdx]+w.Data[newIdx].C) > int(m.Data[oldIdx]+w.Data[oldIdx].C) {
			if m.Data[oldIdx] > m.Data[newIdx] {
				c.water(oldX, oldY, 2)
			}
			return
		}

		d.StayLong = 0 // when moved, reset stayLong
		d.X, d.Y = float32(newX)+0.5, float32(newY)+0.5
		c.flow(oldIdx, newIdx)
		d.Journey++
		distQ := (newX-oldX)*(newX-oldX) + (newY-oldY)*(newY-oldY)

		if distQ > 2 { // 如果跑到远距离，则侵蚀中途
			middleX, middleY := (newX+oldX)/2, (newY+oldY)/2
			middleIdx := middleX + middleY*w.Width
			probe := m.Data[middleX] // 旧版写法，效果略好
			if c.s.p.Compat.FixMiddleIdx {
				probe = m.Data[middleIdx]
			}
			if probe > SeaLevel && m.Data[newIdx] < m.Data[middleIdx] {
				c.debugf("pos(%d,%d to %d,%d) will move to far lowest, but erode middle. t:%d->%d distQ:%d", oldX, oldY, middleX, middleY, m.Data[oldIdx], m.Data[middleIdx], distQ)
				c.topo(middleX, middleY, -1)
				d.TakeSand++
			}
		}

		if distQ < 2 || step&0xF < 7 { // < 7 解决了45度倾向性
			if m.Data[oldIdx] > SeaLevel && m.Data[oldIdx] > m.Data[newIdx] {
				// 旧版在此之前已经 q++，这里按加 1 后的流量判断
				if (w.Data[oldIdx].Q+1)&0x1 == 0 || m.Data[oldIdx] > m.MaxLv/2 {
					c.debugf("pos(%d,%d to %d,%d) will move to lowest and erode by nei. t:%d->%d distQ:%d", oldX, oldY, newX, newY, m.Data[oldIdx], m.Data[newIdx], distQ)
					c.topo(oldX, oldY, -1)
					d.TakeSand++
				}
				if w.Data[oldIdx].C > 0 {
					c.water(oldX, oldY, -1)
				}
			}
		}

		if m.Data[oldIdx]+w.Data[oldIdx].C > m.Data[newIdx]+w.Data[newIdx].C && w.Data[oldIdx].C > 0 {
			c.debugf("pos(%d,%d to %d,%d) will move catchment nei", oldX, oldY, newX, newY)
			c.water(oldX, oldY, -2)
			c.water(newX, newY, 2)
		}
		return
	}

	// 地形场加速 generate velo by field power
	d.VX, d.VY = d.VX+w.Data[oldIdx].XPower, d.VY+w.Data[oldIdx].YPower
	d.reduceSpeed()

	tmpX, tmpY := d.X+d.VX, d.Y+d.VY
	// 注意: 旧版此处 newX,newY 取的是移动前的坐标，下面以 newX,newY 发出的积水事件落在原格子上，保留该行为
	newX, newY = int(d.X), int(d.Y)

	// 越界判断 next step position edge cheking
	if tmpX < 0 || int(tmpX) > w.Width-1 || tmpY < 0 || int(tmpY) > w.Length-1 {
		return
	}

	newIdx = int(tmpX) + int(tmpY)*w.Width
	oldLv, newLv := m.Data[oldIdx], m.Data[newIdx]

	// 不跑到高处 包括积水 should not go to higher land
	if w.Data[newIdx].C+m.Data[newIdx] > w.Data[oldIdx].C+m.Data[oldIdx] {
		c.water(oldX, oldY, 2)
		c.debugf("pos(%d,%d to %d,%d) by velo is too high, stop, t+c:%d+%d->%d+%d stay:%d", oldX, oldY, newIdx%m.Width, newIdx/m.Width, oldLv, w.Data[oldIdx].C, newLv, w.Data[newIdx].C, d.StayLong)
		return
	}

	d.X, d.Y = tmpX, tmpY

	// 无力场，待在原地 TODO 需优化，呆在原地浪费算力
	if newIdx == oldIdx {
		if d.StayLong > 3 {
			_, neiHigh, _ := m.CountNeighbors(newX, newY)
			// 如果目标地方比周围低，则沉淀 // 会导致长链拖尾
			if m.Data[newIdx] < neiHigh && d.TakeSand >= 1 && w.Data[oldIdx].Q < 2 {
				c.debugf("pos(%d,%d) will stay:%d SEDIMENT. h:%d", oldX, oldY, d.StayLong, m.Data[oldIdx])
				c.topo(newX, newY, 1)
				d.TakeSand--
			}
		}
		if d.StayLong&0x7 == 1 {
			c.debugf("pos(%d,%d to %d,%d) will leave a catchment", oldX, oldY, newX, newY)
			c.water(newX, newY, 2+int16(step&1)) // 增加积水
		}
		return
	}

	// 移动到了新位置
	d.StayLong = 0
	c.flow(oldIdx, newIdx)
	d.Journey++

	if oldLv > newLv {
		// 上游地势高 此处要严格判断地势，不能考虑积水。上游积水造成地势高假象，会导致错误侵蚀。
		if w.EvtIdx&0x3 <= 1 || oldLv > m.MaxLv/2 {
			c.debugf("pos(%d,%d to %d,%d) ht:%d->%d will flow to lower and erode by velo.", oldX, oldY, newIdx%m.Width, newIdx/m.Width, oldLv, newLv)
			c.topo(oldX, oldY, -1)
			d.TakeSand++
		}
		if w.Data[oldIdx].C > 0 {
			c.water(oldX, oldY, -1)
		}
		c.qinc(oldIdx)
	} else if oldLv == newLv {
		// if water high -> low, need catchment
		if w.Data[oldIdx].C > 1 && w.Data[oldIdx].C > w.Data[newIdx].C {
			c.water(oldX, oldY, -2)
			c.water(newX, newY, 2)
		}
	} else {
		// m old is lower
		catchDiff := w.Data[oldIdx].C - w.Data[newIdx].C
		if w.Data[oldIdx].C > w.Data[newIdx].C {
			c.water(oldX, oldY, -(catchDiff+1)/2)
			c.water(newX, newY, 1)
			c.topo(oldX, oldY, 1)
		}
		c.debugf("pos(%d,%d to %d,%d) flew to upper land. catchDiff:%d", oldX, oldY, newIdx%m.Width, newIdx/m.Width, catchDiff)
	}
}

func (d *Droplet) neighbors(m *Topomap, maxRing int) []pos {
	dots := make([]pos, 0, 24)
	m.FindNeiDots(int(d.X), int(d.Y), maxRing, func(nx, ny, distSq int) {
		dots = append(dots, pos{nx, ny})
	})
	return dots
}

// 邻居中最低的点不高于本地水面
func (d *Droplet) hasLowestNei(m *Topomap, w *WaterMap, maxRing int) bool {
	idx := int(d.X) + int(d.Y)*m.Width
	lowestLv, lowest := w.Data[idx].getLowestDots(d.neighbors(m, maxRing), m)
	return len(lowest) > 0 && lowestLv <= m.Data[idx]+w.Data[idx].C
}

func (d *Droplet) findNeiLowerDots(m *Topomap, w *WaterMap, maxRing int) (int16, []pos) {
	dots := d.neighbors(m, maxRing)
	shuffleSlice(dots)
	return w.Data[int(d.X)+int(d.Y)*m.Width].getLowestDots(dots, m)
}

// 获取周围最低的点集合，按 arrNei 的顺序返回
func (d *WaterDot) getLowestDots(arrNei []pos, m *Topomap) (lowestLevel int16, lowestPos []pos) {
	lowestLevel = 20000
	for _, nei := range arrNei {
		// 邻居的高度 todo: 有BUG 此处不能加本地的水位 要加邻居的水位
		high := m.Data[nei.x+nei.y*m.Width] + d.C - int16(calcBit(d.Q))
		if high < lowestLevel {
			lowestLevel = high
			lowestPos = append(lowestPos[:0], nei)
		} else if high == lowestLevel {
			lowestPos = append(lowestPos, nei)
		}
	}
	return
}

func shuffleSlice[T any](arr []T) {
	for i := 0; i < len(arr)/2; i += 2 {
		j := len(arr) - 1 - i
		arr[i], arr[j] = arr[j], arr[i]
	}
}
