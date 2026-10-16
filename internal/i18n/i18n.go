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

		// The /privatnost/ policy (PLAN §13). Formal register by user ruling
		// (16 Oct 2026); every sentence is still a fact the code enforces.
		// No controller identity/contact yet — owed before the pilot.
		// Homepage, 404 and the shared footer (D12). Plain words, no slogans.
		"home.title":      "Ima li gužve?",
		"home.lead":       "Pogledaj sliku iz salona prije nego kreneš. Slika se osvježava svake minute.",
		"home.list":       "Saloni",
		"home.empty":      "Još nema salona na listi.",
		"home.cta":        "Pogledaj salone",
		"nav.label":       "Glavni izbornik",
		"nav.venues":      "Saloni",
		"nav.about":       "O nama",
		"nav.privacy":     "Privatnost",
		"nav.contact":     "Kontakt",
		"about.title":     "O nama",
		"about.p1":        "Guzva.ba pokazuje koliko je trenutno ljudi u salonu, brijačnici ili sličnom mjestu, da ne moraš zvati ni čekati u redu.",
		"about.p2":        "Salon dobije malu kameru i svoju stranicu na guzva.ba. Gosti je otvore preko linka ili QR koda na vratima.",
		"how.title":       "Kako radi",
		"how.1":           "Kamera u salonu jednom u minuti napravi sliku.",
		"how.2":           "Slika se odmah pretvori u grubi crtež u nekoliko tonova, bez lica i sitnih detalja.",
		"how.3":           "Na mobitelu vidiš koliko je puno prije nego kreneš.",
		"privsec.title":   "Privatnost",
		"privsec.lead":    "Napravljeno je tako da gosti vide koliko je puno, a ne tko je unutra.",
		"privsec.1.h":     "Bez lica",
		"privsec.2.h":     "Bez spremanja fotografija",
		"privsec.3.h":     "Bez praćenja",
		"privsec.1":       "Slika je gruba i svedena na nekoliko tonova, pa se lica ne mogu prepoznati.",
		"privsec.2":       "Fotografija iz kamere postoji samo dok se ne pretvori u crtež. Nigdje se ne sprema.",
		"privsec.3":       "Stranica nema kolačića, oglasa ni alata za praćenje.",
		"privsec.more":    "Pročitaj politiku privatnosti",
		"contact.title":   "Kontakt",
		"contact.owners":  "Imaš salon ili brijačnicu? Postavimo kameru, a tvoji gosti prije dolaska vide koliko je puno.",
		"contact.button":  "Javi nam se",
		"contact.subject": "Guzva u mom salonu",
		"contact.none":    "Kontakt podaci uskoro.",
		"footer.line":     "Slika iz salona, uživo.",
		"footer.pages":    "Stranice",
		"footer.owners":   "Za vlasnike",
		"home.nopic":      "Slika se ne prikazuje",
		"home.biz.owner":  "Već imaš kameru?",
		"home.biz.login":  "Prijava za vlasnike",
		"nf.title":        "Ova stranica ne postoji",
		"nf.p":            "Možda je link pogrešan ili salon više nije na listi.",
		"nf.home":         "Na naslovnu",
		"privacy.link":    "Politika privatnosti",
		"privacy.title":   "Politika privatnosti",
		"privacy.updated": "Posljednja izmjena: 16. 10. 2026.",
		"privacy.intro":   "Ova politika opisuje kako usluga guzva.ba obrađuje podatke prilikom javnog prikaza popunjenosti objekata.",
		"privacy.s1.h":    "1. Opseg",
		"privacy.s1.p":    "Politika se primjenjuje na kamere usluge guzva.ba postavljene u partnerskim objektima i na javne stranice tih objekata.",
		"privacy.s2.h":    "2. Podaci koje obrađujemo",
		"privacy.s2.p":    "Kamera u objektu približno jednom u minuti bilježi sliku prostora i šalje je šifriranom vezom (HTTPS) isključivo na poslužitelj usluge. Izvorna slika obrađuje se samo u radnoj memoriji poslužitelja radi izrade stilizirane slike i odmah se odbacuje. Ne zapisuje se na disk, ne prosljeđuje se trećim stranama i nije naknadno dostupna.",
		"privacy.s3.h":    "3. Svrha obrade",
		"privacy.s3.p":    "Jedina svrha obrade je javni prikaz trenutne popunjenosti objekta. Objavljuje se isključivo stilizirana slika niske razlučivosti, svedena na nekoliko tonova, na kojoj nije moguće prepoznati lica niti druge pojedinosti koje bi omogućile identifikaciju osoba. Izvan radnog vremena objekta slika se ne prikazuje.",
		"privacy.s4.h":    "4. Biometrija i profiliranje",
		"privacy.s4.p":    "Usluga ne provodi prepoznavanje lica, osoba ni registarskih oznaka, ne prati kretanje pojedinaca i ne izrađuje profile.",
		"privacy.s5.h":    "5. Rok čuvanja",
		"privacy.s5.p":    "Stilizirane slike čuvaju se 48 sati, nakon čega se trajno brišu. Iznimno, zadržava se posljednja objavljena slika objekta dok je ne zamijeni nova.",
		"privacy.s6.h":    "6. Kolačići i statistika",
		"privacy.s6.p":    "Stranica ne koristi kolačiće niti alate za praćenje ili oglašavanje trećih strana. Vodi se isključivo anonimna, zbirna statistika posjeta.",
		"privacy.s7.h":    "7. Sigurnost",
		"privacy.s7.p":    "Svi se podaci prenose šifriranom vezom. Kamera nije dostupna s interneta.",
		"privacy.s8.h":    "8. Vaša prava",
		"privacy.s8.p":    "Ako smatrate da obrada nije u skladu s propisima o zaštiti ličnih podataka, možete se obratiti Agenciji za zaštitu ličnih podataka u Bosni i Hercegovini.",

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
		"venue.maps":  "Otvori u Google Kartama",
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
		"owner.public":               "Javna stranica",
		"owner.details":              "Podaci o salonu",
		"owner.name":                 "Naziv",
		"owner.tz":                   "Vremenska zona",
		"owner.style":                "Stil slike",
		"owner.health":               "Zdravlje uređaja",
		"owner.serial":               "Serijski broj",
		"owner.state":                "Stanje",
		"owner.last":                 "Zadnja slika",
		"owner.signal":               "Signal",
		"owner.no_devices":           "Još nema uređaja — administrator će dodati jedan.",
		"owner.step1":                "Provjeri je li kamera uključena u struju.",
		"owner.step2":                "Provjeri radi li WiFi u salonu.",
		"owner.step3":                "Ako ništa ne pomogne, javi se administratoru.",
		"owner.location":             "Adresa (javno)",
		"owner.location.placeholder": "Ulica i broj, grad",
		"owner.maps":                 "Link na Google Karte (javno)",
		"owner.maps.help":            "U Google Kartama otvorite svoj salon → Podijeli → Kopiraj vezu i zalijepite ovdje. Ako ostane prazno, stranica traži adresu.",
		"owner.maps.bad":             "Link na Google Karte: zalijepite vezu iz Google Karata (Podijeli → Kopiraj vezu), npr. https://maps.app.goo.gl/…",
		"owner.about.placeholder":    "Jedna rečenica za goste koji skeniraju QR kod",
		"owner.kpi.views":            "Pregledi",
		"owner.kpi.visitors":         "Ljudi",
		"owner.kpi.qr":               "QR skeniranja",
		"owner.kpi.window":           "Prošlih 7 dana",
		"owner.live":                 "Upravo gleda: {n}",
		"owner.chart.days":           "Pregledi po danu",
		"owner.chart.days.note":      "Zadnjih 7 dana. Svjetliji stupac je isti dan prošli tjedan.",
		"owner.chart.daytip":         "{date}: {n} pregleda",
		"owner.chart.prevtip":        "Isti dan prošli tjedan: {n} pregleda",
		"owner.kpi.prev":             "Tjedan prije: {n}",
		"owner.chart.hours":          "Kada gosti gledaju",
		"owner.chart.hours.note":     "Pregledi po satu, zadnja 4 tjedna.",
		"owner.chart.hourtip":        "{h} h: {n} pregleda",
		"owner.peak":                 "Najviše gledaju između {from} i {to} h.",
		"owner.share":                "Podijeli stranicu",
		"owner.share.note":           "Gosti skeniraju kod i odmah vide ima li gužve.",
		"owner.share.copy":           "Kopiraj link",
		"owner.share.copied":         "Kopirano",
		"owner.share.poster":         "Ispiši plakat",
		"owner.camera":               "Kamera",
		"owner.uptime":               "Slike su stizale {n} % vremena.",
		"owner.signal.1":             "vrlo slab signal",
		"owner.signal.2":             "slab signal",
		"owner.signal.3":             "dobar signal",
		"owner.signal.4":             "odličan signal",
		"owner.signal.weakhint":      "Kamera je predaleko od WiFi rutera — slike mogu kasniti.",
		"owner.strip.from":           "od {time}",
		"owner.strip.day":            "prije 24 sata",
		"owner.strip.now":            "sada",
		"owner.strip.legend":         "Svaka crtica je 10 minuta; tamna znači da je stigla slika.",
		"owner.trend":                "Zadnja 4 tjedna",
		"owner.trend.note":           "Pregledi po danu. Isprekidana linija je prosjek zadnjih 7 dana.",
		"owner.trend.total":          "Ukupno {n} pregleda.",
		"owner.legend.this":          "ovaj tjedan",
		"owner.legend.prev":          "tjedan prije",
		"owner.legend.open":          "danas otvoreno",
		"owner.legend.peak":          "najviše pregleda",
		"owner.settings":             "Podaci i radno vrijeme",
		"owner.settings.note":        "Naziv, opis, kontakt i radno vrijeme koje gosti vide.",
		"owner.signout":              "Odjava",
		"owner.device.last":          "javila se u {time}",
		"owner.device.never":         "još se nije javila",
		"owner.device.off":           "isključena (administrator)",
		"owner.footer":               "guzva — gosti vide stiliziranu sliku, nikad fotografiju. Pitanja? Javi se administratoru.",
		"poster.cta":                 "Skeniraj i pogledaj uživo",
		"poster.note":                "Stilizirana slika prostora, osvježava se svake minute. Bez lica, ništa se ne snima.",
		"poster.print":               "Ispiši",
		"poster.back":                "Natrag",
		"poster.hint":                "Savjet: ispiši na A5 ili A4 i stavi na ulaz, izlog ili šank.",

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

		"home.title":      "Is it busy?",
		"home.lead":       "Check the picture from the salon before you go. It refreshes every minute.",
		"home.list":       "Salons",
		"home.empty":      "No salons listed yet.",
		"home.cta":        "See the salons",
		"nav.label":       "Main menu",
		"nav.venues":      "Salons",
		"nav.about":       "About",
		"nav.privacy":     "Privacy",
		"nav.contact":     "Contact",
		"about.title":     "About",
		"about.p1":        "Guzva.ba shows how many people are in a salon, barbershop or similar place right now, so you don't have to call or wait in line.",
		"about.p2":        "A salon gets a small camera and its own page on guzva.ba. Guests open it from a link or the QR code on the door.",
		"how.title":       "How it works",
		"how.1":           "The camera in the salon takes a picture once a minute.",
		"how.2":           "The picture is turned straight into a rough drawing in a few tones, without faces or fine detail.",
		"how.3":           "You check on your phone how busy it is before you go.",
		"privsec.title":   "Privacy",
		"privsec.lead":    "It is built so guests see how busy it is, not who is there.",
		"privsec.1.h":     "No faces",
		"privsec.2.h":     "No stored photos",
		"privsec.3.h":     "No tracking",
		"privsec.1":       "The picture is coarse and reduced to a few tones, so faces can't be recognised.",
		"privsec.2":       "The camera's photo exists only until it is turned into the drawing. It is never stored.",
		"privsec.3":       "The site has no cookies, ads or tracking tools.",
		"privsec.more":    "Read the privacy policy",
		"contact.title":   "Contact",
		"contact.owners":  "Run a salon or barbershop? We set up the camera, and your guests can see how busy you are before they come.",
		"contact.button":  "Get in touch",
		"contact.subject": "Guzva for my salon",
		"contact.none":    "Contact details coming soon.",
		"footer.line":     "A live picture from the salon.",
		"footer.pages":    "Pages",
		"footer.owners":   "For owners",
		"home.nopic":      "No picture shown",
		"home.biz.owner":  "Already have a camera?",
		"home.biz.login":  "Owner login",
		"nf.title":        "This page doesn't exist",
		"nf.p":            "The link may be wrong, or the salon is no longer listed.",
		"nf.home":         "Go to the homepage",
		"privacy.link":    "Privacy policy",
		"privacy.title":   "Privacy policy",
		"privacy.updated": "Last updated: 16 Oct 2026",
		"privacy.intro":   "This policy describes how the guzva.ba service processes data when it publicly shows how busy a venue is.",
		"privacy.s1.h":    "1. Scope",
		"privacy.s1.p":    "This policy applies to guzva.ba cameras installed at partner venues and to those venues' public pages.",
		"privacy.s2.h":    "2. Data we process",
		"privacy.s2.p":    "The camera at the venue captures an image of the room roughly once a minute and sends it over an encrypted connection (HTTPS) to the service's server only. The original image is processed solely in the server's memory to produce the stylised picture and is discarded immediately. It is never written to disk, never passed to third parties and cannot be retrieved later.",
		"privacy.s3.h":    "3. Purpose",
		"privacy.s3.p":    "The sole purpose of processing is to show publicly how busy the venue is. Only a stylised low-resolution picture reduced to a few tones is published, in which faces and other details that would allow a person to be identified cannot be made out. Outside the venue's opening hours no picture is shown.",
		"privacy.s4.h":    "4. Biometrics and profiling",
		"privacy.s4.p":    "The service does not recognise faces, people or number plates, does not track individuals and builds no profiles.",
		"privacy.s5.h":    "5. Retention",
		"privacy.s5.p":    "Stylised pictures are kept for 48 hours and then deleted permanently. As the only exception, the venue's most recent published picture is kept until a new one replaces it.",
		"privacy.s6.h":    "6. Cookies and statistics",
		"privacy.s6.p":    "The page uses no cookies and no third-party tracking or advertising tools. Only anonymous, aggregate visit statistics are kept.",
		"privacy.s7.h":    "7. Security",
		"privacy.s7.p":    "All data is transmitted over encrypted connections. The camera cannot be reached from the internet.",
		"privacy.s8.h":    "8. Your rights",
		"privacy.s8.p":    "If you believe the processing does not comply with personal data protection law, you may contact the Personal Data Protection Agency of Bosnia and Herzegovina.",

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
		"venue.maps":  "Open in Google Maps",
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

		"owner.public":               "Public page",
		"owner.details":              "Venue details",
		"owner.name":                 "Name",
		"owner.tz":                   "Time zone",
		"owner.style":                "Picture style",
		"owner.health":               "Device health",
		"owner.serial":               "Serial",
		"owner.state":                "State",
		"owner.last":                 "Last picture",
		"owner.signal":               "Signal",
		"owner.no_devices":           "No device yet — the administrator will add one.",
		"owner.step1":                "Check that the camera is plugged in.",
		"owner.step2":                "Check that the venue WiFi works.",
		"owner.step3":                "If nothing helps, contact the administrator.",
		"owner.location":             "Address (public)",
		"owner.location.placeholder": "Street and number, city",
		"owner.maps":                 "Google Maps link (public)",
		"owner.maps.help":            "In Google Maps open your venue → Share → Copy link and paste it here. Left empty, the page searches the address.",
		"owner.maps.bad":             "Google Maps link: paste the link from Google Maps (Share → Copy link), e.g. https://maps.app.goo.gl/…",
		"owner.about.placeholder":    "One sentence for guests scanning the QR code",
		"owner.kpi.views":            "Views",
		"owner.kpi.visitors":         "People",
		"owner.kpi.qr":               "QR scans",
		"owner.kpi.window":           "Past 7 days",
		"owner.live":                 "Watching now: {n}",
		"owner.chart.days":           "Views per day",
		"owner.chart.days.note":      "Last 7 days. The lighter bar is the same day last week.",
		"owner.chart.daytip":         "{date}: {n} views",
		"owner.chart.prevtip":        "Same day last week: {n} views",
		"owner.kpi.prev":             "The week before: {n}",
		"owner.chart.hours":          "When guests look",
		"owner.chart.hours.note":     "Views by hour, last 4 weeks.",
		"owner.chart.hourtip":        "{h}:00: {n} views",
		"owner.peak":                 "Most views between {from}:00 and {to}:00.",
		"owner.share":                "Share your page",
		"owner.share.note":           "Guests scan the code and see right away whether it's busy.",
		"owner.share.copy":           "Copy link",
		"owner.share.copied":         "Copied",
		"owner.share.poster":         "Print poster",
		"owner.camera":               "Camera",
		"owner.uptime":               "Pictures arrived {n}% of the time.",
		"owner.signal.1":             "very weak signal",
		"owner.signal.2":             "weak signal",
		"owner.signal.3":             "good signal",
		"owner.signal.4":             "excellent signal",
		"owner.signal.weakhint":      "The camera is too far from the WiFi router — pictures may be late.",
		"owner.strip.from":           "from {time}",
		"owner.strip.day":            "24 hours ago",
		"owner.strip.now":            "now",
		"owner.strip.legend":         "Each tick is 10 minutes; dark means a picture arrived.",
		"owner.trend":                "Last 4 weeks",
		"owner.trend.note":           "Views per day. The dashed line is the 7-day average.",
		"owner.trend.total":          "{n} views in total.",
		"owner.legend.this":          "this week",
		"owner.legend.prev":          "the week before",
		"owner.legend.open":          "open today",
		"owner.legend.peak":          "busiest hour",
		"owner.settings":             "Details and opening hours",
		"owner.settings.note":        "Name, description, contact and the hours guests see.",
		"owner.signout":              "Sign out",
		"owner.device.last":          "last checked in at {time}",
		"owner.device.never":         "not connected yet",
		"owner.device.off":           "disabled (administrator)",
		"owner.footer":               "guzva — guests see a stylised picture, never a photo. Questions? Contact your administrator.",
		"poster.cta":                 "Scan to see it live",
		"poster.note":                "A stylised picture of the room, refreshed every minute. No faces, nothing recorded.",
		"poster.print":               "Print",
		"poster.back":                "Back",
		"poster.hint":                "Tip: print on A5 or A4 and put it at the door, in the window or on the bar.",

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
