package pages

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/render"
	"guzva/internal/store"
	"guzva/internal/venue"
	"guzva/internal/webui"
)

// homePage is the rendered homepage (D12) plus when its bytes last changed,
// so an unchanged rebuild keeps Last-Modified and visitors get 304s.
type homePage struct {
	mu    sync.Mutex
	body  []byte
	at    time.Time
	cache pageCache
}

type homeCard struct {
	Slug, Name, Location string
	Still                string // "latest.png?v=<id>" or "" (closed / no frame)
	Pixelated            bool
	StateClass           string
	StateText            string
	StateShape           template.HTML
	OpenText             string
}

// SetContact sets the "for salons" contact shown on the homepage (an email
// or a phone number, from -contact / GUZVA_CONTACT). Empty hides the line —
// the page never shows an invented address.
func (w *Writer) SetContact(c string) { w.contact = strings.TrimSpace(c) }

// buildHome renders the homepage from the listed venues, in the sched job —
// never on a request (D5). The cards use stateOf, the same chip the venue
// page shows, and a closed venue shows no picture (§5.6).
func (w *Writer) buildHome(ctx context.Context, vs []store.Venue) error {
	cat := i18n.For(i18n.HR)
	now := w.now().UTC()
	var cards []homeCard
	for i := range vs {
		v := &vs[i]
		if !v.Listed {
			continue
		}
		latest, err := w.st.LatestFrame(ctx, v.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		hours, err := w.st.Hours(ctx, v.ID)
		if err != nil {
			return err
		}
		loc, err := time.LoadLocation(v.TZ)
		if err != nil {
			loc = time.UTC
		}
		hasFrame := latest != nil && latest.Status == "ok"
		last := v.LastFrameAt
		if last.IsZero() && hasFrame {
			last = latest.ReceivedAt
		}
		localNow := now.In(loc)
		open := hours.ClassifyOpen(localNow)
		c := homeCard{Slug: v.Slug, Name: v.Name, Location: v.Location,
			OpenText: openText(cat, hours, localNow, open)}
		c.StateClass, c.StateText, c.StateShape = stateOf(cat, hasFrame, venue.Classify(last, now), open)
		if hasFrame && open != venue.Closed {
			file := "latest.png"
			if !strings.HasSuffix(latest.Path, ".png") {
				file = "latest.jpg"
			}
			c.Still = file + "?v=" + itoa(latest.ID)
			c.Pixelated = latest.Width > 0 && render.GW%latest.Width == 0
		}
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return strings.ToLower(cards[i].Name) < strings.ToLower(cards[j].Name) })

	contactHref := ""
	if w.contact != "" {
		if strings.Contains(w.contact, "@") {
			contactHref = "mailto:" + w.contact
		} else {
			contactHref = "tel:" + telHref(w.contact)
		}
	}
	var buf bytes.Buffer
	if err := w.tmpl.ExecuteTemplate(&buf, "home.html", struct {
		Lang                 string
		Cat                  *i18n.Catalog
		CSS                  template.CSS
		Cards                []homeCard
		Contact, ContactHref string
	}{string(cat.Lang), cat, template.CSS(webui.Public()), cards, w.contact, contactHref}); err != nil {
		return err
	}
	w.home.mu.Lock()
	defer w.home.mu.Unlock()
	if !bytes.Equal(buf.Bytes(), w.home.body) {
		w.home.body, w.home.at = buf.Bytes(), w.now()
	}
	return nil
}

// Home serves the homepage from memory. Until the first build (a second
// after boot) it answers 503 rather than an empty list that would read as
// "no salons". Indexable on purpose — venue pages stay noindex (⚑7).
func (w *Writer) Home() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.home.mu.Lock()
		body, at := w.home.body, w.home.at
		w.home.mu.Unlock()
		if body == nil {
			http.Error(rw, "503 — starting", http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Cache-Control", "no-cache")
		securityHeaders(rw)
		w.home.cache.serve(rw, r, "home", at, body)
	})
}

var notFound struct {
	once sync.Once
	body []byte
}

// notFoundPage renders the Croatian 404 once (templates are embedded, so it
// cannot fail at runtime once the tests pass; on error it falls back to text).
func notFoundPage() []byte {
	notFound.once.Do(func() {
		tmpl, err := template.ParseFS(assets, "templates/*.html")
		if err != nil {
			return
		}
		cat := i18n.For(i18n.HR)
		var buf bytes.Buffer
		if tmpl.ExecuteTemplate(&buf, "notfound.html", struct {
			Lang string
			Cat  *i18n.Catalog
			CSS  template.CSS
		}{string(cat.Lang), cat, template.CSS(webui.Public())}) == nil {
			notFound.body = buf.Bytes()
		}
	})
	return notFound.body
}

// serveNotFound answers 404 with the page (HTML clients) — CSP-hashed like
// every other public page — or plain text when it could not be built.
func serveNotFound(w http.ResponseWriter, r *http.Request) {
	body := notFoundPage()
	if body == nil {
		http.NotFound(w, r)
		return
	}
	securityHeaders(w)
	w.Header().Set("Content-Security-Policy", buildCSP(body))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func itoa(n int64) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}
