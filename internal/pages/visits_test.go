package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"guzva/internal/stats"
)

// What counts: a person opening the page (view, + qr when ?src=qr) and the
// open tab's still refresh (poll). What does not: the page's own 60 s
// conditional check, the <img> load, bots, link previews and prefetches.
func TestVisitClassification(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	// A still to poll (the fixture venue has no frame row yet).
	if err := os.WriteFile(filepath.Join(f.w.Root(), "jadro", "latest.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	rec := stats.New(func() time.Time { return now })
	h := Static(f.w.Root(), rec)
	const phone = "Mozilla/5.0 (Linux; Android 14) Mobile Safari/537.36"
	for _, c := range []struct {
		path, ua string
		hdr      map[string]string
	}{
		{"/jadro/?src=qr", phone, map[string]string{"Sec-Fetch-Dest": "document"}},                  // view + qr
		{"/jadro/", phone + " b", map[string]string{"Sec-Fetch-Dest": "document"}},                  // view, 2nd person
		{"/jadro/", phone + " c", nil},                                                              // old browser, unconditional: view
		{"/jadro/", phone, map[string]string{"Sec-Fetch-Dest": "empty", "If-Modified-Since": "x"}},  // 60 s check
		{"/jadro/", phone, map[string]string{"If-Modified-Since": "Wed, 14 Oct 2026 10:00:00 GMT"}}, // conditional, no metadata
		{"/jadro/latest.png", phone, map[string]string{"Sec-Fetch-Dest": "empty"}},                  // poll
		{"/jadro/latest.png", phone, map[string]string{"Sec-Fetch-Dest": "image"}},                  // the <img>
		{"/jadro/", "Googlebot/2.1", map[string]string{"Sec-Fetch-Dest": "document"}},
		{"/jadro/", "WhatsApp/2.23", nil},
		{"/jadro/", phone, map[string]string{"Sec-Fetch-Dest": "document", "Sec-Purpose": "prefetch"}},
		{"/nope/", phone, map[string]string{"Sec-Fetch-Dest": "document"}}, // no such venue: 404, uncounted
	} {
		r := httptest.NewRequest(http.MethodGet, c.path, nil)
		r.Header.Set("User-Agent", c.ua)
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	hours, days := rec.Drain()
	if len(hours) != 1 {
		t.Fatalf("hours = %+v, want one jadro row", hours)
	}
	if got := hours[0]; got.Slug != "jadro" || got.Views != 3 || got.QR != 1 || got.Polls != 1 {
		t.Errorf("counts = %+v, want views 3, qr 1, polls 1", got)
	}
	if len(days) != 1 || days[0].Visitors != 3 {
		t.Errorf("days = %+v, want 3 visitors", days)
	}
}

// The still answers carry X-Watching: the distinct visitors with the page
// open right now (including the asker), 304s included.
func TestStillCarriesWatchingCount(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(t.Context(), f.vid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.w.Root(), "jadro", "latest.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := stats.New(nil)
	h := Static(f.w.Root(), rec)
	get := func(ua string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/jadro/latest.png", nil)
		r.Header.Set("User-Agent", ua)
		r.Header.Set("Sec-Fetch-Dest", "empty")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := get("Mozilla/5.0 phone-a", nil).Header().Get("X-Watching"); got != "1" {
		t.Errorf("first watcher sees %q, want 1", got)
	}
	w := get("Mozilla/5.0 phone-b", map[string]string{"If-Modified-Since": "Mon, 01 Jan 2099 00:00:00 GMT"})
	if w.Code != http.StatusNotModified || w.Header().Get("X-Watching") != "2" {
		t.Errorf("304 for a second watcher: code %d, X-Watching %q, want 304 / 2", w.Code, w.Header().Get("X-Watching"))
	}
}
