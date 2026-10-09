// Package admin is the signed-cookie admin console (PLAN §6.4 admin,
// §6.5 auth): fleet board, venue create, device mint/revoke — htmx-driven
// with embedded templates, one bcrypt credential, HMAC cookie session.
// Handlers never touch SQL: everything goes through *store.
package admin

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/proto"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// Server is the admin plane.
type Server struct {
	st       *store.Store
	sess     *auth.Session
	loginLim *auth.Limiter
	passHash string
	secure   bool // cookie Secure flag: only when the listener speaks TLS
	tmpl     *template.Template
	log      *slog.Logger
	now      func() time.Time
}

// New builds the admin server. passHash is the bcrypt hash from
// GUZVA_ADMIN_HASH (empty = console locked; login fails closed). secret
// signs the session cookie.
func New(st *store.Store, passHash string, secret []byte, secure bool, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("admin: parse templates: %w", err)
	}
	return &Server{
		st:       st,
		sess:     auth.NewSession(secret, time.Now),
		loginLim: auth.NewLimiter(0.1, 5, 0.2, 4, time.Now), // 0.1/s, burst 5 per IP
		passHash: passHash,
		secure:   secure,
		tmpl:     tmpl,
		log:      log,
		now:      time.Now,
	}, nil
}

// Register wires the admin routes (Go 1.22 patterns, §2).
func (s *Server) Register(mux *http.ServeMux) {
	static, err := fs.Sub(assets, "static")
	if err == nil {
		mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/",
			http.FileServer(http.FS(static))))
	}
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("POST /admin/login", s.loginSubmit)
	mux.HandleFunc("POST /admin/logout", s.require(s.logout))
	mux.HandleFunc("GET /admin/{$}", s.require(s.fleet))
	mux.HandleFunc("POST /admin/venues", s.require(s.createVenue))
	mux.HandleFunc("POST /admin/devices", s.require(s.createDevice))
	mux.HandleFunc("POST /admin/devices/{id}/revoke", s.require(s.revokeDevice))
}

// --- helpers ---------------------------------------------------------------

func htmx(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// redirect sends the client to loc — a full page reload via HX-Redirect for
// htmx, a plain 303 for the rare non-htmx form.
func redirect(w http.ResponseWriter, r *http.Request, loc string) {
	if htmx(r) {
		w.Header().Set("HX-Redirect", loc)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, loc, http.StatusSeeOther)
}

// flash renders the error/success fragment into the caller's target box.
// Form errors answer 200 on purpose: htmx's default 4xx handling drops the
// body, and a swallowed error message is worse than a visible one.
func (s *Server) flash(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "flash.html", msg); err != nil {
		s.log.Error("admin: render flash", "err", err.Error())
	}
}

// require gates a handler on a valid session cookie (§6.5).
func (s *Server) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.sess.Verify(r) {
			if htmx(r) {
				w.Header().Set("HX-Redirect", "/admin/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// --- login -----------------------------------------------------------------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.sess.Verify(r) {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, http.StatusOK, "")
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	if !s.loginLim.AllowIP(ip) {
		s.log.Warn("admin: login throttled", "ip", ip)
		s.renderLogin(w, http.StatusTooManyRequests, "Too many attempts — wait a minute.")
		return
	}
	if s.passHash == "" {
		s.log.Error("admin: login attempt but GUZVA_ADMIN_HASH is unset")
		s.renderLogin(w, http.StatusServiceUnavailable,
			"Admin console is not configured (set GUZVA_ADMIN_HASH).")
		return
	}
	// The body is a single password field; it is never logged (§1).
	if !auth.CheckAdminPassword(s.passHash, r.FormValue("password")) {
		s.log.Warn("admin: bad password", "ip", ip)
		s.renderLogin(w, http.StatusUnauthorized, "Wrong password.")
		return
	}
	s.sess.Issue(w, auth.SessionTTL, s.secure)
	s.log.Info("admin: login", "ip", ip)
	redirect(w, r, "/admin/")
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "login.html", map[string]string{"Err": errMsg}); err != nil {
		s.log.Error("admin: render login", "err", err.Error())
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.sess.Clear(w, s.secure)
	s.log.Info("admin: logout", "ip", auth.ClientIP(r))
	redirect(w, r, "/admin/login")
}

// --- fleet board -----------------------------------------------------------

type deviceView struct {
	ID         int64
	Serial     string
	Status     string
	FW         string
	RSSI       int
	SeenAge    string // "never" | "2 min ago" (live board — relative is fine)
	Counters   proto.Counters
	CfgVer     int
	CfgApplied int
	Amber      bool // applied version behind desired (read-back mismatch, §1)
}

type venueView struct {
	ID         int64
	Slug       string
	Name       string
	FreshClass string
	FreshText  string
	LastAge    string
	Devices    []deviceView
}

type fleetModel struct {
	Venues      []venueView
	DeviceOpts  []store.Venue // for the create-device <select>
	ShowCreated bool
}

func (s *Server) fleet(w http.ResponseWriter, r *http.Request) {
	venues, err := s.st.ListVenues(r.Context())
	if err != nil {
		s.internal(w, "list venues", err)
		return
	}
	devices, err := s.st.ListDevices(r.Context())
	if err != nil {
		s.internal(w, "list devices", err)
		return
	}
	now := s.now().UTC()
	byVenue := map[int64][]deviceView{}
	for _, d := range devices {
		byVenue[d.VenueID] = append(byVenue[d.VenueID], deviceView{
			ID: d.ID, Serial: d.Serial, Status: d.Status, FW: d.FW, RSSI: d.RSSI,
			SeenAge: age(now, d.LastSeenAt), Counters: d.Counters,
			CfgVer: d.CfgVer, CfgApplied: d.CfgAppliedVer,
			Amber: d.Status == "active" && d.CfgAppliedVer != d.CfgVer,
		})
	}
	model := fleetModel{DeviceOpts: venues, ShowCreated: r.URL.Query().Get("created") != ""}
	for _, v := range venues {
		vv := venueView{
			ID: v.ID, Slug: v.Slug, Name: v.Name,
			LastAge: age(now, v.LastFrameAt), Devices: byVenue[v.ID],
		}
		switch venue.Classify(v.LastFrameAt, now) {
		case venue.Fresh:
			vv.FreshClass, vv.FreshText = "fresh", "Fresh"
		case venue.Stale:
			vv.FreshClass, vv.FreshText = "stale", "STALE"
		default:
			vv.FreshClass, vv.FreshText = "unknown", "No frame"
		}
		model.Venues = append(model.Venues, vv)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "fleet.html", model); err != nil {
		s.log.Error("admin: render fleet", "err", err.Error())
	}
}

// age renders a relative age for the live board. The public page never does
// this (a static file can't keep it correct) — the admin page can.
func age(now, then time.Time) string {
	if then.IsZero() {
		return "never"
	}
	d := now.Sub(then)
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
}

// --- mutations -------------------------------------------------------------

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (s *Server) createVenue(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	name := strings.TrimSpace(r.FormValue("name"))
	style := r.FormValue("style")
	tz := strings.TrimSpace(r.FormValue("tz"))
	hoursSpec := strings.TrimSpace(r.FormValue("hours"))

	switch {
	case !slugRe.MatchString(slug):
		s.flash(w, "Slug must be lowercase letters, digits or dashes (≤40).")
		return
	case name == "":
		s.flash(w, "Name is required.")
		return
	case style != "poster" && style != "mono":
		s.flash(w, "Style must be poster or mono.")
		return
	}
	if tz == "" {
		tz = store.DefaultTZ
	}
	if _, err := time.LoadLocation(tz); err != nil {
		s.flash(w, "Unknown timezone: "+tz)
		return
	}
	h, err := venue.ParseSpec(hoursSpec)
	if err != nil {
		s.flash(w, "Bad hours: "+err.Error())
		return
	}
	id, err := s.st.CreateVenue(r.Context(), store.Venue{Slug: slug, Name: name, TZ: tz, Style: style})
	if err != nil {
		if err == store.ErrSlugTaken {
			s.flash(w, "Slug already exists.")
			return
		}
		s.internal(w, "create venue", err)
		return
	}
	if err := s.st.SetHours(r.Context(), id, h); err != nil {
		s.internal(w, "set hours", err)
		return
	}
	// Config changes are slog audit lines (§6.2).
	s.log.Info("admin: venue created", "slug", slug, "name", name, "tz", tz, "style", style, "actor", "admin")
	redirect(w, r, "/admin/?created=1")
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	venueID, err := strconv.ParseInt(r.FormValue("venue_id"), 10, 64)
	if err != nil || venueID <= 0 {
		s.flash(w, "Pick a venue.")
		return
	}
	if _, err := s.st.VenueByID(r.Context(), venueID); err != nil {
		s.flash(w, "Unknown venue.")
		return
	}
	serial := strings.TrimSpace(r.FormValue("serial"))
	if serial == "" {
		serial = "esp32-" + randHex(3)
	}
	token, err := auth.GenerateToken()
	if err != nil {
		s.internal(w, "token", err)
		return
	}
	if _, err := s.st.CreateDevice(r.Context(), store.Device{
		VenueID: venueID, Serial: serial,
		CfgVer: 1, CfgIntervalS: proto.DefaultIntervalS, CfgQuality: proto.DefaultQuality,
	}, auth.HashToken(token)); err != nil {
		if err == store.ErrSerialTaken {
			s.flash(w, "Serial already exists.")
			return
		}
		s.internal(w, "create device", err)
		return
	}
	s.log.Info("admin: device created", "venue_id", venueID, "serial", serial, "actor", "admin")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "token.html", map[string]string{
		"Serial": serial, "Token": token,
	}); err != nil {
		s.log.Error("admin: render token", "err", err.Error())
	}
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.flash(w, "Bad device id.")
		return
	}
	if err := s.st.SetDeviceStatus(r.Context(), id, "revoked"); err != nil {
		s.internal(w, "revoke", err)
		return
	}
	s.log.Info("admin: device revoked", "device_id", id, "actor", "admin")
	redirect(w, r, "/admin/")
}

func (s *Server) internal(w http.ResponseWriter, op string, err error) {
	s.log.Error("admin: internal error", "op", op, "err", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("admin: entropy failure: " + err.Error())
	}
	return hex.EncodeToString(b)
}
