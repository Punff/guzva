package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"guzva/internal/store"
)

func homeBody(t *testing.T, f *fixture) (int, string, http.Header) {
	t.Helper()
	vs, err := f.st.ListVenues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.w.buildHome(context.Background(), vs); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	f.w.Home().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	return rr.Code, rr.Body.String(), rr.Header()
}

// TestHomeListsListedVenues pins D12: the homepage lists listed venues with
// the same state chip as their page, and an unlisted (bench/test) venue is
// absent. The contact line exists only when one was configured.
func TestHomeListsListedVenues(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hidden, err := f.st.CreateVenue(ctx, store.Venue{Slug: "bench", Name: "Bench rig", Style: "poster", TZ: "Europe/Sarajevo"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetListed(ctx, hidden, false); err != nil {
		t.Fatal(err)
	}
	code, body, hdr := homeBody(t, f)
	if code != http.StatusOK {
		t.Fatalf("home = %d", code)
	}
	for _, want := range []string{
		`<html lang="hr">`, "Ima li gužve?", `href="/jadro/"`, ">Jadro</span>",
		"Obala Kulina bana 3", "Svježa slika", `src="/jadro/latest.jpg?v=`,
		`id="za-salone"`, `href="/owner/"`, `href="/privatnost/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home lacks %q", want)
		}
	}
	if strings.Contains(body, "/bench/") || strings.Contains(body, "Bench rig") {
		t.Error("an unlisted venue appears on the homepage")
	}
	if strings.Contains(body, "Javite nam se") {
		t.Error("contact line shown without a configured contact")
	}
	if strings.Contains(body, "<script") || hdr.Get("X-Robots-Tag") != "" || strings.Contains(body, "noindex") {
		t.Error("homepage must be script-free and indexable")
	}
	if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}

	f.w.SetContact("info@example.ba")
	if _, body, _ = homeBody(t, f); !strings.Contains(body, `href="mailto:info@example.ba"`) {
		t.Error("configured contact not shown as a mailto link")
	}
}

// TestHomeClosedVenueShowsNoPicture: §5.6 holds on the list too.
func TestHomeClosedVenueShowsNoPicture(t *testing.T) {
	f := newFixture(t)
	f.now = f.now.Add(6 * time.Hour) // after 17:00 close
	_, body, _ := homeBody(t, f)
	if strings.Contains(body, "latest.jpg") || strings.Contains(body, "latest.png") {
		t.Error("closed venue's picture is on the homepage")
	}
	if !strings.Contains(body, "tx-shutter") || !strings.Contains(body, "Zatvoreno") {
		t.Error("closed venue card lacks the shutter / closed state")
	}
}

// TestHomeUnchangedKeepsLastModified: a rebuild with nothing new must not
// move Last-Modified, or every conditional GET would miss.
func TestHomeUnchangedKeepsLastModified(t *testing.T) {
	f := newFixture(t)
	_, _, h1 := homeBody(t, f)
	f.now = f.now.Add(time.Second)
	_, _, h2 := homeBody(t, f)
	if h1.Get("Last-Modified") != h2.Get("Last-Modified") {
		t.Errorf("Last-Modified moved on an identical rebuild: %s → %s", h1.Get("Last-Modified"), h2.Get("Last-Modified"))
	}
}

// TestNotFoundPage: unknown public paths get the Croatian page, status 404.
func TestNotFoundPage(t *testing.T) {
	f := newFixture(t)
	rr := httptest.NewRecorder()
	Static(f.w.Root(), nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/nema-ovoga/", nil))
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "Ova stranica ne postoji") ||
		!strings.Contains(rr.Body.String(), `href="/"`) {
		t.Errorf("404 = %d", rr.Code)
	}
}

// TestSharedFooterOnVenuePage: every public page carries the same footer.
func TestSharedFooterOnVenuePage(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(context.Background(), f.vid); err != nil {
		t.Fatal(err)
	}
	idx := f.readIndex(t)
	for _, want := range []string{`href="/">Naslovna</a>`, `href="/#za-salone">Za salone</a>`, `href="/privatnost/">Politika privatnosti</a>`} {
		if !strings.Contains(idx, want) {
			t.Errorf("venue footer lacks %q", want)
		}
	}
}
