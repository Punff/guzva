// Package admin is the signed-cookie admin console (PLAN §6.4 admin,
// §6.5 auth): the super-admin fleet board (venue/device create, edit,
// revoke, device-config, owner passwords) plus the per-venue owner panel
// (owner.go: venue details + read-only device health, one password per
// venue). htmx-driven with embedded templates; HMAC cookie sessions, no
// sessions table. Handlers never touch SQL: everything goes through *store.
package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/i18n"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/stats"
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
	// Kit enables the component gallery at /admin/_kit (DESIGN.md §6/B1).
	// Dev builds only: it exists so a solo builder can eyeball every state,
	// not as a product surface. Set from GUZVA_KIT=1.
	Kit bool
	// OnVenueDeleted removes a deleted venue's files (renders + public
	// page); set by main, which knows the data directory. Nil in tests.
	OnVenueDeleted func(v *store.Venue) error
	// Stats is the live visit recorder ("watching right now"); nil in
	// tests that don't care.
	Stats *stats.Recorder
	// MailOn says whether SMTP is configured (the outbox sends); the
	// fleet page shows it and the test form queues regardless.
	MailOn bool
	// Notify sends the operator a message (Telegram); nil = off. Set by main.
	Notify func(ctx context.Context, text string) error
	// PublicURL is the site's own base URL for links we hand out
	// (GUZVA_PUBLIC_URL); empty = derived from the request.
	PublicURL string
	formKey   []byte        // signs the application form's timestamp
	intakeLim *auth.Limiter // per-IP application limit
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
	// shape resolves a status class to its silhouette (DESIGN.md §3.2). One
	// source of truth: the fleet, the owner panel and the kit gallery all
	// render through it, so a class can never gain the wrong shape.
	funcs := template.FuncMap{"dict": dict, "appCSS": webui.AppPath, "shape": shape, "inc": func(i int) int { return i + 1 },
		"list": func(v ...string) []string { return v }}
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
		formKey:   formKey(secret),
		intakeLim: auth.NewLimiter(0.002, 3, 1, 1, time.Now), // ~7/hour per IP, burst 3
		tmpl:      tmpl,
		log:       log,
		now:       time.Now,
	}, nil
}

// Register wires the admin routes (Go 1.22 patterns, §2). Everything the
// consoles serve — admin, owner, their static assets — is mounted through
// secureHeaders, so a route added tomorrow inherits the security contract
// without remembering to set it (maintenance W2/H8: the consoles shipped
// with none while the public pages had all of them).
func (s *Server) Register(mux *http.ServeMux) {
	sub := http.NewServeMux()
	static, err := fs.Sub(assets, "static")
	if err == nil {
		// No directory listing: the public handler forbids them; a bare
		// http.FileServer here would happily index /admin/static/ (W2/H8).
		sub.Handle("GET /admin/static/",
			http.StripPrefix("/admin/static/", noDirIndex(http.FileServer(http.FS(static)))))
	}
	// The compiled design system (PLAN §9/D6) replaces the inline <style> block:
	// one Tailwind-purged stylesheet, embedded, cached, shared with the public
	// page (which inlines the same bytes).
	// The content-hashed URL is known at build time, so it is registered
	// literally. It is the only URL the templates emit, and because the hash
	// changes with the bytes it can be cached forever without ever going
	// stale. The un-hashed path stays as a courtesy for old bookmarks.
	sub.Handle("GET "+webui.AppPath(), webui.Handler())
	sub.Handle("GET /admin/app.css", webui.Handler())
	sub.HandleFunc("GET /admin/login", s.loginPage)
	sub.HandleFunc("POST /admin/login", s.loginSubmit)
	sub.HandleFunc("POST /admin/logout", s.require(s.logout))
	sub.HandleFunc("GET /admin/{$}", s.require(s.overview))
	sub.HandleFunc("GET /admin/venues", s.require(s.venuesList))
	sub.HandleFunc("GET /admin/venues/new", s.require(s.venueNew))
	sub.HandleFunc("GET /admin/cameras", s.require(s.cameras))
	sub.HandleFunc("GET /admin/alerts", s.require(s.alertsPage))
	sub.HandleFunc("GET /admin/activity", s.require(s.activity))
	sub.HandleFunc("POST /admin/venues", s.require(s.createVenue))
	sub.HandleFunc("GET /admin/venues/{id}", s.require(s.venuePage))
	sub.HandleFunc("POST /admin/venues/{id}", s.require(s.updateVenue))
	sub.HandleFunc("POST /admin/venues/{id}/ownerpw", s.require(s.setOwnerPassword))
	sub.HandleFunc("POST /admin/venues/{id}/status", s.require(s.setVenueStatus))
	sub.HandleFunc("GET /admin/venues/{id}/legal/{doc}", s.require(s.legalDoc))
	sub.HandleFunc("POST /admin/venues/{id}/owner", s.require(s.ownerInvite))
	sub.HandleFunc("POST /admin/venues/{id}/style", s.require(s.setStyle))
	sub.HandleFunc("POST /admin/venues/{id}/delete", s.require(s.deleteVenue))
	sub.HandleFunc("POST /admin/devices/{id}", s.require(s.updateDevice))
	sub.HandleFunc("POST /admin/devices/{id}/delete", s.require(s.deleteDevice))
	sub.HandleFunc("POST /admin/devices", s.require(s.createDevice))
	sub.HandleFunc("POST /admin/mail/test", s.require(s.mailTest))
	sub.HandleFunc("GET /admin/devices/{id}", s.require(s.devicePage))
	sub.HandleFunc("POST /admin/devices/{id}/revoke", s.require(s.revokeDevice))
	sub.HandleFunc("POST /admin/devices/{id}/cmd", s.require(s.deviceCommand))
	sub.HandleFunc("GET /admin/_kit", s.require(s.kit))
	sub.HandleFunc("GET /admin/leads", s.require(s.leadsPage))
	sub.HandleFunc("POST /admin/leads/invite", s.require(s.createInvite))
	sub.HandleFunc("GET /admin/leads/{id}", s.require(s.leadPage))
	sub.HandleFunc("POST /admin/leads/{id}", s.require(s.saveLead))
	sub.HandleFunc("POST /admin/leads/{id}/venue", s.require(s.leadToVenue))
	sub.HandleFunc("POST /admin/leads/{id}/delete", s.require(s.deleteLead))
	sub.HandleFunc("GET /prijava/{$}", s.intakePage)
	sub.HandleFunc("POST /prijava/{$}", s.intakeSubmit)
	sub.HandleFunc("GET /prijava/{token}", s.invitePage)
	s.registerOwner(sub)
	// Method-scoped mounts, the same discipline the ingest route uses: a
	// bare "/admin/" would conflict with the root's "GET /" static catch-all
	// (fewer methods, more general path). GET mounts also give the free
	// /admin → /admin/ canonical redirect.
	mux.Handle("GET /admin/", s.secureHeaders(sub))
	mux.Handle("POST /admin/", s.secureHeaders(sub))
	mux.Handle("GET /owner/", s.secureHeaders(sub))
	mux.Handle("POST /owner/", s.secureHeaders(sub))
	mux.Handle("GET /prijava/", s.secureHeaders(sub))
	mux.Handle("POST /prijava/", s.secureHeaders(sub))
}

// secureHeaders is the consoles' side of the C1 contract. The public pages
// hash their inline styles into a per-page CSP; the consoles carry no
// inline styles or scripts at all (W2 removed the last style= attributes),
// so a static policy suffices. htmx is same-origin (script-src 'self',
// connect-src 'self'); the favicon is a data: URI (img-src data:).
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'self'; script-src 'self'; "+
				"connect-src 'self'; img-src 'self' data:; form-action 'self'; "+
				"base-uri 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		// Console HTML is dynamic (no-store is set per-handler); static
		// assets revalidate instead of caching — htmx.min.js lives at a
		// fixed URL, and a fixed URL under a long max-age is the silent
		// deploy dependency the ops-log already paid for once.
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// noDirIndex 404s any path that would render a listing; files still serve.
func noDirIndex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	s.audit(r, "admin", "admin.login", 0, "")
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
	s.audit(r, "admin", "admin.logout", 0, "")
	redirect(w, r, "/admin/login")
}

// InstallGateRSSI is the weakest signal an install may ship with (PLAN §10).
// Below it, pictures arrive late and TLS handshakes fail; the bench unit sits
// at −74 and is a standing reminder that this is a hardware gate, not a
// preference.
const InstallGateRSSI = -65

// --- fleet board -----------------------------------------------------------

type deviceView struct {
	ID       int64
	Serial   string
	Status   string
	FW       string
	RSSI     int
	RSSIText string // "−76 dBm", or "—" when the board has never been heard from
	Weak     bool   // below the −65 dBm install gate (§10)
	SeenAge  string // "never" | "2 min ago" (live board — relative is fine)
	Counters proto.Counters
	Profile  string // frozen camera profile; "" = firmware before Profile v1
	Failures string // "none" | "2 WiFi, 1 upload" — only the non-zero ones
	Bars     int    // 0–4 signal bars (0 = never heard from)
	SignalW  string // "excellent" | "good" | "weak" | "very weak"
}

// signalBars maps RSSI to 1–4 bars, aligned with the −65 dBm install gate:
// above −60 excellent, above −65 good, above −75 weak, else very weak.
func signalBars(rssi int) int {
	switch {
	case rssi > -60:
		return 4
	case rssi > InstallGateRSSI:
		return 3
	case rssi > -75:
		return 2
	}
	return 1
}

// newDeviceView is the one device row builder (fleet board + owner panel).
// A board that has never checked in has no reading: printing its zero RSSI
// as "0 dBm" would read as a perfect signal, so it shows "—".
func newDeviceView(d store.Device, now time.Time) deviceView {
	dv := deviceView{
		ID: d.ID, Serial: d.Serial, Status: d.Status, FW: d.FW, RSSI: d.RSSI,
		SeenAge: age(now, d.LastSeenAt), Counters: d.Counters, Profile: d.Profile,
		RSSIText: "—",
	}
	if !d.LastSeenAt.IsZero() {
		dv.RSSIText = fmt.Sprintf("%d dBm", d.RSSI)
		dv.Weak = d.RSSI <= InstallGateRSSI
		dv.Bars = signalBars(d.RSSI)
		dv.SignalW = [...]string{"", "very weak", "weak", "good", "excellent"}[dv.Bars]
	}
	var parts []string
	for _, c := range []struct {
		n    uint32
		what string
	}{{d.Counters.JoinFail, "WiFi"}, {d.Counters.PostFail, "upload"}, {d.Counters.TLSFail, "TLS"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	dv.Failures = "none"
	if len(parts) > 0 {
		dv.Failures = strings.Join(parts, ", ")
	}
	return dv
}

type venueView struct {
	ID      int64
	Status  string // lifecycle; shown when not active
	Slug    string
	Name    string
	Fresh   chip // shape + colour + sentence for the status chip
	LastAge string
	Views7  int64 // public page views, last 7 days
	Live    int   // visitors with the page open right now
	Devices []deviceView
	// attention is the fleet sort key (design-pass2 §6): lower = reads
	// first. Never rendered.
	attention int
}

// chip is the "needs attention" state of a venue, ready for the chip partial
// (DESIGN.md §3.2). Shape is markup and must not be escaped; text is the
// signal a screen reader reads, so the state never depends on colour.
type chip struct {
	Cls  string
	Text string
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

// parseVenueForm validates the editable site-plane fields. It is the one
// validator for venue create (super-admin), venue update (super-admin) and
// the owner panel — three forms, one rule, so an owner can never save what
// the console would refuse. Returns the timezone or a user-facing error;
// hours arrive as structured editor fields and are parsed separately, so the
// spec-string format is one input shape among others, not the contract.
// Slug is identity and is never editable.
func parseVenueForm(name, style, tz string) (string, string) {
	name = strings.TrimSpace(name)
	tz = strings.TrimSpace(tz)
	switch {
	case name == "":
		return "", "Name is required."
	case style != "poster" && style != "mono":
		return "", "Style must be poster or mono."
	}
	if tz == "" {
		tz = store.DefaultTZ
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", "Unknown timezone: " + tz
	}
	return tz, ""
}

// parseHours validates the structured editor fields (DESIGN.md §9) with the
// given catalog's day names. copyMon is the "copy Monday to weekdays"
// button. Errors come back already sentenced in the catalog's language.
func parseHours(r *http.Request, cat *i18n.Catalog) (venue.Hours, string) {
	names := cat.DayNames()
	h, herr := venue.ParseHoursForm(r.FormValue, names, r.FormValue("copy_mon") == "1")
	if herr != nil {
		return h, cat.HoursErrorText(herr)
	}
	return h, ""
}

// Bounds for the public venue info (design-pass2 §5).
const (
	maxVenueDescription = 200
	maxVenuePhone       = 20
	maxVenueLocation    = 160
)

// parseVenueInfo validates the optional public info fields. Description is
// free plain text with a hard length bound; the phone allows the characters
// people actually type and is stored as typed (the page normalizes it for
// the tel: href — display keeps the human form). Location is one more plain
// line under the same discipline: no coordinates, no map embed, no links —
// text a guest can read and copy.
func parseVenueInfo(descRaw, phoneRaw, locRaw string) (string, string, string, string) {
	desc := strings.TrimSpace(descRaw)
	if len(desc) > maxVenueDescription {
		return "", "", "", fmt.Sprintf("Description must be at most %d characters.", maxVenueDescription)
	}
	phone := strings.TrimSpace(phoneRaw)
	if phone != "" {
		if len(phone) > maxVenuePhone {
			return "", "", "", fmt.Sprintf("Phone number must be at most %d characters.", maxVenuePhone)
		}
		for _, r := range phone {
			switch {
			case '0' <= r && r <= '9', r == '+', r == ' ', r == '-', r == '.', r == '(', r == ')':
			default:
				return "", "", "", "Phone number: only digits, spaces and + - . ( ) please."
			}
		}
	}
	loc := strings.TrimSpace(locRaw)
	if len(loc) > maxVenueLocation {
		return "", "", "", fmt.Sprintf("Location must be at most %d characters.", maxVenueLocation)
	}
	// A newline would break the one-line layout and let an owner inject a
	// second paragraph into the contact block; collapse it rather than refuse
	// a paste that a phone keyboard produced.
	if strings.ContainsAny(loc, "\r\n") {
		loc = strings.Join(strings.Fields(loc), " ")
	}
	return desc, phone, loc, ""
}

// maxMapsURL bounds the pasted link (Google share links are ~40 chars;
// full place URLs can run to a few hundred).
const maxMapsURL = 600

// parseMapsURL accepts an empty value or an https link to Google Maps —
// the only link an owner can put on the public page, so it cannot become
// a phishing slot: maps.app.goo.gl / goo.gl/maps share links, or
// google.<tld> / maps.google.<tld> with a /maps path. http is upgraded.
func parseMapsURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", true
	}
	if len(s) > maxMapsURL || strings.ContainsAny(s, " \t\r\n\"'<>") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", false
	}
	u.Scheme = "https"
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "maps.app.goo.gl":
	case host == "goo.gl" && strings.HasPrefix(u.Path, "/maps"):
	case googleHost(host) && (strings.HasPrefix(host, "maps.") || strings.HasPrefix(u.Path, "/maps")):
	default:
		return "", false
	}
	if u.Port() != "" {
		return "", false
	}
	return u.String(), true
}

// googleHost: google.<tld> or google.co(m).<cc>, optionally www./maps.
func googleHost(h string) bool {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "www."), "maps.")
	rest, ok := strings.CutPrefix(h, "google.")
	if !ok || rest == "" {
		return false
	}
	for _, part := range strings.Split(rest, ".") {
		if len(part) < 2 || len(part) > 3 {
			return false
		}
		for _, r := range part {
			if r < 'a' || r > 'z' {
				return false
			}
		}
	}
	return strings.Count(rest, ".") <= 1
}

func (s *Server) createVenue(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	if !store.ValidSlug(slug) {
		s.flash(w, "Slug must be lowercase letters, digits or dashes (≤40).")
		return
	}
	tz, ferr := parseVenueForm(
		r.FormValue("name"), r.FormValue("style"), r.FormValue("tz"))
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	h, hferr := parseHours(r, i18n.For(i18n.EN))
	if hferr != "" {
		s.flash(w, hferr)
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
	s.audit(r, "admin", "venue.create", id, "slug "+slug)
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?created=1", id))
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
	if has, err := s.venueHasCamera(r, venueID, 0); err != nil {
		s.internal(w, "camera check", err)
		return
	} else if has {
		s.flash(w, "That venue already has a camera. One camera per venue: delete or move the old one first.")
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
	}, auth.HashToken(token)); err != nil {
		if err == store.ErrSerialTaken {
			s.flash(w, "Serial already exists.")
			return
		}
		s.internal(w, "create device", err)
		return
	}
	s.log.Info("admin: device created", "venue_id", venueID, "serial", serial, "actor", "admin")
	s.audit(r, "admin", "device.create", venueID, "serial "+serial)
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
	s.audit(r, "admin", "device.revoke", 0, fmt.Sprintf("device %d", id))
	redirect(w, r, "/admin/")
}

func (s *Server) internal(w http.ResponseWriter, op string, err error) {
	s.log.Error("admin: internal error", "op", op, "err", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// --- venue + device editing (super-admin) -----------------------------------

type venueEditModel struct {
	Nav       navModel
	Statuses  []string
	Created   bool
	Venue     store.Venue
	Days      [7]venue.DayForm
	HoursSpec string
	HasOwner  bool
	Saved     bool
	OwnerSet  bool
	StyleSet  bool
	HasFrame  bool
	Devices   []deviceView
	// The style panel's choices (render.Look presets; default first).
	Outlines, Blurs, Contrasts []render.LookOption
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
	m := venueEditModel{Venue: *v, HoursSpec: h.Spec(), Days: venue.EditModel(h, i18n.For(i18n.EN).DayNames()), HasOwner: v.OwnerHash != "",
		Saved: r.URL.Query().Get("saved") != "", OwnerSet: r.URL.Query().Get("owner") != "", Created: r.URL.Query().Get("created") != "",
		StyleSet: r.URL.Query().Get("style") != "", HasFrame: !v.LastFrameAt.IsZero(),
		Outlines: render.OutlineOptions, Blurs: render.BlurOptions, Contrasts: render.ContrastOptions}
	devs, err := s.st.DevicesByVenue(r.Context(), v.ID)
	if err != nil {
		s.internal(w, "devices", err)
		return
	}
	for _, d := range devs {
		m.Devices = append(m.Devices, newDeviceView(d, s.now().UTC()))
	}
	m.Nav = s.nav(r.Context())
	m.Statuses = store.VenueStatuses
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
	style := r.FormValue("style")
	if style == "" {
		style = v.Style // the style lives in its own panel now
	}
	tz, ferr := parseVenueForm(
		r.FormValue("name"), style, r.FormValue("tz"))
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	desc, phone, loc, ierr := parseVenueInfo(
		r.FormValue("description"), r.FormValue("phone"), r.FormValue("location"))
	if ierr != "" {
		s.flash(w, ierr)
		return
	}
	maps, ok := parseMapsURL(r.FormValue("maps_url"))
	if !ok {
		s.flash(w, "Google Maps link: paste the link from Maps → Share (maps.app.goo.gl/… or google.com/maps/…).")
		return
	}
	h, hferr := parseHours(r, i18n.For(i18n.EN))
	if hferr != "" {
		s.flash(w, hferr)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	oldHours, _ := s.st.Hours(r.Context(), v.ID)
	listed := r.FormValue("listed") == "1"
	changes := venueChanges(v, oldHours, name, style, tz, desc, phone, loc, maps, &listed, h)
	if err := s.st.UpdateVenue(r.Context(), v.ID, name, style, tz, desc, phone, loc); err != nil {
		s.internal(w, "update venue", err)
		return
	}
	if err := s.st.SetMapsURL(r.Context(), v.ID, maps); err != nil {
		s.internal(w, "set maps url", err)
		return
	}
	if err := s.st.SetListed(r.Context(), v.ID, listed); err != nil {
		s.internal(w, "set listed", err)
		return
	}
	if err := s.st.SetHours(r.Context(), v.ID, h); err != nil {
		s.internal(w, "set hours", err)
		return
	}
	s.log.Info("admin: venue updated", "venue_id", v.ID, "slug", v.Slug,
		"name", name, "tz", tz, "style", style, "actor", "admin")
	s.audit(r, "admin", "venue.update", v.ID, changes)
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
	s.audit(r, "admin", "venue.owner_password", v.ID, "password set (value not recorded)")
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?owner=1", v.ID))
}

type deviceModel struct {
	Nav       navModel
	Device    store.Device
	Weak      bool // signal below the −65 dBm install gate (§10)
	Seen      bool // the board has ever checked in
	SeenAge   string
	Reject    string // last quarantine reason for the venue, "" = none
	RejectAge string
	Venue     string        // the venue's name, for the page header
	Venues    []store.Venue // "move to venue" choices
	Saved     bool
	Bars      int                 // signal bars 1–4 (when Seen)
	SignalW   string              // "excellent" … "very weak"
	Events    []store.DeviceEvent // timeline, newest first (P4)
	Uptime    string              // "3 h 12 min" from the board's own uptime
	CmdQueued bool
}

func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	venues, err := s.st.ListVenues(r.Context())
	if err != nil {
		s.internal(w, "list venues", err)
		return
	}
	now := s.now().UTC()
	dv := newDeviceView(*d, s.now().UTC())
	m := deviceModel{Device: *d, Venues: venues, Saved: r.URL.Query().Get("saved") != "", Bars: dv.Bars, SignalW: dv.SignalW,
		Seen:    !d.LastSeenAt.IsZero(),
		SeenAge: age(now, d.LastSeenAt),
		Weak:    !d.LastSeenAt.IsZero() && d.RSSI <= InstallGateRSSI}
	if reason, at, err := s.st.LastReject(r.Context(), d.VenueID); err == nil {
		m.Reject, m.RejectAge = reason, age(now, at)
	}
	for _, v := range venues {
		if v.ID == d.VenueID {
			m.Venue = v.Name
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	m.Events, _ = s.st.DeviceEvents(r.Context(), d.ID, 40)
	m.CmdQueued = d.PendingCmd != ""
	if d.UptimeS > 0 {
		m.Uptime = (time.Duration(d.UptimeS) * time.Second).Round(time.Minute).String()
	}
	m.Nav = s.nav(r.Context())
	if err := s.tmpl.ExecuteTemplate(w, "device.html", m); err != nil {
		s.log.Error("admin: render device", "err", err.Error())
	}
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
	// Shutter bars, same as the public page's closed chip (§5.6): a closed
	// venue is a different fact from a quiet camera, so it gets a different
	// silhouette — it used to borrow the no-signal hatch here (W2).
	shapeClosed  = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><rect y="2" width="12" height="3" fill="currentColor"/><rect y="7" width="12" height="3" fill="currentColor"/></svg>`
	shapeWaiting = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges"><path d="M0 0h4v4H0zM8 0h4v4H8zM4 4h4v4H4zM0 8h4v4H0zM8 8h4v4H8z" fill="currentColor"/></svg>`
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

// kit renders the component gallery (DESIGN.md §6/B1): every component, both
// themes, each state — the visual regression check for a solo builder. Gated
// on Server.Kit (GUZVA_KIT=1): without it the route 404s, because a gallery
// of all states is a confusing surface to leave on a production box.
func (s *Server) kit(w http.ResponseWriter, r *http.Request) {
	if !s.Kit {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "kit.html", nil); err != nil {
		s.log.Error("admin: render kit", "err", err.Error())
	}
}

// shape maps a status class to its silhouette. Unknown classes get the
// waiting dither — a missing shape must read as "unknown", never as a blank
// chip that implies a state it does not have.
func shape(cls string) template.HTML {
	switch cls {
	case "fresh":
		return shapeFresh
	case "late":
		return shapeLate
	case "stale", "nosignal":
		return shapeNoSignal
	case "closed":
		return shapeClosed
	default:
		return shapeWaiting
	}
}

// setStyle saves the venue's picture style and look presets. Unknown
// values never reach the store (Normalize maps them to the defaults), and
// no option blurs less than the default: privacy has a floor.
func (s *Server) setStyle(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	style := r.FormValue("style")
	if _, ok := render.LookupStyle(style); !ok {
		s.flash(w, "Pick Tisak or Šablon.")
		return
	}
	look := render.Look{Outline: r.FormValue("outline"), Blur: r.FormValue("blur"), Contrast: r.FormValue("contrast")}.Normalize()
	if err := s.st.UpdateVenue(r.Context(), v.ID, v.Name, style, v.TZ, v.Description, v.Phone, v.Location); err != nil {
		s.internal(w, "set style", err)
		return
	}
	if err := s.st.SetLook(r.Context(), v.ID, look); err != nil {
		s.internal(w, "set look", err)
		return
	}
	s.log.Info("admin: style set", "venue_id", v.ID, "style", style, "outline", look.Outline,
		"blur", look.Blur, "contrast", look.Contrast, "actor", "admin")
	s.audit(r, "admin", "venue.style", v.ID, "picture style changed")
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?style=1", v.ID))
}

// deleteVenue removes a venue for good: its devices (tokens stop working),
// stored renders, visit counts and public page. The form must repeat the
// slug — a destructive action takes a deliberate second step, not a click.
func (s *Server) deleteVenue(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	if strings.TrimSpace(r.FormValue("confirm")) != v.Slug {
		s.flash(w, fmt.Sprintf("Type %s to confirm — nothing was deleted.", v.Slug))
		return
	}
	if err := s.st.DeleteVenue(r.Context(), v.ID); err != nil {
		s.internal(w, "delete venue", err)
		return
	}
	if s.OnVenueDeleted != nil {
		if err := s.OnVenueDeleted(v); err != nil {
			// The row is gone; leftover files are logged, never fatal.
			s.log.Error("admin: venue files not removed", "venue_id", v.ID, "slug", v.Slug, "err", err.Error())
		}
	}
	s.log.Info("admin: venue deleted", "venue_id", v.ID, "slug", v.Slug, "actor", "admin")
	s.audit(r, "admin", "venue.delete", v.ID, "slug "+v.Slug)
	redirect(w, r, "/admin/venues?deleted="+v.Slug)
}

// venueHasCamera reports whether the venue has a camera other than
// exceptID. The console keeps one camera per venue (user decision,
// 15 Oct 2026); the fakecam harness (provision -devices) is exempt.
func (s *Server) venueHasCamera(r *http.Request, venueID, exceptID int64) (bool, error) {
	devs, err := s.st.DevicesByVenue(r.Context(), venueID)
	if err != nil {
		return false, err
	}
	for _, d := range devs {
		if d.ID != exceptID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) loadDevice(w http.ResponseWriter, r *http.Request) (*store.Device, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return nil, false
	}
	d, err := s.st.DeviceByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	return d, true
}

// updateDevice renames a device and/or moves it to another venue. The
// token stays, so a flashed board keeps posting — now for the new venue.
func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	serial := strings.TrimSpace(r.FormValue("serial"))
	if serial == "" {
		s.flash(w, "Serial is required.")
		return
	}
	venueID, err := strconv.ParseInt(r.FormValue("venue_id"), 10, 64)
	if err != nil {
		s.flash(w, "Pick a venue.")
		return
	}
	if _, err := s.st.VenueByID(r.Context(), venueID); err != nil {
		s.flash(w, "That venue no longer exists.")
		return
	}
	if has, err := s.venueHasCamera(r, venueID, d.ID); err != nil {
		s.internal(w, "camera check", err)
		return
	} else if has {
		s.flash(w, "That venue already has a camera. One camera per venue.")
		return
	}
	if err := s.st.UpdateDevice(r.Context(), d.ID, serial, venueID); err != nil {
		if errors.Is(err, store.ErrSerialTaken) {
			s.flash(w, "Another device already has that serial.")
			return
		}
		s.internal(w, "update device", err)
		return
	}
	s.log.Info("admin: device updated", "device_id", d.ID, "serial", serial, "venue_id", venueID, "actor", "admin")
	s.audit(r, "admin", "device.update", venueID, fmt.Sprintf("device %d serial %s", d.ID, serial))
	redirect(w, r, fmt.Sprintf("/admin/devices/%d?saved=1", d.ID))
}

// deleteDevice forgets a device and its token. Its frames belong to the
// venue and stay. Revoke is the reversible alternative.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteDevice(r.Context(), d.ID); err != nil {
		s.internal(w, "delete device", err)
		return
	}
	s.log.Info("admin: device deleted", "device_id", d.ID, "serial", d.Serial, "actor", "admin")
	s.audit(r, "admin", "device.delete", d.VenueID, fmt.Sprintf("device %d serial %s", d.ID, d.Serial))
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?devdeleted=1", d.VenueID))
}

// mailTest queues one test email (business plan P1) so the operator can
// prove the SMTP account works before any client email depends on it.
func (s *Server) mailTest(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	to := strings.TrimSpace(r.FormValue("to"))
	if len(to) > 254 || !strings.Contains(to, "@") || strings.ContainsAny(to, " \t\r\n<>,;") {
		s.flash(w, "Enter one email address.")
		return
	}
	now := s.now()
	if _, err := s.st.EnqueueMail(r.Context(), store.OutMail{To: to, Subject: "guzva.ba test email",
		Text:     "This is a test email from the guzva.ba server, sent " + now.UTC().Format(time.RFC1123) + ".",
		Template: "test"}, now); err != nil {
		s.internal(w, "enqueue test mail", err)
		return
	}
	s.log.Info("admin: test mail queued", "actor", "admin")
	s.audit(r, "admin", "mail.test", 0, "")
	if !s.MailOn {
		s.flash(w, "Queued — but SMTP is not configured, so it waits until GUZVA_SMTP_HOST and GUZVA_MAIL_FROM are set.")
		return
	}
	s.flash(w, "Queued. It is sent within 30 seconds; reload to see its status.")
}

// audit records a mutation in the audit log (PLAN §11). It never fails the
// request: a lost audit row is logged loudly, the change itself stands.
func (s *Server) audit(r *http.Request, actor, action string, venueID int64, detail string) {
	if err := s.st.AddAudit(r.Context(), store.AuditEntry{At: s.now(), Actor: actor, Action: action,
		VenueID: venueID, Detail: detail}); err != nil {
		s.log.Error("audit: write failed", "action", action, "err", err.Error())
	}
}

// venueChanges names the fields an edit form changed (names, not values).
func venueChanges(old *store.Venue, oldHours venue.Hours, name, style, tz, desc, phone, loc, maps string, listed *bool, h venue.Hours) string {
	var c []string
	add := func(f string, changed bool) {
		if changed {
			c = append(c, f)
		}
	}
	add("name", old.Name != name)
	add("style", old.Style != style)
	add("tz", old.TZ != tz)
	add("description", old.Description != desc)
	add("phone", old.Phone != phone)
	add("address", old.Location != loc)
	add("maps", old.MapsURL != maps)
	if listed != nil {
		add("listed", old.Listed != *listed)
	}
	add("hours", oldHours.Spec() != h.Spec())
	if len(c) == 0 {
		return "no changes"
	}
	return "changed: " + strings.Join(c, ", ")
}

// auditView is one audit row with its venue's name for display ("" when the
// entry is not venue-scoped or the venue was deleted).
type auditView struct {
	store.AuditEntry
	Venue string
}

// formKey derives the application-form signing key from the session secret,
// so the two never share a value.
func formKey(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("guzva intake form"))
	return m.Sum(nil)
}

// deviceCommand queues a remote command for the camera's next upload reply
// (business plan P4). Push-only: the server never connects to the board; the
// command waits until the board next posts (≤ a minute when healthy).
func (s *Server) deviceCommand(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadDevice(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	cmd := r.FormValue("cmd")
	switch cmd {
	case proto.CmdReboot, proto.CmdInstallWindow, "cancel":
	default:
		s.flash(w, "Unknown command.")
		return
	}
	queued := cmd
	if cmd == "cancel" {
		queued = ""
	}
	if err := s.st.SetPendingCommand(r.Context(), d.ID, queued, s.now()); err != nil {
		s.internal(w, "queue command", err)
		return
	}
	s.log.Info("admin: device command", "device_id", d.ID, "cmd", cmd, "actor", "admin")
	s.audit(r, "admin", "device.cmd", d.VenueID, fmt.Sprintf("device %d: %s", d.ID, cmd))
	redirect(w, r, fmt.Sprintf("/admin/devices/%d?saved=1", d.ID))
}
