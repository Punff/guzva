package frames

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"guzva/internal/auth"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/store"
)

// The ingest contract (docs/proto-v1.md) under test: 201/409/422/401/413/
// 429/507, freshness semantics, and the two privacy invariants — raw frame
// bytes never touch the disk, request bodies never reach the logs.

const testToken = "gkv1_test_token_value"

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

type fixture struct {
	srv     *Server
	st      *store.Store
	vid     int64
	did     int64
	dataDir string
	logs    bytes.Buffer
}

// newFixture builds a fully wired ingest server. Burst parameters are per
// test: the limiter uses a pinned clock, so buckets never refill mid-test.
func newFixture(t *testing.T, devBurst int) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "guzva.db") // DB lives outside dataDir: dataDir is pure renders
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	vid, err := st.CreateVenue(ctx, store.Venue{
		Slug: "jadro", Name: "Jadro", Style: "poster",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	did, err := st.CreateDevice(ctx, store.Device{
		VenueID: vid, Serial: "esp32-aa",
		CfgVer: 2, CfgIntervalS: 30, CfgQuality: 14,
	}, auth.HashToken(testToken))
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	f := &fixture{
		st:      st,
		vid:     vid,
		did:     did,
		dataDir: dataDir,
	}
	clock := func() time.Time { return testNow }
	lim := auth.NewLimiter(2, 30, 0.2, devBurst, clock)
	logger := slog.New(slog.NewTextHandler(&f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.srv = New(st, render.NewEngine(nil, 2), lim, dataDir, 48, logger)
	f.srv.now = clock
	return f
}

// syntheticFrame: gradient + seeded noise — never dark, never uniform.
func syntheticFrame(t *testing.T, w, h int, seed int64) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := rnd.Intn(24) - 12
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(clamp(60 + x*120/w + n)),
				G: uint8(clamp(80 + y*100/h + n)),
				B: uint8(clamp(120 + (x+y)*40/(w+h) + n)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func blackFrame(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode black: %v", err)
	}
	return buf.Bytes()
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func metaJSON(t *testing.T, m proto.Meta) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// post sends one ingest request. metaJSON may be "" to omit the meta part,
// frame nil to omit the frame part; auth is attached when bearer != "".
func (f *fixture) post(t *testing.T, bearer, metaStr string, frame []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if metaStr != "" {
		if err := mw.WriteField(proto.PartMeta, metaStr); err != nil {
			t.Fatal(err)
		}
	}
	if frame != nil {
		w, err := mw.CreateFormFile(proto.PartFrame, "frame.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, proto.PathIngest, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if bearer != "" {
		r.Header.Set(proto.HeaderAuth, bearer)
	}
	w := httptest.NewRecorder()
	f.srv.HandleIngest(w, r)
	return w
}

func (f *fixture) postFrame(t *testing.T, captureID string, frame []byte) *httptest.ResponseRecorder {
	t.Helper()
	return f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{
		CaptureID: captureID, FW: "1.0.0", RSSI: -60, FreeHeap: 120000,
		CfgVerApplied: 2, Counters: proto.Counters{Boot: 1},
	}), frame)
}

func decodeResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return m
}

func (f *fixture) venue(t *testing.T) *store.Venue {
	t.Helper()
	v, err := f.st.VenueByID(context.Background(), f.vid)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *fixture) device(t *testing.T) *store.Device {
	t.Helper()
	d, err := f.st.DeviceByTokenHash(context.Background(), auth.HashToken(testToken))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func stills(t *testing.T, dataDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "venues", "*", "stills", "*.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// --- tests ------------------------------------------------------------------

func TestIngestStoresFrame(t *testing.T) {
	f := newFixture(t, 4)
	raw := syntheticFrame(t, 320, 240, 1)
	w := f.postFrame(t, "C1-1-aaaa", raw)

	if w.Code != proto.StatusStored {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get(proto.HeaderProto); got != proto.Version {
		t.Errorf("X-Proto = %q", got)
	}
	body := decodeResponse(t, w)
	dc := body["desired_cfg"].(map[string]any)
	if dc["ver"] != float64(2) || dc["interval_s"] != float64(30) || dc["quality"] != float64(14) {
		t.Errorf("desired_cfg = %v", dc)
	}
	if body["next_s"] != float64(30) {
		t.Errorf("next_s = %v", body["next_s"])
	}
	if _, ok := body["reason"]; ok {
		t.Errorf("201 must not carry reason: %v", body)
	}

	// File landed at the contract path, public width, no temp leftovers.
	wantPath := filepath.Join(f.dataDir, "venues", "1", "stills", "C1-1-aaaa.jpg")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("render missing: %v", err)
	}
	fh, err := os.Open(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	cfg, err := jpeg.DecodeConfig(fh)
	if err != nil || cfg.Width != render.PublicWidth {
		t.Fatalf("stored render: cfg=%+v err=%v (want width %d)", cfg, err, render.PublicWidth)
	}
	if tmps, _ := filepath.Glob(filepath.Join(f.dataDir, "venues", "*", "stills", ".ingest-*.tmp")); len(tmps) != 0 {
		t.Errorf("temp files left behind: %v", tmps)
	}

	// Freshness + health moved.
	if v := f.venue(t); v.LastFrameAt.IsZero() {
		t.Error("last_frame_at not advanced")
	}
	d := f.device(t)
	if !d.LastSeenAt.Equal(testNow) || d.FW != "1.0.0" || d.RSSI != -60 || d.CfgAppliedVer != 2 {
		t.Errorf("beat not applied: %+v", d)
	}
	if f.srv.Metrics().Get("stored") != 1 {
		t.Errorf("stored metric = %d", f.srv.Metrics().Get("stored"))
	}
}

func TestIngestDuplicateAnswers409SameShape(t *testing.T) {
	f := newFixture(t, 4)
	raw := syntheticFrame(t, 320, 240, 2)
	first := f.postFrame(t, "C1-1-bbbb", raw)
	if first.Code != proto.StatusStored {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	// Retry with the same capture_id (device saw a lost response).
	second := f.postFrame(t, "C1-1-bbbb", syntheticFrame(t, 320, 240, 99))
	if second.Code != proto.StatusDuplicate {
		t.Fatalf("second: %d %s", second.Code, second.Body.String())
	}

	// Same body shape: identical keys, identical desired_cfg/next_s.
	b1, b2 := decodeResponse(t, first), decodeResponse(t, second)
	if len(b1) != len(b2) {
		t.Fatalf("shape differs: %v vs %v", b1, b2)
	}
	for k, v1 := range b1 {
		v2, ok := b2[k]
		if !ok {
			t.Fatalf("409 missing key %q", k)
		}
		if reflect.DeepEqual(v1, v2) {
			continue
		}
		if k == "server_time" {
			continue // per-reply clock (proto §response wording)
		}
		t.Errorf("key %q: %v vs %v", k, v1, v2)
	}

	// Exactly one render on disk, one row consulted, no re-processing.
	if files := stills(t, f.dataDir); len(files) != 1 {
		t.Errorf("files = %v, want exactly 1", files)
	}
	if f.srv.Metrics().Get("duplicate") != 1 || f.srv.Metrics().Get("stored") != 1 {
		t.Errorf("metrics: duplicate=%d stored=%d",
			f.srv.Metrics().Get("duplicate"), f.srv.Metrics().Get("stored"))
	}
}

func TestIngestDarkFrameQuarantined(t *testing.T) {
	f := newFixture(t, 4)
	raw := blackFrame(t, 320, 240)
	w := f.postFrame(t, "C1-1-dead", raw)
	if w.Code != proto.StatusRejected {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if body := decodeResponse(t, w); body["reason"] != proto.ReasonDark {
		t.Errorf("reason = %v, want %q", body["reason"], proto.ReasonDark)
	}

	// Nothing stored: no render, no temp.
	if files := stills(t, f.dataDir); len(files) != 0 {
		t.Errorf("quarantined frame stored files: %v", files)
	}
	// Freshness NOT advanced by a failure (§1: never publish a failure).
	if v := f.venue(t); !v.LastFrameAt.IsZero() {
		t.Errorf("dark frame moved freshness: %v", v.LastFrameAt)
	}
	// Idempotency row exists so a retry answers 409, not a re-render.
	exists, err := f.st.FrameExists(context.Background(), f.vid, "C1-1-dead")
	if err != nil || !exists {
		t.Errorf("quarantine row: exists=%v err=%v", exists, err)
	}

	// quarantine.jsonl: headers only, one line, no frame bytes.
	qp := filepath.Join(f.dataDir, "quarantine.jsonl")
	qb, err := os.ReadFile(qp)
	if err != nil {
		t.Fatalf("quarantine log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(qb)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("bad JSONL: %v", err)
	}
	if rec["capture_id"] != "C1-1-dead" || rec["reason"] != proto.ReasonDark {
		t.Errorf("record = %v", rec)
	}
	if rec["sha256"] == "" || rec["raw_bytes"].(float64) <= 0 {
		t.Errorf("record missing metadata: %v", rec)
	}
	if bytes.Contains(qb, raw) {
		t.Error("raw frame bytes leaked into quarantine.jsonl")
	}
	if f.srv.Metrics().Get("quarantined") != 1 {
		t.Errorf("quarantined metric = %d", f.srv.Metrics().Get("quarantined"))
	}
}

func TestIngestHealthOnlyBeat(t *testing.T) {
	f := newFixture(t, 6)
	// Establish freshness with a real frame first.
	if w := f.postFrame(t, "C1-1-aaaa", syntheticFrame(t, 320, 240, 3)); w.Code != proto.StatusStored {
		t.Fatalf("setup: %d", w.Code)
	}
	before := f.venue(t).LastFrameAt

	// Camera-degraded health beat: meta only, no frame part (§4).
	w := f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{
		CaptureID: "C1-2-bbbb", FW: "1.0.0", RSSI: -71, FreeHeap: 90000,
		CfgVerApplied: 2, Counters: proto.Counters{PostFail: 3},
		Reason: "camera-degraded",
	}), nil)
	if w.Code != proto.StatusStored {
		t.Fatalf("health beat: %d %s", w.Code, w.Body.String())
	}

	d := f.device(t)
	if d.LastReason != "camera-degraded" || d.Counters.PostFail != 3 {
		t.Errorf("beat not applied: %+v", d)
	}
	if v := f.venue(t); !v.LastFrameAt.Equal(before) {
		t.Errorf("health beat moved freshness: %v -> %v", before, v.LastFrameAt)
	}
	if files := stills(t, f.dataDir); len(files) != 1 {
		t.Errorf("health beat wrote files: %v", files)
	}
	if f.srv.Metrics().Get("health") != 1 {
		t.Errorf("health metric = %d", f.srv.Metrics().Get("health"))
	}
}

func TestIngestUnauthorized(t *testing.T) {
	f := newFixture(t, 4)
	for name, bearer := range map[string]string{
		"missing": "", "wrong": "Bearer nope", "basic": "Basic abc",
	} {
		w := f.post(t, bearer, metaJSON(t, proto.Meta{CaptureID: "C1-1-aaaa"}), nil)
		if w.Code != proto.StatusUnauthorized {
			t.Errorf("%s: status = %d", name, w.Code)
		}
		if body := decodeResponse(t, w); body["error"] != "unauthorized" {
			t.Errorf("%s: body = %v", name, body)
		}
	}
	if f.srv.Metrics().Get("unauthorized") != 3 {
		t.Errorf("unauthorized metric = %d", f.srv.Metrics().Get("unauthorized"))
	}
	if files := stills(t, f.dataDir); len(files) != 0 {
		t.Errorf("unauthorized request stored files: %v", files)
	}
}

func TestIngestMetaTooLarge413(t *testing.T) {
	f := newFixture(t, 4)
	// Meta part over the 16 KiB cap (cheaper than a 4 MiB frame).
	w := f.post(t, "Bearer "+testToken, `{"capture_id":"C1-1-aaaa","pad":"`+strings.Repeat("x", proto.MaxMetaBytes)+`"}`, nil)
	if w.Code != proto.StatusTooLarge {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if body := decodeResponse(t, w); body["error"] != "too_large" {
		t.Errorf("body = %v", body)
	}
	if _, err := os.Stat(filepath.Join(f.dataDir, "quarantine.jsonl")); err == nil {
		t.Error("413 must not quarantine (nothing was parsed)")
	}
	if f.srv.Metrics().Get("too_large") != 1 {
		t.Errorf("too_large metric = %d", f.srv.Metrics().Get("too_large"))
	}
}

func TestIngestRateLimited429(t *testing.T) {
	f := newFixture(t, 1) // device burst 1, pinned clock → no refill
	if w := f.postFrame(t, "C1-1-aaaa", syntheticFrame(t, 320, 240, 4)); w.Code != proto.StatusStored {
		t.Fatalf("setup: %d", w.Code)
	}
	w := f.postFrame(t, "C1-1-bbbb", syntheticFrame(t, 320, 240, 5))
	if w.Code != proto.StatusRateLimit {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "10" {
		t.Errorf("Retry-After = %q, want %q", got, "10")
	}
	body := decodeResponse(t, w)
	if body["error"] != "rate_limited" || body["retry_after_s"] != float64(proto.RetryAfterS) {
		t.Errorf("body = %v", body)
	}
	if f.srv.Metrics().Get("ratelimited") != 1 {
		t.Errorf("ratelimited metric = %d", f.srv.Metrics().Get("ratelimited"))
	}
}

func TestIngestBadCaptureID422(t *testing.T) {
	f := newFixture(t, 4)
	w := f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{CaptureID: "nonsense"}), syntheticFrame(t, 320, 240, 6))
	if w.Code != proto.StatusRejected {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if body := decodeResponse(t, w); body["reason"] != proto.ReasonBadCaptureID {
		t.Errorf("reason = %v", body["reason"])
	}
	if files := stills(t, f.dataDir); len(files) != 0 {
		t.Errorf("stored files: %v", files)
	}
	if _, err := os.Stat(filepath.Join(f.dataDir, "quarantine.jsonl")); err != nil {
		t.Errorf("parse-stage rejection must be tailed: %v", err)
	}
}

func TestIngestDiskDegraded507(t *testing.T) {
	f := newFixture(t, 4)
	f.srv.diskFree = func(string) (float64, error) { return 3.0, nil }
	w := f.postFrame(t, "C1-1-aaaa", syntheticFrame(t, 320, 240, 7))
	if w.Code != proto.StatusDiskFull {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if body := decodeResponse(t, w); body["error"] != "disk_degraded" {
		t.Errorf("body = %v", body)
	}
	if f.srv.Metrics().Get("disk_full") != 1 {
		t.Errorf("disk_full metric = %d", f.srv.Metrics().Get("disk_full"))
	}
}

func TestIngestMethodNotAllowed(t *testing.T) {
	f := newFixture(t, 4)
	r := httptest.NewRequest(http.MethodGet, proto.PathIngest, nil)
	w := httptest.NewRecorder()
	f.srv.HandleIngest(w, r)
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" {
		t.Fatalf("status = %d Allow = %q", w.Code, w.Header().Get("Allow"))
	}
}

// TestPrivacyRawNeverOnDisk walks every byte the process writes: the raw
// input must not appear anywhere, every stored JPEG must be the public
// 640-wide render, and no temp file may survive (§1/§7).
func TestPrivacyRawNeverOnDisk(t *testing.T) {
	f := newFixture(t, 4)
	raw := syntheticFrame(t, 320, 240, 42)
	if w := f.postFrame(t, "C1-1-cafe", raw); w.Code != proto.StatusStored {
		t.Fatalf("setup: %d", w.Code)
	}
	// A quarantined attempt too — its bytes must not land either.
	if w := f.postFrame(t, "C1-1-beef", blackFrame(t, 320, 240)); w.Code != proto.StatusRejected {
		t.Fatalf("quarantine setup: %d", w.Code)
	}

	err := filepath.Walk(f.dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".tmp") {
			t.Errorf("temp file survived: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, raw) {
			t.Errorf("raw frame bytes on disk: %s", path)
		}
		if strings.HasSuffix(path, ".jpg") {
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(content))
			if err != nil {
				t.Errorf("stored JPEG unreadable: %s: %v", path, err)
			} else if cfg.Width != render.PublicWidth {
				t.Errorf("stored JPEG width %d != %d: %s", cfg.Width, render.PublicWidth, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRequestBodyNeverLogged drives the failure paths with a marker in the
// request body and asserts it reaches neither the logs nor any written file.
func TestRequestBodyNeverLogged(t *testing.T) {
	f := newFixture(t, 6)
	const marker = "SUPERSECRET_BODY_MARKER"

	// Path 1: unparseable meta → 422 (quarantine + response path).
	if w := f.post(t, "Bearer "+testToken, `not json at all `+marker, syntheticFrame(t, 320, 240, 8)); w.Code != proto.StatusRejected {
		t.Fatalf("path 1: %d", w.Code)
	}
	// Path 2: rejected before body parse → 401.
	if w := f.post(t, "Bearer nope", marker, nil); w.Code != proto.StatusUnauthorized {
		t.Fatalf("path 2: %d", w.Code)
	}
	// Path 3: marker inside otherwise-valid meta (extra fields are ignored
	// on the way in — they must not be logged on the way through).
	if w := f.post(t, "Bearer "+testToken,
		`{"capture_id":"C1-1-cafe","note":"`+marker+`"}`, nil); w.Code != proto.StatusStored {
		t.Fatalf("path 3: %d", w.Code)
	}

	if strings.Contains(f.logs.String(), marker) {
		t.Errorf("request body leaked into logs:\n%s", f.logs.String())
	}
	// Response bodies too: only canonical fields ever come back.
	for _, p := range []string{f.dataDir} {
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, err := os.ReadFile(path)
			if err == nil && bytes.Contains(b, []byte(marker)) {
				t.Errorf("marker written to %s", path)
			}
			return nil
		})
	}
}

// TestPruneKeepsLastGoodFile covers §6.3 end to end at the file level:
// expired renders go, the newest good render per venue survives, and crash
// temp files older than an hour are swept.
func TestPruneKeepsLastGoodFile(t *testing.T) {
	f := newFixture(t, 4)
	ctx := context.Background()
	old := testNow.Add(-72 * time.Hour)
	newer := testNow.Add(-time.Hour)

	mk := func(cid string, at time.Time) string {
		t.Helper()
		rel := filepath.Join("venues", "1", "stills", cid+".jpg")
		abs := filepath.Join(f.dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("jpg"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.InsertFrame(ctx, store.Frame{
			VenueID: f.vid, CaptureID: cid, SHA256: cid, ReceivedAt: at,
			Bytes: 3, Width: 640, Height: 480, Preset: "poster", PresetVer: 1,
			Path: rel, Status: "ok",
		}); err != nil {
			t.Fatal(err)
		}
		return abs
	}
	oldPath := mk("C1-1-aaaa", old)
	midPath := mk("C1-2-bbbb", testNow.Add(-50*time.Hour))
	goodPath := mk("C1-3-cccc", newer)

	// A crash leftover: temp file older than an hour.
	tmp := filepath.Join(f.dataDir, "venues", "1", "stills", ".ingest-crash.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleTime := testNow.Add(-2 * time.Hour)
	if err := os.Chtimes(tmp, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	n, err := f.srv.Prune(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2 (retain_hours=48)", n)
	}
	for _, p := range []string{oldPath, midPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expired render survived: %s", p)
		}
	}
	if _, err := os.Stat(goodPath); err != nil {
		t.Errorf("last-good render deleted: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("crash temp not swept")
	}
	// Rows for the deleted files are gone; the good one is still queryable.
	if exists, err := f.st.FrameExists(ctx, f.vid, "C1-1-aaaa"); err != nil || exists {
		t.Errorf("stale row survived: exists=%v err=%v", exists, err)
	}
	if exists, err := f.st.FrameExists(ctx, f.vid, "C1-3-cccc"); err != nil || !exists {
		t.Errorf("good row lost: exists=%v err=%v", exists, err)
	}
}

// TestOnStoredHook pins the §6.1 pages hook: fired exactly once per stored
// frame, never for duplicates, health-only beats, or rejected frames.
func TestOnStoredHook(t *testing.T) {
	f := newFixture(t, 10)
	var fired []int64
	f.srv.OnStored = func(ctx context.Context, venueID int64) {
		fired = append(fired, venueID)
	}

	// Stored frame → hook fires.
	w := f.postFrame(t, "C1-1-aaaa", syntheticFrame(t, 640, 480, 1))
	if w.Code != proto.StatusStored {
		t.Fatalf("first post = %d", w.Code)
	}
	// Duplicate → 409, no second publish.
	w = f.postFrame(t, "C1-1-aaaa", syntheticFrame(t, 640, 480, 2))
	if w.Code != proto.StatusDuplicate {
		t.Fatalf("dup post = %d", w.Code)
	}
	// Health-only beat (camera-degraded) → 201 but no page work.
	w = f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{
		CaptureID: "C1-2-bbbb", FW: "1.0.0", RSSI: -60,
	}), nil)
	if w.Code != proto.StatusStored {
		t.Fatalf("beat = %d", w.Code)
	}
	// Dark frame → 422, no page work.
	w = f.postFrame(t, "C1-3-cccc", blackFrame(t, 640, 480))
	if w.Code != proto.StatusRejected {
		t.Fatalf("dark = %d", w.Code)
	}

	if len(fired) != 1 {
		t.Fatalf("OnStored fired %d times (%v), want exactly 1", len(fired), fired)
	}
	if fired[0] != f.vid {
		t.Errorf("OnStored venue = %d, want %d", fired[0], f.vid)
	}
}
