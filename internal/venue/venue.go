// Package venue contains the pure domain logic shared by ingest, the public
// page and (later) the owner/admin consoles: opening hours, open/closed
// classification and frame freshness. No I/O, no globals — PLAN §3.
package venue

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Freshness thresholds (PLAN §6.6, DESIGN.md §3.2/⚑3). Two tiers, both
// "not fresh", so the page and the alerting agree: a picture that is merely
// running behind says so in gentler words than one the camera has stopped
// sending. NoSignalAfter is the plan's own 15-minute alert threshold, reused
// rather than invented.
const (
	// StaleAfter is when a good render stops counting as fresh.
	StaleAfter = 3 * time.Minute
	// NoSignalAfter is when the venue is treated as having gone silent.
	NoSignalAfter = 15 * time.Minute
)

// Freshness classifies the age of the last good frame relative to server
// time. The server clock is canonical; device clocks are advisory (§1).
type Freshness int

const (
	// Unknown means no frame has ever been stored (or the venue is new).
	Unknown Freshness = iota
	// Fresh means a good render arrived within StaleAfter.
	Fresh
	// Late means the last good render is older than StaleAfter but within
	// NoSignalAfter: the venue is running behind, not silent.
	Late
	// Stale means the last good render is older than NoSignalAfter — the
	// camera has stopped sending. A failure never erases identity: last-good
	// stays and the page says so (§1).
	Stale
)

func (f Freshness) String() string {
	switch f {
	case Fresh:
		return "fresh"
	case Late:
		return "late"
	case Stale:
		return "stale"
	default:
		return "unknown"
	}
}

// Classify maps a last-frame timestamp to a freshness class. A zero
// lastFrameAt (never stored) is Unknown.
func Classify(lastFrameAt, now time.Time) Freshness {
	if lastFrameAt.IsZero() {
		return Unknown
	}
	switch {
	case now.Sub(lastFrameAt) > NoSignalAfter:
		return Stale
	case now.Sub(lastFrameAt) > StaleAfter:
		return Late
	default:
		return Fresh
	}
}

// Range is one opening interval on one weekday, in minutes from local
// midnight. Close is exclusive and may be 1440 (24:00).
type Range struct {
	OpenMin  int
	CloseMin int
}

// Hours is a weekly schedule. Index 0 is time.Sunday (Go convention), so a
// day row from SQLite maps straight in. Empty means "no hours configured",
// which the public page renders as no open/closed badge at all.
type Hours [7][]Range

// Configured reports whether any range exists.
func (h Hours) Configured() bool {
	for _, rs := range h {
		if len(rs) > 0 {
			return true
		}
	}
	return false
}

// OpenAt reports whether the venue is open at t (local schedule, i.e. the
// time's own location is what defines "local").
func (h Hours) OpenAt(t time.Time) bool {
	for _, r := range h[int(t.Weekday())] {
		m := t.Hour()*60 + t.Minute()
		if m >= r.OpenMin && m < r.CloseMin {
			return true
		}
	}
	return false
}

// OpenClosed is a tri-state for pages that must distinguish "closed right
// now" from "hours not set".
type OpenClosed int

const (
	NoHours OpenClosed = iota
	Open
	Closed
)

func (o OpenClosed) String() string {
	switch o {
	case Open:
		return "open"
	case Closed:
		return "closed"
	default:
		return "no-hours"
	}
}

// ClassifyOpen reports open/closed at t, honouring unconfigured hours.
func (h Hours) ClassifyOpen(t time.Time) OpenClosed {
	if !h.Configured() {
		return NoHours
	}
	if h.OpenAt(t) {
		return Open
	}
	return Closed
}

// Spec renders Hours back into the compact spec form, so edit forms (owner
// panel, super-admin) can prefill the current schedule instead of showing a
// blank field that would clear it on save. Consecutive days (Mo-first order)
// sharing identical ranges collapse into one group: "Mo-Fr 09:00-17:00".
// Unconfigured hours render as "".
func (h Hours) Spec() string {
	dayName := []string{"Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"}
	// Spec order is Mo-first; Go weekday index is Su-first.
	order := []int{1, 2, 3, 4, 5, 6, 0}
	type group struct {
		first, last int // positions in order[]
		rs          []Range
	}
	var groups []group
	for pos, d := range order {
		rs := h[d]
		if len(rs) == 0 {
			continue
		}
		if n := len(groups); n > 0 && slices.Equal(groups[n-1].rs, rs) &&
			groups[n-1].last+1 == pos {
			groups[n-1].last = pos
			continue
		}
		groups = append(groups, group{first: pos, last: pos, rs: rs})
	}
	// One range per group: the spec grammar is "<days> <one range>", so a
	// day with several ranges (only hand-built Hours can hold those — the
	// parser accepts one range per group) emits one group per range. Every
	// Spec() output re-parses.
	var b strings.Builder
	sep := ""
	for _, g := range groups {
		days := dayName[order[g.first]]
		if g.first != g.last {
			days += "-" + dayName[order[g.last]]
		}
		for _, r := range g.rs {
			b.WriteString(sep + days)
			fmt.Fprintf(&b, " %02d:%02d-%02d:%02d",
				r.OpenMin/60, r.OpenMin%60, r.CloseMin/60, r.CloseMin%60)
			sep = ";"
		}
	}
	return b.String()
}

// --- spec parsing ---------------------------------------------------------
//
// Provisioning and the owner/super-admin edit forms both speak the compact spec:
//
//	"Mo-Fr 09:00-17:00;Sa 10:00-14:00"    two groups, ;-separated
//	"Mo-Su 00:00-24:00"                   always open
//	""                                    no hours
//
// Day names: Mo Tu We Th Fr Sa Su (case-insensitive); ranges are inclusive.

var dayIndex = map[string]int{
	"su": 0, "mo": 1, "tu": 2, "we": 3, "th": 4, "fr": 5, "sa": 6,
}

// ParseSpec parses a compact hours spec into Hours. Empty spec yields
// unconfigured Hours and no error.
func ParseSpec(spec string) (Hours, error) {
	var h Hours
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return h, nil
	}
	for _, group := range strings.Split(spec, ";") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		// Split "Mo-Fr 09:00-17:00" into day part and time part.
		fields := strings.Fields(group)
		if len(fields) != 2 {
			return h, fmt.Errorf("hours group %q: want \"<days> <HH:MM-HH:MM>\"", group)
		}
		days, err := parseDays(fields[0])
		if err != nil {
			return h, err
		}
		r, err := parseRange(fields[1])
		if err != nil {
			return h, err
		}
		for _, d := range days {
			h[d] = append(h[d], r)
		}
	}
	// Keep each day sorted so ClassifyOpen and stored rows are canonical.
	for d := range h {
		rs := h[d]
		for i := 1; i < len(rs); i++ {
			for j := i; j > 0 && rs[j].OpenMin < rs[j-1].OpenMin; j-- {
				rs[j], rs[j-1] = rs[j-1], rs[j]
			}
		}
		h[d] = rs
	}
	return h, nil
}

func parseDays(s string) ([]int, error) {
	if strings.Contains(s, "-") {
		a, b, ok := strings.Cut(s, "-")
		from, err1 := parseDay(a)
		to, err2 := parseDay(b)
		if !ok || err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad day range %q", s)
		}
		var out []int
		// Inclusive range walking forward through the week.
		for d := from; ; d = (d + 1) % 7 {
			out = append(out, d)
			if d == to {
				break
			}
			if len(out) > 7 {
				return nil, fmt.Errorf("bad day range %q", s)
			}
		}
		return out, nil
	}
	d, err := parseDay(s)
	if err != nil {
		return nil, err
	}
	return []int{d}, nil
}

func parseDay(s string) (int, error) {
	d, ok := dayIndex[strings.ToLower(strings.TrimSpace(s))]
	if !ok {
		return 0, fmt.Errorf("unknown day %q", s)
	}
	return d, nil
}

func parseRange(s string) (Range, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return Range{}, fmt.Errorf("bad time range %q", s)
	}
	open, err := parseHHMM(a)
	if err != nil {
		return Range{}, fmt.Errorf("bad time %q: %w", a, err)
	}
	closeMin, err := parseHHMM(b)
	if err != nil {
		return Range{}, fmt.Errorf("bad time %q: %w", b, err)
	}
	if closeMin == 0 {
		closeMin = 1440 // 24:00 written as 00:00 is meaningless here; reject below instead
		return Range{}, fmt.Errorf("bad time %q: closing time must not be 00:00", b)
	}
	if open >= closeMin {
		return Range{}, fmt.Errorf("range %q: open must be before close (no midnight crossing)", s)
	}
	return Range{OpenMin: open, CloseMin: closeMin}, nil
}

func parseHHMM(s string) (int, error) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, fmt.Errorf("want HH:MM (zero-padded)")
	}
	h, err := strconv.Atoi(hh)
	if err != nil || h < 0 || h > 24 {
		return 0, fmt.Errorf("bad hour")
	}
	m, err := strconv.Atoi(mm)
	if err != nil || m < 0 || m > 59 {
		return 0, fmt.Errorf("bad minute")
	}
	if h == 24 && m != 0 {
		return 0, fmt.Errorf("bad hour")
	}
	return h*60 + m, nil
}

// NextClose returns the end of the opening interval containing t. False when
// the venue is not open at t, or when the day has no closing boundary (an
// interval running to 24:00). The public page uses it for "Otvoreno do {time}"
// (DESIGN.md §11) rather than saying "open", which claims a duration it
// cannot know.
func (h Hours) NextClose(t time.Time) (time.Time, bool) {
	if !h.OpenAt(t) {
		return time.Time{}, false
	}
	mins := t.Hour()*60 + t.Minute()
	for _, r := range h[int(t.Weekday())] {
		if mins >= r.OpenMin && mins < r.CloseMin {
			end := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).
				Add(time.Duration(r.CloseMin) * time.Minute)
			if r.CloseMin >= 1440 {
				return time.Time{}, false // open past midnight: no closing time
			}
			return end, true
		}
	}
	return time.Time{}, false
}

// NextOpen returns the next moment the venue opens, searching forward up to a
// week. False when no hours are configured — the page must then say nothing
// at all about opening rather than guess.
func (h Hours) NextOpen(t time.Time) (time.Time, bool) {
	if !h.Configured() {
		return time.Time{}, false
	}
	base := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	mins := t.Hour()*60 + t.Minute()
	for d := 0; d < 8; d++ {
		day := base.AddDate(0, 0, d)
		for _, r := range h[int(day.Weekday())] {
			if d == 0 && r.OpenMin <= mins {
				continue // already passed today
			}
			if r.CloseMin > r.OpenMin {
				return day.Add(time.Duration(r.OpenMin) * time.Minute), true
			}
		}
	}
	return time.Time{}, false
}

// DayForm is one row of the hours editor (DESIGN.md §9): two intervals
// (split shifts), a closed checkbox, and the weekday index in Go convention
// (Sunday-first) so the handler never has to think about display order.
type DayForm struct {
	// Dow is time.Sunday..time.Saturday as int, matching Hours indexing.
	Dow    int
	Name   string // display name from the catalog (nominative, not accusative)
	Open1  string // "09:00" or ""
	Close1 string
	Open2  string
	Close2 string
	Closed bool
}

// EditModel renders Hours as seven Mo-first rows for the editor. Days with
// no ranges come back unchecked and un-closed — the editor, not this
// function, decides what an untouched row means.
func EditModel(h Hours, names [7]string) [7]DayForm {
	// Display order is Mo-first; Go weekday index is Su-first.
	order := []int{1, 2, 3, 4, 5, 6, 0}
	var out [7]DayForm
	for i, dow := range order {
		d := DayForm{Dow: dow, Name: names[i]}
		rs := h[dow]
		if len(rs) == 0 {
			d.Closed = true
		} else {
			d.Open1, d.Close1 = mins(rs[0].OpenMin), mins(rs[0].CloseMin)
			if len(rs) > 1 {
				d.Open2, d.Close2 = mins(rs[1].OpenMin), mins(rs[1].CloseMin)
			}
		}
		out[i] = d
	}
	return out
}

func mins(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }

// ParseHoursForm reads the editor's fields back into Hours. Field names are
// h<d>_open1, h<d>_close1, h<d>_open2, h<d>_close2, h<d>_closed with d = 0..6
// Mo-first. copyMon copies Monday's intervals onto Tue–Fri before parsing
// (the "copy Monday to weekdays" button). Errors name the day, because
// "Bad hours: group 3" helps nobody behind a bar.
//
// An untouched row (no times, unchecked) is an error naming the day —
// "closed" is a checkbox, not an empty field, so ambiguity is refused rather
// than guessed.
// HoursError is a structured hours-form failure: the day (Mo-first index
// and display name) plus a code the caller localizes. Error STRINGS are not
// built here, because the owner panel is Croatian and the console is English
// — half-localized sentences are worse than consistent ones.
type HoursError struct {
	Day     int    // Mo-first day index
	DayName string // display name, already localized by the caller
	Code    string // "clock" | "backwards" | "closed_times" | "untouched"
	Second  bool   // the second (split-shift) interval
	Open    string // for "backwards"
	Close   string
}

func ParseHoursForm(get func(string) string, names [7]string, copyMon bool) (Hours, *HoursError) {
	var noErr *HoursError
	type iv struct{ open, close string }
	rows := make([][2]iv, 7)
	closed := make([]bool, 7)
	for d := 0; d < 7; d++ {
		p := fmt.Sprintf("h%d_", d)
		rows[d][0] = iv{strings.TrimSpace(get(p + "open1")), strings.TrimSpace(get(p + "close1"))}
		rows[d][1] = iv{strings.TrimSpace(get(p + "open2")), strings.TrimSpace(get(p + "close2"))}
		closed[d] = get(p+"closed") == "1"
	}
	if copyMon {
		for d := 1; d < 5; d++ {
			rows[d] = rows[0]
			closed[d] = closed[0]
		}
	}
	var h Hours
	order := []int{1, 2, 3, 4, 5, 6, 0}
	for i, dow := range order {
		name := names[i]
		if closed[i] {
			if rows[i][0].open != "" || rows[i][0].close != "" || rows[i][1].open != "" || rows[i][1].close != "" {
				return h, &HoursError{Day: i, DayName: name, Code: "closed_times"}
			}
			continue
		}
		var rs []Range
		for k, v := range rows[i] {
			if v.open == "" && v.close == "" {
				continue
			}
			o, ok1 := parseClock(v.open)
			c, ok2 := parseClock(v.close)
			if !ok1 || !ok2 {
				return h, &HoursError{Day: i, DayName: name, Code: "clock", Second: k == 1}
			}
			if c <= o {
				return h, &HoursError{Day: i, DayName: name, Code: "backwards", Second: k == 1, Open: v.open, Close: v.close}
			}
			rs = append(rs, Range{OpenMin: o, CloseMin: c})
		}
		if len(rs) == 0 {
			return h, &HoursError{Day: i, DayName: name, Code: "untouched"}
		}
		h[dow] = rs
	}
	return h, noErr
}

// parseClock parses "HH:MM" in 24 h time. 24:00 is not accepted here — the
// editor writes closing times explicitly, and "24:00" in a time input is a
// typo for "00:00" next day, which same-day ranges cannot express.
func parseClock(s string) (int, bool) {
	var hh, mm int
	if _, err := fmt.Sscanf(s, "%d:%d", &hh, &mm); err != nil {
		return 0, false
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	return hh*60 + mm, true
}
