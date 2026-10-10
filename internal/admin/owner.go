package admin

import (
	"net/http"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/store"
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
	mux.HandleFunc("GET /owner", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/owner/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /owner/venue", s.requireOwner(s.ownerUpdateVenue))
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

func (s *Server) renderOwnerLogin(w http.ResponseWriter, status int, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, "owner_login.html", map[string]string{"Err": errMsg}); err != nil {
		s.log.Error("admin: render owner login", "err", err.Error())
	}
}

func (s *Server) ownerLoginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	if !s.loginLim.AllowIP(ip) {
		s.log.Warn("owner: login throttled", "ip", ip)
		s.renderOwnerLogin(w, http.StatusTooManyRequests, "Previše pokušaja — pričekajte minutu.")
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
		s.renderOwnerLogin(w, http.StatusUnauthorized, "Pogrešan objekt ili lozinka.")
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
	Venue     store.Venue
	HoursSpec string
	OpenState string
	Devices   []deviceView
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
	m := ownerDashModel{Venue: *v, HoursSpec: h.Spec(),
		OpenState: h.ClassifyOpen(now.In(venueLoc(v.TZ))).String(),
		Saved:     r.URL.Query().Get("saved") != ""}
	for _, d := range devs {
		m.Devices = append(m.Devices, deviceView{
			ID: d.ID, Serial: d.Serial, Status: d.Status, FW: d.FW, RSSI: d.RSSI,
			SeenAge: age(now, d.LastSeenAt), Counters: d.Counters,
			CfgVer: d.CfgVer, CfgApplied: d.CfgAppliedVer,
			Amber: d.Status == "active" && d.CfgAppliedVer != d.CfgVer,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "owner_dash.html", m); err != nil {
		s.log.Error("admin: render owner dash", "err", err.Error())
	}
}

func (s *Server) ownerUpdateVenue(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	r.ParseForm()
	tz, h, ferr := parseVenueForm(
		r.FormValue("name"), r.FormValue("style"), r.FormValue("tz"), r.FormValue("hours"))
	if ferr != "" {
		s.flash(w, ferr)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if err := s.st.UpdateVenue(r.Context(), v.ID, name, r.FormValue("style"), tz); err != nil {
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
