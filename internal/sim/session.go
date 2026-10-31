// Package sim 管理在线模拟会话：每个会话由一个 goroutine 独占驱动 engine.Sim，
// 客户端通过命令通道控制它，并收到二进制地图帧和 JSON 状态消息。
package sim

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"topogen/internal/engine"
	"topogen/internal/store"
)

type State string

const (
	StateGenerating State = "generating"
	StatePaused     State = "paused"
	StateRunning    State = "running"
	StateFinished   State = "finished"
	StateFailed     State = "failed"
)

const (
	frameInterval = 100 * time.Millisecond
	statsInterval = 500 * time.Millisecond
	saveInterval  = 2 * time.Minute // 运行中定期写快照，防止进程意外退出
	clientBuffer  = 16
)

// 每帧最多推进的 tick 数，按速度档位 1-5
var speedTicks = [...]int{1, 4, 16, 64, 1 << 30}

// Request 是客户端发来的 JSON 命令
type Request struct {
	Op    string `json:"op"` // run | pause | step | speed | pixel | full | watch | unwatch
	N     int    `json:"n,omitempty"`
	Level int    `json:"level,omitempty"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
}

// 只有所有者能发送的控制命令
var controlOps = map[string]bool{"run": true, "pause": true, "step": true, "speed": true}

type Message struct {
	Binary bool
	Data   []byte
}

type Client struct {
	send     chan Message
	needFull atomic.Bool
	readOnly bool // 通过分享链接查看的人只能观察，不能控制
	watch    clientWatch
}

func (c *Client) Messages() <-chan Message { return c.send }

// trySend 不阻塞；客户端处理不过来时丢弃，地图帧丢了之后补发全量帧
func (c *Client) trySend(m Message) bool {
	select {
	case c.send <- m:
		return true
	default:
		if m.Binary {
			c.needFull.Store(true)
		}
		return false
	}
}

type Info struct {
	ID      string        `json:"id"`
	State   State         `json:"state"`
	Error   string        `json:"error,omitempty"`
	Speed   int           `json:"speed"`
	Created time.Time     `json:"created"`
	Clients int           `json:"clients"`
	Stats   *engine.Stats `json:"stats,omitempty"`
}

type command struct {
	req    Request
	client *Client
	join   bool
	leave  bool
	do     func(*engine.Sim)
	unload chan struct{}
}

// sessionConfig 描述新建或从存储恢复的会话
type sessionConfig struct {
	id       string
	owner    string
	request  json.RawMessage
	params   engine.Params
	autoRun  bool
	created  time.Time
	store    *store.Store
	restored *engine.Sim // 不为空时跳过地形生成
}

type Session struct {
	ID      string
	Owner   string
	request json.RawMessage
	params  engine.Params
	autoRun bool
	created time.Time
	store   *store.Store

	cmds   chan command
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	clients  map[*Client]struct{}
	info     Info
	lastSeen time.Time

	// 以下字段只在 loop goroutine 中访问
	sim       *engine.Sim
	state     State
	speed     int
	lastSize  [2]int
	lastStats time.Time
	lastSave  time.Time

	watching   []*Client // 有观察像素的客户端
	watchEvery int64
	watchSize  [2]int
}

func newSession(cfg sessionConfig) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	s := &Session{
		ID: cfg.id, Owner: cfg.owner, request: cfg.request, params: cfg.params,
		autoRun: cfg.autoRun, created: cfg.created, store: cfg.store,
		cmds: make(chan command, 64), ctx: ctx, cancel: cancel,
		clients: map[*Client]struct{}{}, lastSeen: now,
		state: StateGenerating, speed: len(speedTicks),
	}
	s.info = Info{ID: cfg.id, State: StateGenerating, Speed: s.speed, Created: cfg.created}
	if r := cfg.restored; r != nil {
		// 恢复的模拟立即可查询，不必等 loop 启动
		st := r.Stats()
		s.info.Stats, s.info.State = &st, StatePaused
		if r.Done() {
			s.info.State = StateFinished
		}
	}
	go s.loop(cfg.restored)
	return s
}

func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.info
	info.Clients = len(s.clients)
	return info
}

// Join 注册一个客户端，会话随后给它发送当前状态和全量帧
func (s *Session) Join(readOnly bool) *Client {
	c := &Client{send: make(chan Message, clientBuffer), readOnly: readOnly}
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.lastSeen = time.Now()
	s.mu.Unlock()
	s.submit(command{join: true, client: c})
	return c
}

func (s *Session) Leave(c *Client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.lastSeen = time.Now()
	s.mu.Unlock()
	s.submit(command{leave: true, client: c})
}

func (s *Session) Command(c *Client, req Request) {
	s.submit(command{req: req, client: c})
}

// Do 在会话 goroutine 中对当前模拟执行 fn；地形尚未就绪或会话已关闭时返回 false
func (s *Session) Do(ctx context.Context, fn func(*engine.Sim)) bool {
	ran := make(chan bool, 1)
	if !s.submit(command{do: func(sim *engine.Sim) {
		if sim == nil {
			ran <- false
			return
		}
		fn(sim)
		ran <- true
	}}) {
		return false
	}
	select {
	case ok := <-ran:
		return ok
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	}
}

// Close 立即关闭会话，不保存
func (s *Session) Close() { s.cancel() }

// Unload 保存快照后关闭会话，之后可以从存储恢复
func (s *Session) Unload(ctx context.Context) {
	done := make(chan struct{})
	if !s.submit(command{unload: done}) {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (s *Session) submit(cmd command) bool {
	select {
	case s.cmds <- cmd:
		return true
	case <-s.ctx.Done():
		return false
	}
}

// idleSince 返回没有客户端连接的起始时间；有客户端时返回零值
func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) > 0 {
		return time.Time{}
	}
	return s.lastSeen
}

func (s *Session) loop(restored *engine.Sim) {
	defer s.cancel()
	if restored != nil {
		s.ready(restored, false)
	} else {
		start := time.Now()
		sim, err := engine.New(s.params)
		if err != nil {
			// 失败后仍然处理命令，让后来连接的客户端也能收到错误
			s.setState(StateFailed, err.Error())
			s.broadcastJSON(s.stateMsg())
			s.persist()
		} else {
			log.Printf("session %s ready in %s (%dx%d, %d ticks)", s.ID, time.Since(start).Round(time.Millisecond), s.params.Width, s.params.Length, sim.TotalTicks())
			s.ready(sim, s.autoRun)
			s.persist()
		}
	}

	for {
		if s.state == StateRunning {
			s.runFrame()
			if time.Since(s.lastSave) >= saveInterval {
				s.persist()
			}
			if !s.drainCommands() {
				return
			}
			continue
		}
		select {
		case cmd := <-s.cmds:
			if !s.handle(cmd) {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Session) ready(sim *engine.Sim, autoRun bool) {
	sim.SetLogger(nil)
	s.sim = sim
	next := StatePaused
	if sim.Done() {
		next = StateFinished
	} else if autoRun {
		next = StateRunning
	}
	s.setState(next, "")
	// 生成期间加入的客户端还排在命令队列里，处理 join 时会收到全量帧；这里只清空脏标记
	w, l := sim.Size()
	s.lastSize = [2]int{w, l}
	s.watchSize = s.lastSize
	s.watchEvery = max(1, sim.TotalTicks()/watchSteps)
	sim.TakeDirtyTiles()
	s.pushStats()
}

// persist 把元数据和快照写入存储
func (s *Session) persist() {
	s.lastSave = time.Now()
	if s.store == nil {
		return
	}
	meta := store.Meta{ID: s.ID, Owner: s.Owner, Created: s.created, Request: s.request, State: string(s.state)}
	if s.sim != nil {
		st := s.sim.Stats()
		meta.Tick, meta.TotalTicks, meta.Width, meta.Length = st.Tick, st.TotalTicks, st.Width, st.Length
		// 恢复后一律从暂停开始
		if s.state == StateRunning {
			meta.State = string(StatePaused)
		}
		if err := s.store.SaveSnapshot(s.ID, func(w io.Writer) error { return s.sim.Snapshot(w) }); err != nil {
			log.Printf("session %s: save snapshot: %v", s.ID, err)
			return
		}
	}
	if err := s.store.SaveMeta(meta); err != nil {
		log.Printf("session %s: save meta: %v", s.ID, err)
	}
}

// runFrame 在一个帧间隔内尽量推进，然后推送增量帧
func (s *Session) runFrame() {
	deadline := time.Now().Add(frameInterval)
	limit := speedTicks[s.speed-1]
	for n := 0; n < limit && !s.sim.Done() && time.Now().Before(deadline); n++ {
		s.advance(1)
	}
	if s.sim.Done() {
		s.setState(StateFinished, "")
		s.pushFrame(false)
		s.pushStats()
		s.persist()
		return
	}
	s.pushFrame(false)
	if time.Since(s.lastStats) >= statsInterval {
		s.pushStats()
	}
	// 慢速档位把剩余时间睡掉，期间仍响应命令
	if wait := time.Until(deadline); wait > 0 {
		select {
		case cmd := <-s.cmds:
			if !s.handle(cmd) {
				s.cancel()
			}
		case <-time.After(wait):
		case <-s.ctx.Done():
		}
	}
}

func (s *Session) drainCommands() bool {
	for {
		select {
		case cmd := <-s.cmds:
			if !s.handle(cmd) {
				return false
			}
		case <-s.ctx.Done():
			return false
		default:
			return true
		}
	}
}

// handle 处理一条命令，返回 false 表示会话应当退出
func (s *Session) handle(cmd command) bool {
	switch {
	case cmd.unload != nil:
		s.persist()
		close(cmd.unload)
		return false
	case cmd.do != nil:
		cmd.do(s.sim)
		return true
	case cmd.leave:
		s.dropWatcher(cmd.client)
		return true
	case cmd.join:
		s.sendJSON(cmd.client, map[string]any{"type": "hello", "owner": !cmd.client.readOnly})
		s.sendJSON(cmd.client, s.stateMsg())
		if s.sim != nil {
			cmd.client.needFull.Store(false)
			cmd.client.trySend(Message{Binary: true, Data: encodeFull(s.sim)})
			s.sendJSON(cmd.client, s.statsMsg())
		}
		return true
	}

	req := cmd.req
	if s.sim == nil {
		s.sendJSON(cmd.client, map[string]any{"type": "error", "message": "地形尚未就绪"})
		return true
	}
	if controlOps[req.Op] && cmd.client.readOnly {
		s.sendJSON(cmd.client, map[string]any{"type": "error", "message": "只读模式：只有创建者可以控制这个模拟"})
		return true
	}
	switch req.Op {
	case "run":
		if s.state == StatePaused {
			s.setState(StateRunning, "")
			s.broadcastJSON(s.stateMsg())
		}
	case "pause":
		if s.state == StateRunning {
			s.setState(StatePaused, "")
			s.pushFrame(false)
			s.pushStats()
			s.persist()
		}
	case "step":
		if s.state != StatePaused {
			return true
		}
		s.advance(min(max(req.N, 1), 1000))
		if s.sim.Done() {
			s.setState(StateFinished, "")
			s.persist()
		}
		s.pushFrame(false)
		s.pushStats()
	case "speed":
		s.speed = min(max(req.Level, 1), len(speedTicks))
		s.mu.Lock()
		s.info.Speed = s.speed
		s.mu.Unlock()
		s.broadcastJSON(s.stateMsg())
	case "pixel":
		info, ok := s.sim.Pixel(req.X, req.Y)
		if !ok {
			return true
		}
		s.sendJSON(cmd.client, struct {
			Type string `json:"type"`
			Tick int64  `json:"tick"`
			engine.PixelInfo
		}{"pixel", s.sim.Tick(), info})
	case "watch":
		s.watch(cmd.client, req.X, req.Y)
	case "unwatch":
		s.unwatch(cmd.client, req.X, req.Y)
	case "full":
		cmd.client.needFull.Store(false)
		cmd.client.trySend(Message{Binary: true, Data: encodeFull(s.sim)})
	default:
		s.sendJSON(cmd.client, map[string]any{"type": "error", "message": "unknown op " + req.Op})
	}
	return true
}

func (s *Session) setState(st State, errMsg string) {
	s.state = st
	s.mu.Lock()
	s.info.State, s.info.Error = st, errMsg
	s.mu.Unlock()
}

func (s *Session) stateMsg() map[string]any {
	s.mu.Lock()
	errMsg := s.info.Error
	s.mu.Unlock()
	return map[string]any{"type": "state", "state": s.state, "speed": s.speed, "error": errMsg}
}

func (s *Session) statsMsg() any {
	st := s.sim.Stats()
	s.mu.Lock()
	s.info.Stats = &st
	s.mu.Unlock()
	return struct {
		Type  string `json:"type"`
		State State  `json:"state"`
		engine.Stats
	}{"stats", s.state, st}
}

func (s *Session) pushStats() {
	s.lastStats = time.Now()
	s.broadcastJSON(s.statsMsg())
}

// pushFrame 发送有变化的区块；地图尺寸变化(放大)或客户端落后时发送全量帧
func (s *Session) pushFrame(forceFull bool) {
	defer s.flushWatches()
	w, l := s.sim.Size()
	tiles, tilesX, _ := s.sim.TakeDirtyTiles()
	if size := [2]int{w, l}; size != s.lastSize {
		s.lastSize = size
		forceFull = true
	}
	if len(tiles) == 0 && !forceFull {
		return
	}

	var full, delta []byte
	getFull := func() []byte {
		if full == nil {
			full = encodeFull(s.sim)
		}
		return full
	}
	s.mu.Lock()
	clients := make([]*Client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		if forceFull || c.needFull.Load() {
			c.needFull.Store(false)
			c.trySend(Message{Binary: true, Data: getFull()})
			continue
		}
		if delta == nil {
			delta = encodeDelta(s.sim, tiles, tilesX)
		}
		c.trySend(Message{Binary: true, Data: delta})
	}
}

func (s *Session) sendJSON(c *Client, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("session %s: marshal: %v", s.ID, err)
		return
	}
	c.trySend(Message{Data: b})
}

func (s *Session) broadcastJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("session %s: marshal: %v", s.ID, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.trySend(Message{Data: b})
	}
}

// Done 在会话关闭后关闭
func (s *Session) Done() <-chan struct{} { return s.ctx.Done() }

// StatsTick 返回最近一次统计时的 tick
func (i Info) StatsTick() int64 {
	if i.Stats == nil {
		return 0
	}
	return i.Stats.Tick
}

// Request 返回创建时的 API 参数
func (s *Session) Request() json.RawMessage { return s.request }
