package pages

import (
	"context"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// base is the pinned clock: 14:00 UTC = 16:00 Europe/Sarajevo (CEST, UTC+2).
var base = time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)

type fixture struct {
	st  *store.Store
	w   *Writer
	vid int64
	rel string // ring render path of the seeded good frame
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	vid, err := st.CreateVenue(ctx, store.Venue{
		Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	// CreateVenue cannot carry the public info card fields (it writes slug,
	// name, tz, style and nothing else), so the phone goes in through the
	// same setter the admin and owner panels use. Stored as typed; the page
	// normalizes it for the tel: href (telHref).
	if err := st.UpdateVenue(ctx, vid, "Jadro", "poster", "Europe/Sarajevo", "", "+387 61 234 567"); err != nil {
		t.Fatalf("update venue: %v", err)
	}
	h, err := venue.ParseSpec("Mo-Fr 09:00-17:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetHours(ctx, vid, h); err != nil {
		t.Fatalf("set hours: %v", err)
	}
	f := &fixture{st: st, vid: vid, now: base}

	// Seed the ring: one good stylised render on disk + its row.
	f.rel = filepath.Join("venues", "1", "stills", "C1-1-aaaa.jpg")
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f.rel)), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, f.rel), []byte("FAKE-STYLISED-RENDER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertFrame(ctx, store.Frame{
		VenueID: vid, CaptureID: "C1-1-aaaa", SHA256: "abc123", ReceivedAt: base,
		Bytes: 20, Width: 640, Height: 480, Preset: "poster", PresetVer: 1,
		Path: f.rel, Status: "ok",
	}); err != nil {
		t.Fatalf("insert frame: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := New(st, dir, log)
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	w.now = func() time.Time { return f.now }
	f.w = w
	return f
}

func (f *fixture) readIndex(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.w.Root(), "jadro", "index.html"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	return string(b)
}

// TestOnFrameWritesOnlyPublicFiles is the D5 privacy proof: the web root
// contains exactly index.html + latest.jpg per venue — nothing else, ever.
func TestOnFrameWritesOnlyPublicFiles(t *testing.T) {
	f := newFixture(t)
	if err := f.w.OnFrame(context.Background(), f.vid); err != nil {
		t.Fatalf("OnFrame: %v", err)
	}
	var files []string
	err := filepath.WalkDir(f.w.Root(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(f.w.Root(), path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	want := []string{"jadro/index.html", "jadro/latest.jpg"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("web root = %v, want %v", files, want)
	}

	// latest.jpg is byte-identical to the ring render (a copy, not a re-encode).
	got, err := os.ReadFile(filepath.Join(f.w.Root(), "jadro", "latest.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FAKE-STYLISED-RENDER" {
		t.Fatalf("latest.jpg = %q", got)
	}

	idx := f.readIndex(t)
	// Croatian by default (DESIGN.md §11 / PLAN D7): the page a guest sees is
	// the product, and its copy lives in internal/i18n, not in this template.
	for _, want := range []string{
		"Jadro",
		"Svježa slika", // state, from the catalog
		// The no-JS fallback lives INSIDE <noscript>: a plain meta refresh is
		// armed at parse time and reloaded the document every 30s even with
		// the swap running (measured: 3 full navigations in 95s).
		`<noscript><meta http-equiv="refresh" content="60">`,
		"16:00",                           // venue-local, absolute (§6.4)
		"danas",                           // day word, localised
		"CEST",                            // venue timezone, not the viewer's
		`src="latest.jpg?v=`,              // cache-busted by frame id (collision-proof)
		"09:00–17:00",                     // today (Friday) hours, in the info card
		"Otvoreno do",                     // the card's open-state headline
		"namjerno grubo stilizirana",      // privacy footer, Croatian
		`aspect-ratio:`,                   // box reserved before the JPEG loads
		`alt="Stilizirana slika prostora`, // alt text says what it is
		// Tisak look: hard stepped shadow, 2px ink border, square corners.
		"shadow-step",
		"border-2",
		// ⚑7: a venue interior does not belong in a search index.
		`name="robots" content="noindex`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	// The tel: href is asserted on what the BROWSER resolves, not the bytes:
	// html/template escapes "+" in a URL attribute, so the wire carries
	// href="tel:&#43;38761234567". The point of the phone is that it is stored
	// as typed and normalized to digits for dialling.
	if got := html.UnescapeString(idx); !strings.Contains(got, `href="tel:+38761234567"`) {
		t.Errorf("index.html missing %q (after unescaping)", `href="tel:+38761234567"`)
	}
	// §3.2: a state is a SHAPE as well as a colour and a sentence. The shape
	// is what carries the state without colour, so it must be in the markup.
	if !strings.Contains(idx, "<svg") || !strings.Contains(idx, "aria-hidden") {
		t.Error("index.html has no status shape; colour would be the only signal")
	}
	// A static page must never show a relative age — it cannot keep it
	// correct (§6.4). "Updated" is gone entirely; the clock + day label
	// replace it and the freshness badge owns "how long ago".
	if strings.Contains(idx, "ago") || strings.Contains(idx, "Updated") {
		t.Error("index.html shows a relative age or the old 'Updated' label")
	}
	// Croatian plurals make relative sentences unreadable, which is why §11
	// forbids them: "prije 1 minutu / 2 minute / 5 minuta". Scan the visible
	// text only — the inlined stylesheet legitimately contains "minute" in
	// unit names.
	for _, bad := range []string{"minuta", "minute", "sati", "sat ", "prije", "ago"} {
		if strings.Contains(visibleText(idx), bad) {
			t.Errorf("page text contains a relative-time word %q", bad)
		}
	}
	// The still's intrinsic size reaches the template so the browser can
	// reserve the box (no layout shift on a new frame).
	if !strings.Contains(idx, `width="640"`) {
		t.Error("index.html missing intrinsic still width")
	}
	// The design system is inlined (D5: still a two-file root), and it is the
	// SAME compiled stylesheet the consoles serve — the guard against the two
	// surfaces drifting into different looks (PLAN §9).
	if !strings.Contains(idx, "--paper") || !strings.Contains(idx, "tx-dither") {
		t.Error("index.html is not inlining the shared design system")
	}
	if strings.Contains(idx, `href="/admin/app.css"`) {
		t.Error("public page must not link the console stylesheet (D5: two-file root)")
	}
}

// TestRefreshFlipsToStale proves the sched half: a static file cannot notice
// time passing, so Refresh rewrites index.html when the state flips.
func TestRefreshFlipsToStale(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	// ⚑3 splits "not fresh" in two. Just past 3 minutes the venue is running
	// LATE — gentler words, because the camera is still sending.
	f.now = base.Add(4 * time.Minute)
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	idx := f.readIndex(t)
	if !strings.Contains(idx, "Slika kasni") {
		t.Fatalf("no LATE badge after freshness flip:\n%s", idx)
	}
	// Past NoSignalAfter the camera has gone quiet, and the page says so in a
	// different sentence rather than reusing the same word.
	f.now = base.Add(20 * time.Minute)
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatalf("Refresh (no signal): %v", err)
	}
	if idx := f.readIndex(t); !strings.Contains(idx, "Nema nove slike") {
		t.Fatalf("no NO-SIGNAL badge after 20 min:\n%s", idx)
	}
	// The last-good still survives the flip (never publish a failure).
	if _, err := os.Stat(filepath.Join(f.w.Root(), "jadro", "latest.jpg")); err != nil {
		t.Fatalf("latest.jpg lost on stale: %v", err)
	}
}

// TestRefreshSkipsUnchangedState pins "index.html is rewritten only when
// state changes" (§6.4) via mtime: same state → untouched file.
func TestRefreshSkipsUnchangedState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(f.w.Root(), "jadro", "index.html")
	past := base.Add(-time.Hour)
	if err := os.Chtimes(idxPath, past, past); err != nil {
		t.Fatal(err)
	}
	// Same clock, same state → no rewrite, mtime stays where we put it.
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	fi, err := os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(past) {
		t.Fatalf("index rewritten with unchanged state: mtime %v", fi.ModTime())
	}
	// State flip → rewrite (mtime moves forward again).
	f.now = base.Add(4 * time.Minute)
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatalf("Refresh flipped: %v", err)
	}
	fi, err = os.Stat(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.ModTime().Equal(past) {
		t.Fatal("index not rewritten after state flip")
	}
}

// TestQuarantineKeepsLastGood: a quarantined beat must not blank the page
// or touch latest.jpg (§1 never publish a failure).
func TestQuarantineKeepsLastGood(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatal(err)
	}
	// A newer, quarantined capture (headers-only — no file exists for it).
	if _, err := f.st.InsertFrame(ctx, store.Frame{
		VenueID: f.vid, CaptureID: "C1-2-bbbb", SHA256: "def456",
		ReceivedAt: base.Add(time.Minute), Status: "quarantined", Reason: "dark",
	}); err != nil {
		t.Fatalf("insert quarantined: %v", err)
	}
	if err := f.w.OnFrame(ctx, f.vid); err != nil {
		t.Fatalf("OnFrame after quarantine: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(f.w.Root(), "jadro", "latest.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "FAKE-STYLISED-RENDER" {
		t.Fatal("latest.jpg changed after a quarantined frame")
	}
	idx := f.readIndex(t)
	if !strings.Contains(idx, "Svježa slika") || strings.Contains(idx, "Čekamo prvu sliku") {
		t.Fatalf("page lost last-good state:\n%s", idx)
	}
	// The clock + day word must still show the last GOOD frame's time.
	if !strings.Contains(idx, "16:00") || !strings.Contains(idx, "danas") {
		t.Fatalf("clock must still show the last GOOD frame:\n%s", idx)
	}
}

// dayLabel must disambiguate a bare clock time without becoming a relative
// age (§6.4): today, yesterday across a month/year boundary, and a date.
// The day word moved into the i18n catalog when the page went Croatian
// (DESIGN.md §11): "danas" / "jučer" / a date, localised.
func TestDayNameIsLocalised(t *testing.T) {
	hr := i18n.For(i18n.HR)
	en := i18n.For(i18n.EN)
	ny := time.Date(2026, 10, 9, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		at time.Time
		hr string
		en string
	}{
		{time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC), "danas", "today"},
		{time.Date(2026, 10, 9, 23, 59, 0, 0, time.UTC), "danas", "today"},
		{time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC), "jučer", "yesterday"},
		{time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC), "06.10.2026.", "06.10.2026."},
	}
	for _, c := range cases {
		if got := hr.DayName(c.at, ny); got != c.hr {
			t.Errorf("hr.DayName(%s) = %q, want %q", c.at.Format("2006-01-02 15:04"), got, c.hr)
		}
		if got := en.DayName(c.at, ny); got != c.en {
			t.Errorf("en.DayName(%s) = %q, want %q", c.at.Format("2006-01-02 15:04"), got, c.en)
		}
	}
	// Boundaries need their own "now" pins, in both directions.
	if got := hr.DayName(time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)); got != "jučer" {
		t.Errorf("month rollover = %q", got)
	}
	if got := hr.DayName(time.Date(2026, 12, 31, 6, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 6, 0, 0, 0, time.UTC)); got != "jučer" {
		t.Errorf("year rollover = %q", got)
	}
}

// zoneLabel prefers a real abbreviation, falls back to a numeric offset.
func TestZoneLabel(t *testing.T) {
	sarajevo, err := time.LoadLocation("Europe/Sarajevo")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	summer := time.Date(2026, 7, 1, 12, 0, 0, 0, sarajevo)
	if got := zoneLabel(summer, sarajevo); got != "CEST" {
		t.Errorf("summer zone = %q, want CEST", got)
	}
	winter := time.Date(2026, 1, 1, 12, 0, 0, 0, sarajevo)
	if got := zoneLabel(winter, sarajevo); got != "CET" {
		t.Errorf("winter zone = %q, want CET", got)
	}
	// UTC is spelled out, not abbreviated to "GMT".
	if got := zoneLabel(summer.UTC(), time.UTC); got != "UTC" {
		t.Errorf("utc zone = %q", got)
	}
}

// visibleText strips the inlined stylesheet and script, leaving what a guest
// actually reads. Scanning raw HTML for prose words false-positives on CSS
// units and on code.
func visibleText(idx string) string {
	for _, tag := range []string{"style", "script"} {
		for {
			i := strings.Index(idx, "<"+tag)
			if i < 0 {
				break
			}
			j := strings.Index(idx[i:], "</"+tag+">")
			if j < 0 {
				idx = idx[:i]
				break
			}
			idx = idx[:i] + idx[i+j+len(tag)+3:]
		}
	}
	return idx
}
