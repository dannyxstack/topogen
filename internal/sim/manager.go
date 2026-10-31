package sim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"topogen/internal/engine"
	"topogen/internal/store"
)

var (
	ErrTooMany  = errors.New("同时运行的模拟已达上限，请稍后再试")
	ErrNotFound = errors.New("模拟不存在或已删除")
)

type Options struct {
	MaxSessions int           // 内存中同时存在的会话上限
	IdleTTL     time.Duration // 没有客户端连接超过该时长的会话会被保存并卸载
	Store       *store.Store  // 为空时不持久化
	// 没有快照时用创建参数重新生成地形
	ParamsFromRequest func(json.RawMessage) (engine.Params, error)
}

type Manager struct {
	opt Options

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewManager(opt Options) *Manager {
	m := &Manager{opt: opt, sessions: map[string]*Session{}}
	go m.reap()
	return m
}

// Create 新建一个会话。perOwner 为该所有者在内存中的会话上限，0 表示不限。
func (m *Manager) Create(owner string, request json.RawMessage, p engine.Params, autoRun bool, perOwner int) (*Session, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if perOwner > 0 {
		if err := m.makeRoomLocked(owner, perOwner); err != nil {
			return nil, err
		}
	}
	if err := m.makeRoomLocked("", m.opt.MaxSessions); err != nil {
		return nil, err
	}
	now := time.Now()
	id := newID()
	if m.opt.Store != nil {
		if err := m.opt.Store.SaveMeta(store.Meta{ID: id, Owner: owner, Created: now, Request: request, State: string(StateGenerating)}); err != nil {
			return nil, fmt.Errorf("保存模拟失败: %w", err)
		}
	}
	s := newSession(sessionConfig{id: id, owner: owner, request: request, params: p, autoRun: autoRun, created: now, store: m.opt.Store})
	m.sessions[id] = s
	return s, nil
}

// makeRoomLocked 确保 owner(为空表示全部)的会话数小于 limit，必要时卸载最久没人看的闲置会话
func (m *Manager) makeRoomLocked(owner string, limit int) error {
	for {
		var count int
		var victim *Session
		var victimIdle time.Time
		for _, s := range m.sessions {
			if owner != "" && s.Owner != owner {
				continue
			}
			count++
			if since := s.idleSince(); !since.IsZero() && (victim == nil || since.Before(victimIdle)) {
				victim, victimIdle = s, since
			}
		}
		if count < limit {
			return nil
		}
		if victim == nil {
			if owner != "" {
				return fmt.Errorf("你同时打开的模拟已达 %d 个，请先关闭一些页面", limit)
			}
			return ErrTooMany
		}
		m.unloadLocked(victim)
	}
}

func (m *Manager) unloadLocked(s *Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.Unload(ctx)
	delete(m.sessions, s.ID)
	log.Printf("session %s unloaded", s.ID)
}

// Get 返回会话；不在内存中时从存储恢复
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s, nil
	}
	st := m.opt.Store
	if st == nil {
		return nil, ErrNotFound
	}
	meta, err := st.LoadMeta(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	cfg := sessionConfig{id: id, owner: meta.Owner, request: meta.Request, created: meta.Created, store: st}
	if rc, err := st.OpenSnapshot(id); err == nil {
		sim, rerr := engine.Restore(rc)
		rc.Close()
		if rerr != nil {
			return nil, fmt.Errorf("恢复快照失败: %w", rerr)
		}
		cfg.restored, cfg.params = sim, sim.Params()
	} else if errors.Is(err, store.ErrNotFound) && m.opt.ParamsFromRequest != nil {
		// 地形还没生成完就被保存的模拟：参数相同结果就相同，重新生成即可
		if cfg.params, err = m.opt.ParamsFromRequest(meta.Request); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("读取快照失败: %w", err)
	}
	if err := m.makeRoomLocked("", m.opt.MaxSessions); err != nil {
		return nil, err
	}
	s := newSession(cfg)
	m.sessions[id] = s
	log.Printf("session %s restored at tick %d", id, meta.Tick)
	return s, nil
}

// Live 只返回内存中的会话，不触发恢复
func (m *Manager) Live(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Summary 是"我的模拟"列表中的一项
type Summary struct {
	ID         string    `json:"id"`
	State      State     `json:"state"`
	Loaded     bool      `json:"loaded"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Tick       int64     `json:"tick"`
	TotalTicks int64     `json:"totalTicks"`
	Width      int       `json:"width"`
	Length     int       `json:"length"`
}

func (m *Manager) List(owner string) ([]Summary, error) {
	var out []Summary
	seen := map[string]bool{}
	m.mu.Lock()
	live := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.Owner == owner {
			live = append(live, s)
		}
	}
	m.mu.Unlock()
	for _, s := range live {
		info := s.Info()
		sum := Summary{ID: s.ID, State: info.State, Loaded: true, Created: info.Created, Updated: time.Now()}
		if info.Stats != nil {
			sum.Tick, sum.TotalTicks, sum.Width, sum.Length = info.Stats.Tick, info.Stats.TotalTicks, info.Stats.Width, info.Stats.Length
		}
		out = append(out, sum)
		seen[s.ID] = true
	}
	if m.opt.Store != nil {
		metas, err := m.opt.Store.List(owner)
		if err != nil {
			return nil, err
		}
		for _, meta := range metas {
			if seen[meta.ID] {
				continue
			}
			out = append(out, Summary{ID: meta.ID, State: State(meta.State), Created: meta.Created, Updated: meta.Updated,
				Tick: meta.Tick, TotalTicks: meta.TotalTicks, Width: meta.Width, Length: meta.Length})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// Owner 返回模拟的所有者，不加载会话
func (m *Manager) Owner(id string) (string, error) {
	if s, ok := m.Live(id); ok {
		return s.Owner, nil
	}
	if m.opt.Store == nil {
		return "", ErrNotFound
	}
	meta, err := m.opt.Store.LoadMeta(id)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotFound
	}
	return meta.Owner, err
}

// Delete 关闭会话并删除存储
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if ok {
		s.Close()
	}
	if m.opt.Store != nil {
		if _, err := m.opt.Store.LoadMeta(id); err == nil {
			return m.opt.Store.Delete(id)
		}
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Shutdown 保存并卸载所有会话，用于进程退出前
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var wg sync.WaitGroup
	for id, s := range m.sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Unload(ctx)
		}()
		delete(m.sessions, id)
	}
	wg.Wait()
}

func (m *Manager) reap() {
	for range time.Tick(time.Minute) {
		m.mu.Lock()
		for _, s := range m.sessions {
			if since := s.idleSince(); !since.IsZero() && time.Since(since) > m.opt.IdleTTL {
				m.unloadLocked(s)
			}
		}
		m.mu.Unlock()
	}
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
