package pages

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The stale-image bug, pinned.
//
// Two defects made the public page look stuck, both found by watching a real
// browser for four minutes rather than by reading the code:
//
//  1. The "still arriving" veil was cleared by a load listener attached AFTER
//     the node was inserted, so a cache-hit image (event already fired) left
//     it spinning; and the server-rendered data-loading="1" had no listener at
//     all until the first successful swap, so a first-time visitor saw a
//     permanently shimmering photo.
//  2. index.html was cached for 30s, so a freshly deployed build (new inlined
//     CSS, new JS) could stay invisible for up to half a minute.
func TestPublicPageShipsTheVeilClearer(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	// The initial image must be settled on arrival...
	if !strings.Contains(idx, `settle(document.getElementById('still'))`) {
		t.Error("page load never settles the server-rendered loading veil")
	}
	// ...and settle() must handle both already-complete and not-yet-loaded.
	for _, want := range []string{
		"function settle(img)",
		"if (img.complete)",
		`img.addEventListener('load'`,
		`img.addEventListener('error'`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("veil handling missing %q", want)
		}
	}
}

// The cache policy the two files need is opposite, and a single policy for
// both was the root cause: index.html must revalidate (it is rewritten on
// every state change and inlines the CSS and JS), while latest.jpg is
// addressed by a unique ?v= and can therefore be cached forever.
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
