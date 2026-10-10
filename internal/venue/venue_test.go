package venue

import (
	"fmt"
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

// Hours editor round-trip (DESIGN.md §9): structured fields parse to the same
// Hours the spec string produces, and every error names the day.
func TestParseHoursForm(t *testing.T) {
	names := [7]string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}
	get := func(v map[string]string) func(string) string {
		return func(k string) string { return v[k] }
	}
	// One interval Monday, closed Sunday, split shift Wednesday.
	h, herr := ParseHoursForm(get(map[string]string{
		"h0_open1": "09:00", "h0_close1": "17:00",
		"h1_open1": "09:00", "h1_close1": "17:00",
		"h2_open1": "09:00", "h2_close1": "12:00",
		"h2_open2": "14:00", "h2_close2": "18:00",
		"h3_open1": "09:00", "h3_close1": "17:00",
		"h4_open1": "09:00", "h4_close1": "17:00",
		"h5_open1": "10:00", "h5_close1": "14:00",
		"h6_closed": "1",
	}), names, false)
	if herr != nil {
		t.Fatalf("valid form: %+v", herr)
	}
	if len(h[1]) != 1 || h[1][0] != (Range{OpenMin: 540, CloseMin: 1020}) {
		t.Fatalf("Monday = %+v", h[1])
	}
	if len(h[3]) != 2 {
		t.Fatalf("Wednesday split = %+v", h[3])
	}
	if len(h[0]) != 0 {
		t.Fatalf("Sunday should be closed: %+v", h[0])
	}
	// Round-trips through the editor model.
	model := EditModel(h, names)
	if model[0].Open1 != "09:00" || model[0].Close1 != "17:00" {
		t.Fatalf("Monday model = %+v", model[0])
	}
	if !model[6].Closed {
		t.Fatalf("Sunday model should be closed: %+v", model[6])
	}
	if model[2].Open2 != "14:00" {
		t.Fatalf("Wednesday second shift = %+v", model[2])
	}

	// Errors name the day. fill makes every row valid except the ones the
	// case sets, so only the target row can fail.
	fill := func(except map[string]string) map[string]string {
		f := map[string]string{}
		for k, v := range except {
			f[k] = v
		}
		touched := map[int]bool{}
		for k := range except {
			var d int
			if _, err := fmt.Sscanf(k, "h%d_", &d); err == nil {
				touched[d] = true
			}
		}
		for d := 0; d < 7; d++ {
			if touched[d] {
				continue
			}
			f["h"+itoa(d)+"_open1"] = "09:00"
			f["h"+itoa(d)+"_close1"] = "17:00"
		}
		return f
	}
	for _, tc := range []struct {
		fields map[string]string
		want   string // day name
		code   string
	}{
		{map[string]string{"h0_close1": "17:00"}, "Mo", "clock"},                          // close without open
		{map[string]string{"h1_open1": "17:00", "h1_close1": "09:00"}, "Tu", "backwards"}, // backwards
		{map[string]string{"h2_open1": "25:00", "h2_close1": "26:00"}, "We", "clock"},     // not a clock
		{map[string]string{"h0_closed": "1", "h0_open1": "09:00"}, "Mo", "closed_times"},  // closed + times
	} {
		if _, herr := ParseHoursForm(get(fill(tc.fields)), names, false); herr == nil || herr.DayName != tc.want || herr.Code != tc.code {
			t.Errorf("fields %v: err %+v, want %s/%s", tc.fields, herr, tc.want, tc.code)
		}
	}
	// An untouched row is refused, naming the first offender.
	if _, herr := ParseHoursForm(get(map[string]string{}), names, false); herr == nil || herr.DayName != "Mo" || herr.Code != "untouched" {
		t.Errorf("empty form: err %+v, want untouched Mo", herr)
	}

	// Copy-Monday spreads Monday over Tue–Fri, leaving Sat/Sun alone.
	h2, herr := ParseHoursForm(get(map[string]string{
		"h0_open1": "08:00", "h0_close1": "20:00",
		"h5_open1": "10:00", "h5_close1": "14:00",
		"h6_closed": "1",
	}), names, true)
	if herr != nil {
		t.Fatalf("copy-monday: %+v", herr)
	}
	for _, dow := range []int{2, 3, 4, 5} {
		if len(h2[dow]) != 1 || h2[dow][0] != (Range{OpenMin: 480, CloseMin: 1200}) {
			t.Errorf("dow %d after copy = %+v", dow, h2[dow])
		}
	}
	if len(h2[6]) != 1 {
		t.Errorf("Saturday changed by copy-monday: %+v", h2[6])
	}
}

func itoa(n int) string { return string(rune('0' + n)) }
