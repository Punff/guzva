package pages

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"guzva/internal/render"
	"guzva/internal/store"
)

// The stale-image bug, pinned — and the fact that the fix has since changed
// shape. There WAS a "still arriving" veil: a shimmering ::after overlay
// cleared by a load listener attached AFTER the node was inserted, so a
// cache-hit image (event already fired) left it spinning forever, and the
// server-rendered veil had no listener at all until the first successful swap.
//
// DESIGN.md §3.6 then removed the shimmer entirely — skeletons are a STATIC
// dither — so there is no veil left to get stuck. The regression that matters
// is therefore not "the veil clears" but "there is no time-based overlay
// state at all": the box is reserved by geometry, and only a real state class
// or a new frame may change it.
func TestPublicPageHasNoStuckOverlayState(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	if strings.Contains(idx, "data-loading") {
		t.Error("page reintroduced a loading veil; §3.6 uses a static dither")
	}
	// The box is reserved by geometry, so the image never shifts the layout.
	if !strings.Contains(idx, "aspect-ratio:") {
		t.Error("frame box is not reserved before the still loads")
	}
	// The waiting state is a static dither, not a shimmer (§3.6).
	if !strings.Contains(idx, "tx-dither") {
		t.Error("waiting state does not use the static dither")
	}
	// Nothing loops forever: the only animation is the 240ms frame blink.
	if strings.Contains(idx, "infinite") {
		t.Error("page contains an infinitely looping animation")
	}
}

// The meta refresh must never reach a JS-enabled visitor: with it plainly in
// <head>, Chrome arms the timer at parse time and neither rewriting the delay
// nor removing the element afterwards disarms it, so the document reloaded
// every 30s and defeated the in-place swap. <noscript> is the only placement
// that works — the element is parsed only when scripting is off.
func TestPublicPageFallsBackOnlyWithoutJS(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	i := strings.Index(idx, `<meta http-equiv="refresh"`)
	if i < 0 {
		t.Fatal("no meta refresh at all — the no-JS fallback is gone")
	}
	// It must sit inside <noscript>, i.e. preceded by the open tag with no
	// intervening </noscript>.
	open := strings.LastIndex(idx[:i], "<noscript>")
	close := strings.LastIndex(idx[:i], "</noscript>")
	if open < 0 || close > open {
		t.Errorf("meta refresh is not inside <noscript>; a JS visitor will reload every 30s")
	}
}

// Both files must revalidate. index.html inlines the CSS and JS, so a cached
// copy is a visitor on an old build — the "struggles to pick it up after a
// deploy" symptom. latest.jpg sits behind a mutable file even though ?v= is
// unique per frame, so immutable would let a stale ?v= pin current bytes
// under an old version's URL for a year.
func TestStaticRevalidatesBothFiles(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	v := idx[strings.Index(idx, "latest.jpg?v=")+len("latest.jpg?v="):]
	if i := strings.IndexAny(v, `&"`); i >= 0 {
		v = v[:i]
	}
	if _, err := strconv.Atoi(v); err != nil {
		t.Fatalf("cache-buster %q is not an integer id: %v", v, err)
	}

	h := Static(f.w.Root(), nil)
	for _, path := range []string{"/jadro/index.html", "/jadro/latest.jpg?v=" + v} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, cc)
		}
	}
}

// Delivery hardening (DESIGN.md §7.4, §13). These are the guarantees a guest
// on mobile data actually depends on, and none of them can be checked by
// reading the template.
func TestPageSecurityHeadersAndCSP(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	h := Static(f.w.Root(), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jadro/index.html", nil))

	want := map[string]string{
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"X-Robots-Tag":                 "noindex, nofollow",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	csp := w.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP relies on unsafe-inline, which makes it decorative: %s", csp)
	}
	// The §7.3 poller fetches the still and shows it from a blob URL: without
	// blob: in img-src and connect-src 'self', the new still never loads.
	for _, want := range []string{"default-src 'none'", "style-src", "script-src", "sha256-", "frame-ancestors 'none'", "base-uri 'none'",
		"img-src 'self' blob: data:", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	// The hashes must be THIS page's, so a template change cannot silently
	// lock the page out of its own script.
	for _, tag := range []string{"style", "script"} {
		sum := sha256.Sum256([]byte(inlineBody(t, f.readIndex(t), tag)))
		if !strings.Contains(csp, base64.StdEncoding.EncodeToString(sum[:])) {
			t.Errorf("CSP hash for <%s> does not match the served page", tag)
		}
	}
	// The no-JS <noscript><style> that hides the clock is a second inline
	// style block; a CSP without its hash would silently drop it.
	noJS := sha256.Sum256([]byte("#hl{display:none}"))
	if !strings.Contains(csp, base64.StdEncoding.EncodeToString(noJS[:])) {
		t.Error("CSP does not authorise the no-JS clock-hiding style")
	}
	// The inline style ATTRIBUTE needs 'unsafe-hashes' AND its own hash; the
	// frame box's aspect-ratio is the one style attribute on the page.
	if !strings.Contains(csp, "'unsafe-hashes'") {
		t.Error("CSP cannot authorise the frame box's inline aspect-ratio")
	}
	for _, h := range styleAttrHashes([]byte(f.readIndex(t))) {
		if !strings.Contains(csp, h) {
			t.Errorf("CSP is missing the hash for a style attribute: %s", h)
		}
	}
}

func TestPageIsGzippedOnTheWire(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	h := Static(f.w.Root(), nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jadro/index.html", nil))
	if enc := w.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q without Accept-Encoding", enc)
	}
	if got, want := w.Body.Len(), len(f.readIndex(t)); got != want {
		t.Errorf("plain body = %d bytes, want %d", got, want)
	}
	if v := w.Header().Get("Vary"); v != "Accept-Encoding" {
		t.Errorf("identity response Vary = %q, want Accept-Encoding (Q-04)", v)
	}

	r := httptest.NewRequest(http.MethodGet, "/jadro/index.html", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Header().Get("Content-Encoding") != "gzip" || w2.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatal("no gzip + Vary when the client accepts it")
	}
	if w2.Body.Len() >= len(f.readIndex(t)) {
		t.Errorf("gzip did not help: %d >= %d", w2.Body.Len(), len(f.readIndex(t)))
	}
	zr, err := gzip.NewReader(w2.Body)
	if err != nil {
		t.Fatalf("not valid gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != f.readIndex(t) {
		t.Error("gzipped body does not round-trip to index.html")
	}
}

// inlineBody extracts one inline block's contents from the rendered page.
func inlineBody(t *testing.T, page, tag string) string {
	t.Helper()
	open, close := "<"+tag+">", "</"+tag+">"
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("no <%s> in page", tag)
	}
	j := strings.Index(page[i:], close)
	if j < 0 {
		t.Fatalf("no </%s> in page", tag)
	}
	return page[i+len(open) : i+j]
}

// The print world (DESIGN.md ⚑1/⚑2): a v2 PNG row publishes latest.png, the
// stale latest.jpg is removed so the root still holds exactly two files, and
// the page references the PNG at integer scale.
func TestPngRowPublishesLatestPng(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Plant a v2 PNG row alongside the fixture's old JPEG row.
	rel := filepath.Join("venues", "1", "stills", "C1-2-bbbb.png")
	abs := filepath.Join(f.w.dataDir, rel)
	if err := os.WriteFile(abs, []byte("FAKE-PRINT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.InsertFrame(ctx, store.Frame{
		VenueID: f.vid, CaptureID: "C1-2-bbbb", SHA256: "def456",
		ReceivedAt: f.now.Add(time.Minute),
		Bytes:      11, Width: render.GW, Height: render.GH,
		Preset: "poster", PresetVer: 2, Path: rel, Status: "ok",
	}); err != nil {
		t.Fatalf("insert png frame: %v", err)
	}
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.w.Root(), "jadro")
	got, err := os.ReadFile(filepath.Join(dir, "latest.png"))
	if err != nil {
		t.Fatalf("latest.png missing: %v", err)
	}
	if string(got) != "FAKE-PRINT" {
		t.Fatalf("latest.png = %q", got)
	}
	// The stale counterpart is gone: still exactly two files (D5).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("web root holds %v, want exactly [index.html latest.png]", names)
	}
	idx := f.readIndex(t)
	for _, want := range []string{
		`src="latest.png?v=`,
		`data-still="latest.png"`,
		`data-tz="Europe/Sarajevo"`,
		"still-print", // integer scale, pixelated (§5.7)
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// ⚑2's core behavior: the per-frame clock lives in the picture now, so a new
// frame with no state change must NOT rewrite index.html. Disk churn and
// Last-Modified movement per frame would defeat the 60 s state-flip check
// (every poll would see a "changed" page and reload constantly).
func TestNewFrameWithoutStateFlipSkipsRewrite(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(f.w.Root(), "jadro", "index.html")
	before, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	mtime := fi.ModTime()

	// A second frame, one minute later, same state (fresh, open, same name).
	f.now = f.now.Add(time.Minute)
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("index.html rewritten for a new frame with no state change — ⚑2 regressed")
	}
	if fi, err := os.Stat(idxPath); err != nil {
		t.Fatal(err)
	} else if !fi.ModTime().Equal(mtime) {
		t.Fatal("index.html mtime moved without a state change — every poll would reload")
	}
	// But the still itself DID advance (the picture is new even though the
	// page is byte-identical — the poller sees it via Last-Modified).
	got, err := os.ReadFile(filepath.Join(f.w.Root(), "jadro", "latest.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FAKE-STYLISED-RENDER" {
		t.Fatalf("still not published: %q", got)
	}
}

// A settings edit (rename) flips the state key, so the next tick rewrites.
func TestSettingsEditFlipsStateKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	before := f.readIndex(t)
	if err := f.st.UpdateVenue(ctx, f.vid, "Jadro Novo", "poster", "Europe/Sarajevo", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	if after := f.readIndex(t); !strings.Contains(after, "Jadro Novo") || after == before {
		t.Fatal("rename did not reach the page on refresh")
	}
}

// A location edit must force a rewrite — Location is on the page, so it is
// part of the state key — and must then SETTLE: a second refresh with no
// change may not rewrite again (that would move Last-Modified every sched
// tick and defeat the 60s state-flip check). Asserted via mtime, like the
// frame test above, because a rewrite with identical bytes still moves it.
func TestLocationEditFlipsStateKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(f.w.Root(), "jadro", "index.html")
	fi, err := os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	mtime := fi.ModTime()

	// Changing the location flips the key: one rewrite. (The fixture seeds a
	// location already, so a *different* one is a real change.)
	if err := f.st.UpdateVenue(ctx, f.vid, "Jadro", "poster", "Europe/Sarajevo",
		"", "+387 61 234 567", "Maršala Tita 1"); err != nil {
		t.Fatal(err)
	}
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(idxPath); err != nil {
		t.Fatal(err)
	} else if fi.ModTime().Equal(mtime) {
		t.Fatal("location edit did not flip the state key — it could never reach the page")
	}

	// ... and it settles: no further rewrite without a change.
	fi, err = os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	if fi2, err := os.Stat(idxPath); err != nil {
		t.Fatal(err)
	} else if !fi2.ModTime().Equal(fi.ModTime()) {
		t.Fatal("state key is not stable: the page would be rewritten every tick")
	}
}

// TodayIdx moves the week table's "today" row at midnight while EVERY data
// field is unchanged — and hoursToday does not flip the key when adjacent
// days share the same hours, nor does open when the venue is closed on both
// sides of midnight. Without TodayIdx in the key, a cached page would keep
// pointing "· danas" at yesterday's row for as long as nothing else changed.
//
// This test isolates exactly that: one frame, one venue, Monday 23:59 →
// Tuesday 00:01 venue-local. Freshness (2 min old, still fresh), open/closed
// (closed both sides), hoursToday (identical ranges) and the still are all
// constant, so TodayIdx (Mo 0 → Tu 1) is the only component that can differ.
// The rewrite must happen, then settle.
func TestTodayIdxFlipsStateKeyAtMidnight(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Europe/Sarajevo")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 10, 5, 23, 59, 0, 0, loc)
	tue := time.Date(2026, 10, 6, 0, 1, 0, 0, loc)
	if mon.Weekday() != time.Monday || tue.Weekday() != time.Tuesday {
		t.Fatalf("test dates are wrong: %s, %s", mon.Weekday(), tue.Weekday())
	}
	// A frame for Monday, so the page is fresh at 23:59 and 2 minutes later.
	rel := filepath.Join("venues", "1", "stills", "C1-2-mon.jpg")
	if err := os.MkdirAll(filepath.Join(f.w.Root(), filepath.Dir(rel)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.w.Root(), rel), []byte("FAKE-STYLISED-RENDER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.InsertFrame(ctx, store.Frame{
		VenueID: f.vid, CaptureID: "C1-2-mon", SHA256: "def456", ReceivedAt: mon,
		Bytes: 20, Width: 640, Height: 480, Preset: "poster", PresetVer: 3,
		Path: rel, Status: "ok",
	}); err != nil {
		t.Fatalf("insert frame: %v", err)
	}

	idxPath := filepath.Join(f.w.Root(), "jadro", "index.html")
	mtime := func() time.Time {
		t.Helper()
		fi, err := os.Stat(idxPath)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}

	f.now = mon
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	before := mtime()

	// Two minutes later, across local midnight: only the weekday moved.
	f.now = tue
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	after := mtime()
	if after.Equal(before) {
		t.Fatal("midnight did not flip the state key — the week table would keep " +
			"pointing \"· danas\" at yesterday's row")
	}

	// And it settles: no further rewrite until something really changes.
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	if settled := mtime(); !settled.Equal(after) {
		t.Fatal("state key not stable after the midnight flip: the page would be " +
			"rewritten on every tick")
	}
}

// The hours-edit report: a settings edit must reach an ALREADY-OPEN page.
// The 60 s poller keys off Last-Modified alone (venue.html: a moved value
// means flip/edit/deploy → reload), so index.html must send the header and
// honor conditional GET with 304 — otherwise document.lastModified stays
// empty and no open page ever flips.
func TestIndexHtmlConditionalGet(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	h := Static(f.w.Root(), nil)
	get := func(target, ims string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if ims != "" {
			r.Header.Set("If-Modified-Since", ims)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := get("/jadro/index.html", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d, want 200", w.Code)
	}
	lm := w.Header().Get("Last-Modified")
	if lm == "" {
		t.Fatal("index.html sends no Last-Modified — the 60 s flip detector can never fire")
	}
	if _, err := time.Parse(http.TimeFormat, lm); err != nil {
		t.Fatalf("Last-Modified %q does not parse as an HTTP date: %v", lm, err)
	}
	if w.Body.Len() == 0 {
		t.Fatal("200 with an empty body")
	}

	// The same timestamp the server just sent → 304, empty body.
	w304 := get("/jadro/index.html", lm)
	if w304.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", w304.Code)
	}
	if w304.Body.Len() != 0 {
		t.Errorf("304 with a %d-byte body, want empty", w304.Body.Len())
	}
	// Security headers survive the 304: the cached page must stay protected.
	for _, k := range []string{"Cache-Control", "X-Content-Type-Options", "X-Frame-Options"} {
		if w304.Header().Get(k) == "" {
			t.Errorf("304 dropped %s", k)
		}
	}

	// A future timestamp → 304 as well (the QA curl proof).
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if w := get("/jadro/index.html", future); w.Code != http.StatusNotModified {
		t.Errorf("future If-Modified-Since GET = %d, want 304", w.Code)
	}

	// A stale timestamp → full 200 again.
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if w := get("/jadro/index.html", past); w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Errorf("stale If-Modified-Since GET = %d with %d-byte body, want 200 with body",
			w.Code, w.Body.Len())
	}

	// The directory form serves the same file and the same contract.
	if w := get("/jadro/", lm); w.Code != http.StatusNotModified {
		t.Errorf("directory-form conditional GET = %d, want 304", w.Code)
	}
}
