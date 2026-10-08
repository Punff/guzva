package web

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"

	"imaliguzve/internal/store"
)

// venuePage renders /v/{slug}: stylised still + freshness + open/closed (§6.4).
// The frame URL carries an unguessable key; the raw camera image is never
// published (D1).
func (s *Server) venuePage(w http.ResponseWriter, r *http.Request) {
	v, latest, hours, err := s.load(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.pageErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "venue.html", s.buildView(v, latest, hours)); err != nil {
		s.log.Error("render venue page", "err", err.Error())
	}
}

// statusFragment is the htmx-polled freshness/open badge (every 15 s).
func (s *Server) statusFragment(w http.ResponseWriter, r *http.Request) {
	v, latest, hours, err := s.load(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.pageErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, "badge.html", s.buildView(v, latest, hours)); err != nil {
		s.log.Error("render badge", "err", err.Error())
	}
}

// stillFragment is the htmx-polled image swap (every 30 s). The cache-busting
// version is the frame's sha prefix, so an unchanged scene doesn't refetch.
func (s *Server) stillFragment(w http.ResponseWriter, r *http.Request) {
	v, latest, _, err := s.load(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.pageErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if latest == nil {
		fmt.Fprint(w, `<div class="still-empty">No frame yet</div>`)
		return
	}
	fmt.Fprintf(w,
		`<img src="/v/%s/frame/%s?v=%s" alt="stylised still" width="%d" height="%d">`,
		html.EscapeString(v.Slug), v.FrameKey,
		latest.SHA256[:min(12, len(latest.SHA256))],
		latest.Width, latest.Height)
}

// frame serves the latest good stylised render. The key is compared in
// constant time and only the store-recorded path is ever opened — there is
// no path from user input to the filesystem (§6.4, no-raw-route test).
func (s *Server) frame(w http.ResponseWriter, r *http.Request) {
	v, err := s.st.VenueBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.pageErr(w, r, err)
		return
	}
	key := r.PathValue("key")
	if subtle.ConstantTimeCompare([]byte(v.FrameKey), []byte(key)) != 1 {
		http.NotFound(w, r)
		return
	}
	latest, err := s.st.LatestFrame(r.Context(), v.ID)
	if errors.Is(err, store.ErrNotFound) || latest == nil {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	f, err := os.Open(filepath.Join(s.DataDir, latest.Path))
	if err != nil {
		// Pruned or lost: honest absence — never a different image (§1).
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	etag := `"` + latest.SHA256[:min(16, len(latest.SHA256))] + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")
	http.ServeContent(w, r, "still.jpg", latest.ReceivedAt, f)
}

// static serves vendored assets (htmx — no CDN, §2).
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name != "htmx.min.js" {
		http.NotFound(w, r)
		return
	}
	b, err := assets.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// pageErr maps store errors to honest HTTP answers: unknown slug → 404,
// anything else → 500 (logged without any request body, §1).
func (s *Server) pageErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	s.internal(w, err)
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.log.Error("web internal error", "err", err.Error())
	http.Error(w, "internal error", http.StatusInternalServerError)
}
