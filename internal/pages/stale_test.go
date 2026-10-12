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

	h := Static(f.w.Root())
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
	h := Static(f.w.Root())
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
	h := Static(f.w.Root())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jadro/index.html", nil))
	if enc := w.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q without Accept-Encoding", enc)
	}
	if got, want := w.Body.Len(), len(f.readIndex(t)); got != want {
		t.Errorf("plain body = %d bytes, want %d", got, want)
	}

	r := httptest.NewRequest(http.MethodGet, "/jadro/index.html", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("no gzip when the client accepts it")
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
