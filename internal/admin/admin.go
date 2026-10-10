// Package admin is the signed-cookie admin console (PLAN §6.4 admin,
// §6.5 auth): the super-admin fleet board (venue/device create, edit,
// revoke, device-config, owner passwords) plus the per-venue owner panel
// (owner.go: venue details + read-only device health, one password per
// venue). htmx-driven with embedded templates; HMAC cookie sessions, no
// sessions table. Handlers never touch SQL: everything goes through *store.
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
	"guzva/internal/webui"
)

// Server is the admin plane.
type Server struct {
	st        *store.Store
	sess      *auth.Session
	ownerSess *auth.OwnerSession
	loginLim  *auth.Limiter
	passHash  string
	secure    bool // cookie Secure flag: only when the listener speaks TLS
	tmpl      *template.Template
	log       *slog.Logger
	now       func() time.Time
}

// New builds the admin server. passHash is the bcrypt hash from
// GUZVA_ADMIN_HASH (empty = console locked; login fails closed). secret
// signs the session cookie.
func New(st *store.Store, passHash string, secret []byte, secure bool, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	// dict builds the small option structs the shared partials take, so a
	// partial like page-head can be called with named values instead of a
	// positional soup. It panics on a bad call at TEMPLATE EXECUTION time,
	// so the pairings are unit-tested rather than discovered in production.
	funcs := template.FuncMap{"dict": dict}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("admin: parse templates: %w", err)
	}
	return &Server{
		st:        st,
		sess:      auth.NewSession(secret, time.Now),
		ownerSess: auth.NewOwnerSession(secret, time.Now),
		loginLim:  auth.NewLimiter(0.1, 5, 0.2, 4, time.Now), // 0.1/s, burst 5 per IP
		passHash:  passHash,
		secure:    secure,
		tmpl:      tmpl,
		log:       log,
		now:       time.Now,
	}, nil
}

// Register wires the admin routes (Go 1.22 patterns, §2).
func (s *Server) Register(mux *http.ServeMux) {
	static, err := fs.Sub(assets, "static")
	if err == nil {
		mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/",
			http.FileServer(http.FS(static))))
	}
	// The compiled design system (PLAN §9/D6) replaces the inline <style> block:
	// one Tailwind-purged stylesheet, embedded, cached, shared with the public
	// page (which inlines the same bytes).
	mux.Handle("GET /admin/app.css", webui.Handler())
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("POST /admin/login", s.loginSubmit)
	mux.HandleFunc("POST /admin/logout", s.require(s.logout))
	mux.HandleFunc("GET /admin/{$}", s.require(s.fleet))
	mux.HandleFunc("POST /admin/venues", s.require(s.createVenue))
	mux.HandleFunc("GET /admin/venues/{id}", s.require(s.venuePage))
	mux.HandleFunc("POST /admin/venues/{id}", s.require(s.updateVenue))
	mux.HandleFunc("POST /admin/venues/{id}/ownerpw", s.require(s.setOwnerPassword))
	mux.HandleFunc("POST /admin/devices", s.require(s.createDevice))
	mux.HandleFunc("GET /admin/devices/{id}", s.require(s.devicePage))
	mux.HandleFunc("POST /admin/devices/{id}/cfg", s.require(s.updateDeviceCfg))
	mux.HandleFunc("POST /admin/devices/{id}/revoke", s.require(s.revokeDevice))
	s.registerOwner(mux)
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

// InstallGateRSSI is the weakest signal an install may ship with (PLAN §10).
// Below it, pictures arrive late and TLS handshakes fail; the bench unit sits
// at −74 and is a standing reminder that this is a hardware gate, not a
// preference.
const InstallGateRSSI = -65

// --- fleet board -----------------------------------------------------------

type deviceView struct {
	ID         int64
	Serial     string
	Status     string
	FW         string
	RSSI       int
	RSSIText   string // "−76 dBm", or "—" when the board has never been heard from
	Weak       bool   // below the −65 dBm install gate (§10)
	SeenAge    string // "never" | "2 min ago" (live board — relative is fine)
	Counters   proto.Counters
	CfgVer     int
	CfgApplied int
	Amber      bool // applied version behind desired (read-back mismatch, §1)
}

type venueView struct {
	ID      int64
	Slug    string
	Name    string
	Fresh   chip // shape + colour + sentence for the status chip
	LastAge string
	Devices []deviceView
}

// chip is the "needs attention" state of a venue, ready for the chip partial
// (DESIGN.md §3.2). Shape is markup and must not be escaped; text is the
// signal a screen reader reads, so the state never depends on colour.
type chip struct {
	Cls   string
	Shape template.HTML
	Text  string
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
		// A board that has never checked in has no reading. Printing its
		// zero-value RSSI as "0 dBm" reads as a perfect signal; "—" is the
		// honest answer, and it keeps it clear of the install-gate styling.
		seen := !d.LastSeenAt.IsZero()
		dv := deviceView{
			ID: d.ID, Serial: d.Serial, Status: d.Status, FW: d.FW, RSSI: d.RSSI,
			SeenAge: age(now, d.LastSeenAt), Counters: d.Counters,
			CfgVer: d.CfgVer, CfgApplied: d.CfgAppliedVer,
			Amber: d.Status == "active" && d.CfgAppliedVer != d.CfgVer,
		}
		if seen {
			dv.RSSIText = fmt.Sprintf("%d dBm", d.RSSI)
			dv.Weak = d.RSSI <= InstallGateRSSI
		} else {
			dv.RSSIText = "—"
		}
		byVenue[d.VenueID] = append(byVenue[d.VenueID], dv)
	}
	model := fleetModel{DeviceOpts: venues, ShowCreated: r.URL.Query().Get("created") != ""}
	for _, v := range venues {
		vv := venueView{
			ID: v.ID, Slug: v.Slug, Name: v.Name,
			LastAge: age(now, v.LastFrameAt), Devices: byVenue[v.ID],
		}
		switch venue.Classify(v.LastFrameAt, now) {
		case venue.Fresh:
			vv.Fresh = chip{"fresh", shapeFresh, "Fresh"}
		case venue.Late:
			vv.Fresh = chip{"late", shapeLate, "Late"}
		case venue.Stale:
			vv.Fresh = chip{"stale", shapeNoSignal, "No signal"}
		default:
			vv.Fresh = chip{"unknown", shapeWaiting, "No frame yet"}
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

// parseVenueForm validates the editable site-plane fields. It is the one
// validator for venue create (super-admin), venue update (super-admin) and
// the owner panel — three forms, one rule, so an owner can never save what
// the console would refuse. Returns tz + parsed hours, or a user-facing
// error. Slug is identity and is never editable.
func parseVenueForm(name, style, tz, hoursSpec string) (string, venue.Hours, string) {
	name = strings.TrimSpace(name)
	tz = strings.TrimSpace(tz)
	hoursSpec = strings.TrimSpace(hoursSpec)
	switch {
	case name == "":
		return "", venue.Hours{}, "Name is required."
	case style != "poster" && style != "mono":
		return "", venue.Hours{}, "Style must be poster or mono."
	}
	if tz == "" {
		tz = store.DefaultTZ
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", venue.Hours{}, "Unknown timezone: " + tz
	}
	h, err := venue.ParseSpec(hoursSpec)
	if err != nil {
		return "", venue.Hours{}, "Bad hours: " + err.Error()
	}
	return tz, h, ""
}

func (s *Server) createVenue(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	if !slugRe.MatchString(slug) {
		s.flash(w, "Slug must be lowercase letters, digits or dashes (≤40).")
		return
	}
	tz, h, ferr := parseVenueForm(
		r.FormValue("name"), r.FormValue("style"), r.FormValue("tz"), r.FormValue("hours"))
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	id, err := s.st.CreateVenue(r.Context(), store.Venue{
		Slug: slug, Name: strings.TrimSpace(r.FormValue("name")), TZ: tz,
		Style: r.FormValue("style"),
	})
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
	s.log.Info("admin: venue created", "slug", slug,
		"name", strings.TrimSpace(r.FormValue("name")),
		"tz", tz, "style", r.FormValue("style"), "actor", "admin")
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

// --- venue + device editing (super-admin) -----------------------------------

type venueEditModel struct {
	Venue     store.Venue
	HoursSpec string
	HasOwner  bool
	Saved     bool
	OwnerSet  bool
}

func (s *Server) loadVenue(w http.ResponseWriter, r *http.Request) (*store.Venue, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return nil, false
	}
	v, err := s.st.VenueByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	return v, true
}

func (s *Server) venuePage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	h, err := s.st.Hours(r.Context(), v.ID)
	if err != nil {
		s.internal(w, "hours", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	m := venueEditModel{Venue: *v, HoursSpec: h.Spec(), HasOwner: v.OwnerHash != "",
		Saved: r.URL.Query().Get("saved") != "", OwnerSet: r.URL.Query().Get("owner") != ""}
	if err := s.tmpl.ExecuteTemplate(w, "venue.html", m); err != nil {
		s.log.Error("admin: render venue", "err", err.Error())
	}
}

func (s *Server) updateVenue(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	tz, h, ferr := parseVenueForm(
		r.FormValue("name"), r.FormValue("style"), r.FormValue("tz"), r.FormValue("hours"))
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.st.UpdateVenue(r.Context(), v.ID, name, r.FormValue("style"), tz); err != nil {
		s.internal(w, "update venue", err)
		return
	}
	if err := s.st.SetHours(r.Context(), v.ID, h); err != nil {
		s.internal(w, "set hours", err)
		return
	}
	s.log.Info("admin: venue updated", "venue_id", v.ID, "slug", v.Slug,
		"name", name, "tz", tz, "style", r.FormValue("style"), "actor", "admin")
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?saved=1", v.ID))
}

// minOwnerPassword is the only password policy in the system: owner logins
// face the venue's visitors indirectly (a vendor reuses passwords), so a
// short one is a real risk. The super-admin password itself is outside this
// file's control (GUZVA_ADMIN_HASH), documented in the runbook.
const minOwnerPassword = 10

func (s *Server) setOwnerPassword(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	pw := r.FormValue("password")
	if len(pw) < minOwnerPassword {
		s.flash(w, fmt.Sprintf("Owner password must be at least %d characters.", minOwnerPassword))
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.internal(w, "hash owner password", err)
		return
	}
	if err := s.st.SetOwnerHash(r.Context(), v.ID, hash); err != nil {
		s.internal(w, "set owner hash", err)
		return
	}
	// The password itself is never logged and never rendered back (§1).
	s.log.Info("admin: owner password set", "venue_id", v.ID, "slug", v.Slug, "actor", "admin")
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?owner=1", v.ID))
}

// cfgField is one row of the device-config form: key, current value and the
// clamp range from the proto table (inputs carry min/max so typos die in the
// browser, and the server re-validates every key anyway).
type cfgField struct {
	Key      string
	Val      int
	Min, Max int
	Range    string // "30…90" — always both ends, never a bare bound
	Cadence  bool   // interval_s/quality: when the board wakes up
	Image    bool   // sensor/imaging keys (DESIGN.md §8.3)
	Hardware bool   // flash LED, test pattern
}

type deviceModel struct {
	Device store.Device
	Cfg    proto.DesiredCfg
	Fields []cfgField
	Amber  bool // applied config version behind desired (read-back mismatch)
	Weak   bool // signal below the −65 dBm install gate (§10)
	Seen   bool // the board has ever checked in
	Saved  bool
}

func deviceFields(cfg proto.DesiredCfg) []cfgField {
	extra := cfg.Extra
	if extra == nil {
		extra = map[string]int{}
	}
	var out []cfgField
	for _, k := range proto.SettableCfgKeys() {
		spec := proto.CfgKeys[k]
		val := extra[k]
		cadence := false
		switch k {
		case "interval_s":
			val, cadence = cfg.IntervalS, true
		case "quality":
			val, cadence = cfg.Quality, true
		}
		// Always print both bounds. A bare "30" under a 45 was unreadable —
		// you could not tell min from max, and a lone "4" under quality
		// looked like a fourth option rather than a ceiling.
		f := cfgField{
			Key: k, Val: val, Min: spec.Min, Max: spec.Max, Cadence: cadence,
			Range: fmt.Sprintf("%d…%d", spec.Min, spec.Max),
		}
		if !cadence {
			switch k {
			case "led_pwm", "test_pattern":
				f.Hardware = true
			default:
				f.Image = true
			}
		}
		out = append(out, f)
	}
	return out
}

func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	d, err := s.st.DeviceByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cfg := d.DesiredCfg()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	m := deviceModel{Device: *d, Cfg: cfg, Fields: deviceFields(cfg),
		Amber: d.Status == "active" && d.CfgAppliedVer != d.CfgVer,
		Seen:  !d.LastSeenAt.IsZero(),
		Weak:  !d.LastSeenAt.IsZero() && d.RSSI <= InstallGateRSSI,
		Saved: r.URL.Query().Get("saved") != ""}
	if err := s.tmpl.ExecuteTemplate(w, "device.html", m); err != nil {
		s.log.Error("admin: render device", "err", err.Error())
	}
}

// parseCfgForm reads the prefilled device-config form: every settable key
// arrives as cfg_<key>; empty keeps the current value (a missing field must
// never zero a sensor key). Each value is validated against the proto table
// — the same gate as `guzva cfg -set`.
func parseCfgForm(r *http.Request, cur proto.DesiredCfg) (proto.DesiredCfg, string) {
	next := proto.DesiredCfg{Ver: cur.Ver + 1, IntervalS: cur.IntervalS, Quality: cur.Quality}
	if cur.Extra != nil {
		next.Extra = map[string]int{}
		for k, v := range cur.Extra {
			next.Extra[k] = v
		}
	} else {
		next.Extra = map[string]int{}
	}
	for _, k := range proto.SettableCfgKeys() {
		raw := strings.TrimSpace(r.FormValue("cfg_" + k))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return cur, k + " must be an integer."
		}
		if err := proto.ValidateCfgKey(k, v); err != nil {
			return cur, err.Error()
		}
		switch k {
		case "interval_s":
			next.IntervalS = v
		case "quality":
			next.Quality = v
		default:
			next.Extra[k] = v
		}
	}
	return next, ""
}

func (s *Server) updateDeviceCfg(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	d, err := s.st.DeviceByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	next, ferr := parseCfgForm(r, d.DesiredCfg())
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	if err := s.st.SetDeviceCfg(r.Context(), d.ID, next); err != nil {
		s.internal(w, "set device cfg", err)
		return
	}
	s.log.Info("admin: device cfg updated", "device_id", d.ID, "serial", d.Serial,
		"ver", d.CfgVer, "actor", "admin")
	redirect(w, r, fmt.Sprintf("/admin/devices/%d?saved=1", d.ID))
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("admin: entropy failure: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Status shapes shared with the public page (DESIGN.md §3.2): a distinct
// silhouette per state, so the state survives with no colour at all. Whole
// strings, never assembled by concatenation, so the inliner sees them.
const (
	shapeFresh    = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><rect width="12" height="12" fill="currentColor"/></svg>`
	shapeLate     = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><rect width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"/><rect y="6" width="12" height="6" fill="currentColor"/></svg>`
	shapeNoSignal = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><rect width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"/><path d="M0 3h12M0 6h12M0 9h12" stroke="currentColor" stroke-width="2"/></svg>`
	shapeWaiting  = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><path d="M0 0h4v4H0zM8 0h4v4H8zM4 4h4v4H4zM0 8h4v4H0zM8 8h4v4H8z" fill="currentColor"/></svg>`
)

// dict builds a map from alternating key/value pairs, for partials that take
// named options. Panicking is right here: a mistyped key in a template is a
// programming error, and failing loudly at the point of use beats a blank
// element on a page nobody is looking at during a deploy.
func dict(pairs ...any) map[string]any {
	if len(pairs)%2 != 0 {
		panic("dict: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			panic("dict: key must be a string")
		}
		m[k] = pairs[i+1]
	}
	return m
}
