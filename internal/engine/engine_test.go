package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func smallParams() Params {
	p := DefaultParams()
	p.Width, p.Length = 64, 48
	p.Seed = 1708692666
	p.Hills, p.HillWide = 30, 12
	p.Ridges, p.RidgeLen, p.RidgeWide = 2, 8, 8
	p.Stuck = 2
	p.DropNum, p.Times = 900, 20
	p.Enlarge = 1
	return p
}

func newQuiet(t *testing.T, p Params) *Sim {
	t.Helper()
	s, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	s.SetLogger(nil)
	return s
}

// 地形、积水、流量、水滴位置全部参与哈希
func stateHash(s *Sim) [32]byte {
	h := sha256.New()
	binary.Write(h, binary.LittleEndian, s.m.Data)
	binary.Write(h, binary.LittleEndian, s.t0)
	binary.Write(h, binary.LittleEndian, s.erodeCnt)
	for i := range s.w.Data {
		d := &s.w.Data[i]
		binary.Write(h, binary.LittleEndian, []int64{int64(d.C), int64(d.Q), int64(d.PrePos), int64(d.OutPos)})
		binary.Write(h, binary.LittleEndian, []float32{d.XPower, d.YPower})
	}
	for i := range s.drops {
		binary.Write(h, binary.LittleEndian, s.drops[i])
	}
	binary.Write(h, binary.LittleEndian, []int64{s.tick, s.stayLongCount, int64(s.m.EvtIdx), int64(s.w.EvtIdx)})
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestDeterministicAcrossWorkers(t *testing.T) {
	p := smallParams()
	var want [32]byte
	for i, workers := range []int{1, 3, 8, 8} {
		p.Workers = workers
		s := newQuiet(t, p)
		s.Run()
		got := stateHash(s)
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("workers=%d gives a different result", workers)
		}
	}
}

func TestStepGranularity(t *testing.T) {
	p := smallParams()
	a := newQuiet(t, p)
	a.Run()

	b := newQuiet(t, p)
	n := 0
	for !b.Done() {
		n += b.Step(7)
	}
	if int64(n) != b.TotalTicks() || b.Tick() != a.Tick() {
		t.Fatalf("ticks: stepped %d, total %d, run %d", n, b.TotalTicks(), a.Tick())
	}
	if stateHash(a) != stateHash(b) {
		t.Fatal("Step(7) loop differs from Run()")
	}
}

func TestSnapshotResume(t *testing.T) {
	p := smallParams()
	full := newQuiet(t, p)
	full.Run()

	// 在初始阶段中途和放大阶段中途各打一次快照
	for _, at := range []int64{37, full.TotalTicks() - 15} {
		s := newQuiet(t, p)
		s.Step(int(at))
		var buf bytes.Buffer
		if err := s.Snapshot(&buf); err != nil {
			t.Fatal(err)
		}
		r, err := Restore(&buf)
		if err != nil {
			t.Fatal(err)
		}
		r.SetLogger(nil)
		if stateHash(r) != stateHash(s) {
			t.Fatalf("restored state differs at tick %d", at)
		}
		r.Run()
		if stateHash(r) != stateHash(full) {
			t.Fatalf("resumed from tick %d differs from uninterrupted run", at)
		}
	}
}

func TestErosionHappens(t *testing.T) {
	s := newQuiet(t, smallParams())
	before := append([]int16(nil), s.m.Data...)
	s.Run()
	st := s.Stats()
	if st.Erosions == 0 || st.QCount == 0 {
		t.Fatalf("no erosion: %+v", st)
	}
	if w, l := s.Size(); w != 128 || l != 96 {
		t.Fatalf("enlarged size = %dx%d", w, l)
	}
	if len(before)*4 != len(s.m.Data) || len(s.t0) != len(s.m.Data) {
		t.Fatal("t0 not upsampled with the map")
	}
	eroded := 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 128; x++ {
			info, ok := s.Pixel(x, y)
			if !ok || info.Eroded != info.T0-info.T || info.Level != info.T+info.C {
				t.Fatalf("bad pixel info at %d,%d: %+v", x, y, info)
			}
			if info.Eroded > 0 {
				eroded++
			}
		}
	}
	if eroded == 0 {
		t.Fatal("no pixel reports erosion")
	}
	if _, ok := s.Pixel(128, 0); ok {
		t.Fatal("out of range pixel accepted")
	}
}

func TestDirtyTiles(t *testing.T) {
	s := newQuiet(t, smallParams())
	tiles, tx, ty := s.TakeDirtyTiles()
	if len(tiles) != tx*ty {
		t.Fatalf("all tiles should start dirty, got %d of %d", len(tiles), tx*ty)
	}
	if tiles, _, _ = s.TakeDirtyTiles(); len(tiles) != 0 {
		t.Fatal("dirty tiles not cleared")
	}
	s.Step(20)
	if tiles, _, _ = s.TakeDirtyTiles(); len(tiles) == 0 {
		t.Fatal("no dirty tiles after stepping")
	}
}

func TestZeroDropsStillEnlarges(t *testing.T) {
	p := smallParams()
	p.DropNum = 0
	s := newQuiet(t, p)
	if !s.Done() || s.TotalTicks() != 0 {
		t.Fatal("simulation without drops should be done immediately")
	}
	if w, _ := s.Size(); w != 128 {
		t.Fatalf("enlarge skipped, width %d", w)
	}
}

func TestRender(t *testing.T) {
	s := newQuiet(t, smallParams())
	s.Run()
	img := s.Render(RenderOptions{Zoom: 3, DrawFlag: DrawFlagCatchment | DrawFlagQflow | DrawFlagField, RiverArrowScale: 0.8})
	if b := img.Bounds(); b.Dx() != 128*3 || b.Dy() != 96*3 {
		t.Fatalf("image size %v", b)
	}
}

func TestExports(t *testing.T) {
	s := newQuiet(t, smallParams())
	s.Run()
	w, l := s.Size()
	lo, hi := s.HeightRange()

	h := s.HeightMap16()
	raw := s.HeightRaw16()
	if h.Bounds().Dx() != w || h.Bounds().Dy() != l || len(raw) != w*l*2 {
		t.Fatalf("height export size %v / %d bytes", h.Bounds(), len(raw))
	}
	var sawLo, sawHi bool
	for i, v := range s.m.Data {
		png := h.Gray16At(i%w, i/w).Y
		if r := binary.LittleEndian.Uint16(raw[i*2:]); r != png {
			t.Fatalf("raw and png differ at %d: %d vs %d", i, r, png)
		}
		sawLo = sawLo || (v == lo && png == 0)
		sawHi = sawHi || (v == hi && png == 65535)
	}
	if !sawLo || !sawHi {
		t.Fatal("height range not mapped to 0..65535")
	}

	f := s.FlowMap16()
	e := s.ErosionMap16()
	m := s.WaterMask()
	var flowMax, erodeMax uint16
	for y := 0; y < l; y++ {
		for x := 0; x < w; x++ {
			flowMax = max(flowMax, f.Gray16At(x, y).Y)
			erodeMax = max(erodeMax, e.Gray16At(x, y).Y)
			idx := x + y*w
			if s.m.Data[idx] < SeaLevel && m.GrayAt(x, y).Y != 255 {
				t.Fatalf("sea pixel %d,%d not masked", x, y)
			}
		}
	}
	if flowMax != 65535 || erodeMax != 65535 {
		t.Fatalf("flow max %d, erosion max %d", flowMax, erodeMax)
	}
}
