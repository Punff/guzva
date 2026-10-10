package venue

import (
	"testing"
	"time"
)

func TestParseSpecOK(t *testing.T) {
	h, err := ParseSpec("Mo-Fr 09:00-17:00;Sa 10:00-14:00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !h.Configured() {
		t.Fatal("expected configured hours")
	}
	// Monday (1) .. Friday (5) get the weekday range.
	for d := 1; d <= 5; d++ {
		if len(h[d]) != 1 || h[d][0] != (Range{9 * 60, 17 * 60}) {
			t.Errorf("day %d: got %v", d, h[d])
		}
	}
	if len(h[6]) != 1 || h[6][0] != (Range{10 * 60, 14 * 60}) {
		t.Errorf("saturday: got %v", h[6])
	}
	if len(h[0]) != 0 {
		t.Errorf("sunday should be empty, got %v", h[0])
	}
}

func TestParseSpecAlwaysOpen(t *testing.T) {
	h, err := ParseSpec("Mo-Su 00:00-24:00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mon := time.Date(2026, 10, 5, 23, 59, 0, 0, time.UTC) // Monday
	if !h.OpenAt(mon) {
		t.Error("always-open spec should be open late Monday")
	}
}

func TestParseSpecErrors(t *testing.T) {
	bad := []string{
		"Mo 25:00-26:00",
		"Xx 09:00-17:00",
		"Mo 17:00-09:00", // no midnight crossing (documented)
		"Mo 09:00",       // missing range
		"Mo 09:00-00:00", // zero close
		"Mo 9:0-10:00",   // malformed
	}
	for _, spec := range bad {
		if _, err := ParseSpec(spec); err == nil {
			t.Errorf("ParseSpec(%q) should fail", spec)
		}
	}
	if h, err := ParseSpec(""); err != nil || h.Configured() {
		t.Errorf("empty spec: h=%v err=%v", h, err)
	}
}

func TestOpenAtAndClassifyOpen(t *testing.T) {
	h, err := ParseSpec("Mo-Fr 09:00-17:00")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-05 is a Monday.
	cases := []struct {
		at   time.Time
		want OpenClosed
	}{
		{time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), Open},
		{time.Date(2026, 10, 5, 8, 59, 0, 0, time.UTC), Closed},
		{time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC), Closed}, // close exclusive
		{time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), Closed}, // Sunday
	}
	for _, c := range cases {
		if got := h.ClassifyOpen(c.at); got != c.want {
			t.Errorf("at %v: got %v want %v", c.at, got, c.want)
		}
	}
	var empty Hours
	if got := empty.ClassifyOpen(time.Now()); got != NoHours {
		t.Errorf("unconfigured hours: got %v want no-hours", got)
	}
}

func TestClassifyFreshness(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		last time.Time
		want Freshness
	}{
		{time.Time{}, Unknown},
		{now.Add(-time.Minute), Fresh},
		{now.Add(-3 * time.Minute), Fresh}, // boundary is exclusive
		// DESIGN.md ⚑3: two tiers, both "not fresh". A venue running a
		// few minutes behind is not the same as one that has gone quiet,
		// and the page should not use the same word for both.
		{now.Add(-3*time.Minute - time.Second), Late},
		{now.Add(-14 * time.Minute), Late},
		{now.Add(-15 * time.Minute), Late}, // NoSignalAfter boundary is exclusive
		{now.Add(-15*time.Minute - time.Second), Stale},
		{now.Add(-time.Hour), Stale},
	}
	for _, c := range cases {
		if got := Classify(c.last, now); got != c.want {
			t.Errorf("last=%v: got %v want %v", c.last, got, c.want)
		}
	}
}

// Spec round-trips the canonical forms: parse → render → identical string.
func TestSpecRoundtrip(t *testing.T) {
	for _, spec := range []string{
		"",
		"Mo-Fr 09:00-17:00",
		"Mo-Fr 09:00-17:00;Sa 10:00-14:00",
		"Sa 10:00-14:00",
		"Mo-Su 00:00-24:00",
		"Tu-Th 10:00-12:00;Sa-Su 10:00-14:00",
	} {
		h, err := ParseSpec(spec)
		if err != nil {
			t.Fatalf("parse %q: %v", spec, err)
		}
		if got := h.Spec(); got != spec {
			t.Errorf("Spec() = %q, want %q", got, spec)
		}
	}
}

// Spec output always re-parses, even for hand-built multi-range days that
// the grammar would not accept in one group.
func TestSpecMultiRangeReparses(t *testing.T) {
	var h Hours
	h[1] = []Range{{OpenMin: 9 * 60, CloseMin: 12 * 60}, {OpenMin: 13 * 60, CloseMin: 17 * 60}} // Mo
	spec := h.Spec()
	if spec != "Mo 09:00-12:00;Mo 13:00-17:00" {
		t.Fatalf("Spec() = %q", spec)
	}
	h2, err := ParseSpec(spec)
	if err != nil {
		t.Fatalf("re-parse %q: %v", spec, err)
	}
	if h2.Spec() != spec {
		t.Fatalf("unstable: %q → %q", spec, h2.Spec())
	}
}
