// Package web serves the public venue pages and the observability endpoints
// (PLAN §3, §6.4, §6.6). Owner and admin consoles arrive with Phase 2's
// owner P1 / admin P1 — their routes are deliberately not registered yet.
//
// Templates and htmx are embedded (§2: html/template + embed, vendored htmx,
// no CDN). Handlers never touch SQL: everything goes through *store.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"imaliguzve/internal/store"
	"imaliguzve/internal/venue"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server is the public/observability plane.
type Server struct {
	st   *store.Store
	tmpl *template.Template
	now  func() time.Time
	log  *slog.Logger

	// Injected so this package keeps no dependency on frames/cmd (§3
	// dependencies flow downward only).
	Version     string
	Started     time.Time
	DataDir     string // root under which venue renders live
	DiskFreePct func() (float64, error)
	Metrics     func() string
}

// New parses the embedded templates and builds the server.
func New(st *store.Store, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, now: time.Now, log: log, Version: "dev"}, nil
}

// Register wires the routes on mux (Go 1.22 patterns, §2).
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v/{slug}", s.venuePage)
	mux.HandleFunc("GET /v/{slug}/status", s.statusFragment)
	mux.HandleFunc("GET /v/{slug}/still", s.stillFragment)
	mux.HandleFunc("GET /v/{slug}/frame/{key}", s.frame)
	mux.HandleFunc("GET /static/{file}", s.static)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /metrics", s.metrics)
	// Fallback for anything unmatched, including the root: still a 404
	// (there is no index or venue list, §6.4) but one that says where to go.
	mux.HandleFunc("GET /", s.notFoundHint)
}

// notFoundHint answers the root and any unknown path. It exposes no venue
// data — just the one thing a lost visitor needs to know.
func (s *Server) notFoundHint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprint(w, "404 — there is no page here.\n\n"+
		"Venue pages live at /v/<slug> — one URL per venue, never a list.\n")
}

// --- shared view model ----------------------------------------------------

// view is what every page/fragment renders from.
type view struct {
	Name       string
	Slug       string
	FrameURL   string
	FreshClass string
	FreshText  string
	OpenClass  string
	OpenText   string
	HasFrame   bool
	StillURL   string
	StillVer   string
}

// buildView derives the freshness badge (server clock is canonical, §1) and
// the open/closed chip from pure venue logic.
func (s *Server) buildView(v *store.Venue, latest *store.Frame, hours venue.Hours) view {
	now := s.now().UTC()
	vw := view{
		Name:     v.Name,
		Slug:     v.Slug,
		FrameURL: fmt.Sprintf("/v/%s/frame/%s", v.Slug, v.FrameKey),
		HasFrame: latest != nil,
	}
	if latest != nil {
		vw.StillURL = vw.FrameURL
		vw.StillVer = latest.SHA256[:min(12, len(latest.SHA256))]
	}

	last := v.LastFrameAt
	if last.IsZero() && latest != nil {
		last = latest.ReceivedAt
	}
	switch venue.Classify(last, now) {
	case venue.Fresh:
		vw.FreshClass, vw.FreshText = "fresh", "Fresh"
	case venue.Stale:
		age := now.Sub(last)
		vw.FreshClass, vw.FreshText = "stale", "STALE — last good "+humanAge(age)+" ago"
	default:
		vw.FreshClass, vw.FreshText = "unknown", "No frame yet"
	}

	switch hours.ClassifyOpen(now.Local()) {
	case venue.Open:
		vw.OpenClass, vw.OpenText = "open", "Open now"
	case venue.Closed:
		vw.OpenClass, vw.OpenText = "closed", "Closed"
	default:
		vw.OpenClass, vw.OpenText = "", ""
	}
	return vw
}

// loadVenue fetches everything a public view needs in two indexed queries.
func (s *Server) load(ctx context.Context, slug string) (*store.Venue, *store.Frame, venue.Hours, error) {
	v, err := s.st.VenueBySlug(ctx, slug)
	if err != nil {
		return nil, nil, venue.Hours{}, err
	}
	latest, err := s.st.LatestFrame(ctx, v.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, nil, venue.Hours{}, err
	}
	hours, err := s.st.Hours(ctx, v.ID)
	if err != nil {
		return nil, nil, venue.Hours{}, err
	}
	return v, latest, hours, nil
}

func humanAge(d time.Duration) string {
	secs := int(d.Seconds())
	switch {
	case secs < 90:
		return fmt.Sprintf("%d s", secs)
	case secs < 5400:
		return fmt.Sprintf("%d min", secs/60)
	default:
		return fmt.Sprintf("%d h", secs/3600)
	}
}
