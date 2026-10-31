package sim

import "slices"

const (
	maxWatches = 4   // 每个客户端最多观察的像素数
	watchSteps = 400 // 整个模拟大约采样这么多次
)

type watchPos struct{ x, y int }

// 以下 Client 字段只在会话 goroutine 中访问
type clientWatch struct {
	watches []watchPos
	pending [][6]int64 // x, y, tick, t, c, q
}

// advance 逐 tick 推进，并按间隔采样被观察的像素
func (s *Session) advance(n int) {
	for i := 0; i < n && !s.sim.Done(); i++ {
		s.sim.Step(1)
		s.remapWatches()
		if len(s.watching) > 0 && (s.sim.Tick()%s.watchEvery == 0 || s.sim.Done()) {
			for _, c := range s.watching {
				for _, p := range c.watch.watches {
					s.sample(c, p)
				}
			}
		}
	}
}

func (s *Session) sample(c *Client, p watchPos) {
	info, ok := s.sim.Pixel(p.x, p.y)
	if !ok {
		return
	}
	c.watch.pending = append(c.watch.pending, [6]int64{int64(p.x), int64(p.y), s.sim.Tick(), int64(info.T), int64(info.C), int64(info.Q)})
}

// 地图放大后坐标翻倍，与前端在收到新尺寸全量帧时的换算一致
func (s *Session) remapWatches() {
	w, l := s.sim.Size()
	if s.watchSize == [2]int{w, l} {
		return
	}
	if s.watchSize[0] > 0 {
		// 放大前的采样先发出去，保证它们排在新尺寸的全量帧之前
		s.flushWatches()
		f := w / s.watchSize[0]
		for _, c := range s.watching {
			for i := range c.watch.watches {
				c.watch.watches[i].x *= f
				c.watch.watches[i].y *= f
			}
		}
	}
	s.watchSize = [2]int{w, l}
}

func (s *Session) watch(c *Client, x, y int) {
	w, l := s.sim.Size()
	if x < 0 || y < 0 || x >= w || y >= l {
		return
	}
	p := watchPos{x, y}
	if !slices.Contains(c.watch.watches, p) {
		if len(c.watch.watches) >= maxWatches {
			c.watch.watches = c.watch.watches[1:]
		}
		c.watch.watches = append(c.watch.watches, p)
	}
	if !slices.Contains(s.watching, c) {
		s.watching = append(s.watching, c)
	}
	s.sample(c, p)
	s.flushWatch(c)
}

func (s *Session) unwatch(c *Client, x, y int) {
	c.watch.watches = slices.DeleteFunc(c.watch.watches, func(p watchPos) bool { return p == watchPos{x, y} })
	if len(c.watch.watches) == 0 {
		s.dropWatcher(c)
	}
}

func (s *Session) dropWatcher(c *Client) {
	s.watching = slices.DeleteFunc(s.watching, func(x *Client) bool { return x == c })
}

func (s *Session) flushWatch(c *Client) {
	if len(c.watch.pending) == 0 {
		return
	}
	s.sendJSON(c, map[string]any{"type": "watch", "s": c.watch.pending})
	c.watch.pending = c.watch.pending[:0]
}

func (s *Session) flushWatches() {
	for _, c := range s.watching {
		s.flushWatch(c)
	}
}
