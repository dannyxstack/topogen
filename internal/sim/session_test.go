package sim

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"topogen/internal/engine"
	"topogen/internal/store"
)

func testParams() engine.Params {
	p := engine.DefaultParams()
	p.Width, p.Length, p.Seed = 64, 48, 7
	p.Hills, p.HillWide = 30, 12
	p.Ridges, p.RidgeLen, p.RidgeWide = 2, 8, 8
	p.DropNum, p.Times = 900, 20
	p.Logf = func(string, ...any) {}
	return p
}

// next 读取下一条满足条件的消息
func next(t *testing.T, c *Client, match func(Message, map[string]any) bool) (Message, map[string]any) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-c.Messages():
			var obj map[string]any
			if !m.Binary {
				json.Unmarshal(m.Data, &obj)
			}
			if match(m, obj) {
				return m, obj
			}
		case <-timeout:
			t.Fatal("timed out waiting for message")
		}
	}
}

func isState(st State) func(Message, map[string]any) bool {
	return func(m Message, o map[string]any) bool { return !m.Binary && o["state"] == string(st) }
}

func TestSessionLifecycle(t *testing.T) {
	mgr := NewManager(Options{MaxSessions: 2, IdleTTL: time.Hour})
	s, err := mgr.Create("u", nil, testParams(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Delete(s.ID)
	c := s.Join(false)

	full, _ := next(t, c, func(m Message, _ map[string]any) bool { return m.Binary })
	if full.Data[0] != FrameFull {
		t.Fatalf("first frame kind %d", full.Data[0])
	}
	if w, h := binary.LittleEndian.Uint16(full.Data[5:]), binary.LittleEndian.Uint16(full.Data[7:]); w != 64 || h != 48 {
		t.Fatalf("frame size %dx%d", w, h)
	}
	next(t, c, isState(StatePaused))

	s.Command(c, Request{Op: "step", N: 5})
	delta, _ := next(t, c, func(m Message, _ map[string]any) bool { return m.Binary })
	if delta.Data[0] != FrameDelta || binary.LittleEndian.Uint32(delta.Data[1:]) != 5 {
		t.Fatalf("step frame kind %d tick %d", delta.Data[0], binary.LittleEndian.Uint32(delta.Data[1:]))
	}

	s.Command(c, Request{Op: "pixel", X: 10, Y: 10})
	_, px := next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "pixel" })
	if px["x"].(float64) != 10 || px["tick"].(float64) != 5 {
		t.Fatalf("pixel reply %v", px)
	}

	s.Command(c, Request{Op: "run"})
	next(t, c, isState(StateFinished))
	if info := s.Info(); info.State != StateFinished || info.Stats == nil || info.Stats.Tick != info.Stats.TotalTicks {
		t.Fatalf("info after finish: %+v", info)
	}

	s2, err := mgr.Create("u", nil, testParams(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	s2.Join(false) // 有人在看的会话不会被卸载
	if _, err := mgr.Create("u", nil, testParams(), false, 0); err != ErrTooMany {
		t.Fatalf("expected ErrTooMany, got %v", err)
	}
}

func TestSessionInvalidParams(t *testing.T) {
	mgr := NewManager(Options{MaxSessions: 2, IdleTTL: time.Hour})
	p := testParams()
	p.Width = 0
	if _, err := mgr.Create("u", nil, p, false, 0); err == nil {
		t.Fatal("invalid params accepted")
	}
}

func TestWatchAcrossEnlarge(t *testing.T) {
	mgr := NewManager(Options{MaxSessions: 1, IdleTTL: time.Hour})
	p := testParams()
	p.Enlarge = 1
	s, err := mgr.Create("u", nil, p, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Delete(s.ID)
	c := s.Join(false)
	next(t, c, isState(StatePaused))

	s.Command(c, Request{Op: "watch", X: 20, Y: 30})
	_, first := next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "watch" })
	if got := first["s"].([]any)[0].([]any); got[0].(float64) != 20 || got[1].(float64) != 30 || got[2].(float64) != 0 {
		t.Fatalf("initial sample %v", got)
	}

	s.Command(c, Request{Op: "run"})
	var last []any
	count := 0
	for {
		m, o := next(t, c, func(m Message, o map[string]any) bool {
			return o["type"] == "watch" || o["state"] == string(StateFinished)
		})
		if !m.Binary && o["type"] == "watch" {
			for _, v := range o["s"].([]any) {
				last = v.([]any)
				count++
			}
			continue
		}
		break
	}
	// 结束时的 finished 状态可能先于最后一批采样到达，再取一次
	select {
	case m := <-c.Messages():
		var o map[string]any
		if !m.Binary && json.Unmarshal(m.Data, &o) == nil && o["type"] == "watch" {
			for _, v := range o["s"].([]any) {
				last = v.([]any)
				count++
			}
		}
	case <-time.After(200 * time.Millisecond):
	}
	total := s.Info().Stats.TotalTicks
	if count < int(total)/2 {
		t.Fatalf("only %d samples for %d ticks", count, total)
	}
	if last[0].(float64) != 40 || last[1].(float64) != 60 || int64(last[2].(float64)) != total {
		t.Fatalf("last sample after enlarge %v (total %d)", last, total)
	}
}

func runToEnd(t *testing.T, s *Session) engine.Stats {
	t.Helper()
	c := s.Join(false)
	defer s.Leave(c)
	next(t, c, func(m Message, o map[string]any) bool { return m.Binary })
	s.Command(c, Request{Op: "run"})
	next(t, c, isState(StateFinished))
	var st engine.Stats
	s.Do(context.Background(), func(sim *engine.Sim) { st = sim.Stats() })
	return st
}

func TestPersistAndRestore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	p.Enlarge = 1
	req := json.RawMessage(`{"width":64}`)

	// 不中断地跑完，作为对照
	ref := NewManager(Options{MaxSessions: 4, IdleTTL: time.Hour})
	rs, _ := ref.Create("u", req, p, false, 0)
	want := runToEnd(t, rs)

	mgr := NewManager(Options{MaxSessions: 4, IdleTTL: time.Hour, Store: st})
	s, err := mgr.Create("owner1", req, p, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := s.Join(false)
	next(t, c, isState(StatePaused))
	s.Command(c, Request{Op: "step", N: 37})
	next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "stats" && o["tick"].(float64) == 37 })
	s.Leave(c)
	mgr.Shutdown(context.Background())

	meta, err := st.LoadMeta(s.ID)
	if err != nil || meta.Tick != 37 || meta.Owner != "owner1" || meta.State != string(StatePaused) || string(meta.Request) != string(req) {
		t.Fatalf("meta after shutdown: %+v %v", meta, err)
	}
	if list, _ := mgr.List("owner1"); len(list) != 1 || list[0].Loaded || list[0].Tick != 37 {
		t.Fatalf("list after shutdown: %+v", list)
	}

	// 新进程：从磁盘恢复后继续跑完
	mgr2 := NewManager(Options{MaxSessions: 4, IdleTTL: time.Hour, Store: st})
	s2, err := mgr2.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Owner != "owner1" {
		t.Fatalf("owner = %q", s2.Owner)
	}
	c2 := s2.Join(false)
	full, _ := next(t, c2, func(m Message, _ map[string]any) bool { return m.Binary })
	if tick := binary.LittleEndian.Uint32(full.Data[1:]); tick != 37 {
		t.Fatalf("restored at tick %d", tick)
	}
	s2.Leave(c2)
	got := runToEnd(t, s2)
	if got != want {
		t.Fatalf("restored run differs:\n got %+v\nwant %+v", got, want)
	}

	if err := mgr2.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr2.Get(s.ID); err != ErrNotFound {
		t.Fatalf("get after delete: %v", err)
	}
}

func TestReadOnlyClientCannotControl(t *testing.T) {
	mgr := NewManager(Options{MaxSessions: 2, IdleTTL: time.Hour})
	s, _ := mgr.Create("u", nil, testParams(), false, 0)
	defer mgr.Delete(s.ID)
	c := s.Join(true)
	_, hello := next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "hello" })
	if hello["owner"] != false {
		t.Fatalf("hello = %v", hello)
	}
	next(t, c, isState(StatePaused))
	s.Command(c, Request{Op: "run"})
	_, e := next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "error" })
	if e["message"] == "" {
		t.Fatal("no error for read-only run")
	}
	if s.Info().State != StatePaused {
		t.Fatalf("read-only client changed state to %s", s.Info().State)
	}
	// 观察类命令仍然可用
	s.Command(c, Request{Op: "pixel", X: 1, Y: 1})
	next(t, c, func(m Message, o map[string]any) bool { return o["type"] == "pixel" })
}

func TestOwnerLimit(t *testing.T) {
	mgr := NewManager(Options{MaxSessions: 8, IdleTTL: time.Hour})
	a, _ := mgr.Create("u", nil, testParams(), false, 1)
	a.Join(false)
	if _, err := mgr.Create("u", nil, testParams(), false, 1); err == nil {
		t.Fatal("owner limit not enforced")
	}
	if _, err := mgr.Create("other", nil, testParams(), false, 1); err != nil {
		t.Fatalf("other owner blocked: %v", err)
	}
}
