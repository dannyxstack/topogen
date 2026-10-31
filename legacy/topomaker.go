/*
	usage: ./topomaker.v8.exe --zoom 9 -h 10 -w 10 --hill 10 --hill-wide 5 --ridge 5 --ridge-len 8 --ridge-wide 5 --color-tpl-step 40 --stuck 2 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 10 --times 15 --seed 1685609509
	time ./topomaker.v8.exe --zoom 3 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 2 --dropnum 100000 --times 50 --seed 1685609509 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 100000 --times 50 --seed 1708692666 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 -h 800 -w 800 --hill 250 --hill-wide 200 --ridge 5 --ridge-len 20 --ridge-wide 100 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 200000 --times 80 --seed 1709090872 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 60000 --times 50 --seed 1708692666 --enlarge 1 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 45 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 50000 --times 50 --seed 1712497877 --enlarge 1 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 45 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 50000 --times 50 --seed 1679292301 --enlarge 1 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 1 -h 100 -w 100 --hill 250 --hill-wide 22 --ridge 5 --ridge-len 20 --ridge-wide 13 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 0 --dropnum 3125 --times 50 --enlarge 3 --seed 1709176764 >none.txt 2>&1; tail -n 5 none.txt
	time ./topomaker.v8.exe --zoom 5 --basemap 'd:/Documents/Pictures/xx.jpg' --basemap-tone r+ --dropnum 200000 --times 80 >base.txt 2>&1; tail -n 5 none.txt

todo: table lize with http server
  - parallel fill hills to topomap # done
  - triangle stuck, triangle hill, petalize hill # done
  - stuck support # done
  - revert hill, like well # done
  - ridge hills # done
  - river flow # done
  - river erode topomap # developing
  - make ridges like forks and strings
  - enhance the ridges beside edge of continent
*/
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"log"
	"math"
	randpkg "math/rand"
	"os"
	"sync"
	"time"
)

// 等高线文件模板 取垂直第一列的像素
var colorTplFile = "./image/color-tpl2.png"

const (
	RidgeHeightMedian   = 7   // ridge 高度中间数 在此基础上浮动
	HillHeightMedian    = 5   // hill 高度中间数 在此基础上浮动
	DropsCohesiveDistSq = 9.0 // drops凝聚力距离平方
	// 水滴之间的吸引力度 类似于万有引力常量
	AttractPowerDecay = 0.25
	// 距离平方超过这个值 就会被等比例缩减速度 但是保持方向
	MinDistToReduce = 0.9999
	//
	MaxStayLong = 20
	// 使用1w会出现maxLv=16382，并且绘制png出现tpl溢出.
	// MaxHeight 范围在255-10000之间。越大，需要越多的水滴来侵蚀.
	MaxHeight = 1000
)

const (
	// drawFlag 绘制参数
	DrawFlagField        = 1 << iota // 绘制地形落差场
	DrawFlagDroplet                  // 绘制水滴
	DrawFlagCatchment                // 绘制积水
	DrawFlagQflow                    // 绘制流量痕迹
	DrawFlagDebugDroplet             // 打印痕迹日志
	DrawFlagTaddC                    // topo+catchment 颜色取地形+积水
	DrawFlagTminusC                  // topo-catchment 颜色取地形-积水
)

const (
	SeaLevel = 2 // include 2
)

var rand *randpkg.Rand = randpkg.New(randpkg.NewSource(1))

var stayLongCount = 0

type Pos struct {
	x int
	y int
}

type Droplet struct {
	x        float32
	y        float32
	vx       float32 // 滑行速度
	vy       float32
	journey  int //走过的路程
	birthH   int16
	finalH   int16
	birthPos int // no use
	takeSand int //冲掉的沙子，带在身上，走到下坡的时候有可能放下
	stayLong uint8
}

// 将变成固定不移动 记录场 被流动水雕刻
type WaterDot struct {
	x      float32 // 将不变化,数量与Topomap.data一样多 =Topomap[x,y] +(0.5, 0.5)
	y      float32
	xPower float32 // x方向速度增益 场强度 v2 根据地形得出 初始化后不变(地形改变则会变) 基于Atan2 范围(-1,1)
	yPower float32 // y方向速度增益 场强度 v2 根据地形得出 初始化后不变(地形改变则会变)
	c      int16   // 积水高度，产生积水不参与流动，流动停止 // 应当由静止的Droplet转化而来 catchment
	q      int     // 流量 0=无 历史流量    //

	prePos int // 上一个位置
	outPos int //预期下游位置
}
type Topomap struct {
	data   []int16 // 对应坐标只保存高度
	width  int
	length int
	events chan *ErodeEvent
	evtIdx int
	maxLv  int16
	minLv  int16

	plusCnt  int
	minusCnt int
}
type WaterMap struct {
	data   []WaterDot //初始化与Topomap.data数量同样多。通过WaterDot.h++/--来操作水量流动
	width  int
	length int
	events chan *ErodeEvent
	evtIdx int
	qcount int
	maxC   int16
	minC   int16
	maxQ   int

	exchCnt  int
	plusCnt  int
	minusCnt int
}

// 预先处理每个点的场向量  只计算地势的影响，不考虑流量的影响 启动只执行1次
// 假设每个点都有一个场，计算出这个场的方向
// @param Topomap m is basic topomap
// @param int ring 表示计算到几环 默认2环
func (w *WaterMap) AssignVector(m *Topomap, ring int) {

	// 并行并没有明显提速
	// wg := &sync.WaitGroup{}
	// ch := make(chan *WaterDot, 100)

	// nCpu := 8
	// for i := 0; i < nCpu; i++ {
	// 	wg.Add(1)
	// 	go func() {
	// 		for curDot := range ch {
	// 			curDot.AssignVector2(w, m, ring)
	// 		}
	// 		wg.Done()
	// 	}()
	// }

	// go func() {
	// 	for _, curDot := range w.data {
	// 		ch <- &curDot
	// 	}
	// 	close(ch)
	// }()

	// wg.Wait()

	for _, curDot := range w.data {
		curDot.AssignVector(w, m, ring)
	}
	// log.Printf("AssignVector for field done.") //巨大耗时 需要优化
}

func (curDot *WaterDot) AssignVector(w *WaterMap, m *Topomap, ring int) {
	var xPower, yPower int // xPower, yPower 单位为1
	curX, curY := int(curDot.x), int(curDot.y)
	idx := int(curDot.x) + int(curDot.y)*w.width
	curLv := m.data[idx] + w.data[idx].c - int16(calcBit(w.data[idx].q))

	m.FindNeiDots(int(curDot.x), int(curDot.y), ring, func(nx, ny, distSq int) {
		neiLv := m.data[nx+ny*w.width] + w.data[nx+ny*w.width].c - int16(calcBit(w.data[nx+ny*w.width].q))
		if neiLv < curLv {
			// 优先考虑邻居低处的吸引力
			xPower += (nx - curX) * int(curLv-neiLv) * int(curLv-neiLv) * 10
			yPower += (ny - curY) * int(curLv-neiLv) * int(curLv-neiLv) * 10
		} else if neiLv == curLv {
		} else {
			// 邻居高的时候，提供一定的推力
			xPower += (curX - nx) * int(neiLv-curLv) * int(neiLv-curLv) * 2
			yPower += (curY - ny) * int(neiLv-curLv) * int(neiLv-curLv) * 2
		}
	})

	// 平坦时候 // 策略1: 朝地图周围走 2: 随机方向 3: 找下游 4: 按速度加速
	if xPower == 0 && yPower == 0 {
		xPower, yPower = (calcBit(curX-(m.width/2))+4)/4, (calcBit(curY-(m.length/2))+4)/4
		log.Printf("field no power, try flow to margin. x,y:%d,%d", curX, curY)
	}

	curDot.xPower, curDot.yPower = float32(xPower), float32(yPower)
	for (curDot.xPower*curDot.xPower + curDot.yPower*curDot.yPower) > MinDistToReduce {
		curDot.xPower, curDot.yPower = curDot.xPower*0.95, curDot.yPower*0.95
	}
}

func DropletsMove(times int, drops []*Droplet, m *Topomap, w *WaterMap, flag int) []*Droplet {
	//考虑分批降水
	pageSize := 400
	for pageIdx := 0; pageIdx*pageSize < len(drops); pageIdx++ {
		wg := &sync.WaitGroup{}
		for idx := pageIdx * pageSize; idx < (pageIdx+1)*pageSize && idx < len(drops); idx++ {
			wg.Add(1)
			//此处未考虑并发问题
			go func(d *Droplet, idx int) {
				for i := 1; i <= times; i++ {
					d.Move(m, w, drops, i, flag)
				}
				wg.Done()
			}(drops[idx], idx)

		}
		wg.Wait()
		// w.AssignVector(m, 2) //会增加5倍以上耗时
		w.CollectWater(m) // 会增加耗时 // 批次大小不会影响w.exchange效果
		// if pageIdx&3 == 1 {
		// 	m.FillLowlying() //会把河谷填平，不能操作 // 增加2倍以上耗时
		// }
	}
	return drops
}

func MakeDroplet(w *WaterMap, m *Topomap) *Droplet {
	rnd := rand.Int()
	idx := rnd % len(w.data)
	// 尝试让水滴朝赤道集中一半
	// if rnd&0xF == (0x4+0x2) {
	// 	idx = (idx >> 2) + len(w.data)/8 + len(w.data)/4
	// }

	d := Droplet{
		x:        float32(idx%w.width) + 0.5,
		y:        float32(idx/w.width) + 0.5,
		birthH:   m.data[idx],
		birthPos: idx,
	}

	w.data[idx].c++

	thedir := rand.Float64() * math.Pi * 2
	d.vx, d.vy = float32(math.Cos(thedir))/2, float32(math.Sin(thedir))/2
	return &d
}

func MakeWindDroplet(w *WaterMap, m *Topomap, maxLevel int) *Droplet {
	rnd := rand.Int()
	rndY0 := (rnd % w.length) * w.width
	d := Droplet{
		x: float32(rndY0%w.width) + 0.5,
		y: float32(rndY0/w.width) + 0.5,
		// fallPower: 2,
	}

	rnd2 := (rnd / 19) % 37

	for xi := 1; xi < m.width-1; xi++ {
		// 碰到高度大于一定数值就降落
		if rndY0+xi < m.width*m.length && m.data[rndY0+xi] >= int16(maxLevel/5+rnd2) {
			d.x += float32(xi)
			break
		}
	}
	// fix if they did not find a high enough point
	if d.x < 1 {
		d.x = float32((rand.Int()/29)%w.width) + 0.5
	}

	rndY0 = int(d.x) + int(d.y)*w.width

	d.birthH = m.data[rndY0]
	d.birthPos = rndY0
	w.data[rndY0].c++

	thedir := rand.Float64() * math.Pi * 2
	d.vx, d.vy = float32(math.Cos(thedir))/2, float32(math.Sin(thedir))/2
	return &d
}

func MakeDroplets(m *Topomap, w *WaterMap, dropNum, maxLevel int, drops []*Droplet) {
	for di := 0; di < dropNum; di++ {
		if di&0xF >= 2 {
			drops[di] = MakeDroplet(w, m)
		} else {
			drops[di] = MakeWindDroplet(w, m, maxLevel) // west wind drops
		}
	}
}

// FindNeiDots 原型区域搜索 相比FindNeiDotsOld，消除了一定的45度倾向性. ring=1的时候会由明显的45度倾向性，需要调用者处理 TODO: 需要倒序遍历
// traversal neighbors. maxRingth: 1=nei*8 2=nei*16 3=nei*24
func (m *Topomap) FindNeiDots(x, y, maxRingth int, iterFunc func(nx, ny, distSq int)) (iterCount int) {
	conditionalIter := func(xi, yi, xii, yii int) {
		if xii < 0 || yii < 0 || xii >= m.width || yii >= m.length || yii*m.width+xii >= len(m.data) {
			return
		}
		if xii == xi && yii == yi || (xii-xi)*(xii-xi)+(yii-yi)*(yii-yi) > maxRingth*maxRingth+1 {
			return
		}
		iterFunc(xii, yii, (xii-xi)*(xii-xi)+(yii-yi)*(yii-yi))
		iterCount++
	}
	iterNE := func(xi, yi int) {
		for xii := xi - maxRingth; xii <= xi+maxRingth; xii++ {
			for yii := yi - maxRingth; yii <= yi+maxRingth; yii++ {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterSE := func(xi, yi int) {
		for xii := xi - maxRingth; xii <= xi+maxRingth; xii++ {
			for yii := yi + maxRingth; yii >= yi-maxRingth; yii-- {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterNW := func(xi, yi int) {
		for xii := xi + maxRingth; xii >= xi-maxRingth; xii-- {
			for yii := yi - maxRingth; yii <= yi+maxRingth; yii++ {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterSW := func(xi, yi int) {
		for xii := xi + maxRingth; xii >= xi-maxRingth; xii-- {
			for yii := yi + maxRingth; yii >= yi-maxRingth; yii-- {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterWN := func(xi, yi int) {
		for yii := yi + maxRingth; yii >= yi-maxRingth; yii-- {
			for xii := xi - maxRingth; xii <= xi+maxRingth; xii++ {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterWS := func(xi, yi int) {
		for yii := yi - maxRingth; yii <= yi+maxRingth; yii++ {
			for xii := xi - maxRingth; xii <= xi+maxRingth; xii++ {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterEN := func(xi, yi int) {
		for yii := yi + maxRingth; yii >= yi-maxRingth; yii-- {
			for xii := xi + maxRingth; xii >= xi-maxRingth; xii-- {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}
	iterES := func(xi, yi int) {
		for yii := yi - maxRingth; yii <= yi+maxRingth; yii++ {
			for xii := xi + maxRingth; xii >= xi-maxRingth; xii-- {
				conditionalIter(xi, yi, xii, yii)
			}
		}
	}

	iterArray := []func(int, int){iterNE, iterSE, iterNW, iterSW, iterWN, iterWS, iterEN, iterES}
	iterArray[m.evtIdx&0x7](x, y) // choose iterFunc
	return
}

func (m *Topomap) Init(width int, length int) {
	m.data = make([]int16, width*length)
	m.width = width
	m.length = length
	m.events = make(chan *ErodeEvent, 10)
	// go m.eroding()
}
func (w *WaterMap) Init(width int, length int) {
	w.data = make([]WaterDot, width*length)
	w.width = width
	w.length = length
	// 把点的实际基点摆在中间
	for x := 0; x < w.width; x++ {
		for y := 0; y < w.length; y++ {
			w.data[x+y*w.width].x = float32(x) + 0.5
			w.data[x+y*w.width].y = float32(y) + 0.5
		}
	}
	w.events = make(chan *ErodeEvent, 10)
	// go w.eroding()
}

func (w *WaterMap) Clear() {
	for i := 0; i < w.width*w.length; i++ {
		w.data[i].c = 0
	}
}

// 放大地图 会耗时
func (m *Topomap) Enlarge() {
	for y := 0; y < m.length; y++ {
		for x := 0; x < m.width; x++ {
			m.data[x+y*m.width] *= 2
		}
	}
	newData := make([]int16, m.width*m.length*4)
	for y := 0; y < m.length; y++ {
		for x := 0; x < m.width; x++ {
			// if (x*17+y*13)&0x7 == 0 {
			// 	newData[x*2+y*2*m.width*2] = m.data[x+y*m.width] 						 // 直接赋值会导致锯齿效果，45度网格
			// 	newData[x*2+1+y*2*m.width*2] = m.data[x+y*m.width]           // 直接赋值会导致锯齿效果
			// 	newData[x*2+1+y*2*m.width*2+m.width*2] = m.data[x+y*m.width] // 直接赋值会导致锯齿效果
			// 	newData[x*2+y*2*m.width*2+m.width*2] = m.data[x+y*m.width]   // 直接赋值会导致锯齿效果
			// 	continue
			// }

			nwNeis := []struct{ x, y int }{{x - 1, y}, {x - 1, y - 1}, {x, y - 1}}
			neNeis := []struct{ x, y int }{{x, y - 1}, {x + 1, y - 1}, {x + 1, y}}
			seNeis := []struct{ x, y int }{{x + 1, y}, {x + 1, y + 1}, {x, y + 1}}
			swNeis := []struct{ x, y int }{{x, y + 1}, {x - 1, y + 1}, {x - 1, y}}
			// nwNeis := []struct{ x, y int }{{x - 1, y}, {x - 1, y}, {x - 1, y - 1}, {x, y - 1}, {x, y - 1}} // 增加中间点比重，效果可以;要结合平均数收敛
			// neNeis := []struct{ x, y int }{{x, y - 1}, {x, y - 1}, {x + 1, y - 1}, {x + 1, y}, {x + 1, y}} // 具有矩形倾向性
			// seNeis := []struct{ x, y int }{{x + 1, y}, {x + 1, y}, {x + 1, y + 1}, {x, y + 1}, {x, y + 1}}
			// swNeis := []struct{ x, y int }{{x, y + 1}, {x, y + 1}, {x - 1, y + 1}, {x - 1, y}, {x - 1, y}}
			// nwNeis := []struct{ x, y int }{{x - 1, y}, {x - 1, y}, {x - 1, y}, {x - 1, y}, {x - 1, y - 1}, {x - 1, y - 1}, {x - 1, y - 1}, {x, y - 1}, {x, y - 1}, {x, y - 1}, {x, y - 1}} // 3:4
			// neNeis := []struct{ x, y int }{{x, y - 1}, {x, y - 1}, {x, y - 1}, {x, y - 1}, {x + 1, y - 1}, {x + 1, y - 1}, {x + 1, y - 1}, {x + 1, y}, {x + 1, y}, {x + 1, y}, {x + 1, y}}
			// seNeis := []struct{ x, y int }{{x + 1, y}, {x + 1, y}, {x + 1, y}, {x + 1, y}, {x + 1, y + 1}, {x + 1, y + 1}, {x + 1, y + 1}, {x, y + 1}, {x, y + 1}, {x, y + 1}, {x, y + 1}}
			// swNeis := []struct{ x, y int }{{x, y + 1}, {x, y + 1}, {x, y + 1}, {x, y + 1}, {x - 1, y + 1}, {x - 1, y + 1}, {x - 1, y + 1}, {x - 1, y}, {x - 1, y}, {x - 1, y}, {x - 1, y}}
			// nwNeis := []struct{ x, y int }{{x - 1, y + 1}, {x - 1, y}, {x - 1, y - 1}, {x, y - 1}, {x + 1, y - 1}}
			// neNeis := []struct{ x, y int }{{x - 1, y - 1}, {x, y - 1}, {x + 1, y - 1}, {x + 1, y}, {x + 1, y + 1}}
			// seNeis := []struct{ x, y int }{{x + 1, y - 1}, {x + 1, y}, {x + 1, y + 1}, {x, y + 1}, {x - 1, y + 1}}
			// swNeis := []struct{ x, y int }{{x + 1, y + 1}, {x, y + 1}, {x - 1, y + 1}, {x - 1, y}, {x - 1, y - 1}}

			nwSum, nwCnt := m.data[x+y*m.width]*4, int16(1)*4
			for _, nei := range nwNeis {
				if nei.x >= 0 && nei.y >= 0 && nei.x < m.width && nei.y < m.length {
					newData[x*2+y*2*m.width*2] = (newData[x*2+y*2*m.width*2] + m.data[nei.x+nei.y*m.width]) / 2
					// newData[x*2+y*2*m.width*2] = (newData[x*2+y*2*m.width*2] + m.data[nei.x+nei.y*m.width] + m.data[x+y*m.width]) / 3
					nwSum += m.data[nei.x+nei.y*m.width]
					nwCnt++
				}
			}
			// newData[x*2+y*2*m.width*2] = (newData[x*2+y*2*m.width*2] + m.data[x+y*m.width]) / 2 //
			// newData[x*2+y*2*m.width*2] = (newData[x*2+y*2*m.width*2] + m.data[x+y*m.width]*2) / 3 //
			// newData[x*2+y*2*m.width*2] = max(newData[x*2+y*2*m.width*2], m.data[x+y*m.width]) //方块感明显
			newData[x*2+y*2*m.width*2] = min((newData[x*2+y*2*m.width*2]+m.data[x+y*m.width])/2+1, m.data[x+y*m.width]) //效果挺好，但是有加剧45度倾向性的嫌疑 某些方向的线条锯齿厉害
			// newData[x*2+y*2*m.width*2] = nwSum / nwCnt

			neSum, neCnt := m.data[x+y*m.width]*4, int16(1)*4
			for _, nei := range neNeis {
				if nei.x >= 0 && nei.y >= 0 && nei.x < m.width && nei.y < m.length {
					newData[x*2+1+y*2*m.width*2] = (newData[x*2+1+y*2*m.width*2] + m.data[nei.x+nei.y*m.width]) / 2
					// newData[x*2+1+y*2*m.width*2] = (newData[x*2+1+y*2*m.width*2] + m.data[nei.x+nei.y*m.width] + m.data[x+y*m.width]) / 3
					neSum += m.data[nei.x+nei.y*m.width]
					neCnt++
				}
			}
			// newData[x*2+1+y*2*m.width*2] = (newData[x*2+1+y*2*m.width*2] + m.data[x+y*m.width]) / 2 //
			// newData[x*2+1+y*2*m.width*2] = (newData[x*2+1+y*2*m.width*2] + m.data[x+y*m.width]*2) / 3 //
			// newData[x*2+1+y*2*m.width*2] = max(newData[x*2+1+y*2*m.width*2], m.data[x+y*m.width]) //
			newData[x*2+1+y*2*m.width*2] = min((newData[x*2+1+y*2*m.width*2]+m.data[x+y*m.width])/2+1, m.data[x+y*m.width]) //
			// newData[x*2+1+y*2*m.width*2] = neSum / neCnt

			seSum, seCnt := m.data[x+y*m.width]*4, int16(1)*4
			for _, nei := range seNeis {
				if nei.x >= 0 && nei.y >= 0 && nei.x < m.width && nei.y < m.length {
					newData[x*2+1+y*2*m.width*2+m.width*2] = (newData[x*2+1+y*2*m.width*2+m.width*2] + m.data[nei.x+nei.y*m.width]) / 2
					// newData[x*2+1+y*2*m.width*2+m.width*2] = (newData[x*2+1+y*2*m.width*2+m.width*2] + m.data[nei.x+nei.y*m.width] + m.data[x+y*m.width]) / 3
					seSum += m.data[nei.x+nei.y*m.width]
					seCnt++
				}
			}
			// newData[x*2+1+y*2*m.width*2+m.width*2] = (newData[x*2+1+y*2*m.width*2+m.width*2] + m.data[x+y*m.width]) / 2 // mean of we four
			// newData[x*2+1+y*2*m.width*2+m.width*2] = (newData[x*2+1+y*2*m.width*2+m.width*2] + m.data[x+y*m.width]*2) / 3 // mean of we four
			// newData[x*2+1+y*2*m.width*2+m.width*2] = max(newData[x*2+1+y*2*m.width*2+m.width*2], m.data[x+y*m.width]) //
			newData[x*2+1+y*2*m.width*2+m.width*2] = min((newData[x*2+1+y*2*m.width*2+m.width*2]+m.data[x+y*m.width])/2+1, m.data[x+y*m.width]) //
			// newData[x*2+1+y*2*m.width*2+m.width*2] = seSum / seCnt

			swSum, swCnt := m.data[x+y*m.width]*4, int16(1)*4
			for _, nei := range swNeis {
				if nei.x >= 0 && nei.y >= 0 && nei.x < m.width && nei.y < m.length {
					newData[x*2+y*2*m.width*2+m.width*2] = (newData[x*2+y*2*m.width*2+m.width*2] + m.data[nei.x+nei.y*m.width]) / 2
					// newData[x*2+y*2*m.width*2+m.width*2] = (newData[x*2+y*2*m.width*2+m.width*2] + m.data[nei.x+nei.y*m.width] + m.data[x+y*m.width]) / 3
					swSum += m.data[nei.x+nei.y*m.width]
					swCnt++
				}
			}
			// newData[x*2+y*2*m.width*2+m.width*2] = (newData[x*2+y*2*m.width*2+m.width*2] + m.data[x+y*m.width]) / 2 //
			// newData[x*2+y*2*m.width*2+m.width*2] = (newData[x*2+y*2*m.width*2+m.width*2] + m.data[x+y*m.width]*2) / 3 //
			// newData[x*2+y*2*m.width*2+m.width*2] = max(newData[x*2+y*2*m.width*2+m.width*2], m.data[x+y*m.width]) //
			newData[x*2+y*2*m.width*2+m.width*2] = min((newData[x*2+y*2*m.width*2+m.width*2]+m.data[x+y*m.width])/2+1, m.data[x+y*m.width]) //
			// newData[x*2+y*2*m.width*2+m.width*2] = swSum / swCnt
		}
	}
	m.data = newData
	m.width, m.length = m.width*2, m.length*2
}
func (w *WaterMap) Enlarge() {
	w.data = make([]WaterDot, w.width*w.length*4)
	w.width, w.length = w.width*2, w.length*2
	// 把点的实际基点摆在中间
	for x := 0; x < w.width; x++ {
		for y := 0; y < w.length; y++ {
			w.data[x+y*w.width].x = float32(x) + 0.5
			w.data[x+y*w.width].y = float32(y) + 0.5
		}
	}
}

// 改变地形的事件 异步化 不并行处理 防止并行修改计算错误
// 只更新WaterMap中的场 不更新里面的坐标
// 如果下游有落差，并且和上游也有落差，才侵蚀上游
// 先找邻居中具有最低流出预计高度的邻居；如果邻居平等，则按照场强度选择
func (d *Droplet) Move(m *Topomap, w *WaterMap, drops []*Droplet, step, flag int) {
	// check edge; useless
	oldX, oldY := int(d.x), int(d.y)
	oldIdx := int(d.x) + int(d.y)*w.width
	if oldIdx >= len(w.data) || oldIdx < 0 {
		log.Printf("oldIdx(%d) out of w.data. stop it.", oldIdx) // never happen
		return
	}

	// check sea level
	if m.data[oldIdx] < SeaLevel {
		return
	}

	if d.stayLong > MaxStayLong {
		// log.Printf("stop (%d,%d) because stayLong:%d.", int(d.x), int(d.y), d.stayLong)
		stayLongCount++
		if d.takeSand > 0 {
			// TODO 必须是本地最低才能沉淀
			// neiLv, _ := d.FindNeiLowerDots(m, w, 1)
			// if neiLv < m.data[oldIdx] {
			// if neiLv >= m.data[oldIdx] {
			if d.stayLong&0x1 == 0 {
				m.EmitErodeEvents(oldX, oldY, w, d, 1, m.data[int(d.x)+int(d.y)*w.width]+1)
				log.Printf("make sediment for stayLong exceeded (%d,%d) stayLong:%d.", int(d.x), int(d.y), d.stayLong)
				d.takeSand -= 1
			}
		}
		return
	}

	if d.stayLong&0x7 == 2 {
		// stop moving when stay too long // TODO: transfer to WaterDot.h
		// log.Printf("pos(%d,%d) will convert as catchment, stay", oldX, oldY)
		w.EmitErodeEvents(oldX, oldY, m, d, 2+int16(step&0x1)) //增加积水
		if d.takeSand > 1 && w.data[oldIdx].q < 2 {
			// if d.takeSand > 0 {
			// sediment here
			log.Printf("pos(%d,%d) stay too long:%d SEDIMENT. t:%d", oldX, oldY, d.stayLong, m.data[oldIdx])
			m.EmitErodeEvents(oldX, oldY, w, d, 1, m.data[int(d.x)+int(d.y)*w.width]+1) //增加沉淀
			d.takeSand -= 1
			d.stayLong++
			return
		}
	}

	d.stayLong++
	newIdx, newX, newY := 0, 0, 0

	// find the lowest neighbor
	lowestNeis := d.FindNeiLowestDots(m, w, 1) // 属于find highest q neighbor 分支 2 会导致天坑
	//bx1111:1536/10000, bxF:3750/10000; 0x1111->0xF
	if step&0x7 <= 5 && len(lowestNeis) > 0 {
		// 从邻居中寻找潜在的流出最低的下游; 效果好于上面的代码; 无45度倾向性
		lowestLv, lowestNeis := d.FindNeiLowerDots(m, w, 2) //如果提取到if外层，会导致天坑
		if len(lowestNeis) == 0 {
			log.Printf("cannot find lowest neighbor, stay. t:%d oldPos:%d,%d", m.data[oldIdx], oldX, oldY)
			return
		}

		newPos := lowestNeis[(step*7)%len(lowestNeis)]
		newX, newY = newPos.x, newPos.y
		newIdx = newX + newY*w.width

		if lowestLv == m.data[oldIdx] {
			// find out the neighbor with lowest out level
			lowestNeiOutLv := int16(MaxHeight)
			lowestNeiOut := -1
			for _, nei := range lowestNeis {
				if w.data[nei.x+nei.y*m.width].outPos > 0 {
					if m.data[w.data[nei.x+nei.y*m.width].outPos] < lowestNeiOutLv {
						lowestNeiOutLv = m.data[w.data[nei.x+nei.y*m.width].outPos]
						lowestNeiOut = nei.x + nei.y*m.width
					}
				}
			}
			if lowestNeiOut > 0 {
				newIdx = lowestNeiOut
				newX, newY = newIdx%w.width, newIdx/w.width
			}
		}

		// 如果旧的地形低，但是积水高，则给旧地形沉淀
		catchDiff := w.data[oldIdx].c - w.data[newIdx].c
		if m.data[oldIdx] < m.data[newIdx] && w.data[oldIdx].c > 0 && w.data[oldIdx].c > w.data[newIdx].c {
			log.Printf("pos(%d,%d to %d,%d) SEDIMENT. stay by nei is too high, t+c:%d+%d->%d+%d stay:%d", int(d.x), int(d.y), newX, newY, m.data[oldIdx], w.data[oldIdx].c, m.data[newIdx], w.data[newIdx].c, d.stayLong)
			w.EmitErodeEvents(oldX, oldY, m, d, -(catchDiff+1)/2)
			w.EmitErodeEvents(newX, newY, m, d, 1)
			m.EmitErodeEvents(oldX, oldY, w, d, 1, m.data[oldIdx]+1) // experimental 22129/20000
			return
		}

		// check if the newIdx is too high
		if int(m.data[newIdx]+w.data[newIdx].c) > int(m.data[oldIdx]+w.data[oldIdx].c) {
			if m.data[oldIdx] > m.data[newIdx] {
				w.EmitErodeEvents(oldX, oldY, m, d, 2)
			}
			// log.Printf("pos(%d,%d to %d,%d) by nei is too high, stop, t+c:%d+%d->%d+%d stay:%d", int(d.x), int(d.y), newX, newY, m.data[oldIdx], w.data[oldIdx].c, m.data[newIdx], w.data[newIdx].c, d.stayLong)
			return
		}
		// risk: inlet and outlet will be same
		// get new position
		d.stayLong = 0                                  // when moved, reset stayLong
		d.x, d.y = float32(newX)+0.5, float32(newY)+0.5 //float32(newIdx%w.width)+0.5, float32(newIdx/w.width)+0.5
		w.data[oldIdx].q++                              // 流出，才算流量
		w.qcount++
		d.journey++
		w.data[oldIdx].outPos = newIdx
		w.data[newIdx].prePos = oldIdx
		distQ := (newX-oldX)*(newX-oldX) + (newY-oldY)*(newY-oldY)

		if distQ > 2 { // 如果跑到远距离，则侵蚀中途 该效果非常好，15000个drop效果很好 后续考虑快步移动而侵蚀中途
			middleX, middleY := (newX+oldX)/2, (newY+oldY)/2
			middleIdx := middleX + middleY*w.width
			// 此修复有争议 导致地图整体高度被侵蚀低了
			// if m.data[middleIdx] > SeaLevel && m.data[newIdx] < m.data[middleIdx] { // 优化后，导致少量天坑
			if m.data[middleX] > SeaLevel && m.data[newIdx] < m.data[middleIdx] { // 原始错误版本,但效果略好
				log.Printf("pos(%d,%d to %d,%d) will move to far lowest, but erode middle. t:%d->%d distQ:%d", oldX, oldY, middleX, middleY, m.data[oldIdx], m.data[middleIdx], distQ) // 726731/15000
				m.EmitErodeEvents(middleX, middleY, w, d, -1, m.data[middleIdx]-1)
				d.takeSand++
			}
		}

		if distQ < 2 || step&0xF < 7 { // < 7 解决了45度倾向性
			if m.data[oldIdx] > SeaLevel && m.data[oldIdx] > m.data[newIdx] {
				if w.data[oldIdx].q&0x1 == 0 || m.data[oldIdx] > m.maxLv/2 {
					log.Printf("pos(%d,%d to %d,%d) will move to lowest and erode by nei. t:%d->%d distQ:%d", oldX, oldY, newX, newY, m.data[oldIdx], m.data[newIdx], distQ)
					m.EmitErodeEvents(oldX, oldY, w, d, -1, m.data[oldIdx]-1) //m.data[oldIdx]--
					d.takeSand++
				}
				if w.data[oldIdx].c > 0 {
					w.EmitErodeEvents(oldX, oldY, m, d, -1)
				}
			}
		}

		if m.data[oldIdx]+w.data[oldIdx].c > m.data[newIdx]+w.data[newIdx].c {
			if w.data[oldIdx].c > 0 {
				log.Printf("pos(%d,%d to %d,%d) will move catchment nei", oldX, oldY, newX, newY)
				w.EmitErodeEvents(oldX, oldY, m, d, -2)
				w.EmitErodeEvents(newX, newY, m, d, 2)
			}
		}
		return
	}

	// 地形场加速 generate velo by field power
	d.vx, d.vy = (d.vx + w.data[oldIdx].xPower), (d.vy + w.data[oldIdx].yPower)

	// 将超出的速度限制成标准速度 reduce velo if velo is too big
	d.ReduceSpeed()

	// 场速度与自身速度的平均值 calculate next step position
	tmpX, tmpY := d.x+d.vx, d.y+d.vy // todo:精度损失风险
	newX, newY = int(d.x), int(d.y)

	// 越界判断 next step position edge cheking
	if tmpX < 0 || int(tmpX) > w.width-1 || tmpY < 0 || int(tmpY) > w.length-1 {
		// log.Printf("flow by velo will go out of bound(x=%f,y=%f). stop move. stay:%d", tmpX, tmpY, d.stayLong)
		return
	}

	// calc next idx
	newIdx = int(tmpX) + int(tmpY)*w.width
	oldLv, newLv := m.data[oldIdx], m.data[newIdx]

	// 不跑到高处 包括积水 should not go to higher land
	if w.data[newIdx].c+m.data[newIdx] > w.data[oldIdx].c+m.data[oldIdx] {
		w.EmitErodeEvents(oldX, oldY, m, d, 2) // 675664/30000; 277984/20000
		// if oldLv > newLv {
		// }
		log.Printf("pos(%d,%d to %d,%d) by velo is too high, stop, t+c:%d+%d->%d+%d stay:%d", int(d.x), int(d.y), newIdx%m.width, newIdx/m.width, oldLv, w.data[oldIdx].c, newLv, w.data[newIdx].c, d.stayLong)
		return
	}

	// log.Printf("dot(%d) moved, pos(%d,%d to %d,%d) (%.2f,%.2f to %.2f,%.2f) h:%d->%d v:%.1f,%.1f", d.birthPos, oldIdx%w.width, oldIdx/w.width, newIdx%w.width, newIdx/w.width, d.x, d.y, tmpX, tmpY, m.data[oldIdx], m.data[newIdx], d.vx, d.vy)
	d.x, d.y = tmpX, tmpY

	// 无力场，待在原地 TODO 需优化，呆在原地浪费算力
	if newIdx == oldIdx {
		neiHigh := 0
		log.Printf("pos:%.0f,%.0f no field power. stay:%d v:%.2f,%.2f flag=%d s=%d ht=%d neih=%d", d.x, d.y, d.stayLong, d.vx, d.vy, flag, d.takeSand, m.data[oldIdx], neiHigh) // 29236/20000
		if d.stayLong > 3 {
			_, neiHigh, _ := m.CountNeighbors(int(newX), int(newY))
			// 如果目标地方比周围低，则沉淀 // 会导致长链拖尾
			if m.data[newIdx] < neiHigh && d.takeSand >= 1 && w.data[oldIdx].q < 2 { //v5
				log.Printf("pos(%d,%d) will stay:%d SEDIMENT. h:%d", oldX, oldY, d.stayLong, m.data[oldIdx]) // count: 181/50000
				m.EmitErodeEvents(newX, newY, w, d, 1, m.data[newIdx]+1)                                     // m.data[newIdx]++ // 量很少
				d.takeSand -= 1                                                                              // v6
			}
		}

		if d.stayLong&0x7 == 1 {
			log.Printf("pos(%d,%d to %d,%d) will leave a catchment", oldX, oldY, newX, newY) // 74722/30000
			w.EmitErodeEvents(newX, newY, m, d, 2+int16(step&1))                             //增加积水
		}

		return
	}

	// 移动到了新位置
	d.stayLong = 0     // when moved, reset stayLong
	w.data[oldIdx].q++ // 流出，才算流量
	w.qcount++
	d.journey++
	w.data[oldIdx].outPos = newIdx
	w.data[newIdx].prePos = oldIdx

	//如果上游比新地方高 移动到低处

	if oldLv > m.data[newIdx] { //v5 上游地势高 此处要严格判断地势，不能考虑积水。上游积水造成地势高假象，会导致错误侵蚀。
		// log.Printf("pos(%d,%d to %d,%d) flew to lower. h:%d->%d", oldX, oldY, newX, newY, oldLv, m.data[newIdx])
		//侵蚀上游，带上沙子			//必须放在此处才生效，放在m.eroding中不生效。。。
		if w.evtIdx&0x3 <= 1 || oldLv > m.maxLv/2 {
			// if oldLv > m.maxLv/2 {
			log.Printf("pos(%d,%d to %d,%d) ht:%d->%d will flow to lower and erode by velo.", oldX, oldY, newX, newY, m.data[oldIdx], m.data[newIdx])
			m.EmitErodeEvents(oldX, oldY, w, d, -1, oldLv-1) //m.data[oldIdx]--
			d.takeSand++
		}
		if w.data[oldIdx].c > 0 {
			w.EmitErodeEvents(oldX, oldY, m, d, -1)
		}
		w.data[oldIdx].q++
	} else if oldLv == m.data[newIdx] {
		log.Printf("pos(%d,%d to %d,%d) flew horizontally", oldX, oldY, newX, newY)
		// if water high -> low, need catchment
		if w.data[oldIdx].c > 1 && w.data[oldIdx].c > w.data[newIdx].c {
			w.EmitErodeEvents(oldX, oldY, m, d, -2)
			w.EmitErodeEvents(newX, newY, m, d, 2)
		}
	} else {
		// m old is lower
		catchDiff := w.data[oldIdx].c - w.data[newIdx].c
		// if w.data[oldIdx].c > 0 && w.data[oldIdx].c > w.data[newIdx].c {
		if w.data[oldIdx].c > w.data[newIdx].c {
			w.EmitErodeEvents(oldX, oldY, m, d, -(catchDiff+1)/2)
			w.EmitErodeEvents(newX, newY, m, d, 1)
			m.EmitErodeEvents(oldX, oldY, w, d, 1, oldLv+1) // 该部分在r6中填平了盆地
		}
		log.Printf("pos(%d,%d to %d,%d) flew to upper land. catchDiff:%d", oldX, oldY, newX, newY, catchDiff)
	}

	// if m.data[oldIdx]+w.data[oldIdx].c > m.data[newIdx]+w.data[newIdx].c {
	// 	// 效果一般，还会造成少量天坑
	// 	catchDiff := w.data[oldIdx].c - w.data[newIdx].c
	// 	if catchDiff > 1 {
	// 		log.Printf("pos(%d,%d to %d,%d) will move catchment with diff:%d", oldX, oldY, newX, newY, catchDiff)
	// 		w.EmitErodeEvents(oldX, oldY, m, d, -catchDiff/2)
	// 		w.EmitErodeEvents(newX, newY, m, d, catchDiff/2)
	// 	}
	// }
}

func (d *Droplet) FindNeiLowestDots(m *Topomap, w *WaterMap, maxRingth int) (dots []int) {
	dotst := make([]struct{ x, y int }, 0) // 有序dots
	m.FindNeiDots(int(d.x), int(d.y), maxRingth, func(nx, ny, distSq int) {
		dotst = append(dotst, struct{ x, y int }{nx, ny})
	})
	lowestLv, lowestPoses := w.data[int(d.x)+int(d.y)*m.width].getLowestDots(dotst, m)
	if lowestLv > m.data[int(d.x)+int(d.y)*m.width]+w.data[int(d.x)+int(d.y)*m.width].c {
		return
	}
	for _, neiPos := range lowestPoses {
		dots = append(dots, neiPos.x+neiPos.y*m.width)
	}
	return
}

func (d *Droplet) FindNeiLowerDots(m *Topomap, w *WaterMap, maxRingth int) (lv int16, dots []struct{ x, y int }) {
	dotst := make([]struct{ x, y int }, 0) // 有序dots
	m.FindNeiDots(int(d.x), int(d.y), maxRingth, func(nx, ny, distSq int) {
		dotst = append(dotst, struct{ x, y int }{nx, ny})
	})
	shuffleSlice(dotst)
	return w.data[int(d.x)+int(d.y)*m.width].getLowestDots(dotst, m)
}

/*获取周围最低的点 最低点集合数组中随机取一个 返回安全的坐标，不在地图外*/
func (d *WaterDot) getLowestDots(arrNei []struct{ x, y int }, m *Topomap) (lowestLevel int16, lowestPos []struct{ x, y int }) {
	// 原理： highMap[high] = []struct{x,y}
	highMap := make(map[int16][]struct{ x, y int }, 8)
	for _, nei := range arrNei {
		if nei.x < 0 || nei.x > m.width-1 || nei.y < 0 || nei.y > m.length-1 {
			// 超出地图边界的点
			continue
		}
		// 邻居的高度 todo: 有BUG 此处不能加本地的水位 要加邻居的水位
		// high := (m.data[nei.x+nei.y*m.width]) // 效果最好
		// high := (m.data[nei.x+nei.y*m.width]) + d.c // 效果很好
		// high := (m.data[nei.x+nei.y*m.width]) - calcBit(d.c) - int16(calcBit(d.q)) // experimental
		// high := (m.data[nei.x+nei.y*m.width]) - int16(calcBit(d.q)) // 效果还行
		high := (m.data[nei.x+nei.y*m.width]) + d.c - int16(calcBit(d.q)) // experimental 效果不好，出现模糊和天坑 导致大量内流区 效果已改善
		// if d.q&0x1 == 1 {
		// 	high = (m.data[nei.x+nei.y*m.width]) // mix
		// }

		if len(highMap[high]) == 0 {
			highMap[high] = []struct{ x, y int }{{nei.x, nei.y}} //make([]struct{ x, y int }, 1)
			//highMap[int(m.data[nei.x+nei.y*w.width])][0].x, highMap[int(m.data[nei.x+nei.y*w.width])][0].y = nei.x, nei.y
		} else {
			highMap[high] = append(highMap[high], struct{ x, y int }{nei.x, nei.y})
		}
	}
	lowestLevel = 20000
	for k, _ := range highMap {
		if k < lowestLevel {
			lowestLevel = k
		}
	}
	//log.Println("lowest,highMap,count(highMap),d=", lowest, highMap, len(highMap), *d)
	if len(highMap[lowestLevel]) == 0 {
		log.Printf("how could len(highMap[lowestLevel]) be zero? x,y:%d,%d lowest:%d", int(d.x), int(d.y), lowestLevel)
		return lowestLevel, nil
	}
	return lowestLevel, highMap[lowestLevel]
}

type ErodeEvent struct {
	// idx      int
	x        int
	y        int
	plus     int16 //plus > 0 表示沉淀
	expectLv int16
	m        *Topomap
	w        *WaterMap
	drop     *Droplet
}

// plus>0 表示沉淀 plus<0 表示侵蚀
func (m *Topomap) EmitErodeEvents(x, y int, w *WaterMap, drop *Droplet, plus, expectLv int16) {
	m.events <- &ErodeEvent{x: x, y: y, m: m, w: w, drop: drop, plus: plus, expectLv: expectLv}
}

func (w *WaterMap) EmitErodeEvents(x, y int, m *Topomap, drop *Droplet, plus int16) {
	w.events <- &ErodeEvent{x: x, y: y, m: m, w: w, drop: drop, plus: plus}
}

func (m *Topomap) StartEroding() {
	go m.eroding()
}

func (w *WaterMap) StartEroding() {
	go w.eroding()
}

func (m *Topomap) eroding() {
	for {
		select {
		case e := <-m.events:
			m.evtIdx++
			idx := e.x + e.y*m.width
			if e.plus > 0 {
				if m.data[idx] < e.expectLv {
					log.Printf("evt sediment idx:%d,%d h+1: %d->%d", e.x, e.y, m.data[idx], e.expectLv)
					m.data[idx] = e.expectLv
					m.plusCnt++
				}
			} else {
				if m.data[idx] > e.expectLv {
					log.Printf("evt eroding idx:%d,%d h-1: %d->%d", e.x, e.y, m.data[idx], e.expectLv)
					m.data[idx] = e.expectLv
					m.minusCnt++
					// 增加 25% 的几率将侵蚀周围 加宽河谷
					if m.evtIdx&0x3 == 1 && m.data[idx] > SeaLevel { // 有或没有效果都挺好，有了会让河谷更宽
						m.FindNeiDots(e.x, e.y, 2, func(nx, ny, distQ int) {
							// log.Printf("nei diff from %d,%d to %d,%d is %d", e.x, e.y, xi, yi, diff)
							// if distQ < 2 || m.evtIdx&0x1111 < 4 { // 解决45度倾向性问题
							if distQ < 2 || m.evtIdx&0xF < 4 { // 解决45度倾向性问题
								if m.data[nx+ny*m.width] > e.expectLv+1 {
									// m.data[xi+yi*m.width] += int16(m.evtIdx) % 2 // 1
									m.data[nx+ny*m.width] -= 1
									log.Printf("erode nei idx:%d,%d nei %d,%d h-1 >:%d distQ:%d", e.x, e.y, nx, ny, m.data[nx+ny*m.width], distQ)
								}
							}
						})
					}
					// TODO 侵蚀后，应该吸引给周围水滴
				}
			}
		}
	}
}
func (w *WaterMap) eroding() {
	for {
		select {
		case e := <-w.events:
			w.evtIdx++
			idx := e.x + e.y*w.width
			w.data[idx].c += e.plus
			if e.plus > 0 {
				w.plusCnt++
			} else {
				w.minusCnt++
			}
			if e.m.data[idx] <= SeaLevel {
				w.data[idx].c -= 2 // 减掉太多会导致w.minC太小 -10-> -1800
			}
			if w.evtIdx&0x1 == 1 {
				w.data[idx].AssignVector(w, e.m, 2) //v8
			}
		}
	}
}

func (w *WaterMap) CollectWater(m *Topomap) {
	//期望会挖断路桥 效果可以 会让水聚集在地处；但是不会挖断路桥; 会造成碎石边缘效果
	for idx := 0; idx < w.width*w.length; idx++ {
		if m.data[idx] >= SeaLevel {
			m.FindNeiDots(int(w.data[idx].x), int(w.data[idx].y), 2, func(nx, ny, distQ int) {
				catchDiff := (m.data[nx+ny*m.width] + w.data[nx+ny*m.width].c) - (m.data[idx] + w.data[idx].c)
				// catchDiff := (m.data[nx+ny*m.width] + w.data[nx+ny*m.width].c - int16(calcBit(w.data[nx+ny*m.width].q))) - (m.data[idx] + w.data[idx].c - int16(calcBit(w.data[idx].q)))
				// catchDiff := w.data[nx+ny*m.width].c - w.data[idx].c
				// topoDiff := m.data[nx+ny*m.width] - m.data[idx]
				// if w.data[nx+ny*m.width].c > 0 && catchDiff > 1 { // 如果发现邻居水多，则和邻居交换以持平
				if catchDiff > 1 && w.data[nx+ny*m.width].q > 0 { // 如果发现邻居水多，则和邻居交换以持平 q>0 很重要，否则会无差别荡平 抽水不干净，可能与q有关 交换稍微少
					// if catchDiff > 1 && topoDiff >= 0 { // 如果发现邻居水多，则和邻居交换以持平 运行量大 会从沿海朝内陆扫荡，沿海地形细节消失
					// if catchDiff > 1 && w.data[idx].q > 0 { // 如果发现邻居水多，则和邻居交换以持平 q>0 很重要，否则会无差别荡平 河谷模糊 交换稍微多
					log.Printf("collect w.exchange: %d -> %d c: %d->%d distQ:%d diff:%d evtId:%d,%d", idx, nx+ny*m.width, w.data[nx+ny*m.width].c, w.data[idx].c, distQ, catchDiff, m.evtIdx, w.evtIdx)
					outflow := catchDiff / 2 // catchDiff/3 会导致水源不足
					if m.data[idx] == SeaLevel {
						outflow = 0
					}
					w.data[idx].c, w.data[nx+ny*m.width].c = w.data[idx].c+outflow, w.data[nx+ny*m.width].c-catchDiff/2 // 换平 速度慢一点点，但是效果还行
					// w.data[idx].c, w.data[nx+ny*m.width].c = w.data[idx].c+outflow, w.data[nx+ny*m.width].c-catchDiff // 交换水量 会导致扫平出大量平原 近海溶解了，高原还是流不下去
					// w.data[idx].c, w.data[nx+ny*m.width].c = w.data[idx].c+2, w.data[nx+ny*m.width].c-2 // 效果不明显
					// w.data[xi+yi*m.width].AssignVector(w, m, 2) //会增加耗时
					w.exchCnt++
					// if (distQ == 1 || distQ == 2 && (w.evtIdx+m.evtIdx)&0x7 < 5) && w.evtIdx&0x3 <= 1 && m.data[idx] > SeaLevel {
					// if (distQ == 1 || distQ == 2 && (w.evtIdx+m.evtIdx)&0x7 < 6) && m.data[idx] > SeaLevel {
					if w.evtIdx&0x3 <= 1 && m.data[idx] > SeaLevel { //同样dropnum下，此分支侵蚀更多
						if m.data[nx+ny*m.width] > m.data[idx] { // 会由海洋向内陆侵蚀，不会形成河道
							m.data[nx+ny*m.width]-- // 如果放入Emit事件中，拓宽下河谷，可能更好 侵蚀比Emit多
							// m.EmitErodeEvents(nx, ny, w, nil, -1, m.data[nx+ny*m.width]-1) // Emit后河谷更宽更柔和 TODO 需要优化，考虑周围地形
							w.data[nx+ny*m.width].q++
							if distQ > 2 { // 如果是远距离的，将途中的点也侵蚀 以防止天坑 效果可以
								middleDotX, middleDotY := w.data[idx].x+(w.data[nx+ny*m.width].x-w.data[idx].x)/2, w.data[idx].y+(w.data[nx+ny*m.width].y-w.data[idx].y)/2
								middleIdx := int(middleDotX) + int(middleDotY)*m.width
								if m.data[middleIdx] > m.data[idx] {
									// m.data[middleIdx]-- // 如果放入Emit事件中，拓宽下河谷，可能更好 侵蚀比Emit多
									m.EmitErodeEvents(int(middleDotX), int(middleDotY), w, nil, -1, m.data[middleIdx]-1)
								}
							}
						} else {
							// m.data[idx]-- //可能挖出天坑
						}
					}
				}
			})
			// 逐层提高SeaLevel来演绎，可能效果更好
			// w.data[idx].AssignVector(w, m, 1) //会增加2倍耗时
			// 目前问题: 积水不能冲出低矮边缘，会导致内流区;
			// 当w.maxC 接近m.maxLv时候，会导致侵蚀效果很差。因为地形落差被水量淹没，流动性差
		}
	}
}

// 获取某个点周围的最高值和最低值
func (m *Topomap) CountNeighbors(x, y int) (low, high, higherCount int16) {
	neis := []struct{ x, y int }{
		{x - 1, y - 1}, {x, y - 1}, {x + 1, y - 1},
		{x - 1, y + 0} /*middle */, {x + 1, y + 0},
		{x - 1, y + 1}, {x, y + 1}, {x + 1, y + 1},
	}
	low = MaxHeight
	myHigh := m.data[y*m.width+x]
	for _, nei := range neis {
		if nei.x < 0 || nei.x >= m.width || nei.y < 0 || nei.y >= m.length {
			continue
		}
		if low > m.data[nei.x+nei.y*m.width] {
			low = m.data[nei.x+nei.y*m.width]
		}
		if high < m.data[nei.x+nei.y*m.width] {
			high = m.data[nei.x+nei.y*m.width]
		}
		if myHigh < m.data[nei.x+nei.y*m.width] {
			higherCount++
		}
	}
	return
}

// 尝试填平低洼地形
func (m *Topomap) FillLowlying() {
	batchJobNum := 20
	wg := &sync.WaitGroup{}
	batchSize := m.width / batchJobNum
	if batchSize <= 0 {
		batchSize = 1
	}
	for batchIdx := 0; batchIdx < batchJobNum; batchIdx++ {
		wg.Add(1)
		go func(batchIdx int) {
			log.Printf("fill batch lines y: %v -> %v", batchIdx*batchSize, batchIdx*batchSize+batchSize)
			for y := batchIdx * batchSize; (y < batchIdx*batchSize+batchSize) && (y < m.length); y++ {
				for x := 0; x < m.width; x++ {
					if m.data[x+y*m.width] < SeaLevel {
						continue
					}
					// _, _, neiHighCnt := m.CountNeighbors(x, y) // 该函数会导致少量45度倾向性。比下面代码填充的多. 16.9w/2.7w
					neiHighCnt := 0 // TODO: test this code
					m.FindNeiDots(x, y, 1, func(xi, yi, distQ int) {
						// if distQ <= 1 || (m.evtIdx)&0xF <= 5 {
						if m.data[xi+yi*m.width] > m.data[x+y*m.width] {
							neiHighCnt++
						}
						// }
					})
					newIdx := y*m.width + x
					if neiHighCnt > 6 { // v6
						log.Printf("will fill sediment by enclosure: IDX:%d,%d altitude: %d evtId:%d", x, y, m.data[newIdx], m.evtIdx)
						m.EmitErodeEvents(x, y, nil, nil, 1, m.data[newIdx]+1) // m.data[newIdx]++
					}
				}
			}
			wg.Done()
		}(batchIdx)
	}
	wg.Wait()
}

func (w *WaterMap) SumH() (c int) {
	for idx := range w.data {
		c += int(w.data[idx].c)
	}
	return
}
func (w *WaterMap) CalcMaxC() (maxC, minC, maxQ int16) {
	w.maxC = 0
	w.maxQ = 0
	w.minC = 32767
	for _, d := range w.data {
		if d.c > w.maxC {
			w.maxC = d.c
		}
		if d.q > w.maxQ {
			w.maxQ = d.q
		}
		if d.c < w.minC {
			w.minC = d.c
		}
	}
	return w.maxC, w.minC, int16(w.maxQ)
}

func (m *Topomap) CalcMaxMin() (maxLv, minLv int16) {
	m.maxLv, m.minLv = 0, 32767
	for idx := range m.data {
		if m.data[idx] > m.maxLv {
			m.maxLv = m.data[idx]
		}
		if m.data[idx] < m.minLv {
			m.minLv = m.data[idx]
		}
	}
	return m.maxLv, m.minLv
}

type Hill struct {
	x       int
	y       int
	r       int
	h       int
	tiltDir float64 // 倾斜方向
	tiltLen int     // 倾斜长度
}

type PetalFlag struct {
	Shape    int     // 形状
	PetalNum float64 // 花瓣数
	Sharp    float64 // 锋利度
}

func main() {
	var nowTs = time.Now()
	var seed = nowTs.Unix()

	var width, length int = 500, 500
	flag.IntVar(&width, "w", width, "width of map")
	flag.IntVar(&length, "h", width, "length of map")

	var outname = flag.String("out", "topomap", "image filename of output")
	var outdir = flag.String("outdir", "output", "out put dir")

	var nHills = flag.Int("hill", 100, "hill number for making rand topo by hill")
	var hillWide = flag.Int("hill-wide", 100, "hill wide for making rand topo by hill")

	// ridges 数组
	var nRidge = flag.Int("ridge", 1, "num of ridges for making ridges")
	var ridgeWide = flag.Int("ridge-wide", 50, "ridge wide for making ridge each")
	var ridgeLen = flag.Int("ridge-len", 100, "ridge length when making ridge each")

	// stuck
	var stuckNum = flag.Int("stuck", 0, "stuck hill number, hill in stuck area will pressed, even length be 0")

	// petal
	petalFlag := &PetalFlag{}
	flag.IntVar(&petalFlag.Shape, "petal-shape", 2, "shape of petal, 0:圆形 无花瓣 1:圆角 2:锐角")
	flag.Float64Var(&petalFlag.PetalNum, "petal-num", 3, "petal numbers of hill")
	flag.Float64Var(&petalFlag.Sharp, "petal-sharp", 0.5, "petal sharp, 取值 0-1.0, 越大越锋利")
	flag.StringVar(&colorTplFile, "color-tpl", colorTplFile, "color template file path")

	lowerWaterLevel := 0
	flag.IntVar(&lowerWaterLevel, "lower", 0, "lower water level, default 0")
	blur := 0
	flag.IntVar(&blur, "blur", 0, "blur radius, default 0")

	//seed
	flag.Int64Var(&seed, "seed", 0, "random seed")

	// drops
	var dropNum = flag.Int("dropnum", 1, "number of drops")
	var times = flag.Int("times", 1000, "update times")
	var enlarge = flag.Int("enlarge", 0, "enlarge scale of out put image, if enlarge=1, then the count of pixels of output image will be 2x2 times of input")

	// output draw zoom
	var zoom = flag.Int("zoom", 1, "zoom of out put image")
	var riverArrowScale = flag.Float64("river-arrow-scale", 0.8, "river arrow scale")
	var drawFlag = flag.Int("draw-flag", DrawFlagDroplet, "draw flag: 1=draw filled vector in topomap 2=draw hisway of droplet")
	var colorTplStep = flag.Int("color-tpl-step", 0, "color tpl file step line, will igore there step in tpl")

	var basemap = ""
	flag.StringVar(&basemap, "basemap", "", "base map file path")
	var basemapTone = "0" // 要选取的基调色
	flag.StringVar(&basemapTone, "basemap-tone", "0", "0=light is high, 1=dark is high, r=red is high, g=green is high, b=blue is high")
	var basemapCover = "1"
	flag.StringVar(&basemapCover, "basemap-cover", "", "options: sum,avg,max,shape")

	flag.Parse()

	if seed == 0 {
		seed = nowTs.Unix()
	}
	rand = randpkg.New(randpkg.NewSource(seed)) // rand.Seed(seed)

	// DEBUG
	// TestTopo()

	var m Topomap
	var w WaterMap
	var baseM Topomap
	var baseW WaterMap
	var maxLevel int16 = 1
	var minLevel int16 = 1

	// ---- start of making terrain ----
	if basemap != "" {
		// var basemapImg *image.RGBA
		basemapFileIo, err := os.Open(basemap)
		if err != nil {
			log.Printf("open basemap %s err:%s", basemap, err)
			return
		}
		// only support png and jpg
		basemapImg, basemapFmt, err := image.Decode(basemapFileIo)
		if err != nil {
			log.Printf("decode basemap %s err:%s", basemap, err)
			return
		}

		log.Printf("basemap parsed: format:%s bounds: %+v ColorModel:%+v", basemapFmt, basemapImg.Bounds(), basemapImg.ColorModel())

		width, length = basemapImg.Bounds().Dx(), basemapImg.Bounds().Dy()
		zoomOut := 1
		for width > 600 || length > 600 {
			width /= 2
			length /= 2
			zoomOut *= 2
		}

		baseM.Init(width, length)
		baseW.Init(width, length)
		m, w = baseM, baseW

		// var maxR, maxG, maxB, minR, minG, minB int16 = 0, 0, 0, 0x7FFF, 0x7FFF, 0x7FFF

		// 从basemap中获取高度
		for y := 0; y < length; y++ {
			for x := 0; x < width; x++ {
				ru, gu, bu, _ := basemapImg.At(x*zoomOut, y*zoomOut).RGBA()
				r, g, b := int16(ru>>8), int16(gu>>8), int16(bu>>8)
				// 0-65535 -> negative 0-127 // because white is 65535
				if basemapTone == "1" { // light is high
					m.data[x+y*width] = (r + g + b) / 4
				} else if basemapTone == "0" { // dark is high
					m.data[x+y*width] = (256 - (r+g+b)/3) / 4
				} else if basemapTone == "r" {
					m.data[x+y*width] = r / 4
				} else if basemapTone == "g" {
					m.data[x+y*width] = g / 4
				} else if basemapTone == "b" {
					m.data[x+y*width] = b / 4
				} else if basemapTone == "r+" {
					if (r*2 - b - g) > 0 {
						m.data[x+y*width] = (r - b + r - g) / 4
					}
				} else if basemapTone == "g+" {
					if (g*2 - r - b) > 0 {
						m.data[x+y*width] = (g - r + g - b) / 4
					}
				} else if basemapTone == "b+" {
					if (b*2 - r - g) > 0 {
						m.data[x+y*width] = (b - r + b - g) / 4
					}
				} else if basemapTone == "rs" {
					// redness_simple = R - max(G, B)     # 范围 ≈ -255 到 255 深红色值更高
					if (r*2 - b - g) > 0 {
						m.data[x+y*width] = (r-max(g, b))/2 + 128
					}
				} else if basemapTone == "gs" {
					if (g*2 - r - b) > 0 {
						m.data[x+y*width] = (g-max(r, b))/2 + 128
					}
				} else if basemapTone == "bs" {
					if (b*2 - r - g) > 0 {
						m.data[x+y*width] = (b-max(r, g))/2 + 128
					}
				} else if basemapTone == "rh" {
					// redness_simple2 = R - (G + B) / 2  # 范围 ≈ -127.5 到 255 深红色值更高
					if (r*2 - b - g) > 0 {
						m.data[x+y*width] = (r - (g+b)/2 + 128) / 2
					}
				} else if basemapTone == "gh" {
					if (g*2 - r - b) > 0 {
						m.data[x+y*width] = (g - (r+b)/2 + 128) / 2
					}
				} else if basemapTone == "bh" {
					if (b*2 - r - g) > 0 {
						m.data[x+y*width] = (b - (g+r)/2 + 128) / 2
					}
				} else if basemapTone == "rg" {
					if r+g > 2*b {
						m.data[x+y*width] = (r + g) / 4
					}
				} else if basemapTone == "gb" {
					if g+b > 2*r {
						m.data[x+y*width] = (g + b) / 4
					}
				} else if basemapTone == "rb" {
					if r+b > 2*g {
						m.data[x+y*width] = (r + b) / 4
					}
				} else if basemapTone == "rg+" {
					if r+g > 2*b {
						m.data[x+y*width] = g/16 + r/4 // poor effect
					}
				} else if basemapTone == "t+" { // terrain
					m.data[x+y*width] = (r*4 + g*2 + b) / 28
				}
				if maxLevel < (m.data[x+y*width]) {
					maxLevel = (m.data[x+y*width])
				}
				if minLevel > (m.data[x+y*width]) {
					minLevel = (m.data[x+y*width])
				}
			}
		}

		// maxLevel = 64
		// *colorTplStep = int(maxLevel) / 4
		if *colorTplStep <= 0 {
			*colorTplStep = 20
		}
		log.Printf("basemap width:%d length:%d zoomOut:%d min:%d max:%d colorTplStep=%d", width, length, zoomOut, minLevel, maxLevel, *colorTplStep)
		// log.Printf("max R,G,B=%x,%x,%x min R,G,B=%x,%x,%x", maxR, maxG, maxB, minR, minG, minB)
		// *colorTplStep = 10
		basemapFileIo.Close()
	}
	if basemap == "" || basemap != "" && basemapCover != "" {

		// 初始化 watermap topomap
		w.Init(width, length)
		m.Init(width, length)

		log.Printf("will make hills(n:%d,wide:%d)", *nHills, *hillWide)
		// 随机n个圆圈 累加抬高 输出到m中
		hills := MakeHills(width, length, *hillWide, *nHills)
		// elevate the first hills
		for hi := 0; hi < len(hills); hi++ {
			if hills[hi].h > 0 {
				hills[hi].h *= 4
				break
			}
		}

		log.Printf("will make ridge(n:%d, wide:%d)", *ridgeLen, *ridgeWide)
		// 转换痕迹为ridge 为每个环分配随机半径 输出到m中
		var ridgeHills []Hill
		for ri := 0; ri < *nRidge; ri++ {
			ridgeHills = append(ridgeHills, MakeRidge(*ridgeLen, *ridgeWide, width, length)...)
		}

		// no terrian in stuck area
		stuckHills := MakeHills(width, length, width/2, *stuckNum)
		// strip hills from stuckHills
		for sti := range stuckHills {
			stuckedCnt := 0
			for hi := 0; hi < len(hills); hi++ {
				distM := (hills[hi].x-stuckHills[sti].x)*(hills[hi].x-stuckHills[sti].x) + (hills[hi].y-stuckHills[sti].y)*(hills[hi].y-stuckHills[sti].y)
				stuckR := stuckHills[sti].R(hills[hi].x, hills[hi].y, petalFlag)
				if distM < stuckR*stuckR {
					stuckedCnt++
					// todo: do not accumulate calculate stucks
					if sti&0x1 == 0 {
						hills[hi].h /= 4
					} else {
						hills[hi].h += 2
					}
				}
			}

			for rhi := 0; rhi < len(ridgeHills); rhi++ {
				distM := (ridgeHills[rhi].x-stuckHills[sti].x)*(ridgeHills[rhi].x-stuckHills[sti].x) + (ridgeHills[rhi].y-stuckHills[sti].y)*(ridgeHills[rhi].y-stuckHills[sti].y)
				stuckR := stuckHills[sti].R(ridgeHills[rhi].x, ridgeHills[rhi].y, petalFlag)
				if distM < stuckR*stuckR {
					stuckedCnt++
					if sti&0x1 == 0 {
						ridgeHills[rhi].h /= 2
					} else {
						ridgeHills[rhi].h += 2
					}
				}
			}
			log.Printf("hills of stucked shield:%d/%d", stuckedCnt, len(hills)+len(ridgeHills))
		}

		log.Printf("will fill hills and ridges to TopoMap(all times:%d)", width*length*(len(hills)+len(ridgeHills)))

		wgf := &sync.WaitGroup{}

		// 输出hills地形 制造地形 将上面生成的ridge和hills输出到m上
		wgf.Add(length)
		for y := 0; y < length; y++ {
			go func(y int) {
				defer wgf.Done()
				var tmpLevel float32 = 0
				for x := 0; x < width; x++ {
					tmpLevel = 0
					// 收集ridgeHills产生的altitude
					for _, r := range ridgeHills {
						distM := (x-r.x)*(x-r.x) + (y-r.y)*(y-r.y)
						rn := r.R(x, y, petalFlag) //(r.r) // R() 会造成圆圈齿效果 不推荐
						if distM <= rn*rn {
							tmpLevel += float32(r.h) - float32(float64(r.h)*math.Sqrt(math.Sqrt(float64(distM)/float64((rn*rn)))))
							//tmpLevel += float32(r.h) - float32(float64(r.h)*(math.Sqrt(float64(distM)/float64((rn*rn))))) //todo test 效果不好
							//tmpLevel += float32(distM) / float32(r.r*r.r) * rand.Float32()
							//log.Println("color fill x,y,r,c=", x, y, r, tmpLevel)
						}
					}
					// 收集hills产生的altitude
					for _, r := range hills {
						distM := (x-r.x)*(x-r.x) + (y-r.y)*(y-r.y)
						//rn := float64(r.tiltLen)*math.Sin(r.tiltDir-math.Atan2(float64(y), float64(x))) + float64(r.r)	// 尝试倾斜地图中的圆环 尝试失败
						rn := r.R(x, y, petalFlag) //(r.r) // 使用花瓣半径效果好
						if distM < rn*rn {
							// 产生的ring中间隆起
							tmpLevel += float32(r.h) - float32(float64(r.h)*math.Sqrt(math.Sqrt(float64(distM)/float64((rn*rn)))))
							// if rn*rn-distM > 4 {
							// 	tmpLevel2 += float32(r.h)
							// }
							// tmpLevel2 += float32(r.h) - float32(r.h)*(4.0/float32(rn*rn-distM)) + 2 //float32(float64(r.h)*math.Sqrt(math.Sqrt(float64(distM)/4.0)))
							// tmpLevel2 += float32(r.h) * (float32(rn*rn - distM)) / float32(rn*rn)
							//tmpLevel += float32(distM) / float32(rn*rn) * rand.Float32()
							//log.Println("color fill x,y,r,c=", x, y, r, tmpLevel, "r=", r)
							// if tmpLevel == 0 {
							// 	tmpLevel = tmpLevel2
							// } else {
							// 	tmpLevel = (tmpLevel + tmpLevel2) / 2.0
							// }
						}
					}

					if tmpLevel < 0 {
						tmpLevel = 0
					}
					// if m.data[x+y*width] == 0 {
					// 	m.data[x+y*width] = int16(tmpLevel) //+ int16(rand.Int()&0x1) //int8(width - x)
					// } else {
					// 	m.data[x+y*width] = (m.data[x+y*width] + int16(tmpLevel)) / 2 //平均一下
					// }
					m.data[x+y*width] = max(m.data[x+y*width], int16(tmpLevel))
				}
			}(y)
		}
		wgf.Wait()
	}

	if basemapCover == "sum" {
		for idx := range m.data {
			m.data[idx] += baseM.data[idx]
		}
	}

	if basemapCover == "shape" {
		for idx := range m.data {
			if baseM.data[idx] <= SeaLevel {
				m.data[idx] = 0 //baseM.data[idx]
			}
		}
	}

	if basemapCover == "avg" {
		for idx := range m.data {
			m.data[idx] = (m.data[idx] + baseM.data[idx]) / 2
		}
	}

	if basemapCover == "max" {
		for idx := range m.data {
			m.data[idx] = max(m.data[idx], baseM.data[idx])
		}
	}

	if lowerWaterLevel > 0 {
		// 将整体高度降低
		for idx := range m.data {
			m.data[idx] -= int16(lowerWaterLevel)
			if m.data[idx] < 0 {
				m.data[idx] = 0
			}
		}
	}
	if blur > 0 {
		// 让地形高度取周围半径为blur的平均值
		for li := 0; li < m.length; li++ {
			for wi := 0; wi < m.width; wi++ {
				m.evtIdx += 13
				sum := int16(0)
				cnt := m.FindNeiDots(wi, li, blur, func(nx, ny, distSq int) {
					sum += m.data[nx+ny*m.width]
				})
				if cnt > 0 {
					avg := sum / int16(cnt)
					m.data[wi+li*m.width] = avg
				}
			}
		}
		m.evtIdx = 0
	}

	maxLevel, minLevel = m.CalcMaxMin()
	if maxLevel > 1 && maxLevel < MaxHeight {
		upliftTimes := MaxHeight / maxLevel / 2
		for i := range m.data {
			m.data[i] *= upliftTimes
		}
	}
	maxLevel, minLevel = m.CalcMaxMin()
	log.Printf("terrain makes ok. min:%d max:%d", minLevel, maxLevel)

	// ---- end of making terrain ----

	var drops []*Droplet
	log.Printf("will make drops(n:%d)", *dropNum)

	if *dropNum > 0 {
		w.AssignVector(&m, 2)
		rand.Seed(int64(*times)) // rand = randpkg.New(randpkg.NewSource())
		drops = make([]*Droplet, *dropNum)
	}

	w.StartEroding()
	m.StartEroding()

	// // 生成一组随机*Droplet
	MakeDroplets(&m, &w, *dropNum, int(maxLevel), drops)

	log.Printf("will move drops(times:%d) with flag=0", *times/2)
	drops = DropletsMove(*times, drops, &m, &w, 0) //do sediment
	log.Printf("update drops done. times=%d num drops=%d->%d", *times/2, *dropNum, len(drops))
	time.Sleep(time.Millisecond * 10)

	// m.FillLowlying() // fill lower lands
	// w.Clear()
	widthOri := width

	if *enlarge > 0 {
		for ei := 0; ei < *enlarge; ei++ {
			time.Sleep(time.Millisecond * 50) //等待eroding执行结束，应该使用更严格的方式判断
			width, length = width*2, length*2
			m.Enlarge()
			w.Enlarge()
			w.AssignVector(&m, 2)
			log.Printf("enlarge %dth time, h,w=%d,%d, will make %d droplets", ei+1, width, length, *dropNum*(2<<ei)*(2<<ei))
			drops = make([]*Droplet, *dropNum*(2<<ei)*(2<<ei))
			MakeDroplets(&m, &w, *dropNum*(2<<ei)*(2<<ei), int(maxLevel), drops)
			drops = DropletsMove(*times, drops, &m, &w, 0) //do sediment
			log.Printf("update drops done in enlargement(%d). times=%d num drops=%d->%d", ei+1, *times/2, *dropNum*(2<<ei)*(2<<ei), len(drops))
		}
	}

	time.Sleep(time.Millisecond * 50) //等待eroding执行结束，应该使用更严格的方式判断

	maxLevel, minLevel = m.CalcMaxMin()
	wMaxC, wMinC, wMaxQ := w.CalcMaxC()

	if _, derr := os.Open(*outdir); derr != nil {
		log.Println("output dir seems not exist and will be maked:", *outdir, derr)
		if cerr := os.Mkdir(*outdir, os.ModePerm); cerr != nil {
			log.Println("os.mkdir:", *outdir, cerr)
		}
	}

	log.Printf("will draw to image(zoom:%d, width:%d, length:%d) nHills=%d nRidge=%d ridgelen=%d maxLv:%d ", *zoom, width, length, *nHills, *nHills, *ridgeLen, maxLevel)
	log.Printf("wMaxC,minC,maxQ=%d,%d,%d we=%d/+%d,-%d,ex:%d me=%d/+%d,-%d", wMaxC, wMinC, wMaxQ, w.evtIdx, w.plusCnt, w.minusCnt, w.exchCnt, m.evtIdx, m.plusCnt, m.minusCnt)
	// then draw
	img := image.NewRGBA(image.Rect(0, 0, width**zoom, length**zoom))
	DrawToImg(img, &m, &w, *zoom, *riverArrowScale, drops, *drawFlag, *colorTplStep)

	wgm := sync.WaitGroup{}

	// 输出图片文件
	wgm.Add(1)
	go func() {
		tnTag := ""
		if basemap != "" {
			tnTag = "-tn" + basemapTone
		}
		timeUsed := time.Now().Sub(nowTs).Milliseconds() / 1000
		ImgToFile(fmt.Sprintf("%s/%s-%s-s%d-w%d-hi%d-hw%d-r%d-rl%d-rw%d-st%d-dr%d-t%d-e%d%s.%ds.r8.png", *outdir, *outname, time.Now().Format("20060102150405"), seed, widthOri, *nHills, *hillWide, *nRidge, *ridgeLen, *ridgeWide, *stuckNum, *dropNum, *times, *enlarge, tnTag, timeUsed), img, "png")
		wgm.Done()
	}()

	wgm.Wait()

	var maxJourney, sumJourney, upDotNum, downDotNum = 0, 0, 0, 0
	for di, d := range drops {
		if (*drawFlag & DrawFlagDebugDroplet) > 0 {
			log.Printf("drops[%d]=%+v", di, *d)
		}
		sumJourney += d.journey
		if d.journey > maxJourney {
			maxJourney = d.journey
		}
		pos := int(d.x) + int(d.y)*m.width
		if pos >= len(m.data) || pos < 0 {
			continue
		}
		d.finalH = m.data[pos]
		if d.finalH > d.birthH {
			upDotNum++
		} else {
			downDotNum++
		}
	}

	log.Printf("stayLongCount=%d maxJourney=%d avgJourney=%d upDotNum=%d downDotNum=%d seed=%d", stayLongCount, maxJourney, sumJourney/(len(drops)+1), upDotNum, downDotNum, seed)
}

func DrawToImg(img *image.RGBA, m *Topomap, w *WaterMap, zoom int, riverArrowScale float64, drops []*Droplet, drawFlag int, colorTplStep int) {
	length := m.length
	width := m.width
	var tmpLevel float32 = 1

	var maxLevel float32 = float32(m.maxLv) //+ float32(w.maxLv) // m.maxLv must be set before
	if drawFlag&DrawFlagTaddC > 0 {
		maxLevel = float32(m.maxLv) + float32(w.maxC) // for debug
	} else if drawFlag&DrawFlagTminusC > 0 {
		maxLevel = float32(max(m.maxLv, w.maxC)) // for debug
	}
	// var maxLevel float32 = float32(m.maxLv) + float32(w.maxLv) // m.maxLv must be set before
	// var maxLevel float32 = float32(w.maxC - w.minC) // m.maxLv must be set before
	// var maxLevel float32 = float32(w.maxQ) // m.maxLv must be set before

	// 获取颜色模板
	cs := colorTpl(colorTplFile, colorTplStep)
	cslen := len(cs) - 1
	log.Printf("color-tpl has %d steps, maxLv=%.1f", cslen, maxLevel)
	if maxLevel <= 0.1 {
		maxLevel = 2 // 防止除法错误
	}
	// 地图背景地形绘制
	for y := 0; y < length; y++ {
		for x := 0; x < width; x++ {
			tmpLevel = float32(m.data[x+y*width]) // work well
			if drawFlag&DrawFlagTaddC > 0 {
				tmpLevel = float32(m.data[x+y*width] + w.data[x+y*width].c) // for debug
			} else if drawFlag&DrawFlagTminusC > 0 {
				tmpLevel = float32(m.data[x+y*width] - max(0, w.data[x+y*width].c)) // for debug
			}
			// tmpLevel = float32(w.data[x+y*width].c - w.minC) // only catchment, for debug
			// tmpLevel = float32(w.data[x+y*width].q) // only quantity, for debug
			if tmpLevel < 0 {
				tmpLevel = 0
			}
			// 放大
			ctmp := cs[int(float32(cslen)*tmpLevel/maxLevel)]
			for zix := 0; zix < zoom; zix++ {
				for ziy := 0; ziy < zoom; ziy++ {
					img.Set(x*zoom+zix, y*zoom+ziy, ctmp)
				}
			}
		}
	}

	// 绘制WaterMap
	// tmpLakeColorBlue := color.RGBA{0x50, 0xd6, 0xFE, 0xFF}
	// tmpLakeColor := color.RGBA{0, 0xa0, 0xE0, 0xFF}     // alpha=255 表示不透明 青色 有积水
	tmpLakeColor2 := color.RGBA{0x40, 0x72, 0xcb, 0xFF} //#b0d2eb    // blue-gray // 流量痕迹
	//tmpColor4 := color.RGBA{66, 50, 209, 0xFF}          //    // 蓝紫色 // 水滴
	tmpLakeColor3 := color.RGBA{0x99, 0xFF, 0xFF, 0xFF} //#亮蓝色    // 水滴痕迹
	tmpColor4 := color.RGBA{0, 0x10, 0xF0, 0xFF}        //    // 青色 // 水滴最终位置
	//tmpColor5 := color.CMYK{100, 10, 10, 0}
	for _, dot := range w.data {
		// 绘制积水 点周围绘制 必须在zoom>=5时才能看到效果
		if (drawFlag&DrawFlagCatchment) > 0 && dot.c > 0 {
			neis := []struct{ x, y int }{
				{int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2 + 1}, {int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2}, {int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2 - 1}, // right three
				{int(dot.x)*zoom + zoom/2, int(dot.y)*zoom + zoom/2 - 1},                                                                                                                             // top one
				{int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2 - 1}, {int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2}, {int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2 + 1}, // left three
				{int(dot.x)*zoom + zoom/2, int(dot.y)*zoom + zoom/2 + 1}, // bottom one
			}
			catchmentSqueeze := calcBit(int(dot.c) + 1)
			if catchmentSqueeze > len(neis) {
				catchmentSqueeze = len(neis) - 1
			}
			for cci := 0; cci < catchmentSqueeze; cci++ {
				img.Set(neis[cci].x, neis[cci].y, tmpLakeColor3)
			}
		}
		// 绘制流量痕迹
		if (drawFlag&DrawFlagQflow) > 0 && dot.q > 0 {
			neis := []struct{ x, y int }{
				{int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2 + 1}, {int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2}, {int(dot.x)*zoom + zoom/2 + 1, int(dot.y)*zoom + zoom/2 - 1}, // right three
				{int(dot.x)*zoom + zoom/2, int(dot.y)*zoom + zoom/2 - 1},                                                                                                                             // top one
				{int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2 - 1}, {int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2}, {int(dot.x)*zoom + zoom/2 - 1, int(dot.y)*zoom + zoom/2 + 1}, // left three
				{int(dot.x)*zoom + zoom/2, int(dot.y)*zoom + zoom/2 + 1}, // bottom one
			}
			qSqueeze := calcBit(int(dot.q) + 1)
			if qSqueeze > len(neis) {
				qSqueeze = len(neis) - 1
			}
			for qci := 0; qci < qSqueeze; qci++ {
				img.Set(neis[qci].x, neis[qci].y, tmpLakeColor2)
			}
			// img.Set(int(dot.x)*zoom+zoom/2-1, int(dot.y)*zoom+zoom/2, tmpLakeColor2)
		}
	}

	// 绘制流动 在v2下相当于场
	if (drawFlag & DrawFlagField) > 0 {
		for di, dot := range w.data {
			// 绘制当前点 如果是源头 则绘制白色
			if dot.xPower != 0.0 || dot.yPower != 0.0 {
				// 计算相对比例尺的高度
				tmpLevel := int(m.data[di] + dot.c)
				tmpLevel = int(float32(cslen*tmpLevel) / maxLevel)
				// 防止越界
				if tmpLevel >= len(cs) {
					tmpLevel = len(cs) - 1
				}
				if tmpLevel < 0 {
					tmpLevel = 0
				}

				// 绘制流动方向 考虑缩放
				tmpColor := cs[tmpLevel]
				lineTo(img, int(dot.x)*zoom+zoom/2, int(dot.y)*zoom+zoom/2, int(dot.x)*zoom+zoom/2+int(float32(zoom)*dot.xPower), int(dot.y)*zoom+zoom/2+int(float32(zoom)*dot.yPower), color.RGBA{0x66, 0x66, 0xFF, 0xFF}, tmpColor, riverArrowScale)
			}
		}
	}
	// 绘制droplets
	if (drawFlag & DrawFlagDroplet) > 0 {
		for _, drop := range drops {
			img.Set(int(drop.x)*zoom+zoom/2, int(drop.y)*zoom+zoom/2, tmpColor4)
		}
	}
	// 绘制颜色模板
	csWidth := zoom/2 - 1
	if csWidth <= 0 {
		csWidth = 1
	}
	for i := 0; i < len(cs); i++ {
		color := cs[len(cs)-i-1]
		for wi := 0; wi < csWidth; wi++ {
			img.Set(wi, i, color)
		}
	}
	// 加1条白色
	for wi := 0; wi < 4; wi++ {
		img.Set(wi, len(cs), color.White)
	}
	// 给SeaLevel加一条白色
	for wi := 1; wi < 4; wi++ {
		img.Set(wi, len(cs)-1-int(float32(len(cs)*SeaLevel)/maxLevel), color.White)
	}
}

// @param ridge = []Hill
// @param ridgeLen = Hill 个数 =count(Hill)
// @param ridgeWide = Hill wide
func MakeRidge(ridgeLen, ridgeWide, mWidth, mLength int) []Hill {
	ridgeHills := make([]Hill, ridgeLen)
	widthEdge := mWidth / 8
	lengthEdge := mLength / 8
	// toward as step
	baseTowardX1, baseTowardY1 := randomDir()
	baseTowardX, baseTowardY := int(baseTowardX1*float32(ridgeWide)), int(baseTowardY1*float32(ridgeWide))
	for ri := 0; ri < int(ridgeLen); ri++ {
		r := &ridgeHills[ri]
		r.h = rand.Int()%RidgeHeightMedian + RidgeHeightMedian/2
		r.tiltDir, r.tiltLen = rand.Float64()*math.Pi*2, (rand.Int()%20)+1
		if ri == 0 {
			// 第一个
			r.x, r.y, r.r = (rand.Int()%(mWidth-widthEdge*2))+widthEdge, (rand.Int()%(mLength-lengthEdge*2))+lengthEdge, (rand.Int()%ridgeWide)/2+ridgeWide/2
		} else {
			// 其他 基础方向: ridgeHills[ri-1].x+baseTowardX 摆动:(rand.Int()%ridgeWide)/2-(rand.Int()%ridgeWide)/2
			waveX, waveY := 0, 0
			if baseTowardX != 0 {
				waveY = (rand.Int() % baseTowardX) - (rand.Int() % baseTowardX)
			}
			if baseTowardY != 0 {
				waveX = (rand.Int() % baseTowardY) - (rand.Int() % baseTowardY)
			}
			r.x, r.y, r.r = ridgeHills[ri-1].x+baseTowardX/2+waveX, ridgeHills[ri-1].y+baseTowardY/2+waveY, (rand.Int()%ridgeWide)/2+ridgeWide/2
		}
	}

	return ridgeHills
}

func ImgToFile(outputFilePath string, img *image.RGBA, format string) {
	picFile2, err := os.Create(outputFilePath)
	if err != nil {
		log.Printf("when create file %s error:%v", outputFilePath, err)
		return
	}
	defer picFile2.Close()
	if err := png.Encode(picFile2, img); err != nil {
		log.Println("png.Encode error:", err)
	}
}

// 会改变d的方向 即会改变 vx,vy 值
func (d *Droplet) CloseTo(target *Droplet, distSquare float32) {
	distSquareRoot := math.Sqrt(float64(distSquare))
	d.vx = d.vx + (target.x-d.x)*float32(distSquareRoot)*AttractPowerDecay
	d.vy = d.vy + (target.y-d.y)*float32(distSquareRoot)*AttractPowerDecay
}

func (d *Droplet) ReduceSpeed() {
	for (d.vx*d.vx + d.vy*d.vy) < 0.5 {
		d.vx, d.vy = d.vx*1.05, d.vy*1.05
	}
	for (d.vx*d.vx + d.vy*d.vy) > MinDistToReduce {
		d.vx, d.vy = d.vx*0.95, d.vy*0.95
	}
}

// 返回随机方向 x,y 取值范围 [-1,1]
func randomDir() (x, y float32) {
	thedir := rand.Float64() * math.Pi * 2
	x, y = float32(math.Cos(thedir)), float32(math.Sin(thedir))
	return
}

func MakeHills(width, length, hillWide, num int) []Hill {
	// 随机n个圆圈 累加抬高 输出到m中
	widthEdge := width / 9
	lengthEdge := length / 9
	hills := make([]Hill, num)
	for ri, _ := range hills {
		r := &hills[ri]
		// todo 地图边框附近不要去
		r.x, r.y, r.h = (rand.Int()%(width-widthEdge*2))+widthEdge, (rand.Int()%(length-lengthEdge*2))+lengthEdge, rand.Int()%HillHeightMedian+HillHeightMedian/2
		// 倾斜度 todo tilt: 未生效
		r.tiltDir, r.tiltLen, r.r = rand.Float64()*math.Pi*2, (rand.Int()%20)+1, int(math.Sqrt(float64(rand.Int()%(hillWide*hillWide+1))))
		//r.r, r.h = hillWide, 10 /// todo: this is debug
		if ri%3 == 1 {
			// 1/3 是反向海拔，成为盆地
			r.h *= -1
		}
		//log.Printf("ri=%d h=%d s=%d", ri, r.h, (ri%2)*2-1)
	}

	return hills
}

// get radius 一个点(x,y)看hill的边距离 hill是三角形 不同视角看到的距离不一样 返回的R不能比hill.r大
// 返回花瓣状距离 花瓣hill产生的高原效果特别好
// todo: 有模糊横线 精度损失导致
func (h *Hill) R(x, y int, petalFlag *PetalFlag) (r int) {

	switch petalFlag.Shape {
	case 1:
		// 尖角瓣状 需要加大hill-wide 否则都是细线  1-abs(sin(dir))
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir // 找到方向差
		dist := 1.0 - math.Abs(math.Sin(diffDir*petalFlag.PetalNum/2.0))*petalFlag.Sharp
		return int(dist * float64(h.r))
	case 2:
		// 圆花瓣状 细腰长叶花瓣 1-sin(dir)
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir // 找到方向差
		//dist := math.Sin(diffDir*petalFlag.PetalNum)/2.0 + 1.0            // [0.5-1.5]
		dist := 1.0 - (math.Sin(diffDir*petalFlag.PetalNum)+1.0)/2.0*petalFlag.Sharp
		return int(dist * float64(h.r))
	case 3:
		// 圆形瓣状 圆润丰满 1-abs(sin(dir))
		diffDir := math.Atan2(float64(y-h.y), float64(x-h.x)) - h.tiltDir // 找到方向差
		dist := 1.0 - (1.0-math.Abs(math.Sin(diffDir*petalFlag.PetalNum/2.0)))*petalFlag.Sharp
		return int(dist * float64(h.r))
	default:
		//原型 相同hill-wide，比其他2种占用面积大
		r = h.r
	}
	return
}

/*返回颜色数组，下标越大颜色海拔越高*/
func colorTpl(colorTplFile string, colorTplStep int) []color.Color {
	var colorTplFileIo, _ = os.Open(colorTplFile)
	defer colorTplFileIo.Close()
	var colorTplPng, err = png.Decode(colorTplFileIo)

	if err != nil {
		log.Println("png.decode err when read colorTpl:", err)
		return nil
	}

	// 从colorTplStep以上的部分取
	theLen := colorTplPng.Bounds().Dy() - colorTplStep
	cs := make([]color.Color, theLen)
	for i := 0; i < theLen; i++ {
		cs[theLen-i-1] = colorTplPng.At(0, i)
	}
	cs = append(cs, color.White, color.White, color.White)
	return cs
}

func lineTo(img *image.RGBA, startX, startY, destX, destY int, lineColor, startColor color.Color, scale float64) {
	distM := math.Sqrt(float64((startX-destX)*(startX-destX) + (startY-destY)*(startY-destY)))
	var i float64
	for i = 0; i < distM*scale; i++ {
		img.Set(startX+int(i/distM*float64(destX-startX)), startY+int(i/distM*float64(destY-startY)), lineColor)
	}
	// 线段最后一点 绘制成始发地地形的颜色 startColor
	if startX != destX && startY != destY {
		img.Set(startX+int(i/distM*float64(destX-startX)), startY+int(i/distM*float64(destY-startY)), startColor)
		// img.Set(startX, startY, startColor)
	}
}

func DrawToConsole(m *Topomap) {
	str := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ~!@#$%^&*-=_+()[]{}<>\\/;:,.???????????????????????????????????????"
	for di, dd := range m.data {
		fmt.Printf("%c", str[dd])
		if di%m.width == (m.width - 1) {
			fmt.Printf("\n")
		}
	}
}

func TestTopo() {
	var m1 Topomap
	var w1 WaterMap
	m1.Init(10, 10)
	w1.Init(10, 10)
	for i := 0; i < len(w1.data); i++ {
		m1.data[i] = 0
		w1.data[i].c = 0
	}
	a := 1
	m1.FindNeiDots(6, 5, 2, func(nx, ny, distSq int) {
		m1.data[nx+ny*m1.width] = int16(a)
		a++
	})
	DrawToConsole(&m1)
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

func max[T int | int16 | int32 | int64](a, b T) T {
	if a > b {
		return a
	}
	return b
}
func min[T int | int16 | int32 | int64](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func shuffleSlice[T any](arr []T) {
	for i := 0; i < len(arr)/2; i += 2 {
		j := len(arr) - 1 - i
		arr[i], arr[j] = arr[j], arr[i]
	}
}
