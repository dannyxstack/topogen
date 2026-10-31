package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"

	"topogen/internal/sim"
	"topogen/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *sim.Manager) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := sim.NewManager(sim.Options{MaxSessions: 4, IdleTTL: time.Hour, Store: st, ParamsFromRequest: ParamsFromRequest})
	static := fstest.MapFS{"index.html": {Data: []byte("<html>")}}
	srv := httptest.NewServer(Middleware(New(mgr, static, VisitorAuth{})))
	t.Cleanup(srv.Close)
	return srv, mgr
}

func newClient(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

var smallReq = CreateRequest{
	Width: 64, Length: 48, Hills: 30, HillWide: 12, Ridges: 2, RidgeLen: 8, RidgeWide: 8,
	PetalShape: 2, PetalNum: 3, PetalSharp: 0.5, DropNum: 900, Times: 20, AutoRun: true,
}

func waitFinished(t *testing.T, c *http.Client, url string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		var info struct{ State string }
		doJSON(t, c, "GET", url, nil, &info)
		if info.State == "finished" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("simulation did not finish")
}

func TestAPIFlow(t *testing.T) {
	srv, mgr := newTestServer(t)
	owner, viewer := newClient(t), newClient(t)

	var me Principal
	if code := doJSON(t, owner, "GET", srv.URL+"/api/me", nil, &me); code != 200 || me.ID == "" || me.Plan.Name != "free" {
		t.Fatalf("/api/me = %d %+v", code, me)
	}

	var created struct {
		ID   string
		Seed int64
	}
	if code := doJSON(t, owner, "POST", srv.URL+"/api/sims", smallReq, &created); code != 201 || created.Seed == 0 {
		t.Fatalf("create = %d %+v", code, created)
	}
	simURL := srv.URL + "/api/sims/" + created.ID
	waitFinished(t, owner, simURL)

	var info struct {
		Owner   bool
		Request CreateRequest
	}
	doJSON(t, owner, "GET", simURL, nil, &info)
	if !info.Owner || info.Request.Seed != created.Seed || info.Request.Width != 64 {
		t.Fatalf("owner view = %+v", info)
	}
	doJSON(t, viewer, "GET", simURL, nil, &info)
	if info.Owner {
		t.Fatal("viewer reported as owner")
	}

	var mine, theirs []sim.Summary
	doJSON(t, owner, "GET", srv.URL+"/api/sims", nil, &mine)
	doJSON(t, viewer, "GET", srv.URL+"/api/sims", nil, &theirs)
	if len(mine) != 1 || mine[0].ID != created.ID || len(theirs) != 0 {
		t.Fatalf("lists: mine=%v theirs=%v", mine, theirs)
	}

	for _, f := range []string{"png", "height16", "flow16", "erosion16", "watermask"} {
		res, err := viewer.Get(simURL + "/export?fmt=" + f)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || err != nil || img.Bounds().Dx() != 64 {
			t.Fatalf("export %s: %d %v", f, res.StatusCode, err)
		}
	}
	res, _ := owner.Get(simURL + "/export?fmt=r16")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if len(raw) != 64*48*2 || !strings.Contains(res.Header.Get("Content-Disposition"), "64x48.r16") {
		t.Fatalf("r16: %d bytes, %q", len(raw), res.Header.Get("Content-Disposition"))
	}
	if code := doJSON(t, owner, "GET", simURL+"/export?fmt=bogus", nil, nil); code != 403 {
		t.Fatalf("unknown export format = %d", code)
	}

	// 只读查看者：WebSocket 收到 owner=false，控制命令被拒绝
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/sims/" + created.ID
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: viewer})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"owner":false`) {
		t.Fatalf("hello = %s %v", data, err)
	}

	if code := doJSON(t, viewer, "DELETE", simURL, nil, nil); code != 403 {
		t.Fatalf("viewer delete = %d", code)
	}

	// 卸载到磁盘后仍能访问
	mgr.Shutdown(context.Background())
	var reloaded struct {
		Owner   bool
		State   string
		Stats   *struct{ Tick int64 }
		Request CreateRequest
	}
	doJSON(t, owner, "GET", simURL, nil, &reloaded)
	if !reloaded.Owner || reloaded.Request.Seed != created.Seed || reloaded.State != "finished" || reloaded.Stats == nil || reloaded.Stats.Tick == 0 {
		t.Fatalf("after reload = %+v", reloaded)
	}
	if code := doJSON(t, owner, "DELETE", simURL, nil, nil); code != 204 {
		t.Fatalf("owner delete = %d", code)
	}
	if code := doJSON(t, owner, "GET", simURL, nil, nil); code != 404 {
		t.Fatalf("get after delete = %d", code)
	}
}

func TestCreateRejectsOverLimit(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t)
	big := smallReq
	big.Width = 5000
	var e struct{ Error string }
	if code := doJSON(t, c, "POST", srv.URL+"/api/sims", big, &e); code != 400 || !strings.Contains(e.Error, "边长") {
		t.Fatalf("oversize = %d %q", code, e.Error)
	}
}

func TestParamsFromRequestReproduces(t *testing.T) {
	req := smallReq
	req.Seed = 99
	raw, _ := json.Marshal(req)
	p, err := ParamsFromRequest(raw)
	if err != nil || p.Seed != 99 || p.Width != 64 {
		t.Fatalf("ParamsFromRequest = %+v %v", p, err)
	}
}
