// Package engine 是地形生成与水滴侵蚀的纯算法实现，CLI 和 Web 服务共用。
// 所有状态都在 Sim 中，没有全局变量；相同 Params 必然得到相同结果。
package engine

import (
	"errors"
	"fmt"
	"image"
)

const (
	RidgeHeightMedian = 7 // ridge 高度中间数 在此基础上浮动
	HillHeightMedian  = 5 // hill 高度中间数 在此基础上浮动
	// 距离平方超过这个值 就会被等比例缩减速度 但是保持方向
	MinDistToReduce = 0.9999
	MaxStayLong     = 20
	// 使用1w会出现maxLv=16382，并且绘制png出现tpl溢出.
	// MaxHeight 范围在255-10000之间。越大，需要越多的水滴来侵蚀.
	MaxHeight = 1000
	SeaLevel  = 2 // include 2

	// 每批降水的水滴数，一页水滴并行走完 Times 步后做一次 CollectWater
	PageSize = 400
	// 增量推送的脏区块边长
	TileSize = 64
)

type PetalFlag struct {
	Shape    int     // 形状 0:圆形 无花瓣 1:尖角 2:细腰长叶 3:圆润
	PetalNum float64 // 花瓣数
	Sharp    float64 // 锋利度 取值 0-1.0, 越大越锋利
}

// Basemap 从图片读取高度
type Basemap struct {
	Image image.Image
	// 0=dark is high 1=light is high r/g/b, r+/g+/b+, rs/gs/bs, rh/gh/bh, rg/gb/rb, rg+, t+
	Tone string
	// 与随机地形的合成方式: "" 只用底图, sum, avg, max, shape
	Cover string
}

// CompatFlags 控制旧版中"效果依赖的缺陷"。默认全部关闭，即保留旧版行为。
type CompatFlags struct {
	// 旧版 AssignVector 用 range 值拷贝，初始场向量从未写回 WaterMap
	FixFieldInit bool
	// 旧版中途侵蚀判断写成了 m.data[middleX]，应为 m.data[middleIdx]
	FixMiddleIdx bool
}

type Params struct {
	Width  int
	Length int
	Seed   int64

	Hills    int
	HillWide int

	Ridges    int
	RidgeLen  int
	RidgeWide int

	Stuck int // stuck hill number, hill in stuck area will pressed
	Petal PetalFlag

	LowerWaterLevel int // 将整体高度降低
	Blur            int // 让地形高度取周围半径为blur的平均值

	Basemap *Basemap

	DropNum int
	Times   int
	Enlarge int

	Workers int // 并行计算水滴的协程数，0 = GOMAXPROCS
	Verbose bool
	Logf    func(format string, args ...any) // nil = log.Printf；不写入快照
	Compat  CompatFlags
}

// DefaultParams 与旧版命令行参数的默认值一致
func DefaultParams() Params {
	return Params{
		Width: 500, Length: 500,
		Hills: 100, HillWide: 100,
		Ridges: 1, RidgeLen: 100, RidgeWide: 50,
		Petal:   PetalFlag{Shape: 2, PetalNum: 3, Sharp: 0.5},
		DropNum: 1, Times: 1000,
	}
}

var validTones = map[string]bool{
	"0": true, "1": true, "r": true, "g": true, "b": true, "r+": true, "g+": true, "b+": true,
	"rs": true, "gs": true, "bs": true, "rh": true, "gh": true, "bh": true,
	"rg": true, "gb": true, "rb": true, "rg+": true, "t+": true,
}

var validCovers = map[string]bool{"": true, "sum": true, "avg": true, "max": true, "shape": true}

func (p *Params) Validate() error {
	if p.Basemap == nil {
		if p.Width <= 0 || p.Length <= 0 {
			return fmt.Errorf("width and length must be positive, got %dx%d", p.Width, p.Length)
		}
	} else {
		if p.Basemap.Image == nil {
			return errors.New("basemap image is nil")
		}
		if !validTones[p.Basemap.Tone] {
			return fmt.Errorf("unknown basemap tone %q", p.Basemap.Tone)
		}
		if !validCovers[p.Basemap.Cover] {
			return fmt.Errorf("unknown basemap cover %q", p.Basemap.Cover)
		}
	}
	if p.Hills < 0 || p.HillWide < 0 || p.Ridges < 0 || p.RidgeLen < 0 || p.Stuck < 0 {
		return errors.New("hill, ridge and stuck counts must not be negative")
	}
	if p.Ridges > 0 && p.RidgeLen > 0 && p.RidgeWide <= 0 {
		return errors.New("ridge-wide must be positive when ridges are made")
	}
	if p.DropNum < 0 || p.Times < 0 || p.Enlarge < 0 || p.Blur < 0 {
		return errors.New("dropnum, times, enlarge and blur must not be negative")
	}
	return nil
}
