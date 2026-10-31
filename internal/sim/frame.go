package sim

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math"

	"topogen/internal/engine"
)

// 二进制帧格式(小端)：
//
//	FULL : u8 1 | u32 tick | u16 w | u16 h | gzip( q[n]i32 | t[n]i16 | t0[n]i16 | c[n]i16 )
//	DELTA: u8 2 | u32 tick | u16 tiles | gzip( 每块: u16 x0 | u16 y0 | u16 tw | u16 th | q[a]i32 | t[a]i16 | c[a]i16 )
//
// q 放在最前面，保证前端可以直接在解压后的缓冲上建 Int32Array / Int16Array。
const (
	FrameFull  = 1
	FrameDelta = 2
)

func frameHeader(kind byte, tick int64) *bytes.Buffer {
	var buf bytes.Buffer
	buf.WriteByte(kind)
	binary.Write(&buf, binary.LittleEndian, uint32(tick))
	return &buf
}

func clampQ(q int) int32 {
	return int32(min(q, math.MaxInt32))
}

func encodeFull(s *engine.Sim) []byte {
	w, l := s.Size()
	buf := frameHeader(FrameFull, s.Tick())
	binary.Write(buf, binary.LittleEndian, [2]uint16{uint16(w), uint16(l)})

	water := s.Water()
	n := w * l
	raw := make([]byte, n*10)
	for i := range water {
		binary.LittleEndian.PutUint32(raw[i*4:], uint32(clampQ(water[i].Q)))
	}
	putInt16s(raw[n*4:], s.Heights())
	putInt16s(raw[n*6:], s.Initial())
	for i := range water {
		binary.LittleEndian.PutUint16(raw[n*8+i*2:], uint16(water[i].C))
	}
	writeGzip(buf, raw)
	return buf.Bytes()
}

func encodeDelta(s *engine.Sim, tiles []int, tilesX int) []byte {
	w, l := s.Size()
	buf := frameHeader(FrameDelta, s.Tick())
	binary.Write(buf, binary.LittleEndian, uint16(len(tiles)))

	heights, water := s.Heights(), s.Water()
	var raw bytes.Buffer
	for _, tile := range tiles {
		x0, y0 := tile%tilesX*engine.TileSize, tile/tilesX*engine.TileSize
		tw, th := min(engine.TileSize, w-x0), min(engine.TileSize, l-y0)
		a := tw * th
		chunk := make([]byte, 8+a*8)
		binary.LittleEndian.PutUint16(chunk[0:], uint16(x0))
		binary.LittleEndian.PutUint16(chunk[2:], uint16(y0))
		binary.LittleEndian.PutUint16(chunk[4:], uint16(tw))
		binary.LittleEndian.PutUint16(chunk[6:], uint16(th))
		k := 0
		for y := y0; y < y0+th; y++ {
			for x := x0; x < x0+tw; x++ {
				idx := x + y*w
				binary.LittleEndian.PutUint32(chunk[8+k*4:], uint32(clampQ(water[idx].Q)))
				binary.LittleEndian.PutUint16(chunk[8+a*4+k*2:], uint16(heights[idx]))
				binary.LittleEndian.PutUint16(chunk[8+a*6+k*2:], uint16(water[idx].C))
				k++
			}
		}
		raw.Write(chunk)
	}
	writeGzip(buf, raw.Bytes())
	return buf.Bytes()
}

func putInt16s(dst []byte, src []int16) {
	for i, v := range src {
		binary.LittleEndian.PutUint16(dst[i*2:], uint16(v))
	}
}

func writeGzip(buf *bytes.Buffer, raw []byte) {
	zw, _ := gzip.NewWriterLevel(buf, gzip.BestSpeed)
	zw.Write(raw)
	zw.Close()
}
