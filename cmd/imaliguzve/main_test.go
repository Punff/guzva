package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"imaliguzve/internal/auth"
	"imaliguzve/internal/frames"
	"imaliguzve/internal/proto"
	"imaliguzve/internal/render"
	"imaliguzve/internal/store"
	"imaliguzve/internal/web"
)

// newTestMux assembles the mux exactly as serveMain does. The point: route
// registration panics on conflicting ServeMux patterns, and only the full
// composition (ingest + web's GET "/" fallback) can surface that — web's own
// tests build a mux without the ingest route and vice versa.
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	eng := render.NewEngine(render.RealClock{}, 1)
	lim := auth.DefaultLimiter(time.Now)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := frames.New(st, eng, lim, dir, 48, log)
	pub, err := web.New(st, log)
	if err != nil {
		t.Fatalf("web: %v", err)
	}
	return newMux(ing, pub) // panics if the route patterns conflict
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
		// PUT's Allow comes from ServeMux's auto-405 (it unions methods of
		// every pattern matching the path, including the GET "/" subtree),
		// so only the status is pinned; our own GET hint says "POST".
		{"ingest other method", http.MethodPut, proto.PathIngest, http.StatusMethodNotAllowed, ""},
		{"root hint", http.MethodGet, "/", http.StatusNotFound, ""},
		{"unknown venue", http.MethodGet, "/v/nope", http.StatusNotFound, ""},
		{"healthz", http.MethodGet, "/healthz", http.StatusOK, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.want {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, w.Code, c.want)
		}
		if c.allow != "" && w.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: Allow = %q, want %q", c.method, c.path,
				w.Header().Get("Allow"), c.allow)
		}
	}

	// The GET "/" fallback must not swallow method checks on registered paths.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v/jadro", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST venue page: status = %d, want 405", w.Code)
	}
}
