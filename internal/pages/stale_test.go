package pages

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
	for _, want := range []string{"default-src 'none'", "style-src", "script-src", "sha256-", "frame-ancestors 'none'", "base-uri 'none'"} {
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
