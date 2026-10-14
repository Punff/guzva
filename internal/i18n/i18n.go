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

	"guzva/internal/venue"
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
	// names (en). daysNom is nominative, Mo-first, for editor labels.
	weekdays   [7]string
	weekdaysEn [7]string
	daysNom    [7]string
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

		"hours.title":  "Radno vrijeme",
		"hours.closed": "Zatvoreno",

		// Hours editor v2 (design-pass2 §2): every input labeled.
		"hours.first":    "1. smjena",
		"hours.second":   "2. smjena",
		"hours.optional": "neobavezno",
		"hours.open":     "Otvara",
		"hours.close":    "Zatvara",

		"when.yesterday": "jučer",
		"live.newpic":    "Nova slika.",

		"venue.about": "O salonu",

		"owner.about": "Opis (javno)",
		"owner.phone": "Telefon (javno)",

		"owner.save":       "Spremi promjene",
		"owner.saved":      "Promjene su spremljene",
		"owner.health.old": "Slika je stara — što učiniti?",
		// The owner dash's one-sentence summary (§9.4): the thing the owner
		// opens the panel for, in their venue's language.
		"owner.fresh": "Slika je svježa — zadnja je objavljena u {time}.",
		"owner.late":  "Slika kasni — zadnja je objavljena u {time}.",
		"owner.stale": "Slika je stara — zadnja je objavljena u {time}. Provjeri kameru i internet.",

		// Owner console chrome (§9.4). One catalog, so an English venue
		// gets an English panel — never a mix.
		"owner.public":            "Javna stranica",
		"owner.details":           "Podaci o salonu",
		"owner.name":              "Naziv",
		"owner.tz":                "Vremenska zona",
		"owner.style":             "Stil slike",
		"owner.health":            "Zdravlje uređaja",
		"owner.serial":            "Serijski broj",
		"owner.state":             "Stanje",
		"owner.last":              "Zadnja slika",
		"owner.signal":            "Signal",
		"owner.no_devices":        "Još nema uređaja — administrator će dodati jedan.",
		"owner.step1":             "Provjeri je li kamera uključena u struju.",
		"owner.step2":             "Provjeri radi li WiFi u salonu.",
		"owner.step3":             "Ako ništa ne pomogne, javi se administratoru.",
		"owner.location":          "Lokacija (javno)",
		"owner.about.placeholder": "Jedna rečenica za goste koji skeniraju QR kod",

		"owner.login.title":        "prijava",
		"owner.login.tag":          "Prijava vlasnika objekta.",
		"owner.login.venue":        "Objekat",
		"owner.login.password":     "Lozinka",
		"owner.login.submit":       "Prijava",
		"owner.login.note":         "Pristup izdaje administrator. Tokeni uređaja i postavke kamere ostaju kod njega — ovaj panel pokriva podatke o objektu i stanje.",
		"owner.login.err.throttle": "Previše pokušaja — pričekajte minutu.",
		"owner.login.err.badcreds": "Prijava nije uspjela.",

		"hours.err.clock":        "{day}{shift}: upiši vrijeme kao 09:00",
		"hours.err.backwards":    "{day}{shift}: zatvara u {close}, a otvara u {open}",
		"hours.err.closed_times": "{day}: označeno je zatvoreno, a ima upisano vrijeme — obriši jedno",
		"hours.err.untouched":    "{day}: upiši vrijeme ili označi zatvoreno",
		"hours.err.second":       " (druga smjena)",
		"hours.copy":             "Kopiraj ponedjeljak na radne dane",
		"hours.splitwarn":        "Druga smjena %s se ovdje neuređuje — spremanje zadržava samo prvi interval.",
	},
	weekdays: [7]string{
		"ponedjeljak", "utorak", "srijedu", "četvrtak", "petak", "subotu", "nedjelju",
	},
	// Nominative, Mo-first, for editor labels. The accusative above is only
	// correct after "u" — a label reading "u srijedu" where a name belongs
	// would be wrong in the other direction.
	daysNom: [7]string{
		"Ponedjeljak", "Utorak", "Srijeda", "Četvrtak", "Petak", "Subota", "Nedjelja",
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

		"hours.title":  "Opening hours",
		"hours.closed": "Closed",

		// Hours editor v2 (design-pass2 §2): every input labeled.
		"hours.first":    "First shift",
		"hours.second":   "Second shift",
		"hours.optional": "optional",
		"hours.open":     "Opens",
		"hours.close":    "Closes",

		"when.yesterday": "yesterday",
		"live.newpic":    "New picture.",

		"venue.about": "About",

		"owner.about": "Description (public)",
		"owner.phone": "Phone (public)",

		"owner.save":       "Save changes",
		"owner.saved":      "Changes saved",
		"owner.health.old": "The picture is old — what to do?",
		"owner.fresh":      "The picture is fresh — the last one was published at {time}.",
		"owner.late":       "The picture is late — the last one was published at {time}.",
		"owner.stale":      "The picture is old — the last one was published at {time}. Check the camera and the internet.",

		"owner.public":            "Public page",
		"owner.details":           "Venue details",
		"owner.name":              "Name",
		"owner.tz":                "Time zone",
		"owner.style":             "Picture style",
		"owner.health":            "Device health",
		"owner.serial":            "Serial",
		"owner.state":             "State",
		"owner.last":              "Last picture",
		"owner.signal":            "Signal",
		"owner.no_devices":        "No device yet — the administrator will add one.",
		"owner.step1":             "Check that the camera is plugged in.",
		"owner.step2":             "Check that the venue WiFi works.",
		"owner.step3":             "If nothing helps, contact the administrator.",
		"owner.location":          "Location (public)",
		"owner.about.placeholder": "One sentence for guests scanning the QR code",

		"owner.login.title":        "sign in",
		"owner.login.tag":          "Venue owner sign-in.",
		"owner.login.venue":        "Venue",
		"owner.login.password":     "Password",
		"owner.login.submit":       "Sign in",
		"owner.login.note":         "Access is issued by the administrator. Device tokens and camera settings stay with them — this panel covers venue details and status.",
		"owner.login.err.throttle": "Too many attempts — wait a minute.",
		"owner.login.err.badcreds": "Sign-in failed.",

		"hours.err.clock":        "{day}{shift}: use HH:MM times like 09:00",
		"hours.err.backwards":    "{day}{shift}: closes {close} before it opens at {open}",
		"hours.err.closed_times": "{day}: marked closed but has times — clear one or the other",
		"hours.err.untouched":    "{day}: set hours or mark it closed",
		"hours.err.second":       " (second shift)",
		"hours.copy":             "Copy Monday to weekdays",
		"hours.splitwarn":        "Second interval %s is not editable here — saving keeps only the first interval.",
	},
	weekdays:   [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	weekdaysEn: [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
	daysNom:    [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
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

// DayNames returns the seven editor labels, Mo-first.
func (c *Catalog) DayNames() [7]string { return c.daysNom }

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
		return c.Get("when.yesterday")
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

// HoursErrorText renders a structured hours-form failure in this catalog's
// language. The day name arrived already localized; only the sentence around
// it is translated here. F() substitutes in placeholder order, so the call
// argument order must match each message's {…} order.
func (c *Catalog) HoursErrorText(e *venue.HoursError) string {
	if e == nil {
		return ""
	}
	shift := ""
	if e.Second {
		shift = c.Get("hours.err.second")
	}
	switch e.Code {
	case "clock":
		return c.F("hours.err.clock", e.DayName, shift)
	case "backwards":
		return c.F("hours.err.backwards", e.DayName, shift, e.Close, e.Open)
	case "closed_times":
		return c.F("hours.err.closed_times", e.DayName)
	default:
		return c.F("hours.err.untouched", e.DayName)
	}
}
