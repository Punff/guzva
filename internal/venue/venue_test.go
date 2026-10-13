package venue

import (
	"fmt"
	"strings"
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
	// One interval per day (⚑ single-shift), closed Sunday.
	h, herr := ParseHoursForm(get(map[string]string{
		"h0_open": "09:00", "h0_close": "17:00",
		"h1_open": "09:00", "h1_close": "17:00",
		"h2_open": "09:00", "h2_close": "12:00",
		"h3_open": "09:00", "h3_close": "17:00",
		"h4_open": "09:00", "h4_close": "17:00",
		"h5_open": "10:00", "h5_close": "14:00",
		"h6_closed": "1",
	}), names, false)
	if herr != nil {
		t.Fatalf("valid form: %+v", herr)
	}
	for _, dow := range []int{1, 2, 4, 5} {
		if len(h[dow]) != 1 || h[dow][0] != (Range{OpenMin: 540, CloseMin: 1020}) {
			t.Fatalf("dow %d = %+v", dow, h[dow])
		}
	}
	if len(h[6]) != 1 || h[6][0] != (Range{OpenMin: 600, CloseMin: 840}) {
		t.Fatalf("saturday = %+v", h[6])
	}
	// Wednesday closes at noon in this schedule, not 17:00.
	if len(h[3]) != 1 || h[3][0] != (Range{OpenMin: 540, CloseMin: 720}) {
		t.Fatalf("wednesday = %+v", h[3])
	}
	if len(h[0]) != 0 {
		t.Fatalf("Sunday should be closed: %+v", h[0])
	}
	// A schedule the single-shift editor wrote itself is never "multi".
	if md := h.MultiDay(); len(md) != 0 {
		t.Fatalf("MultiDay = %v, want none for a single-shift schedule", md)
	}

	// Round-trips through the editor model.
	model := EditModel(h, names)
	if model[0].Open != "09:00" || model[0].Close != "17:00" {
		t.Fatalf("Monday model = %+v", model[0])
	}
	if !model[6].Closed {
		t.Fatalf("Sunday model should be closed: %+v", model[6])
	}
	for i, d := range model {
		if d.Split {
			t.Errorf("row %d flagged split on a single-shift schedule", i)
		}
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
			f["h"+itoa(d)+"_open"] = "09:00"
			f["h"+itoa(d)+"_close"] = "17:00"
		}
		return f
	}
	for _, tc := range []struct {
		fields map[string]string
		want   string // day name
		code   string
	}{
		{map[string]string{"h0_close": "17:00"}, "Mo", "clock"},                         // close without open
		{map[string]string{"h1_open": "17:00", "h1_close": "09:00"}, "Tu", "backwards"}, // backwards
		{map[string]string{"h2_open": "25:00", "h2_close": "26:00"}, "We", "clock"},     // not a clock
		{map[string]string{"h0_closed": "1", "h0_open": "09:00"}, "Mo", "closed_times"}, // closed + times
	} {
		if _, herr := ParseHoursForm(get(fill(tc.fields)), names, false); herr == nil || herr.DayName != tc.want || herr.Code != tc.code {
			t.Errorf("fields %v: err %+v, want %s/%s", tc.fields, herr, tc.want, tc.code)
		}
	}
	// An untouched row is refused, naming the first offender.
	if _, herr := ParseHoursForm(get(map[string]string{}), names, false); herr == nil || herr.DayName != "Mo" || herr.Code != "untouched" {
		t.Errorf("empty form: err %+v, want untouched Mo", herr)
	}

	// A LEGACY two-interval form post (the shipped template) still parses and
	// still stores both intervals: the lunch break is the owner's data and
	// collapsing it here would delete it silently. The single-shift contract
	// narrows what we WRITE from the new form, not what we can read.
	stale := map[string]string{"h0_open1": "09:00", "h0_close1": "17:00"}
	for d := 1; d < 7; d++ {
		stale["h"+itoa(d)+"_closed"] = "1"
	}
	hLegacy, herr := ParseHoursForm(get(stale), names, false)
	if herr != nil {
		t.Fatalf("legacy post must still parse: %+v", herr)
	}
	if len(hLegacy[1]) != 1 || hLegacy[1][0] != (Range{OpenMin: 540, CloseMin: 1020}) {
		t.Errorf("legacy Monday = %+v", hLegacy[1])
	}
	// Legacy SECOND interval survives the round trip, both halves.
	legacySplit := map[string]string{"h0_open1": "09:00", "h0_close1": "13:00",
		"h0_open2": "14:00", "h0_close2": "18:00"}
	for d := 1; d < 7; d++ {
		legacySplit["h"+itoa(d)+"_closed"] = "1"
	}
	hSplit, herr := ParseHoursForm(get(legacySplit), names, false)
	if herr != nil {
		t.Fatalf("legacy split post: %+v", herr)
	}
	if len(hSplit[1]) != 2 {
		t.Errorf("legacy split lost its second interval: %+v", hSplit[1])
	}
	// An error on the second interval says so, so the catalog can name it.
	badSecond := map[string]string{"h0_open1": "09:00", "h0_close1": "17:00",
		"h0_open2": "18:00", "h0_close2": "17:00"}
	for d := 1; d < 7; d++ {
		badSecond["h"+itoa(d)+"_closed"] = "1"
	}
	if _, herr := ParseHoursForm(get(badSecond), names, false); herr == nil || !herr.Second || herr.Code != "backwards" {
		t.Errorf("bad second interval: err %+v, want backwards with Second", herr)
	}

	// Copy-Monday spreads Monday over Tue–Fri, leaving Sat/Sun alone.
	h2, herr := ParseHoursForm(get(map[string]string{
		"h0_open": "08:00", "h0_close": "20:00",
		"h5_open": "10:00", "h5_close": "14:00",
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

// A stored two-interval row (written before ⚑ single-shift) still parses,
// still classifies, and is REPORTED rather than silently collapsed: stored
// data is not migrated, only the write path narrows.
func TestLegacySplitShiftSurvivesRead(t *testing.T) {
	h, err := ParseSpec("Mo 09:00-13:00;Mo 14:00-18:00;Tu 09:00-17:00")
	if err != nil {
		t.Fatalf("legacy spec must still parse: %v", err)
	}
	if len(h[1]) != 2 {
		t.Fatalf("Monday should hold two ranges: %+v", h[1])
	}
	if md := h.MultiDay(); len(md) != 1 || md[0] != 0 {
		t.Errorf("MultiDay = %v, want [0] (Monday, Mo-first)", md)
	}
	// Classification is unchanged: the lunch break is still closed.
	lunch := time.Date(2026, 10, 5, 13, 30, 0, 0, time.UTC) // Monday 13:30
	if h.OpenAt(lunch) {
		t.Error("13:30 must fall in the lunch break")
	}
	if !h.OpenAt(time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)) {
		t.Error("09:30 should be open")
	}
	// Both intervals still print, so the page tells the truth.
	if got, want := DayText(h[1]), "09:00–13:00, 14:00–18:00"; got != want {
		t.Errorf("DayText = %q, want %q", got, want)
	}
	// The editor shows the first interval and flags the day, so the owner is
	// never shown a schedule that quietly lost half of itself.
	names := [7]string{"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"}
	model := EditModel(h, names)
	if !model[0].Split || model[0].Open != "09:00" || model[0].Close != "13:00" {
		t.Errorf("Monday model = %+v, want Split with 09:00–13:00", model[0])
	}
	if model[1].Split {
		t.Errorf("Tuesday should not be flagged: %+v", model[1])
	}
}

// DayIdx is the Mo-first convention in one place, so "today" about the
// schedule never drifts by one (Go's Weekday is Sunday=0, every hours surface
// here is Monday-first).
func TestDayIdx(t *testing.T) {
	// One week, Sarajevo: Monday 5 Oct 2026 … Sunday 11 Oct 2026.
	loc, err := time.LoadLocation("Europe/Sarajevo")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		day := time.Date(2026, 10, 5+i, 12, 0, 0, 0, loc) // Mon..Sun, midday
		if got := DayIdx(day); got != i {
			t.Errorf("%s: DayIdx = %d, want %d", day.Weekday(), got, i)
		}
	}
	// The mapping must not care about the hour: 23:59 and 00:01 of the same
	// local day are the same index — the day, not the clock, is the input.
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, loc)
	if DayIdx(mon.Add(23*time.Hour+59*time.Minute)) != DayIdx(mon) {
		t.Error("23:59 and 00:00 of one local day must share a DayIdx")
	}
	// And it must follow the VENUE's local day, not UTC's: 01:00 Sarajevo on
	// Tuesday is still Monday 23:00 UTC.
	if got := DayIdx(time.Date(2026, 10, 6, 1, 0, 0, 0, loc)); got != 1 {
		t.Errorf("Tue 01:00 Sarajevo: DayIdx = %d, want 1 (Tuesday, Mo-first)", got)
	}
}

// WeekCards is what the public page renders (7 small day cards, Mo-first).
func TestWeekCards(t *testing.T) {
	names := [7]string{"Pon", "Uto", "Sri", "Čet", "Pet", "Sub", "Ned"}
	h, err := ParseSpec("Mo-Fr 09:00-17:00;Sa 10:00-14:00")
	if err != nil {
		t.Fatal(err)
	}
	cards := h.WeekCards(names)
	// Mo-first ordering is the contract every locale and surface uses.
	for i, want := range names {
		if cards[i].Name != want {
			t.Errorf("card %d name = %q, want %q", i, cards[i].Name, want)
		}
	}
	if cards[0].Text != "09:00–17:00" || cards[0].Closed {
		t.Errorf("Monday card = %+v", cards[0])
	}
	if cards[5].Text != "10:00–14:00" || cards[5].Ranges != 1 {
		t.Errorf("Saturday card = %+v", cards[5])
	}
	if !cards[6].Closed || cards[6].Text != "" || cards[6].Ranges != 0 {
		t.Errorf("Sunday should be closed and empty: %+v", cards[6])
	}
	// Dow stays Go-convention (Sunday-first) so the caller can index Hours.
	if cards[0].Dow != 1 || cards[6].Dow != 0 {
		t.Errorf("Dow convention broken: Monday=%d Sunday=%d", cards[0].Dow, cards[6].Dow)
	}
	// Unconfigured hours: every day closed, nothing invented.
	var empty Hours
	for i, c := range empty.WeekCards(names) {
		if !c.Closed || c.Text != "" {
			t.Errorf("unconfigured day %d = %+v, want closed", i, c)
		}
	}
}

// An always-open day (24:00) prints as 23:59: "24:00" is not a time a form or
// a browser will agree on, and the schedule must not change meaning by being
// displayed differently.
func TestDayTextAlwaysOpen(t *testing.T) {
	h, err := ParseSpec("Mo-Su 00:00-24:00")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := DayText(h[1]), "00:00–23:59"; got != want {
		t.Errorf("DayText = %q, want %q", got, want)
	}
}

// Every clock time Go renders is 24 h, zero-padded, Europe/Sarajevo — the
// house format (item: 24h). DayText is the one formatter both the page label
// and the weekly cards use, so pinning it here pins every surface they feed.
// i18n.Catalog.Time ("15:04") is the other Go-side renderer and is covered by
// its own package.
func TestDayTextIs24Hour(t *testing.T) {
	for _, tc := range []struct {
		rs   []Range
		want string
	}{
		{[]Range{{0, 65}}, "00:00–01:05"},                               // after midnight, single digits
		{[]Range{{540, 1020}}, "09:00–17:00"},                           // the normal case
		{[]Range{{0, 1439}}, "00:00–23:59"},                             // all day
		{[]Range{{720, 780}, {1080, 1200}}, "12:00–13:00, 18:00–20:00"}, // legacy split
	} {
		got := DayText(tc.rs)
		if got != tc.want {
			t.Errorf("DayText(%v) = %q, want %q", tc.rs, got, tc.want)
		}
		// No 12-hour markers and no AM/PM ever reach a guest.
		if strings.ContainsAny(got, "apmAPM") {
			t.Errorf("DayText(%v) = %q contains a 12-hour marker", tc.rs, got)
		}
		// Every rendered time is exactly HH:MM.
		for _, part := range strings.FieldsFunc(got, func(r rune) bool {
			return r == '–' || r == ',' || r == ' '
		}) {
			if len(part) != 5 || part[2] != ':' {
				t.Errorf("DayText(%v) = %q: %q is not HH:MM", tc.rs, got, part)
			}
		}
	}
}

func itoa(n int) string { return string(rune('0' + n)) }
