// TopoGen Web 服务：提供前端页面、REST 和 WebSocket
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"topogen/internal/api"
	"topogen/internal/sim"
	"topogen/internal/store"
	"topogen/web"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", envOr("TOPOGEN_ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("TOPOGEN_DATA", "data"), "directory for saved simulations")
	maxSessions := flag.Int("max-sessions", 8, "max simulations kept in memory")
	idleTTL := flag.Duration("idle-ttl", 15*time.Minute, "save and unload a simulation after no viewer for this long")
	secureCookie := flag.Bool("secure-cookie", os.Getenv("TOPOGEN_SECURE_COOKIE") == "1", "send the visitor cookie over HTTPS only")
	dev := flag.String("dev", "", "serve static files from this directory instead of the embedded copy")
	flag.Parse()

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open data dir: %v", err)
	}
	var static fs.FS = web.Static
	if *dev != "" {
		static = os.DirFS(*dev)
	}
	mgr := sim.NewManager(sim.Options{
		MaxSessions: *maxSessions, IdleTTL: *idleTTL,
		Store: st, ParamsFromRequest: api.ParamsFromRequest,
	})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Middleware(api.New(mgr, static, api.VisitorAuth{Secure: *secureCookie})),
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("shutting down, saving simulations")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		mgr.Shutdown(ctx)
		log.Printf("saved, bye")
		os.Exit(0)
	}()

	log.Printf("TopoGen listening on %s, data in %s", *addr, *dataDir)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	select {} // 等待保存完成后由信号处理退出
}
