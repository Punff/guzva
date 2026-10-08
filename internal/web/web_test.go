package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imaliguzve/internal/store"
	"imaliguzve/internal/venue"
)

// Covers the public plane (§6.4) and observability endpoints (§6.6):
// freshness badge states, frame-key gating, honest 404s, healthz and
// metrics — all against a real embedded-template parse.

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

type webFixture struct {
	srv     *Server
	st      *store.Store
	mux     *http.ServeMux
	dataDir string
	vid     int64
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vid, err := st.CreateVenue(context.Background(), store.Venue{
		Slug: "jadro", Name: "Jadro", Style: "poster", FrameKey: "fk-1234567890",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, nil)
	if err != nil {
		t.Fatalf("web.New: %v (embedded templates must parse)", err)
	}
	srv.now = func() time.Time { return testNow }
	srv.Started = testNow.Add(-time.Hour)
	srv.Version = "test"
	srv.DataDir = dataDir
	srv.DiskFreePct = func() (float64, error) { return 42.5, nil }
	srv.Metrics = func() string { return "imaliguzve_ingest_total{outcome=\"stored\"} 7\n" }
	mux := http.NewServeMux()
	srv.Register(mux)
	return &webFixture{srv: srv, st: st, mux: mux, dataDir: dataDir, vid: vid}
}

// addFrame writes a render file and its ok row; receivedAt drives freshness.
func (f *webFixture) addFrame(t *testing.T, receivedAt time.Time) *store.Frame {
	t.Helper()
	fr := store.Frame{
		VenueID: f.vid, CaptureID: "C1-1-aaaa",
		SHA256:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ReceivedAt: receivedAt, Bytes: 512, Width: 640, Height: 480,
		Preset: "poster", PresetVer: 1,
		Path:   filepath.Join("venues", "1", "stills", "C1-1-aaaa.jpg"),
		Status: "ok",
	}
	abs := filepath.Join(f.dataDir, fr.Path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("\xff\xd8\xff fake jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.InsertFrame(context.Background(), fr); err != nil {
		t.Fatal(err)
	}
	return &fr
}

func get(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestVenuePageFreshBadge(t *testing.T) {
	f := newWebFixture(t)
	f.addFrame(t, testNow.Add(-30*time.Second))

	w := get(t, f.mux, "/v/jadro")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<span class="badge fresh">Fresh</span>`) {
		t.Errorf("fresh badge missing:\n%s", body)
	}
	if !strings.Contains(body, "Jadro") || !strings.Contains(body, `/v/jadro/frame/fk-1234567890`) {
		t.Errorf("page missing venue identity or frame URL")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
}

func TestVenuePageStaleBadge(t *testing.T) {
	f := newWebFixture(t)
	f.addFrame(t, testNow.Add(-2*time.Hour))

	w := get(t, f.mux, "/v/jadro/status") // htmx-polled badge fragment
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="badge stale"`) {
		t.Errorf("stale badge missing: %s", body)
	}
	if !strings.Contains(body, "STALE — last good 2 h ago") {
		t.Errorf("stale text wrong: %s", body)
	}
}

func TestVenuePageNoFrameYet(t *testing.T) {
	f := newWebFixture(t)
	w := get(t, f.mux, "/v/jadro")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "No frame yet") {
		t.Errorf("expected empty state: %s", body)
	}
	still := get(t, f.mux, "/v/jadro/still")
	if still.Code != http.StatusOK || !strings.Contains(still.Body.String(), "No frame yet") {
		t.Errorf("still fragment = %d %q", still.Code, still.Body.String())
	}
}

func TestVenuePageUnknownSlug404(t *testing.T) {
	f := newWebFixture(t)
	for _, p := range []string{"/v/nope", "/v/nope/status", "/v/nope/still", "/v/nope/frame/fk-1234567890"} {
		if w := get(t, f.mux, p); w.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d", p, w.Code)
		}
	}
}

func TestFrameKeyGating(t *testing.T) {
	f := newWebFixture(t)
	f.addFrame(t, testNow.Add(-30*time.Second))

	// Wrong key → 404, never an image.
	wrong := get(t, f.mux, "/v/jadro/frame/wrong-key")
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong key: status = %d", wrong.Code)
	}
	if ct := wrong.Header().Get("Content-Type"); strings.Contains(ct, "image") {
		t.Errorf("wrong key leaked content-type %q", ct)
	}

	// Right key → the stored render with ETag.
	ok := get(t, f.mux, "/v/jadro/frame/fk-1234567890")
	if ok.Code != http.StatusOK {
		t.Fatalf("right key: status = %d", ok.Code)
	}
	if ct := ok.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	etag := ok.Header().Get("ETag")
	if etag == "" {
		t.Error("missing ETag")
	}

	// Conditional revalidation → 304.
	r := httptest.NewRequest(http.MethodGet, "/v/jadro/frame/fk-1234567890", nil)
	r.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	f.mux.ServeHTTP(notModified, r)
	if notModified.Code != http.StatusNotModified {
		t.Errorf("revalidate: status = %d", notModified.Code)
	}
}

func TestStillFragmentCarriesCacheBuster(t *testing.T) {
	f := newWebFixture(t)
	f.addFrame(t, testNow.Add(-30*time.Second))
	w := get(t, f.mux, "/v/jadro/still")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `<img src="/v/jadro/frame/fk-1234567890?v=`) {
		t.Errorf("still fragment missing cache-busted img: %s", body)
	}
}

func TestOpenClosedChip(t *testing.T) {
	f := newWebFixture(t)
	f.addFrame(t, testNow.Add(-30*time.Second))
	// Full-day opening hours → open regardless of the machine's timezone.
	h, err := venue.ParseSpec("Mo-Su 00:00-23:59")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetHours(context.Background(), f.vid, h); err != nil {
		t.Fatal(err)
	}
	w := get(t, f.mux, "/v/jadro/status")
	if !strings.Contains(w.Body.String(), `class="badge open"`) {
		t.Errorf("open chip missing: %s", w.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	f := newWebFixture(t)
	w := get(t, f.mux, "/healthz")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	if body["ok"] != true || body["version"] != "test" {
		t.Errorf("body = %v", body)
	}
	if body["disk_free_pct"] != "42.5" {
		t.Errorf("disk_free_pct = %v", body["disk_free_pct"])
	}
	if _, ok := body["uptime_s"].(float64); !ok {
		t.Errorf("uptime_s missing: %v", body)
	}
	// No venue data, no secrets (§6.6).
	if strings.Contains(w.Body.String(), "jadro") {
		t.Error("healthz leaked venue data")
	}
}

func TestMetrics(t *testing.T) {
	f := newWebFixture(t)
	w := get(t, f.mux, "/metrics")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if want := "imaliguzve_ingest_total{outcome=\"stored\"} 7"; !strings.Contains(w.Body.String(), want) {
		t.Errorf("metrics body = %q", w.Body.String())
	}
}

func TestStaticHtmxVendored(t *testing.T) {
	f := newWebFixture(t)
	w := get(t, f.mux, "/static/htmx.min.js")
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte("htmx")) {
		t.Fatalf("htmx: status = %d len = %d", w.Code, w.Body.Len())
	}
	// Path traversal: ServeMux 307-cleans to /etc/passwd; the cleaned path
	// must then 404 — nothing ever resolves outside /static.
	if w2 := get(t, f.mux, "/static/../../etc/passwd"); w2.Code != http.StatusTemporaryRedirect {
		t.Errorf("static traversal: status = %d, want mux 307", w2.Code)
	} else if w3 := get(t, f.mux, w2.Header().Get("Location")); w3.Code != http.StatusNotFound {
		t.Errorf("cleaned traversal path: status = %d", w3.Code)
	}
	if w3 := get(t, f.mux, "/static/other.js"); w3.Code != http.StatusNotFound {
		t.Errorf("unknown static: status = %d", w3.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	f := newWebFixture(t)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v/jadro", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}
