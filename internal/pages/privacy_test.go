package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPrivacyPage pins the footer's link target: served from memory with
// the venue page's headers, CSP-clean (no script), and every claim present.
func TestPrivacyPage(t *testing.T) {
	h, err := Privacy()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, PrivacyPath, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`<html lang="hr">`, "<h1", "Politika privatnosti", "Posljednja izmjena",
		"radnoj memoriji", "48 sati", "prepoznavanje lica", "ne koristi kolačiće", "s interneta",
		"8. Vaša prava", "Agenciji za zaštitu ličnih podataka",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy page lacks %q", want)
		}
	}
	// User ruling (16 Oct 2026): no visit-counting mechanics on the page.
	for _, gone := range []string{"sažetak", "nasumičnim ključem", "400 dana", "Posjete ovoj stranici"} {
		if strings.Contains(body, gone) {
			t.Errorf("privacy page still discloses %q", gone)
		}
	}
	if strings.Contains(body, "<script") {
		t.Error("privacy page carries a script; it needs none")
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "style-src 'unsafe-hashes' 'sha256-") {
		t.Errorf("CSP = %q", csp)
	}
	if w.Header().Get("X-Robots-Tag") == "" || w.Header().Get("Last-Modified") == "" {
		t.Errorf("headers: %v", w.Header())
	}
	// Conditional GET answers 304 like a venue page.
	r := httptest.NewRequest(http.MethodGet, PrivacyPath, nil)
	r.Header.Set("If-Modified-Since", w.Header().Get("Last-Modified"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", w2.Code)
	}
}

// TestVenueFooterLinksPrivacy: the venue page's caption now has a target.
func TestVenueFooterLinksPrivacy(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(context.Background(), f.vid); err != nil {
		t.Fatalf("OnFrame: %v", err)
	}
	if !strings.Contains(f.readIndex(t), `href="/privatnost/">Politika privatnosti</a>`) {
		t.Error("venue footer does not link the privacy page")
	}
}
