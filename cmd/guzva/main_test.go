package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"guzva/internal/admin"
	"guzva/internal/auth"
	"guzva/internal/frames"
	"guzva/internal/pages"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/store"
)

// newTestMux assembles the mux exactly as serveMain does. The point: route
// registration panics on conflicting ServeMux patterns, and only the full
// composition (ingest + admin + static catch-all) can surface that — each
// package's own tests build a mux without the other half.
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	eng := render.NewEngine(nil, 1)
	lim := auth.DefaultLimiter(time.Now)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := frames.New(st, eng, lim, dir, 48, log)
	pub, err := pages.New(st, dir, log)
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	ing.OnStored = func(ctx context.Context, venueID int64) {}
	hash, err := auth.HashPassword("sekret")
	if err != nil {
		t.Fatalf("hashpw: %v", err)
	}
	adm, err := admin.New(st, hash, []byte("test-secret"), false, log)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	privacy, err := pages.Privacy()
	if err != nil {
		t.Fatal(err)
	}
	return newMux(ing, adm, pub.Root(), nil, healthzHandler(dir, time.Now()), privacy)
}

func TestMuxComposition(t *testing.T) {
	mux := newTestMux(t)

	cases := []struct {
		name, method, path string
		want               int
		allow              string
	}{
		{"ingest alive (no token)", http.MethodPost, proto.PathIngest, http.StatusUnauthorized, ""},
		{"ingest wrong method", http.MethodGet, proto.PathIngest, http.StatusMethodNotAllowed, "POST"},
		// PUT's Allow is ServeMux's auto-405 union (GET + POST routes on the
		// path plus the GET "/" subtree) — only the status is pinned.
		{"ingest other method", http.MethodPut, proto.PathIngest, http.StatusMethodNotAllowed, ""},
		{"root has no index", http.MethodGet, "/", http.StatusNotFound, ""},
		{"unknown venue static", http.MethodGet, "/jadro/", http.StatusNotFound, ""},
		{"healthz", http.MethodGet, proto.PathHealthz, http.StatusOK, ""},
		{"admin login page", http.MethodGet, "/admin/login", http.StatusOK, ""},
		{"admin board redirects when logged out", http.MethodGet, "/admin/", http.StatusSeeOther, ""},
		{"admin POST redirects when logged out", http.MethodPost, "/admin/venues", http.StatusSeeOther, ""},
		{"admin static htmx", http.MethodGet, "/admin/static/htmx.min.js", http.StatusOK, ""},
		{"privacy page", http.MethodGet, "/privatnost/", http.StatusOK, ""},
		{"privacy page without slash", http.MethodGet, "/privatnost", http.StatusTemporaryRedirect, ""}, // ServeMux subtree redirect,
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s: %s %s: status = %d, want %d", c.name, c.method, c.path, w.Code, c.want)
		}
		if c.allow != "" && w.Header().Get("Allow") != c.allow {
			t.Errorf("%s: Allow = %q, want %q", c.name, w.Header().Get("Allow"), c.allow)
		}
	}
}

func TestHealthzBody(t *testing.T) {
	mux := newTestMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, proto.PathHealthz, nil))
	var body struct {
		OK      bool    `json:"ok"`
		Version string  `json:"version"`
		UptimeS int     `json:"uptime_s"`
		DiskPct float64 `json:"disk_free_pct"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if !body.OK {
		t.Error("healthz ok = false")
	}
}

// TestStaticServesOnlyPublicFiles pins D5: the catch-all serves written
// bytes, never a directory listing, and unknown paths are 404.
func TestStaticServesOnlyPublicFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pub, err := pages.New(st, dir, log)
	if err != nil {
		t.Fatal(err)
	}
	// A venue page as pages.OnFrame would write it.
	slug := "jadro"
	if err := os.MkdirAll(filepath.Join(pub.Root(), slug), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pub.Root(), slug, "index.html"), []byte("<h1>Jadro</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pub.Root(), slug, "latest.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatal(err)
	}
	static := pages.Static(pub.Root(), nil)
	for path, want := range map[string]int{
		"/" + slug + "/":           http.StatusOK,
		"/" + slug + "/latest.png": http.StatusOK,
		"/" + slug + "/missing":    http.StatusNotFound,
		"/nope/":                   http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		static.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Errorf("GET %s: %d, want %d", path, w.Code, want)
		}
	}
	// No listing anywhere: GET / has the hint, GET /<venue>/ serves index.
	w := httptest.NewRecorder()
	static.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "/<slug>/") {
		t.Errorf("root hint = %d %q", w.Code, w.Body.String())
	}
	// The slashless form is canonicalised: served from /<slug>, the still
	// would resolve to /latest.png (404 — the broken-image bug on mobile
	// links that arrive without the trailing slash).
	w = httptest.NewRecorder()
	static.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+slug+"?p=1", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /%s: %d, want 301", slug, w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/"+slug+"/?p=1" {
		t.Errorf("Location = %q, want %q", loc, "/"+slug+"/?p=1")
	}
	// POST is refused — the static plane is read-only.
	w = httptest.NewRecorder()
	static.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/"+slug+"/", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST static: %d, want 405", w.Code)
	}
}
