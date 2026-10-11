// Package pages pre-renders the public side as static files (PLAN D5,
// §6.4): data/public/{slug}/index.html + latest.png. Public requests never
// touch SQLite, templates, or the render pipeline — Static below only serves
// bytes. All writes are temp-file + rename under one mutex (single process),
// and index.html is rewritten only when something it shows changed.
package pages

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/render"
	"guzva/internal/store"
	"guzva/internal/venue"
	"guzva/internal/webui"

	_ "time/tzdata" // venue tz names resolve even without a system zone db (§2)
)

//go:embed templates/venue.html
var assets embed.FS

// Writer owns data/public. One instance per process; OnFrame (ingest) and
// Refresh (sched) serialize on the same mutex so a state key can be kept in
// memory to decide whether a rewrite is needed.
type Writer struct {
	st      *store.Store
	dataDir string // ring root: dataDir/venues/<id>/stills/…
	root    string // public web root: dataDir/public
	tmpl    *template.Template
	log     *slog.Logger
	now     func() time.Time

	mu   sync.Mutex
	last map[string]string // slug → state key of the last index.html write
}

// New builds the writer and ensures the web root exists.
func New(st *store.Store, dataDir string, log *slog.Logger) (*Writer, error) {
	if log == nil {
		log = slog.Default()
	}
	root := filepath.Join(dataDir, "public")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("pages: create web root: %w", err)
	}
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("pages: parse templates: %w", err)
	}
	return &Writer{
		st: st, dataDir: dataDir, root: root, tmpl: tmpl, log: log,
		now: time.Now, last: map[string]string{},
	}, nil
}

// Root is the directory Static serves.
func (w *Writer) Root() string { return w.root }

// OnFrame publishes a freshly stored frame: latest.png first, then
// index.html — the §6.1 order, called from the ingest handler before it
// responds. An error is returned to the caller but must never fail the
// ingest: the frame IS stored and the next tick repairs the page.
func (w *Writer) OnFrame(ctx context.Context, venueID int64) error {
	return w.write(ctx, venueID, true)
}

// Refresh re-derives state for one venue (freshness flip, open→closed) and
// rewrites index.html only when the state key changed. This is the sched
// tick's job — a static page cannot notice time passing by itself (§6.4).
func (w *Writer) Refresh(ctx context.Context, venueID int64) error {
	return w.write(ctx, venueID, false)
}

// RefreshAll walks every venue. Failures are logged per venue; one broken
// page must not stop the others (the next tick retries anyway).
func (w *Writer) RefreshAll(ctx context.Context) error {
	vs, err := w.st.ListVenues(ctx)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if err := w.Refresh(ctx, v.ID); err != nil {
			w.log.Error("pages: refresh failed", "venue", v.Slug, "err", err.Error())
		}
	}
	return nil
}

// view is the template model. Deliberately free of relative ages: a static
// file cannot keep them correct, so the page shows an absolute "Updated
// HH:MM" in venue-local time (§6.4).
type view struct {
	Name       string
	HasFrame   bool
	StillVer   string // still cache-buster: the frame row id (unique, monotonic)
	StillW     int    // intrinsic render size, so the browser reserves the
	StillH     int    // box before the still lands (no layout shift)
	StillFile  string // "latest.png" (or latest.jpg until the first v2 frame)
	Pixelated  bool   // grid print: integer scale, pixelated (§5.7)
	TZName     string // IANA venue tz for the client-side headline (data-tz)
	Updated    string // "14:07" venue-local, empty before the first frame
	UpdatedDay string // "danas" | "jučer" | "10.10.2026." — disambiguates
	// the bare clock time without a relative age
	HoursToday string // "09:00–17:00" venue-local, empty if hours unset
	HoursTitle string // localized section heading, from the catalog (W2)
	TZ         string // venue tz abbreviation, e.g. "CET"; "" before any frame
	Tagline    string // "Ima li gužve?" — title suffix (DESIGN §7.4)
	AltStill   string // full alt with the capture time (localized, W2)
	AltNow     string // alt before the first JS frame swap (localized)
	AltPrefix  string // alt.still up to the time; the poller appends to it

	// Poller copy, read from data-* attributes so the script carries no
	// second hand-written dictionary (W2: one catalog, internal/i18n).
	StrOffline, StrIdle, StrResume    string
	StrNewPic, StrToday, StrYesterday string

	// DESIGN.md §3.2: a state is a SHAPE, a COLOUR and a SENTENCE. Colour is
	// never the only signal, so all three travel together.
	StateClass  string        // fresh | late | nosignal | waiting | closed
	StateText   string        // Croatian (or per-venue English) from internal/i18n
	StateShape  template.HTML // svg shape carrying the state without colour
	StateDetail string        // one plain sentence, or ""
	OpenText    string
	PrivacyLine string
	ShowStill   bool // §5.6: a closed venue shows no still at all
	Lang        string
	CSS         template.CSS // embedded design-system stylesheet (inlined: D5)
}

func (w *Writer) write(ctx context.Context, venueID int64, force bool) error {
	v, err := w.st.VenueByID(ctx, venueID)
	if err != nil {
		return err
	}
	latest, err := w.st.LatestFrame(ctx, venueID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		latest = nil
	}
	hours, err := w.st.Hours(ctx, venueID)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(v.TZ)
	if err != nil {
		w.log.Warn("pages: unknown venue tz, falling back to UTC", "tz", v.TZ, "err", err.Error())
		loc = time.UTC
	}

	now := w.now().UTC()
	last := v.LastFrameAt
	if last.IsZero() && latest != nil && latest.Status == "ok" {
		last = latest.ReceivedAt
	}
	fresh := venue.Classify(last, now)
	localNow := now.In(loc)
	open := hours.ClassifyOpen(localNow)

	// The state key is everything the page shows. The per-frame clock is NOT
	// part of it: the stamp burned into the picture carries the time (⚑2), so
	// index.html is rewritten only on state flips and settings edits — not
	// per frame. Style/TZ/lang ARE part of it, because a settings edit must
	// reach the page on the next sched tick even though no frame arrived.
	// The still FILENAME is part of it too (W2/B8): on a jpg→png row-kind
	// flip the page must be rewritten once, or the poller keeps fetching a
	// just-deleted latest.jpg until the next state flip.
	cat0 := i18n.For(i18n.Normalize(v.Lang))
	stillFile := "latest.png"
	if latest != nil && latest.Status == "ok" && latest.Path != "" && !strings.HasSuffix(latest.Path, ".png") {
		stillFile = "latest.jpg"
	}
	stateKey := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s", fresh, open, v.Name, v.Style,
		v.TZ, v.Lang, hoursToday(cat0, hours, localNow), stillFile)

	w.mu.Lock()
	defer w.mu.Unlock()

	dir := filepath.Join(w.root, v.Slug)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("pages: venue dir: %w", err)
	}

	// latest.png: only a good stylised render ever lands here; a quarantined
	// beat leaves the previous still untouched (never publish a failure).
	//
	// Transition honesty: old ring rows are JPEGs and can never become PNGs
	// (raws are not stored), so the published filename follows the row —
	// latest.png for new rows, latest.jpg until a venue's first v2 frame.
	// The stale counterpart is removed, so the web root still holds exactly
	// two files. Within one capture interval every venue converges to .png.
	if force && latest != nil && latest.Status == "ok" && latest.Path != "" {
		src := filepath.Join(w.dataDir, latest.Path)
		if err := copyAtomic(src, filepath.Join(dir, stillFile)); err != nil {
			w.log.Error("pages: still publish failed", "venue", v.Slug, "err", err.Error())
		}
		for _, stale := range []string{"latest.png", "latest.jpg"} {
			if stale != stillFile {
				_ = os.Remove(filepath.Join(dir, stale))
			}
		}
	}

	// ⚑2: the still carries its own time, so index.html moves only on state
	// flips and settings edits — never per frame. (force once meant "a new
	// frame arrived, rewrite regardless"; that bypass is gone, because the
	// rewritten-identical page still moved Last-Modified and every 60 s poll
	// would have reloaded the page constantly.)
	if w.last[v.Slug] == stateKey {
		return nil // nothing the page shows has changed
	}

	cat := i18n.For(i18n.Normalize(v.Lang))
	vw := view{
		Name:         v.Name,
		HasFrame:     latest != nil && latest.Status == "ok",
		TZ:           tzLabel(last, loc),
		TZName:       v.TZ,
		CSS:          template.CSS(webui.Public()),
		Lang:         string(cat.Lang),
		PrivacyLine:  cat.Get("privacy.line"),
		ShowStill:    true,
		StillFile:    stillFile,
		Tagline:      cat.Get("tagline"),
		HoursTitle:   cat.Get("hours.title"),
		AltNow:       cat.F("alt.nowframe", v.Name),
		AltPrefix:    cat.F("alt.still", v.Name, ""),
		StrOffline:   cat.Get("net.offline"),
		StrIdle:      cat.Get("idle.paused"),
		StrResume:    cat.Get("idle.resume"),
		StrNewPic:    cat.Get("live.newpic"),
		StrToday:     cat.Get("when.today"),
		StrYesterday: cat.Get("when.yesterday"),
	}
	if vw.HasFrame {
		// The cache-buster is the frame row id, NOT received_at: ids are
		// unique and monotonic, so this URL can never be reused for
		// different bytes. Seconds-resolution timestamps could collide if
		// two frames ever landed in the same second, and since latest.jpg
		// is served immutable for a year, one collision would pin a stale
		// photo in a visitor's cache permanently.
		vw.StillVer = fmt.Sprintf("%d", latest.ID)
		local := last.In(loc)
		vw.Updated = cat.Time(local)
		vw.UpdatedDay = cat.DayName(local, now.In(loc))
		vw.AltStill = cat.F("alt.still", v.Name, vw.Updated)
		if latest.Width > 0 && latest.Height > 0 {
			vw.StillW, vw.StillH = latest.Width, latest.Height
		}
		// The print is shown at integer scale with pixelated rendering (§5.7);
		// anything else on disk is a pre-print JPEG shown 1:1.
		vw.Pixelated = latest.Width == render.GW && latest.Height == render.GH
	}
	// State = shape + colour + sentence (§3.2). Two stale tiers (DESIGN.md
	// ⚑3) so a camera merely running behind reads differently from one that
	// has gone quiet.
	switch {
	case !vw.HasFrame:
		vw.StateClass, vw.StateText, vw.StateShape = "waiting", cat.Get("state.waiting"), shapeWaiting
	case fresh == venue.Fresh:
		vw.StateClass, vw.StateText, vw.StateShape = "fresh", cat.Get("state.fresh"), shapeFresh
	case fresh == venue.Late:
		vw.StateClass, vw.StateText, vw.StateShape = "late", cat.Get("state.late"), shapeLate
		vw.StateDetail = cat.F("late.detail", vw.Updated)
	default:
		vw.StateClass, vw.StateText, vw.StateShape = "nosignal", cat.Get("state.nosignal"), shapeNoSignal
		vw.StateDetail = cat.F("stale.detail", vw.Updated)
	}

	// §5.6: a closed venue shows no still at all — privacy after hours,
	// honesty (there is nothing to claim), and bytes.
	if open == venue.Closed {
		vw.StateClass, vw.StateText, vw.StateShape = "closed", cat.Get("state.closed"), shapeClosed
		vw.ShowStill, vw.StateDetail = false, ""
	}
	vw.OpenText = openText(cat, hours, localNow, open)
	vw.HoursToday = hoursToday(cat, hours, localNow)

	if err := renderAtomic(filepath.Join(dir, "index.html"), w.tmpl, "venue.html", vw); err != nil {
		return err
	}
	w.last[v.Slug] = stateKey
	return nil
}

// tzLabel abbreviates the venue timezone for display ("CET", "GMT+2"), but
// a frame-less venue has a zero `last`, and rendering the zero time in a
// zone yields "LMT" (Sarajevo's pre-1916 local mean time) — a nonsense label
// a waiting venue used to print. Empty means the template hides the slot.
func tzLabel(last time.Time, loc *time.Location) string {
	if last.IsZero() {
		return ""
	}
	return zoneLabel(last.In(loc), loc)
}

// zoneLabel abbreviates the venue's timezone for display ("CET", "GMT+2").
// Abbreviations are ambiguous across zones, so the offset is the fallback
// whenever the zone has no stable abbreviation at that instant.
func zoneLabel(at time.Time, loc *time.Location) string {
	name, off := at.Zone()
	if name == "" || name == "UTC" {
		return "UTC"
	}
	// Only trust the abbreviation when it is not a pure numeric GMT±N.
	if len(name) > 1 && (name[0] < '0' || name[0] > '9') {
		return name
	}
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("GMT%s%d", sign, off/3600)
}

// hoursToday formats today's ranges in venue-local time, "" when unset.
func hoursToday(cat *i18n.Catalog, h venue.Hours, t time.Time) string {
	rs := h[int(t.Weekday())]
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range rs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%02d:%02d–%02d:%02d",
			r.OpenMin/60, r.OpenMin%60, r.CloseMin/60, r.CloseMin%60)
	}
	return b.String()
}

// copyAtomic copies src over dst via a same-directory temp file + rename.
func copyAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pub-*.tmp")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// renderAtomic renders the template to a same-directory temp file + rename,
// so a reader (http.FileServer) never sees a half-written page.
func renderAtomic(dst string, tmpl *template.Template, name string, v view) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".idx-*.tmp")
	if err != nil {
		return err
	}
	if err := tmpl.ExecuteTemplate(tmp, name, v); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// openText is the second line: when this venue is open, or when it next
// opens. Absent hours means the page never claims anything about opening.
func openText(cat *i18n.Catalog, h venue.Hours, now time.Time, open venue.OpenClosed) string {
	if !h.Configured() {
		return ""
	}
	switch open {
	case venue.Open:
		if end, ok := h.NextClose(now); ok {
			return cat.F("open.until", cat.Time(end))
		}
		return cat.Get("open.now")
	default:
		if next, ok := h.NextOpen(now); ok {
			when := cat.Get("when.today")
			if !sameDay(now, next) {
				when = cat.Get("when.tomorrow")
			}
			return cat.F("closed.opens", when, cat.Time(next))
		}
		return cat.Get("state.closed")
	}
}

// sameDay reports whether two instants fall on the same calendar day.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// Status shapes (DESIGN.md §3.2). Colour is never the only signal, so every
// state carries a DISTINCT silhouette as well as a colour and a sentence:
// filled / half / hatched / shutter / dither. 12×12, crispEdges, currentColor
// so the chip's colour drives them. Rendered aria-hidden; the text beside them
// is what a screen reader reads.
//
// These are whole strings, never assembled by concatenation — the Tailwind
// scanner and the SVG inliner both need to see them literally (§4).
const (
	shapeFresh = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges" aria-hidden="true"><rect width="12" height="12" fill="currentColor"/></svg>`
	shapeLate  = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges" aria-hidden="true"><rect width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"/><rect y="6" width="12" height="6" fill="currentColor"/></svg>`
	// Hatched: bars, so it reads as "no clean signal" without colour.
	shapeNoSignal = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges" aria-hidden="true"><rect width="12" height="12" fill="none" stroke="currentColor" stroke-width="2"/><path d="M0 3h12M0 6h12M0 9h12" stroke="currentColor" stroke-width="2"/></svg>`
	// Shutter bars: a closed venue shows no still at all (§5.6).
	shapeClosed  = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges" aria-hidden="true"><rect y="2" width="12" height="3" fill="currentColor"/><rect y="7" width="12" height="3" fill="currentColor"/></svg>`
	shapeWaiting = `<svg viewBox="0 0 12 12" width="12" height="12" shape-rendering="crispEdges" aria-hidden="true"><path d="M0 0h4v4H0zM8 0h4v4H8zM4 4h4v4H4zM0 8h4v4H0zM8 8h4v4H8z" fill="currentColor"/></svg>`
)
