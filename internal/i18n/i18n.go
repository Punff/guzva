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
		"venue.call":  "Nazovi",
		"watch.one":   "{n} osoba upravo gleda",
		"watch.few":   "{n} osobe upravo gledaju",
		"watch.many":  "{n} osoba upravo gleda",

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
		"owner.kpi.views":         "Pregledi",
		"owner.kpi.visitors":      "Posjetitelji",
		"owner.kpi.minutes":       "Minute gledanja",
		"owner.kpi.qr":            "QR skeniranja",
		"owner.kpi.window":        "Zadnjih 7 dana",
		"owner.trend.up":          "+{n} % od prošlog tjedna",
		"owner.trend.down":        "−{n} % od prošlog tjedna",
		"owner.trend.flat":        "Isto kao prošli tjedan",
		"owner.trend.new":         "Novo ovaj tjedan",
		"owner.live":              "Upravo gleda: {n}",
		"owner.chart.days":        "Pregledi po danu",
		"owner.chart.days.note":   "Zadnjih 14 dana; danas je istaknuto.",
		"owner.chart.daytip":      "{date}: {views} pregleda, {visitors} posjetitelja",
		"owner.chart.hours":       "Kada gosti gledaju",
		"owner.chart.hours.note":  "Pregledi po satu u danu, zadnjih 28 dana.",
		"owner.chart.hourtip":     "{h} h: {n} pregleda",
		"owner.peak":              "Najviše gledaju između {from} i {to} h.",
		"owner.insights":          "Savjeti",
		"owner.insight.empty":     "Još nema posjeta. Podijeli link na Instagramu, Google profilu ili WhatsAppu — gosti vide gužvu prije nego što dođu.",
		"owner.insight.qr.none":   "Ovaj tjedan nitko nije skenirao QR kod. Ispiši plakat i stavi ga na ulaz ili izlog.",
		"owner.insight.qr":        "{n} gostiju je ovaj tjedan skeniralo QR kod.",
		"owner.insight.best":      "Najviše pregleda ovaj tjedan: {day} ({n}).",
		"owner.privacy":           "Brojimo samo posjete — bez kolačića i bez spremanja IP adresa.",
		"owner.share":             "Podijeli stranicu",
		"owner.share.note":        "Gosti skeniraju kod i odmah vide ima li gužve.",
		"owner.share.copy":        "Kopiraj link",
		"owner.share.copied":      "Kopirano",
		"owner.share.poster":      "Ispiši plakat",
		"owner.camera":            "Kamera",
		"owner.uptime":            "Radila je {n} % vremena u zadnja 24 sata.",
		"owner.settings":          "Podaci i radno vrijeme",
		"owner.settings.note":     "Naziv, opis, kontakt i radno vrijeme koje gosti vide.",
		"owner.signout":           "Odjava",
		"owner.device.last":       "javila se u {time}",
		"owner.device.never":      "još se nije javila",
		"owner.device.off":        "isključena (administrator)",
		"owner.signal.ok":         "signal dobar ({dbm} dBm)",
		"owner.signal.weak":       "signal slab ({dbm} dBm) — kamera je predaleko od WiFi-ja",
		"owner.footer":            "guzva — gosti vide stiliziranu sliku, nikad fotografiju. Pitanja? Javi se administratoru.",
		"poster.cta":              "Skeniraj i pogledaj uživo",
		"poster.note":             "Stilizirana slika prostora, osvježava se svake minute. Bez lica, ništa se ne snima.",
		"poster.print":            "Ispiši",
		"poster.back":             "Natrag",
		"poster.hint":             "Savjet: ispiši na A5 ili A4 i stavi na ulaz, izlog ili šank.",

		"owner.login.title":        "prijava",
		"owner.login.tag":          "Prijava vlasnika objekta.",
		"owner.login.venue":        "Objekat",
		"owner.login.password":     "Lozinka",
		"owner.login.submit":       "Prijava",
		"owner.login.note":         "Pristup daje administrator. Ovdje vidiš koliko gostiju gleda tvoju stranicu, dijeliš link i QR kod i uređuješ podatke i radno vrijeme.",
		"owner.login.venue.hint":   "Adresa tvoje stranice — npr. jadro za /jadro/",
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
		"venue.call":  "Call",
		"watch.one":   "{n} person watching now",
		"watch.few":   "{n} people watching now",
		"watch.many":  "{n} people watching now",

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
		"owner.kpi.views":         "Views",
		"owner.kpi.visitors":      "Visitors",
		"owner.kpi.minutes":       "Minutes watched",
		"owner.kpi.qr":            "QR scans",
		"owner.kpi.window":        "Last 7 days",
		"owner.trend.up":          "+{n}% vs last week",
		"owner.trend.down":        "−{n}% vs last week",
		"owner.trend.flat":        "Same as last week",
		"owner.trend.new":         "New this week",
		"owner.live":              "Watching now: {n}",
		"owner.chart.days":        "Views per day",
		"owner.chart.days.note":   "Last 14 days; today is highlighted.",
		"owner.chart.daytip":      "{date}: {views} views, {visitors} visitors",
		"owner.chart.hours":       "When guests look",
		"owner.chart.hours.note":  "Views by hour of day, last 28 days.",
		"owner.chart.hourtip":     "{h}:00: {n} views",
		"owner.peak":              "Most views between {from}:00 and {to}:00.",
		"owner.insights":          "Tips",
		"owner.insight.empty":     "No visits yet. Share the link on Instagram, your Google profile or WhatsApp — guests see how busy you are before they come.",
		"owner.insight.qr.none":   "Nobody scanned the QR code this week. Print the poster and put it at the door or in the window.",
		"owner.insight.qr":        "{n} guests scanned the QR code this week.",
		"owner.insight.best":      "Most views this week: {day} ({n}).",
		"owner.privacy":           "We only count visits — no cookies, no IP addresses stored.",
		"owner.share":             "Share your page",
		"owner.share.note":        "Guests scan the code and see right away whether it's busy.",
		"owner.share.copy":        "Copy link",
		"owner.share.copied":      "Copied",
		"owner.share.poster":      "Print poster",
		"owner.camera":            "Camera",
		"owner.uptime":            "Online {n}% of the last 24 hours.",
		"owner.settings":          "Details and opening hours",
		"owner.settings.note":     "Name, description, contact and the hours guests see.",
		"owner.signout":           "Sign out",
		"owner.device.last":       "last checked in at {time}",
		"owner.device.never":      "not connected yet",
		"owner.device.off":        "disabled (administrator)",
		"owner.signal.ok":         "good signal ({dbm} dBm)",
		"owner.signal.weak":       "weak signal ({dbm} dBm) — the camera is too far from the WiFi",
		"owner.footer":            "guzva — guests see a stylised picture, never a photo. Questions? Contact your administrator.",
		"poster.cta":              "Scan to see it live",
		"poster.note":             "A stylised picture of the room, refreshed every minute. No faces, nothing recorded.",
		"poster.print":            "Print",
		"poster.back":             "Back",
		"poster.hint":             "Tip: print on A5 or A4 and put it at the door, in the window or on the bar.",

		"owner.login.title":        "sign in",
		"owner.login.tag":          "Venue owner sign-in.",
		"owner.login.venue":        "Venue",
		"owner.login.password":     "Password",
		"owner.login.submit":       "Sign in",
		"owner.login.note":         "Access comes from your administrator. Here you see how many guests view your page, share the link and QR code, and edit your details and hours.",
		"owner.login.venue.hint":   "Your page address — e.g. jadro for /jadro/",
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
