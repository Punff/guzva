package pages

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{
		"Jadro",
		"Fresh",
		`<meta http-equiv="refresh" content="30"`,
		"Updated 16:00",             // venue-local, absolute (§6.4)
		`src="latest.jpg?v=`,        // cache-busted by received_at
		"Today 09:00–17:00",         // today (Friday) hours, venue-local
		"raw camera image is never", // privacy footer
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	// A static page must never show a relative age — it cannot keep it
	// correct (§6.4).
	if strings.Contains(idx, "ago") {
		t.Error("index.html shows a relative age")
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
	f.now = base.Add(4 * time.Minute) // > StaleAfter (3 min)
	if err := f.w.Refresh(ctx, f.vid); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	idx := f.readIndex(t)
	if !strings.Contains(idx, "STALE") {
		t.Fatalf("no STALE badge after freshness flip:\n%s", idx)
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
	if !strings.Contains(idx, "Fresh") || strings.Contains(idx, "No frame yet") {
		t.Fatalf("page lost last-good state:\n%s", idx)
	}
	if !strings.Contains(idx, "Updated 16:00") {
		t.Fatalf("Updated must still show the last GOOD frame:\n%s", idx)
	}
}
