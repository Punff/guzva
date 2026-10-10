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
func TestStaticCachePolicyPerFile(t *testing.T) {
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
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	if cc := get("/jadro/index.html").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("index.html Cache-Control = %q, want no-cache (a cached page hides a new deploy)", cc)
	}
	if cc := get("/jadro/latest.jpg?v=" + v).Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("latest.jpg Cache-Control = %q, want immutable (unique URL per frame)", cc)
	}
}
