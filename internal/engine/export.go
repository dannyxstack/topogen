package engine

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
)

// HeightRange 返回当前地图的最低和最高海拔，导出的 16 位数据按它线性映射到 0-65535
func (s *Sim) HeightRange() (lo, hi int16) {
	hi, lo = extremes(s.m.Data)
	return
}

func (s *Sim) normalized(v int16, lo, hi int16) uint16 {
	if hi <= lo {
		return 0
	}
	return uint16(math.Round(float64(v-lo) / float64(hi-lo) * 65535))
}

// HeightMap16 是 16 位灰度高度图，最低海拔为 0，最高海拔为 65535
func (s *Sim) HeightMap16() *image.Gray16 {
	w, l := s.Size()
	lo, hi := s.HeightRange()
	img := image.NewGray16(image.Rect(0, 0, w, l))
	for i, v := range s.m.Data {
		n := s.normalized(v, lo, hi)
		img.Pix[i*2], img.Pix[i*2+1] = byte(n>>8), byte(n)
	}
	return img
}

// HeightRaw16 是小端序 uint16 的 RAW 高度数据(Unity 的 Windows 字节序)，按行排列，映射规则同 HeightMap16
func (s *Sim) HeightRaw16() []byte {
	lo, hi := s.HeightRange()
	out := make([]byte, len(s.m.Data)*2)
	for i, v := range s.m.Data {
		binary.LittleEndian.PutUint16(out[i*2:], s.normalized(v, lo, hi))
	}
	return out
}

// FlowMap16 是对数缩放的累计流量，log(q+1)/log(maxQ+1) 映射到 0-65535
func (s *Sim) FlowMap16() *image.Gray16 {
	w, l := s.Size()
	_, _, maxQ := s.w.CalcMaxC()
	img := image.NewGray16(image.Rect(0, 0, w, l))
	if maxQ == 0 {
		return img
	}
	den := math.Log(float64(maxQ) + 1)
	for i := range s.w.Data {
		v := uint16(math.Round(math.Log(float64(s.w.Data[i].Q)+1) / den * 65535))
		img.SetGray16(i%w, i/w, color.Gray16{Y: v})
	}
	return img
}

// ErosionMap16 是侵蚀深度 max(t0-t, 0)，按最大侵蚀深度映射到 0-65535；沉积处为 0
func (s *Sim) ErosionMap16() *image.Gray16 {
	w, l := s.Size()
	img := image.NewGray16(image.Rect(0, 0, w, l))
	var maxD int16
	for i, v := range s.m.Data {
		maxD = max(maxD, s.t0[i]-v)
	}
	if maxD <= 0 {
		return img
	}
	for i, v := range s.m.Data {
		d := max(s.t0[i]-v, 0)
		img.SetGray16(i%w, i/w, color.Gray16{Y: uint16(math.Round(float64(d) / float64(maxD) * 65535))})
	}
	return img
}

// WaterMask 是 8 位水体蒙版：海洋为 255，陆地积水按深度 64-254，干燥陆地为 0
func (s *Sim) WaterMask() *image.Gray {
	w, l := s.Size()
	maxC, _, _ := s.w.CalcMaxC()
	img := image.NewGray(image.Rect(0, 0, w, l))
	for i, v := range s.m.Data {
		switch c := s.w.Data[i].C; {
		case v < SeaLevel:
			img.Pix[i] = 255
		case c > 0 && maxC > 0:
			img.Pix[i] = uint8(64 + int(c)*190/int(maxC))
		}
	}
	return img
}
