// Command imaliguzve is the whole service: one static Go binary (PLAN §2).
//
//	imaliguzve serve      run ingest + public pages + observability (default)
//	imaliguzve provision  create a venue + device on the VPS, print the token
//
// Layout follows PLAN §3: this file only wires packages together — no
// business logic, no SQL.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"imaliguzve/internal/auth"
	"imaliguzve/internal/frames"
	"imaliguzve/internal/proto"
	"imaliguzve/internal/render"
	"imaliguzve/internal/sched"
	"imaliguzve/internal/store"
	"imaliguzve/internal/web"
)

// version is stamped via -ldflags "-X main.version=…" at release build.
var version = "dev"

// renderConcurrency caps concurrent Process calls (§6.1 global semaphore).
// 4 keeps fast p95 unaffected while 1 slow client waits (§6.1 gate: fakecam
// 1 slow + 19 fast).
const renderConcurrency = 4

// newMux wires ingest + public routes. Kept out of serveMain so a test can
// pin the composition: conflicting ServeMux patterns panic at registration,
// and route behaviour only changes when both halves are assembled together.
// The ingest route is POST-scoped so it cannot conflict with web's GET "/"
// fallback (Go 1.22: method sets must be disjoint or paths comparable);
// every other method on /v1/ingest still lands on 405 Allow: POST —
// either via the web hint or ServeMux's own method-mismatch answer.
func newMux(ing *frames.Server, pub *web.Server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proto.PathIngest, ing.HandleIngest)
	pub.Register(mux)
	return mux
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "provision":
			os.Exit(provisionMain(args[1:]))
		case "serve":
			args = args[1:] // strip the subcommand so flags actually parse
		}
	}
	os.Exit(serveMain(args))
}

func serveMain(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "loopback listen address (only Caddy reaches it)")
	dataDir := fs.String("data", "/var/lib/imaliguzve", "data dir: SQLite + venue renders")
	retainHours := fs.Int("retain-hours", 48, "public render ring size in hours (§6.3)")
	tlsCert := fs.String("tls-cert", "", "PEM certificate — enables TLS (LAN bench; production TLS is Caddy's, runbook)")
	tlsKey := fs.String("tls-key", "", "PEM key (must pair with -tls-cert)")
	fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if (*tlsCert == "") != (*tlsKey == "") {
		log.Error("tls flags", "err", "-tls-cert and -tls-key must be set together")
		return 1
	}

	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		log.Error("create data dir", "err", err.Error())
		return 1
	}
	st, err := store.Open(filepath.Join(*dataDir, "imaliguzve.db"))
	if err != nil {
		log.Error("open store", "err", err.Error())
		return 1
	}
	defer st.Close()

	eng := render.NewEngine(render.RealClock{}, renderConcurrency)
	lim := auth.DefaultLimiter(time.Now)
	ing := frames.New(st, eng, lim, *dataDir, *retainHours, log)

	pub, err := web.New(st, log)
	if err != nil {
		log.Error("web init", "err", err.Error())
		return 1
	}
	started := time.Now()
	pub.Version = version
	pub.Started = started
	pub.DataDir = *dataDir
	pub.DiskFreePct = func() (float64, error) { return frames.DiskFreePct(*dataDir) }
	pub.Metrics = func() string {
		var b strings.Builder
		b.WriteString(ing.Metrics().Prometheus())
		fmt.Fprintf(&b, "# TYPE imaliguzve_up gauge\nimaliguzve_up 1\n")
		fmt.Fprintf(&b, "imaliguzve_uptime_seconds %d\n", int(time.Since(started).Seconds()))
		if pct, err := frames.DiskFreePct(*dataDir); err == nil {
			fmt.Fprintf(&b, "imaliguzve_disk_free_pct %.1f\n", pct)
		}
		return b.String()
	}

	mux := newMux(ing, pub)

	srv := &http.Server{
		Addr:    *listen,
		Handler: mux,
		// Slow clients must not tie up resources (§6.1). ReadTimeout
		// exceeds the device's 25 s POST budget on purpose.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One ticker goroutine (§2): pruner runs ~1 s after boot, then hourly.
	go sched.Run(ctx, log, sched.Job{
		Name:  "prune",
		Every: time.Hour,
		Run: func(ctx context.Context) error {
			_, err := ing.Prune(ctx)
			return err
		},
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *listen, "tls", *tlsCert != "",
			"data", *dataDir, "version", version)
		if *tlsCert != "" {
			errCh <- srv.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("shutdown", "err", err.Error())
			return 1
		}
		return 0
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err.Error())
			return 1
		}
		return 0
	}
}
