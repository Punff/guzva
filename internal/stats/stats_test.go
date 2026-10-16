package stats

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"guzva/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestRecordDrainLive(t *testing.T) {
	c := &clock{time.Date(2026, 10, 14, 10, 15, 0, 0, time.UTC)}
	r := New(c.now)
	r.Record("jadro", View, "1.1.1.1", "phone-a", true)
	r.Record("jadro", View, "1.1.1.1", "phone-a", false) // same person reloads
	r.Record("jadro", View, "2.2.2.2", "phone-b", false)
	r.Record("jadro", Poll, "1.1.1.1", "phone-a", false)
	r.Record("other", View, "3.3.3.3", "phone-c", false)
	// Watching comes from the still polls only, one per open tab: two
	// identical iPhones behind one café IP are two watchers; a page load
	// never counts (it would double the guest who then polls).
	r.Watch("jadro", "00112233aabbccdd", "1.1.1.1", "iphone")
	r.Watch("jadro", "00112233aabbccdd", "1.1.1.1", "iphone") // same tab, next poll
	r.Watch("jadro", "ffeeddccbbaa9988", "1.1.1.1", "iphone") // same IP+UA, other tab
	r.Watch("jadro", "", "2.2.2.2", "old-page")               // pre-header page: IP+UA
	r.Watch("jadro", "NOT-HEX!", "2.2.2.2", "old-page")       // malformed → same fallback

	if n := r.Live("jadro"); n != 3 {
		t.Errorf("live = %d, want 3", n)
	}
	if n := r.Live("other"); n != 0 {
		t.Errorf("a page load alone counted as watching: live = %d", n)
	}
	c.t = c.t.Add(LiveWindow + time.Second)
	if n := r.Live("jadro"); n != 0 {
		t.Errorf("live after the window = %d, want 0", n)
	}

	hours, days := r.Drain()
	var j Hour
	for _, h := range hours {
		if h.Slug == "jadro" {
			j = h
		}
	}
	if j.Views != 3 || j.Polls != 1 || j.QR != 1 || j.Hour != time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("jadro hour = %+v", j)
	}
	got := map[string]int64{}
	for _, d := range days {
		got[d.Slug] = d.Visitors
		if d.Day != "2026-10-14" {
			t.Errorf("day = %q", d.Day)
		}
	}
	if got["jadro"] != 2 || got["other"] != 1 {
		t.Errorf("visitors = %v, want jadro 2, other 1", got)
	}
	if h, d := r.Drain(); len(h) != 0 || len(d) != 0 {
		t.Errorf("second drain not empty: %v %v", h, d)
	}
}

// A new UTC day forgets every visitor and replaces the salt: the same
// person tomorrow is a new, unlinkable visitor.
func TestDayRotationForgetsVisitors(t *testing.T) {
	c := &clock{time.Date(2026, 10, 14, 23, 59, 0, 0, time.UTC)}
	r := New(c.now)
	r.Record("jadro", View, "1.1.1.1", "ua", false)
	salt := r.salt
	before := r.visitor("1.1.1.1", "ua")
	c.t = c.t.Add(2 * time.Minute)
	r.Record("jadro", View, "1.1.1.1", "ua", false)
	if r.salt == salt || r.visitor("1.1.1.1", "ua") == before {
		t.Error("salt did not rotate at UTC midnight")
	}
	_, days := r.Drain()
	if len(days) != 1 || days[0].Day != "2026-10-15" || days[0].Visitors != 1 {
		t.Errorf("days after rotation = %+v", days)
	}
}

// Flush writes counts — and only counts — to the store; unknown slugs are
// dropped, a second flush adds deltas, the day count keeps its maximum.
func TestFlushToStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	vid, err := st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster"})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)}
	r := New(c.now)
	r.Record("jadro", View, "1.1.1.1", "ua-secret-agent", true)
	r.Record("ghost", View, "1.1.1.1", "ua", false)
	if err := r.Flush(ctx, st); err != nil {
		t.Fatal(err)
	}
	r.Record("jadro", View, "2.2.2.2", "ua", false)
	r.Record("jadro", Poll, "2.2.2.2", "ua", false)
	if err := r.Flush(ctx, st); err != nil {
		t.Fatal(err)
	}
	hs, _ := st.VisitHours(ctx, vid, c.t.Add(-time.Hour))
	if len(hs) != 1 || hs[0].Views != 2 || hs[0].Polls != 1 || hs[0].QR != 1 {
		t.Fatalf("hours = %+v", hs)
	}
	ds, _ := st.VisitDays(ctx, vid, "2026-10-01")
	if len(ds) != 1 || ds[0].Visitors != 2 {
		t.Fatalf("days = %+v", ds)
	}
	// A restart empties the visitor set; a smaller count must not win.
	if err := st.SetVisitDay(ctx, vid, "2026-10-14", 1); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.VisitDays(ctx, vid, "2026-10-14"); ds[0].Visitors != 2 {
		t.Errorf("day count shrank to %d", ds[0].Visitors)
	}
	// Privacy: nothing identifying in the database file.
	st.Close()
	var raw string
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		raw += string(b)
	}
	if !strings.Contains(raw, "Jadro") {
		t.Fatal("scan read no data (positive control failed)")
	}
	for _, leak := range []string{"1.1.1.1", "2.2.2.2", "ua-secret-agent"} {
		if strings.Contains(raw, leak) {
			t.Errorf("database contains %q", leak)
		}
	}
}

// TestWatchIsCapped: client-chosen tab ids cannot grow memory without bound.
func TestWatchIsCapped(t *testing.T) {
	r := New(nil)
	for i := 0; i < liveCap+50; i++ {
		r.Watch("jadro", fmt.Sprintf("%016x", i), "1.1.1.1", "ua")
	}
	if n := r.Live("jadro"); n != liveCap {
		t.Errorf("live = %d, want the cap %d", n, liveCap)
	}
}
