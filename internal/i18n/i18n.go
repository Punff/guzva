// Package i18n is the single place user-facing copy lives (DESIGN.md §11,
// PLAN D7). Templates and handlers reference keys, never literal strings, so
// the Croatian review in §16 happens against one file rather than by grepping
// templates.
//
// Croatian is the default because the product is for Bosnia; English is a
// fallback, not a second product. bs/sr are one file each when a venue needs
// them; Cyrillic is out of scope for v1.
//
// Two notes the catalog encodes deliberately (§11):
//   - Weekdays after "u" take the accusative: u ponedjeljak, u utorak…
//   - No relative-time sentences ("prije 5 minuta") — they need three plural
//     forms and a case. Absolute times plus "min"/"h" abbreviations instead.
package i18n

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Lang identifies a catalog. Unknown/absent means the default.
type Lang string

const (
	// HR is the product default (Bosnia, D7).
	HR Lang = "hr"
	// EN is the fallback for tourist venues (DESIGN.md §11).
	EN Lang = "en"
)

// Normalize maps a stored venue value to a supported catalog, defaulting to HR.
func Normalize(s string) Lang {
	switch Lang(strings.ToLower(strings.TrimSpace(s))) {
	case EN:
		return EN
	default:
		return HR
	}
}

// Catalog is the message set for one language.
type Catalog struct {
	Lang Lang
	msg  map[string]string
	// weekdays are the accusative forms used after "u" (hr) and the plain
	// names (en).
	weekdays   [7]string
	weekdaysEn [7]string
}

var hr = &Catalog{
	Lang: HR,
	msg: map[string]string{
		"tagline": "Ima li gužve?",

		"state.fresh":    "Svježa slika",
		"state.late":     "Slika kasni",
		"state.nosignal": "Nema nove slike",
		"state.closed":   "Zatvoreno",
		"state.paused":   "Na pauzi",
		"state.waiting":  "Čekamo prvu sliku…",

		"open.until":    "Otvoreno do {time}",
		"open.now":      "Otvoreno",
		"closed.opens":  "Otvara se {when} u {time}",
		"when.today":    "danas",
		"when.tomorrow": "sutra",

		"late.detail":  "Zadnja slika je iz {time}.",
		"stale.detail": "Zadnja slika je iz {time}. Ne mora prikazivati trenutno stanje.",
		"net.offline":  "Nema internetske veze. Pokušavamo ponovno…",
		"idle.paused":  "Osvježavanje je pauzirano.",
		"idle.resume":  "Nastavi",

		"alt.still":    "Stilizirana slika prostora {venue}, snimljena u {time}",
		"alt.nowframe": "Stilizirana slika prostora {venue}",

		"privacy.line": "Slika je namjerno grubo stilizirana radi privatnosti. Izvorne fotografije se ne pohranjuju.",
		"privacy.note": "Stylised render — the raw camera image is never stored or published.",

		"hours.title":  "Radno vrijeme",
		"hours.closed": "Zatvoreno",

		"owner.save":       "Spremi promjene",
		"owner.saved":      "Promjene su spremljene",
		"owner.health.old": "Slika je stara — što učiniti?",
	},
	weekdays: [7]string{
		"ponedjeljak", "utorak", "srijedu", "četvrtak", "petak", "subotu", "nedjelju",
	},
	weekdaysEn: [7]string{
		"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday",
	},
}

var en = &Catalog{
	Lang: EN,
	msg: map[string]string{
		"tagline": "Is it busy?",

		"state.fresh":    "Fresh picture",
		"state.late":     "Picture is late",
		"state.nosignal": "No new picture",
		"state.closed":   "Closed",
		"state.paused":   "On a break",
		"state.waiting":  "Waiting for the first picture…",

		"open.until":    "Open until {time}",
		"open.now":      "Open",
		"closed.opens":  "Opens {when} at {time}",
		"when.today":    "today",
		"when.tomorrow": "tomorrow",

		"late.detail":  "The last picture is from {time}.",
		"stale.detail": "The last picture is from {time}. It may not show the current situation.",
		"net.offline":  "No internet connection. Trying again…",
		"idle.paused":  "Refreshing is paused.",
		"idle.resume":  "Resume",

		"alt.still":    "Stylised view of {venue}, taken at {time}",
		"alt.nowframe": "Stylised view of {venue}",

		"privacy.line": "The picture is deliberately coarsely stylised for privacy. The original photos are not stored.",
		"privacy.note": "Stylised render — the raw camera image is never stored or published.",

		"hours.title":  "Opening hours",
		"hours.closed": "Closed",

		"owner.save":       "Save changes",
		"owner.saved":      "Changes saved",
		"owner.health.old": "The picture is old — what to do?",
	},
	weekdays:   [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	weekdaysEn: [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
}

// For returns the catalog for a language, defaulting to Croatian.
func For(l Lang) *Catalog {
	if l == EN {
		return en
	}
	return hr
}

// Get returns the message for key, falling back to Croatian and then to the
// key itself — a missing string must never render as an empty gap.
func (c *Catalog) Get(key string) string {
	if m, ok := c.msg[key]; ok {
		return m
	}
	if c != hr {
		if m, ok := hr.msg[key]; ok {
			return m
		}
	}
	return key
}

// F is Get with {placeholder} substitution. Values are formatted in order of
// appearance, so a translator can reorder them.
func (c *Catalog) F(key string, args ...any) string {
	return subst(c.Get(key), args...)
}

// subst replaces each {…} in order. A malformed template is left alone rather
// than rendered as a broken sentence.
func subst(s string, args ...any) string {
	if len(args) == 0 {
		return s
	}
	var b strings.Builder
	ai := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			if j := strings.IndexByte(s[i:], '}'); j > 0 {
				b.WriteString(fmt.Sprint(args[ai]))
				ai++
				i += j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Weekday returns the weekday name for a time in the given location.
// Croatian uses the accusative, because the copy puts it after "u".
func (c *Catalog) Weekday(t time.Time) string {
	wd := int(t.Weekday())
	if c == en {
		return en.weekdaysEn[wd]
	}
	return hr.weekdays[wd]
}

// Time renders a venue-local 24-hour clock time.
func (c *Catalog) Time(t time.Time) string {
	return t.Format("15:04")
}

// DayName is the headline's day word: danas/jučer, or a date (DESIGN.md §7.3).
//
// Compares calendar days by rounding, not by YearDay: the day number wraps at
// the new year, so 31 Dec → 1 Jan looked like a 364-day gap and rendered a
// date instead of "jučer". Rounding also absorbs the 23h/25h days a DST change
// makes, which Europe/Sarajevo has twice a year.
func (c *Catalog) DayName(then, now time.Time) string {
	switch daysApart(then, now) {
	case 0:
		return c.Get("when.today")
	case 1:
		if c == en {
			return "yesterday"
		}
		return "jučer"
	default:
		return then.Format("02.01.2006.")
	}
}

// daysApart is the number of calendar days from then to now in their own
// locations. Negative when then is in the future.
func daysApart(then, now time.Time) int {
	mid := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}
	return int(math.Round(mid(now).Sub(mid(then)).Hours() / 24))
}
