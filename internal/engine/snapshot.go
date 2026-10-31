package engine

import (
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"io"
	"math/rand"
)

const snapshotVersion = 1

type snapshot struct {
	Version int
	P       Params // 不含 Basemap 图片，地形已经生成

	RngSeed  int64
	RngCalls uint64

	M        Topomap
	W        WaterMap
	T0       []int16
	ErodeCnt []uint16
	SediCnt  []uint16

	Drops        []Droplet
	DropMaxLevel int

	Stage, Page, Step int
	Tick              int64
	Done              bool
	StayLongCount     int64
}

// Snapshot 把完整状态写入 out(gzip + gob)，Restore 后继续 Step 与不中断运行结果一致
func (s *Sim) Snapshot(out io.Writer) error {
	p := s.p
	p.Basemap = nil
	snap := snapshot{
		Version: snapshotVersion, P: p,
		RngSeed: s.src.seed, RngCalls: s.src.n,
		M: s.m, W: s.w, T0: s.t0, ErodeCnt: s.erodeCnt, SediCnt: s.sediCnt,
		Drops: s.drops, DropMaxLevel: s.dropMaxLevel,
		Stage: s.stage, Page: s.page, Step: s.step, Tick: s.tick, Done: s.done,
		StayLongCount: s.stayLongCount,
	}
	zw := gzip.NewWriter(out)
	if err := gob.NewEncoder(zw).Encode(&snap); err != nil {
		return err
	}
	return zw.Close()
}

func Restore(in io.Reader) (*Sim, error) {
	zr, err := gzip.NewReader(in)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var snap snapshot
	if err := gob.NewDecoder(zr).Decode(&snap); err != nil {
		return nil, err
	}
	if snap.Version != snapshotVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d", snap.Version)
	}
	if n := snap.M.Width * snap.M.Length; n == 0 || len(snap.M.Data) != n || len(snap.W.Data) != n ||
		len(snap.T0) != n || len(snap.ErodeCnt) != n || len(snap.SediCnt) != n {
		return nil, fmt.Errorf("snapshot map size mismatch")
	}
	s := &Sim{
		p: snap.P,
		m: snap.M, w: snap.W, t0: snap.T0, erodeCnt: snap.ErodeCnt, sediCnt: snap.SediCnt,
		drops: snap.Drops, dropMaxLevel: snap.DropMaxLevel,
		stage: snap.Stage, page: snap.Page, step: snap.Step, tick: snap.Tick, done: snap.Done,
		stayLongCount: snap.StayLongCount,
	}
	s.setup()
	s.src = newCountingSource(snap.RngSeed)
	s.src.replay(snap.RngSeed, snap.RngCalls)
	s.rng = rand.New(s.src)
	s.resetDirty()
	return s, nil
}
