package engine

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	// drawFlag 绘制参数
	DrawFlagField        = 1 << iota // 绘制地形落差场
	DrawFlagDroplet                  // 绘制水滴
	DrawFlagCatchment                // 绘制积水
	DrawFlagQflow                    // 绘制流量痕迹
	DrawFlagDebugDroplet             // 打印痕迹日志(由 CLI 处理)
	DrawFlagTaddC                    // topo+catchment 颜色取地形+积水
	DrawFlagTminusC                  // topo-catchment 颜色取地形-积水
)

type RenderOptions struct {
	Zoom            int
	RiverArrowScale float64
	DrawFlag        int
	Colors          []color.Color // 下标越大海拔越高，nil 使用 DefaultColors
}

// LoadColorTemplate 从等高线模板图片取第一列像素，顶部为最高海拔；step 表示忽略底部的行数
func LoadColorTemplate(path string, step int) ([]color.Color, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode color template %s: %w", path, err)
	}
	b := img.Bounds()
	n := b.Dy() - step
	if n <= 0 {
		return nil, fmt.Errorf("color template %s has %d rows, step %d leaves none", path, b.Dy(), step)
	}
	cs := make([]color.Color, n)
	for i := 0; i < n; i++ {
		cs[n-i-1] = img.At(b.Min.X, b.Min.Y+i)
	}
	return append(cs, color.White, color.White, color.White), nil
}

// DefaultColors 是内置的分层设色带：海洋、低地绿、黄土、褐色山地、雪顶
func DefaultColors() []color.Color {
	stops := []struct {
		at      float64
		r, g, b float64
	}{
		{0.00, 0x2a, 0x5d, 0x8f},
		{0.01, 0x8c, 0xc4, 0xa0},
		{0.20, 0x5f, 0x9e, 0x5a},
		{0.42, 0xa7, 0xb8, 0x66},
		{0.58, 0xd9, 0xc2, 0x7a},
		{0.74, 0xb9, 0x8a, 0x55},
		{0.88, 0x8a, 0x6a, 0x52},
		{1.00, 0xf2, 0xf2, 0xf0},
	}
	const n = 512
	cs := make([]color.Color, 0, n+3)
	for i := 0; i < n; i++ {
		v := float64(i) / (n - 1)
		k := 1
		for k < len(stops)-1 && v > stops[k].at {
			k++
		}
		a, b := stops[k-1], stops[k]
		t := 0.0
		if b.at > a.at {
			t = math.Min(1, math.Max(0, (v-a.at)/(b.at-a.at)))
		}
		lerp := func(x, y float64) uint8 { return uint8(math.Round(x + (y-x)*t)) }
		cs = append(cs, color.RGBA{lerp(a.r, b.r), lerp(a.g, b.g), lerp(a.b, b.b), 0xFF})
	}
	return append(cs, color.White, color.White, color.White)
}

// Render 按旧版 DrawToImg 的规则把当前状态画成图片
func (s *Sim) Render(opt RenderOptions) *image.RGBA {
	zoom := max(opt.Zoom, 1)
	cs := opt.Colors
	if len(cs) == 0 {
		cs = DefaultColors()
	}
	m, w := &s.m, &s.w
	width, length := m.Width, m.Length
	img := image.NewRGBA(image.Rect(0, 0, width*zoom, length*zoom))
	drawFlag := opt.DrawFlag

	mMaxLv, _ := extremes(m.Data)
	wMaxC, _, _ := w.CalcMaxC()
	maxLevel := float32(mMaxLv)
	if drawFlag&DrawFlagTaddC > 0 {
		maxLevel = float32(mMaxLv) + float32(wMaxC)
	} else if drawFlag&DrawFlagTminusC > 0 {
		maxLevel = float32(max(mMaxLv, wMaxC))
	}
	if maxLevel <= 0.1 {
		maxLevel = 2 // 防止除法错误
	}
	cslen := len(cs) - 1
	colorAt := func(level float32) color.Color {
		i := int(float32(cslen) * max(level, 0) / maxLevel)
		return cs[min(max(i, 0), cslen)]
	}

	// 地图背景地形绘制
	for y := 0; y < length; y++ {
		for x := 0; x < width; x++ {
			idx := x + y*width
			level := float32(m.Data[idx])
			if drawFlag&DrawFlagTaddC > 0 {
				level = float32(m.Data[idx] + w.Data[idx].C)
			} else if drawFlag&DrawFlagTminusC > 0 {
				level = float32(m.Data[idx] - max(0, w.Data[idx].C))
			}
			c := colorAt(level)
			for zy := 0; zy < zoom; zy++ {
				for zx := 0; zx < zoom; zx++ {
					img.Set(x*zoom+zx, y*zoom+zy, c)
				}
			}
		}
	}

	flowColor := color.RGBA{0x40, 0x72, 0xcb, 0xFF}  // blue-gray 流量痕迹
	catchColor := color.RGBA{0x99, 0xFF, 0xFF, 0xFF} // 亮蓝色 积水
	dropColor := color.RGBA{0, 0x10, 0xF0, 0xFF}     // 水滴最终位置
	// 点周围 8 个像素，按数量级点亮。必须在 zoom>=5 时才能看到效果
	ring := func(dot *WaterDot, amount int, c color.Color) {
		cx, cy := int(dot.X)*zoom+zoom/2, int(dot.Y)*zoom+zoom/2
		neis := [8]pos{{cx + 1, cy + 1}, {cx + 1, cy}, {cx + 1, cy - 1}, {cx, cy - 1}, {cx - 1, cy - 1}, {cx - 1, cy}, {cx - 1, cy + 1}, {cx, cy + 1}}
		n := calcBit(amount + 1)
		if n > len(neis) {
			n = len(neis) - 1
		}
		for i := 0; i < n; i++ {
			img.Set(neis[i].x, neis[i].y, c)
		}
	}
	for i := range w.Data {
		dot := &w.Data[i]
		if drawFlag&DrawFlagCatchment > 0 && dot.C > 0 {
			ring(dot, int(dot.C), catchColor)
		}
		if drawFlag&DrawFlagQflow > 0 && dot.Q > 0 {
			ring(dot, dot.Q, flowColor)
		}
	}

	// 绘制场向量
	if drawFlag&DrawFlagField > 0 {
		for i := range w.Data {
			dot := &w.Data[i]
			if dot.XPower == 0 && dot.YPower == 0 {
				continue
			}
			sx, sy := int(dot.X)*zoom+zoom/2, int(dot.Y)*zoom+zoom/2
			lineTo(img, sx, sy, sx+int(float32(zoom)*dot.XPower), sy+int(float32(zoom)*dot.YPower),
				color.RGBA{0x66, 0x66, 0xFF, 0xFF}, colorAt(float32(m.Data[i]+dot.C)), opt.RiverArrowScale)
		}
	}
	if drawFlag&DrawFlagDroplet > 0 {
		for i := range s.drops {
			img.Set(int(s.drops[i].X)*zoom+zoom/2, int(s.drops[i].Y)*zoom+zoom/2, dropColor)
		}
	}

	// 左上角绘制色带图例，加一条白色和海平面标记
	csWidth := max(zoom/2-1, 1)
	for i := range cs {
		for wi := 0; wi < csWidth; wi++ {
			img.Set(wi, i, cs[len(cs)-i-1])
		}
	}
	for wi := 0; wi < 4; wi++ {
		img.Set(wi, len(cs), color.White)
	}
	for wi := 1; wi < 4; wi++ {
		img.Set(wi, len(cs)-1-int(float32(len(cs)*SeaLevel)/maxLevel), color.White)
	}
	return img
}

func lineTo(img *image.RGBA, startX, startY, destX, destY int, lineColor, startColor color.Color, scale float64) {
	distM := math.Sqrt(float64((startX-destX)*(startX-destX) + (startY-destY)*(startY-destY)))
	var i float64
	for i = 0; i < distM*scale; i++ {
		img.Set(startX+int(i/distM*float64(destX-startX)), startY+int(i/distM*float64(destY-startY)), lineColor)
	}
	// 线段最后一点 绘制成始发地地形的颜色
	if startX != destX && startY != destY {
		img.Set(startX+int(i/distM*float64(destX-startX)), startY+int(i/distM*float64(destY-startY)), startColor)
	}
}
