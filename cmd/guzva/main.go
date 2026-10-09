// Command guzva is the whole service: one static Go binary (PLAN §2).
//
//	guzva serve      ingest + static public pages + admin console (default)
//	guzva provision  create a venue + device, print the token once (-token = import)
//	guzva cfg        show or change a device's desired_cfg (-set k=v,...)
//	guzva hashpw     bcrypt-hash a password for GUZVA_ADMIN_HASH
//
// Layout follows PLAN §3: this file only wires packages together — no
// business logic, no SQL.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // venue tz names resolve without a system zone db (§2)

	"guzva/internal/admin"
	"guzva/internal/auth"
	"guzva/internal/frames"
	"guzva/internal/pages"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/sched"
	"guzva/internal/store"
)

// version is stamped via -ldflags "-X main.version=…" at release build.
var version = "dev"

// renderConcurrency caps concurrent Process calls (§6.1 global semaphore).
// 4 keeps fast p95 unaffected while 1 slow client waits (§6.1 gate: fakecam
// 1 slow + 19 fast).
const renderConcurrency = 4

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "provision":
			os.Exit(provisionMain(args[1:]))
		case "cfg":
			os.Exit(cfgMain(args[1:]))
		case "hashpw":
			os.Exit(hashpwMain(args[1:]))
		case "serve":
			args = args[1:] // strip the subcommand so flags actually parse
		}
	}
	os.Exit(serveMain(args))
}

// envOr returns the flag value, falling back to the environment (§2: config
// = env vars + flags; secrets live in .env, never on the command line of a
// running unit).
func envOr(flagVal, envName string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envName)
}

// newMux assembles every route. Kept out of serveMain so a test can pin the
// composition: conflicting ServeMux patterns panic at registration, and
// route behaviour only changes when all halves are assembled together.
//
// The ingest route is method-scoped (POST/GET) so the static GET "/" catch-
// all cannot swallow its 405 (Go 1.22: method sets must be disjoint or
// paths comparable).
func newMux(ing *frames.Server, adm *admin.Server, publicDir string, healthz http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proto.PathIngest, ing.HandleIngest)
	mux.HandleFunc("GET "+proto.PathIngest, methodNotPOST)
	adm.Register(mux)
	mux.Handle("GET "+proto.PathHealthz, healthz) // method-scoped: a bare "/healthz" would conflict with GET "/"
	mux.Handle("GET /", pages.Static(publicDir))  // file bytes only, no DB (D5)
	return mux
}

func methodNotPOST(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	fmt.Fprint(w, "405 — method not allowed (POST only)\n")
}

// healthzHandler answers the unauthenticated liveness probe (§6.6): version,
// uptime, free disk. No venue data (§1).
func healthzHandler(dataDir string, started time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"ok":       true,
			"version":  version,
			"uptime_s": int(time.Since(started).Seconds()),
		}
		if pct, err := frames.DiskFreePct(dataDir); err == nil {
			body["disk_free_pct"] = pct
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(body)
	})
}

func serveMain(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8080", "listen address (production: :443 with autocert, §8)")
	dataDir := fs.String("data", "/var/lib/guzva", "data dir: SQLite + stills ring + public pages")
	retainHours := fs.Int("retain-hours", 48, "render ring size in hours (§6.3)")
	tlsCert := fs.String("tls-cert", "", "PEM certificate — enables TLS (bench; production TLS is autocert's, §8)")
	tlsKey := fs.String("tls-key", "", "PEM key (must pair with -tls-cert)")
	adminHash := fs.String("admin-hash", "", "bcrypt hash for the admin console (env GUZVA_ADMIN_HASH)")
	cookieSecret := fs.String("cookie-secret", "", "admin cookie HMAC secret (env GUZVA_COOKIE_SECRET)")
	fs.Parse(args)
	*adminHash = envOr(*adminHash, "GUZVA_ADMIN_HASH")
	*cookieSecret = envOr(*cookieSecret, "GUZVA_COOKIE_SECRET")

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if (*tlsCert == "") != (*tlsKey == "") {
		log.Error("tls flags", "err", "-tls-cert and -tls-key must be set together")
		return 1
	}
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		log.Error("create data dir", "err", err.Error())
		return 1
	}
	st, err := store.Open(filepath.Join(*dataDir, "guzva.db"))
	if err != nil {
		log.Error("open store", "err", err.Error())
		return 1
	}
	defer st.Close()

	eng := render.NewEngine(render.RealClock{}, renderConcurrency)
	lim := auth.DefaultLimiter(time.Now)
	ing := frames.New(st, eng, lim, *dataDir, *retainHours, log)

	// §6.1 order inside ingest: store → replace latest.jpg → rewrite
	// index.html → respond. A page failure never fails the ingest (the frame
	// is stored; the 30 s sched tick repairs the page).
	pub, err := pages.New(st, *dataDir, log)
	if err != nil {
		log.Error("pages init", "err", err.Error())
		return 1
	}
	ing.OnStored = func(ctx context.Context, venueID int64) {
		if err := pub.OnFrame(ctx, venueID); err != nil {
			log.Error("pages: on stored", "venue_id", venueID, "err", err.Error())
		}
	}

	secret := []byte(*cookieSecret)
	if len(secret) == 0 {
		secret = auth.RandSecret(32)
		log.Warn("GUZVA_COOKIE_SECRET unset — admin sessions reset on every restart")
	}
	if *adminHash == "" {
		log.Warn("admin console locked: set GUZVA_ADMIN_HASH (guzva hashpw)")
	}
	adm, err := admin.New(st, *adminHash, secret, *tlsCert != "", log)
	if err != nil {
		log.Error("admin init", "err", err.Error())
		return 1
	}

	started := time.Now()
	mux := newMux(ing, adm, pub.Root(), healthzHandler(*dataDir, started))

	srv := &http.Server{
		Addr:    *listen,
		Handler: mux,
		// Slow clients must not tie up resources (§6.1). ReadTimeout
		// exceeds the device's 25 s POST budget on purpose.
		// ReadHeaderTimeout also bounds the TLS handshake: it must
		// outlast the camera's 12 s connect budget (ClientHello
		// retransmits on a marginal link) or we hang up first and
		// turn a slow handshake into a guaranteed failure.
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second, // > device keep-alive idle (45 s)
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One ticker goroutine (§2/§3): pruner ~1 s after boot then hourly;
	// page refresh every 30 s re-derives STALE/open flips a static file
	// cannot notice on its own (§6.4).
	go sched.Run(ctx, log,
		sched.Job{
			Name:  "prune",
			Every: time.Hour,
			Run: func(ctx context.Context) error {
				_, err := ing.Prune(ctx)
				return err
			},
		},
		sched.Job{
			Name:  "pages",
			Every: 30 * time.Second,
			Run:   pub.RefreshAll,
		},
	)

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
