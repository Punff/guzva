package admin

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/i18n"
	"guzva/internal/qr"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// Owner panel (per-venue login): venue details the owner may change
// themselves (name, style, timezone, hours) plus read-only device health.
// What the owner can never touch — tokens, device config, revoke, other
// venues — is enforced by construction: the only venue id in play comes
// from the signed session cookie, never from a form field or URL.
//
// No CSRF tokens here either (§6.2): the session cookie is SameSite=Strict
// + HttpOnly, same threat model as the super-admin console.

func (s *Server) registerOwner(mux *http.ServeMux) {
	mux.HandleFunc("GET /owner/login", s.ownerLoginPage)
	mux.HandleFunc("POST /owner/login", s.ownerLoginSubmit)
	mux.HandleFunc("POST /owner/logout", s.requireOwner(s.ownerLogout))
	mux.HandleFunc("GET /owner/{$}", s.requireOwner(s.ownerDash))
	mux.HandleFunc("POST /owner/venue", s.requireOwner(s.ownerUpdateVenue))
	mux.HandleFunc("GET /owner/poster", s.requireOwner(s.ownerPoster))
}

// requireOwner gates a handler on a valid owner session and loads the venue
// it scopes. A session for a deleted venue fails closed (cookie cleared,
// back to login) — a venue id must always resolve to exactly one venue.
func (s *Server) requireOwner(next func(http.ResponseWriter, *http.Request, *store.Venue)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vid, ok := s.ownerSess.VerifyOwner(r)
		if !ok {
			s.ownerLoginRedirect(w, r)
			return
		}
		v, err := s.st.VenueByID(r.Context(), vid)
		if err != nil {
			s.ownerSess.ClearOwner(w, s.secure)
			s.ownerLoginRedirect(w, r)
			return
		}
		next(w, r, v)
	}
}

func (s *Server) ownerLoginRedirect(w http.ResponseWriter, r *http.Request) {
	if htmx(r) {
		w.Header().Set("HX-Redirect", "/owner/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/owner/login", http.StatusSeeOther)
}

func (s *Server) ownerLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.ownerSess.VerifyOwner(r); ok {
		http.Redirect(w, r, "/owner/", http.StatusSeeOther)
		return
	}
	s.renderOwnerLogin(w, http.StatusOK, "")
}

// ownerLoginModel is the login page's template model. Cat is the Croatian
// catalog: the page is lang="hr" and the owner has not yet identified the
// venue, so no per-venue language applies (D7: Croatian is the product
// default). The template renders the owner.login.* keys through it.
type ownerLoginModel struct {
	Err string // one sentence, already Croatian
	Cat *i18n.Catalog
}

// renderOwnerLogin renders the owner login page. errKey is a catalog KEY,
// never a sentence: copy lives in internal/i18n, and a literal here is how
// the panel drifted into hard-coded Croatian in the first place. "" renders
// no error box (Get falls through to the empty key and yields an empty Err).
// The catalog is Croatian (D7) — the page is lang="hr" and the owner has not
// yet identified a venue, so there is no v.Lang to normalize from.
func (s *Server) renderOwnerLogin(w http.ResponseWriter, status int, errKey string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	cat := i18n.For(i18n.HR)
	m := ownerLoginModel{Err: cat.Get(errKey), Cat: cat}
	if err := s.tmpl.ExecuteTemplate(w, "owner_login.html", m); err != nil {
		s.log.Error("admin: render owner login", "err", err.Error())
	}
}

func (s *Server) ownerLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	if !s.loginLim.AllowIP(ip) {
		s.log.Warn("owner: login throttled", "ip", ip)
		s.renderOwnerLogin(w, http.StatusTooManyRequests, "owner.login.err.throttle")
		return
	}
	r.ParseForm()
	slug := r.FormValue("slug")
	v, err := s.st.VenueBySlug(r.Context(), slug)
	// One generic message for unknown venue, unset password and wrong
	// password alike: the login form must not reveal which venues exist or
	// which ones have owner access enabled.
	if err != nil || v.OwnerHash == "" ||
		!auth.CheckAdminPassword(v.OwnerHash, r.FormValue("password")) {
		s.log.Warn("owner: bad login", "ip", ip)
		s.renderOwnerLogin(w, http.StatusUnauthorized, "owner.login.err.badcreds")
		return
	}
	s.ownerSess.IssueOwner(w, v.ID, auth.SessionTTL, s.secure)
	s.log.Info("owner: login", "venue_id", v.ID, "slug", v.Slug, "ip", ip)
	redirect(w, r, "/owner/")
}

func (s *Server) ownerLogout(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	s.ownerSess.ClearOwner(w, s.secure)
	s.log.Info("owner: logout", "venue_id", v.ID, "slug", v.Slug, "ip", auth.ClientIP(r))
	redirect(w, r, "/owner/login")
}

type ownerDashModel struct {
	Stats     ownerStats
	Share     shareCard
	HasFrame  bool
	Venue     store.Venue
	HoursSpec string
	Days      [7]venue.DayForm
	OpenState string
	State     chip
	Summary   string
	Cat       *i18n.Catalog
	Devices   []ownerDevice
	Saved     bool
}

func (s *Server) ownerDash(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	h, err := s.st.Hours(r.Context(), v.ID)
	if err != nil {
		s.internal(w, "hours", err)
		return
	}
	devs, err := s.st.DevicesByVenue(r.Context(), v.ID)
	if err != nil {
		s.internal(w, "devices", err)
		return
	}
	now := s.now().UTC()
	loc := venueLoc(v.TZ)
	cat := i18n.For(i18n.Normalize(v.Lang))
	m := ownerDashModel{Venue: *v, HoursSpec: h.Spec(), Days: venue.EditModel(h, cat.DayNames()), Cat: cat,
		OpenState: h.ClassifyOpen(now.In(loc)).String(),
		Saved:     r.URL.Query().Get("saved") != ""}

	// One plain sentence, in the owner's language, saying what the picture
	// means right now — the thing they actually open this panel for (§9).
	fresh := venue.Classify(v.LastFrameAt, now)
	clock := "—"
	if !v.LastFrameAt.IsZero() {
		clock = cat.Time(v.LastFrameAt.In(loc))
	}
	switch {
	case v.LastFrameAt.IsZero():
		m.State = chip{"unknown", cat.Get("state.waiting")}
		m.Summary = cat.Get("state.waiting")
	case fresh == venue.Fresh:
		m.State = chip{"fresh", cat.Get("state.fresh")}
		m.Summary = cat.F("owner.fresh", clock)
	case fresh == venue.Late:
		m.State = chip{"late", cat.Get("state.late")}
		m.Summary = cat.F("owner.late", clock)
	default:
		m.State = chip{"stale", cat.Get("state.nosignal")}
		m.Summary = cat.F("owner.stale", clock)
	}

	since := now.Add(-24 * time.Hour)
	for _, d := range devs {
		m.Devices = append(m.Devices, newOwnerDevice(d, loc, cat))
		if d.Status == "active" && d.CreatedAt.After(since) {
			since = d.CreatedAt
		}
	}
	m.HasFrame = !v.LastFrameAt.IsZero()
	m.Share = s.share(r, v)
	vh, err := s.st.VisitHours(r.Context(), v.ID, now.AddDate(0, 0, -29))
	if err != nil {
		s.internal(w, "visit hours", err)
		return
	}
	vd, err := s.st.VisitDays(r.Context(), v.ID, now.AddDate(0, 0, -29).Format("2006-01-02"))
	if err != nil {
		s.internal(w, "visit days", err)
		return
	}
	ft, err := s.st.FrameTimes(r.Context(), v.ID, now.Add(-24*time.Hour))
	if err != nil {
		s.internal(w, "frame times", err)
		return
	}
	live := 0
	if s.Stats != nil {
		live = s.Stats.Live(v.Slug)
	}
	m.Stats = buildOwnerStats(now, loc, vh, vd, ft, since, live, cat)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "owner_dash.html", m); err != nil {
		s.log.Error("admin: render owner dash", "err", err.Error())
	}
}

func (s *Server) ownerUpdateVenue(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	r.ParseForm()
	tz, ferr := parseVenueForm(
		r.FormValue("name"), r.FormValue("style"), r.FormValue("tz"))
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
	cat := i18n.For(i18n.Normalize(v.Lang))
	h, hferr := parseHours(r, cat)
	if hferr != "" {
		s.flash(w, hferr)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.st.UpdateVenue(r.Context(), v.ID, name, r.FormValue("style"), tz, desc, phone, loc); err != nil {
		s.internal(w, "owner update venue", err)
		return
	}
	if err := s.st.SetHours(r.Context(), v.ID, h); err != nil {
		s.internal(w, "owner set hours", err)
		return
	}
	s.log.Info("owner: venue updated", "venue_id", v.ID, "slug", v.Slug,
		"name", name, "tz", tz, "style", r.FormValue("style"), "actor", "owner")
	redirect(w, r, "/owner/?saved=1")
}

// venueLoc loads the IANA location for the open/closed badge, falling back
// to UTC for legacy rows (only possible if written before validation
// existed) rather than failing the whole dashboard.
func venueLoc(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc
	}
	return time.UTC
}

// shareCard is the public link and its QR code. The QR carries ?src=qr so
// scans are counted apart from shared links (internal/stats).
type shareCard struct {
	URL string        // what the owner copies and shares
	QR  template.HTML // inline SVG, generated here from a URL we built
}

// share builds the venue's public URL from the host the owner reached the
// panel on — the same host guests use.
func (s *Server) share(r *http.Request, v *store.Venue) shareCard {
	scheme := "http"
	if r.TLS != nil || s.secure {
		scheme = "https"
	}
	base := scheme + "://" + r.Host + "/" + v.Slug + "/"
	c := shareCard{URL: base}
	if code, err := qr.Encode(base + "?src=qr"); err == nil {
		// Safe to mark as HTML: the SVG is built from module coordinates
		// and a constant colour, never from user text.
		c.QR = template.HTML(code.SVG("#12202d"))
	}
	return c
}

type posterModel struct {
	Venue store.Venue
	Share shareCard
	Cat   *i18n.Catalog
}

// ownerPoster is the printable table/door poster: name, the question, the
// QR code and one honest line about what guests will see.
func (s *Server) ownerPoster(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	m := posterModel{Venue: *v, Share: s.share(r, v), Cat: i18n.For(i18n.Normalize(v.Lang))}
	if err := s.tmpl.ExecuteTemplate(w, "owner_poster.html", m); err != nil {
		s.log.Error("admin: render poster", "err", err.Error())
	}
}

// ownerDevice is a camera in the owner's words: no serial jargon beyond the
// label, a clock time instead of "12 s ago", signal as good or weak.
type ownerDevice struct {
	Serial string
	Off    bool   // revoked by the administrator
	Last   string // "zadnja slika u 19:21" / "još nije poslala sliku"
	Signal string // "" when never heard from
	Weak   bool
}

func newOwnerDevice(d store.Device, loc *time.Location, cat *i18n.Catalog) ownerDevice {
	od := ownerDevice{Serial: d.Serial, Off: d.Status != "active", Last: cat.Get("owner.device.never")}
	if !d.LastSeenAt.IsZero() {
		od.Last = cat.F("owner.device.last", cat.Time(d.LastSeenAt.In(loc)))
		od.Weak = d.RSSI <= InstallGateRSSI
		key := "owner.signal.ok"
		if od.Weak {
			key = "owner.signal.weak"
		}
		od.Signal = cat.F(key, d.RSSI)
	}
	return od
}
