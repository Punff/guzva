// Command guzva is the whole service: one static Go binary (PLAN §2).
//
//	guzva serve      ingest + static public pages + admin console
//	guzva provision  create a venue + device, print the token once (-token = import)
//	guzva hashpw     bcrypt-hash a password for GUZVA_ADMIN_HASH
//	guzva version    print the build version
//
// Dispatch lives in cli.go: no arguments means help, an unknown command is
// an error — a server must never start by accident.
//
// Layout follows PLAN §3: this file only wires packages together — no
// business logic, no SQL.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // venue tz names resolve without a system zone db (§2)

	"guzva/internal/admin"
	"guzva/internal/alert"
	"guzva/internal/auth"
	"guzva/internal/frames"
	"guzva/internal/mail"
	"guzva/internal/pages"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/sched"
	"guzva/internal/stats"
	"guzva/internal/store"
)

// version is stamped via -ldflags "-X main.version=…" at release build.
var version = "dev"

// renderConcurrency caps concurrent Process calls (§6.1 global semaphore).
// 4 keeps fast p95 unaffected while 1 slow client waits (§6.1 gate: fakecam
// 1 slow + 19 fast).
const renderConcurrency = 4

func main() {
	os.Exit(run(os.Args[1:]))
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
func newMux(ing *frames.Server, adm *admin.Server, publicDir string, rec *stats.Recorder, healthz, privacy, home http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proto.PathIngest, ing.HandleIngest)
	mux.HandleFunc("GET "+proto.PathIngest, methodNotPOST)
	adm.Register(mux)
	mux.Handle("GET "+proto.PathHealthz, healthz)         // method-scoped: a bare "/healthz" would conflict with GET "/"
	mux.Handle("GET /{$}", home)                          // homepage venue list (D12), in memory
	mux.Handle("GET "+pages.PrivacyPath, privacy)         // in memory; the slug is reserved (store.ValidSlug)
	mux.Handle("POST "+pages.LeavePath, pages.Leave(rec)) // watcher beacon, memory only
	mux.Handle("GET /", pages.Static(publicDir, rec))     // file bytes + in-memory visit counts, no DB (D5)
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
	contact := fs.String("contact", "", "homepage 'Za salone' contact, email or phone (env GUZVA_CONTACT); empty hides it")
	fs.Parse(args)
	*contact = envOr(*contact, "GUZVA_CONTACT")
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

	eng := render.NewEngine(nil, renderConcurrency)
	lim := auth.DefaultLimiter(time.Now)
	ing := frames.New(st, eng, lim, *dataDir, *retainHours, log)

	// §6.1 order inside ingest: store → replace latest.png → rewrite
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
	// GUZVA_KIT=1 enables the component gallery at /admin/_kit (dev builds
	// only — never on the production box).
	if os.Getenv("GUZVA_KIT") == "1" {
		adm.Kit = true
		log.Info("component kit enabled at /admin/_kit")
	}

	started := time.Now()
	// Visit counts for the owner panel: memory on the request path, a
	// once-a-minute flush to SQLite from sched (internal/stats).
	rec := stats.New(nil)
	adm.Stats = rec
	// Deleting a venue in the console removes its files too: the public
	// page and the stored renders (the DB rows cascade in the store).
	adm.OnVenueDeleted = func(v *store.Venue) error {
		if err := pub.Remove(v.Slug); err != nil {
			return err
		}
		return os.RemoveAll(filepath.Join(*dataDir, "venues", strconv.FormatInt(v.ID, 10)))
	}
	privacy, err := pages.Privacy()
	if err != nil {
		log.Error("privacy page", "err", err.Error())
		os.Exit(1)
	}
	pub.SetContact(*contact)
	// Build every page (and the homepage list) once before serving, so "/"
	// never answers 503 in the seconds before the first pages tick.
	if err := pub.RefreshAll(context.Background()); err != nil {
		log.Warn("pages: initial build", "err", err.Error())
	}
	mux := newMux(ing, adm, pub.Root(), rec, healthzHandler(*dataDir, started), privacy, pub.Home())

	srv := &http.Server{
		Addr:    *listen,
		Handler: mux,
		// TLS profile for the bench cutover, shaped to what the ESP32's
		// mbedtls (arduino-esp32 2.x) completes: TLS 1.2 only (the core
		// offers a TLS 1.3 draft we ignore — pinning 1.2 keeps both sides
		// on one path) and NO session tickets (Go's NewSessionTicket is
		// what stalls the core; with tickets off the handshake completes
		// in ~2.4 s instead of timing out at the device's 12 s budget).
		// Revisit with a newer core when autocert lands (§8).
		TLSConfig: &tls.Config{
			MinVersion:             tls.VersionTLS12,
			MaxVersion:             tls.VersionTLS12,
			SessionTicketsDisabled: true,
		},
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

	// Operator alerts on Telegram (business plan P1, PLAN §6.6). Secrets come
	// from the environment only. Without a bot the ledger is still kept and
	// every change logged, so adding the bot later loses nothing.
	tg := &alert.Telegram{Token: os.Getenv("GUZVA_TELEGRAM_TOKEN"), Chat: os.Getenv("GUZVA_TELEGRAM_CHAT")}
	mon := &alert.Monitor{St: st, Engine: alert.NewEngine(), TG: tg, Log: log, Now: time.Now,
		DiskFree: func() (float64, error) { return frames.DiskFreePct(*dataDir) }, Started: time.Now()}
	if tg.Configured() {
		go func() {
			sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := tg.Send(sctx, "guzva "+version+" started"); err != nil {
				log.Warn("alert: startup message failed", "err", err.Error())
			}
		}()
	} else {
		log.Info("alert: Telegram not configured (GUZVA_TELEGRAM_TOKEN/GUZVA_TELEGRAM_CHAT); alerts are logged only")
	}
	jobs := []sched.Job{{Name: "alerts", Every: time.Minute, Run: mon.Tick}}

	// Outgoing email (business plan P1): SMTP submission from env only.
	mcfg := mail.Config{Host: os.Getenv("GUZVA_SMTP_HOST"), User: os.Getenv("GUZVA_SMTP_USER"),
		Pass: os.Getenv("GUZVA_SMTP_PASS"), From: os.Getenv("GUZVA_MAIL_FROM")}
	if p, err := strconv.Atoi(os.Getenv("GUZVA_SMTP_PORT")); err == nil {
		mcfg.Port = p
	}
	adm.MailOn = mcfg.Configured()
	if !mcfg.Configured() {
		log.Info("mail: SMTP not configured (GUZVA_SMTP_HOST/GUZVA_MAIL_FROM); outbox queues only")
	}
	sender := &mail.Sender{Cfg: mcfg, St: st, Log: log, Now: time.Now}
	jobs = append(jobs, sched.Job{Name: "mail", Every: 30 * time.Second, Run: sender.Tick})
	if u := os.Getenv("GUZVA_DEADMAN_URL"); u != "" {
		jobs = append(jobs, sched.Job{Name: "deadman", Every: 5 * time.Minute,
			Run: func(ctx context.Context) error { return alert.Ping(ctx, nil, u) }})
	}

	// One ticker goroutine (§2/§3): pruner ~1 s after boot then hourly;
	// page refresh every 30 s re-derives STALE/open flips a static file
	// cannot notice on its own (§6.4).
	go sched.Run(ctx, log, append([]sched.Job{
		{
			Name:  "prune",
			Every: time.Hour,
			Run: func(ctx context.Context) error {
				if _, err := ing.Prune(ctx); err != nil {
					return err
				}
				if err := st.PruneAudit(ctx, time.Now().Add(-730*24*time.Hour)); err != nil {
					return err
				}
				if err := st.PruneOutbox(ctx, time.Now().Add(-90*24*time.Hour)); err != nil {
					return err
				}
				return st.PruneVisits(ctx, time.Now().Add(-stats.Retention))
			},
		},
		{
			Name:  "visits",
			Every: time.Minute,
			Run:   func(ctx context.Context) error { return rec.Flush(ctx, st) },
		},
		{
			Name:  "pages",
			Every: 30 * time.Second,
			Run:   pub.RefreshAll,
		},
	}, jobs...)...)

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
		// Up to a minute of visit counts is still in memory: keep it.
		if err := rec.Flush(shutCtx, st); err != nil {
			log.Error("visits: final flush", "err", err.Error())
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
