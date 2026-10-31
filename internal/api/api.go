// Package api 提供 REST 和 WebSocket 接口
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"topogen/internal/engine"
	"topogen/internal/sim"
)

type CreateRequest struct {
	Width      int     `json:"width"`
	Length     int     `json:"length"`
	Seed       int64   `json:"seed"`
	Hills      int     `json:"hills"`
	HillWide   int     `json:"hillWide"`
	Ridges     int     `json:"ridges"`
	RidgeLen   int     `json:"ridgeLen"`
	RidgeWide  int     `json:"ridgeWide"`
	Stuck      int     `json:"stuck"`
	PetalShape int     `json:"petalShape"`
	PetalNum   float64 `json:"petalNum"`
	PetalSharp float64 `json:"petalSharp"`
	DropNum    int     `json:"dropNum"`
	Times      int     `json:"times"`
	Enlarge    int     `json:"enlarge"`
	FixField   bool    `json:"fixField"`
	FixMiddle  bool    `json:"fixMiddle"`
	AutoRun    bool    `json:"autoRun"`
}

// params 按套餐上限校验并转换为引擎参数；seed 为 0 时取当前时间并写回请求
func (r *CreateRequest) params(plan Plan) (engine.Params, error) {
	if r.Seed == 0 {
		r.Seed = time.Now().Unix()
	}
	p := engine.DefaultParams()
	p.Width, p.Length, p.Seed = r.Width, r.Length, r.Seed
	p.Hills, p.HillWide = r.Hills, r.HillWide
	p.Ridges, p.RidgeLen, p.RidgeWide = r.Ridges, r.RidgeLen, r.RidgeWide
	p.Stuck = r.Stuck
	p.Petal = engine.PetalFlag{Shape: r.PetalShape, PetalNum: r.PetalNum, Sharp: r.PetalSharp}
	p.DropNum, p.Times, p.Enlarge = r.DropNum, r.Times, r.Enlarge
	p.Compat = engine.CompatFlags{FixFieldInit: r.FixField, FixMiddleIdx: r.FixMiddle}
	p.Logf = func(string, ...any) {}

	switch {
	case p.Width < 16 || p.Length < 16 || p.Width > plan.MaxSide || p.Length > plan.MaxSide:
		return p, fmt.Errorf("地图边长需在 16–%d 之间", plan.MaxSide)
	case p.Enlarge < 0 || p.Enlarge > plan.MaxEnlarge:
		return p, fmt.Errorf("放大次数需在 0–%d 之间", plan.MaxEnlarge)
	case (p.Width<<p.Enlarge)*(p.Length<<p.Enlarge) > plan.MaxFinalPixels:
		return p, fmt.Errorf("放大后地图超过 %d 像素，请减小边长或放大次数", plan.MaxFinalPixels)
	case p.DropNum < 0 || p.DropNum > plan.MaxDrops:
		return p, fmt.Errorf("水滴数需在 0–%d 之间", plan.MaxDrops)
	case p.Times < 1 || p.Times > plan.MaxTimes:
		return p, fmt.Errorf("每批步数需在 1–%d 之间", plan.MaxTimes)
	case p.Hills > 2000 || p.HillWide > 400 || p.Ridges > 50 || p.RidgeLen > 200 || p.RidgeWide > 400 || p.Stuck > 50:
		return p, errors.New("地形参数超出范围")
	case p.Petal.Shape < 0 || p.Petal.Shape > 3 || p.Petal.Sharp < 0 || p.Petal.Sharp > 1:
		return p, errors.New("花瓣形状需在 0–3，锋利度需在 0–1")
	}
	return p, p.Validate()
}

// ParamsFromRequest 把保存的创建参数还原为引擎参数，用于恢复没有快照的模拟。
// 创建时已按当时的套餐校验过，这里不再限制。
func ParamsFromRequest(raw json.RawMessage) (engine.Params, error) {
	var req CreateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return engine.Params{}, err
	}
	unlimited := Plan{MaxSide: 1 << 15, MaxFinalPixels: 1 << 30, MaxDrops: 1 << 30, MaxTimes: 1 << 20, MaxEnlarge: 8}
	return req.params(unlimited)
}

type Server struct {
	mgr    *sim.Manager
	static fs.FS
	auth   Authenticator
}

func New(mgr *sim.Manager, static fs.FS, auth Authenticator) http.Handler {
	s := &Server{mgr: mgr, static: static, auth: auth}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("POST /api/sims", s.create)
	mux.HandleFunc("GET /api/sims", s.list)
	mux.HandleFunc("GET /api/sims/{id}", s.get)
	mux.HandleFunc("DELETE /api/sims/{id}", s.delete)
	mux.HandleFunc("GET /api/sims/{id}/export", s.export)
	mux.HandleFunc("GET /ws/sims/{id}", s.ws)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.auth.Identify(w, r))
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	var req CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	p, err := req.params(me.Plan)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, _ := json.Marshal(req) // 含实际使用的 seed，可原样复现
	sess, err := s.mgr.Create(me.ID, stored, p, req.AutoRun, me.Plan.MaxLive)
	if errors.Is(err, sim.ErrTooMany) {
		writeErr(w, http.StatusTooManyRequests, err.Error())
		return
	} else if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": sess.ID, "seed": p.Seed})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	list, err := s.mgr.List(me.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []sim.Summary{}
	}
	writeJSON(w, http.StatusOK, list)
}

// session 返回会话，不在内存时从存储恢复
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*sim.Session, bool) {
	sess, err := s.mgr.Get(r.PathValue("id"))
	switch {
	case errors.Is(err, sim.ErrNotFound):
		writeErr(w, http.StatusNotFound, "模拟不存在或已删除")
		return nil, false
	case errors.Is(err, sim.ErrTooMany):
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return nil, false
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return sess, true
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		sim.Info
		Owner   bool            `json:"owner"`
		Request json.RawMessage `json:"request,omitempty"`
	}{sess.Info(), sess.Owner == me.ID, sess.Request()})
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	id := r.PathValue("id")
	owner, err := s.mgr.Owner(id)
	if errors.Is(err, sim.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "模拟不存在或已删除")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if owner != me.ID {
		writeErr(w, http.StatusForbidden, "只有创建者可以删除这个模拟")
		return
	}
	if err := s.mgr.Delete(id); err != nil && !errors.Is(err, sim.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// export 导出当前地图。fmt: png(彩色预览) height16 r16 flow16 erosion16 watermask
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	format := r.URL.Query().Get("fmt")
	if format == "" {
		format = "png"
	}
	if !me.Plan.allowsExport(format) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("当前套餐不支持导出 %s", format))
		return
	}
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	zoom, _ := strconv.Atoi(r.URL.Query().Get("zoom"))

	var (
		img          image.Image
		raw          []byte
		tick         int64
		width, lngth int
		lo, hi       int16
	)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	// 在会话协程里只取数据，PNG 编码放到这里做，避免阻塞模拟
	ok = sess.Do(ctx, func(m *engine.Sim) {
		tick = m.Tick()
		width, lngth = m.Size()
		lo, hi = m.HeightRange()
		switch format {
		case "png":
			img = m.Render(engine.RenderOptions{Zoom: min(max(zoom, 1), 4), DrawFlag: engine.DrawFlagQflow, RiverArrowScale: 0.8})
		case "height16":
			img = m.HeightMap16()
		case "r16":
			raw = m.HeightRaw16()
		case "flow16":
			img = m.FlowMap16()
		case "erosion16":
			img = m.ErosionMap16()
		case "watermask":
			img = m.WaterMask()
		}
	})
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "地图尚未就绪")
		return
	}
	if img == nil && raw == nil {
		writeErr(w, http.StatusBadRequest, "未知的导出格式 "+format)
		return
	}
	name := fmt.Sprintf("topogen-%s-t%d-%s", sess.ID, tick, format)
	w.Header().Set("X-Map-Size", fmt.Sprintf("%dx%d", width, lngth))
	w.Header().Set("X-Height-Range", fmt.Sprintf("%d,%d", lo, hi))
	if raw != nil {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%dx%d.r16"`, name, width, lngth))
		w.Write(raw)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.png"`, name))
	if err := png.Encode(w, img); err != nil {
		log.Printf("export %s: %v", sess.ID, err)
	}
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	me := s.auth.Identify(w, r)
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(4 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	client := sess.Join(sess.Owner != me.ID)
	defer sess.Leave(client)

	go func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var req sim.Request
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			sess.Command(client, req)
		}
	}()

	for {
		select {
		case msg := <-client.Messages():
			typ := websocket.MessageText
			if msg.Binary {
				typ = websocket.MessageBinary
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, typ, msg.Data)
			wcancel()
			if err != nil {
				conn.CloseNow()
				return
			}
		case <-sess.Done():
			conn.Close(websocket.StatusGoingAway, "模拟已关闭")
			return
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		}
	}
}

// Middleware 记录 API 请求并加上基础安全头
func Middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
