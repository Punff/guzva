// Package stats counts public-page visits for the owner panel, without
// cookies, without storing IPs, and without the public request path ever
// touching SQLite (D5): the static handler calls Record, which only updates
// memory; a sched job Drains the deltas into the store once a minute.
//
// Privacy (PLAN §1): a visitor is sha256(daily salt ‖ IP ‖ User-Agent),
// truncated to 64 bits, held in memory for the current UTC day only. The
// salt is random, never written anywhere, and replaced at UTC midnight
// together with every visitor set — so yesterday's visitors cannot be
// re-identified even by us, and nothing personal ever reaches the disk.
// What is persisted is counts: views, watch polls, QR scans per hour and
// distinct visitors per day.
package stats

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"
)

// Kind is what a public request means.
type Kind int

const (
	// View is a page navigation: a person opened the venue page.
	View Kind = iota
	// Poll is the open tab refreshing the still (every ~30 s while the
	// tab is visible): each one is roughly half a minute of watching.
	Poll
)

// PollSeconds is the public page's still-poll interval: one Poll ≈ this
// much watching. Kept in step with venue.html's data-poll.
const PollSeconds = 30

// LiveWindow is how recently a visitor must have polled to count as
// "watching right now": two missed polls.
const LiveWindow = 75 * time.Second

// Hour is one venue-hour of counts (Hour is the unix time of its start).
type Hour struct {
	Slug  string
	Hour  int64
	Views int64
	Polls int64
	QR    int64
}

// Day is a venue's distinct visitors on one UTC day ("2006-01-02").
type Day struct {
	Slug     string
	Day      string
	Visitors int64
}

type hourKey struct {
	slug string
	hour int64
}

// Recorder is safe for concurrent use.
type Recorder struct {
	mu      sync.Mutex
	now     func() time.Time
	salt    [16]byte
	day     string
	pending map[hourKey]*Hour
	seen    map[string]map[uint64]struct{}  // slug → today's visitors
	live    map[string]map[uint64]time.Time // slug → visitor → last activity
	dirty   map[string]bool                 // slugs whose day count changed
}

// New builds a Recorder; now nil means time.Now.
func New(now func() time.Time) *Recorder {
	if now == nil {
		now = time.Now
	}
	return &Recorder{now: now, pending: map[hourKey]*Hour{},
		seen: map[string]map[uint64]struct{}{}, live: map[string]map[uint64]time.Time{},
		dirty: map[string]bool{}}
}

// rotate replaces the salt and forgets every visitor when the UTC day
// changes. Called with mu held.
func (r *Recorder) rotate(t time.Time) {
	day := t.UTC().Format("2006-01-02")
	if day == r.day {
		return
	}
	r.day = day
	if _, err := rand.Read(r.salt[:]); err != nil {
		panic("stats: entropy failure: " + err.Error())
	}
	clear(r.seen)
	clear(r.live)
	clear(r.dirty)
}

func (r *Recorder) visitor(ip, ua string) uint64 {
	h := sha256.New()
	h.Write(r.salt[:])
	h.Write([]byte(ip))
	h.Write([]byte{0})
	h.Write([]byte(ua))
	return binary.BigEndian.Uint64(h.Sum(nil))
}

// Record counts one classified public request.
func (r *Recorder) Record(slug string, k Kind, ip, ua string, qr bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	r.rotate(t)
	id := r.visitor(ip, ua)

	key := hourKey{slug, t.Unix() / 3600 * 3600}
	h := r.pending[key]
	if h == nil {
		h = &Hour{Slug: slug, Hour: key.hour}
		r.pending[key] = h
	}
	switch k {
	case View:
		h.Views++
		if qr {
			h.QR++
		}
		set := r.seen[slug]
		if set == nil {
			set = map[uint64]struct{}{}
			r.seen[slug] = set
		}
		if _, ok := set[id]; !ok {
			set[id] = struct{}{}
			r.dirty[slug] = true
		}
	case Poll:
		h.Polls++
	}
}

// liveCap bounds the per-venue watcher set: tab ids come from the client,
// so a flood of made-up ids must not grow memory without limit.
const liveCap = 1000

// Watch marks one open tab as watching. tab is the page's per-load random
// id (the poll's X-Viewer header): IP + User-Agent cannot tell apart two
// guests on the same café WiFi or carrier NAT with the same phone — every
// iPhone on one iOS version sends the same UA — so they collapsed into one
// and the "others watching" badge never showed. The id lives in that tab's
// memory only, is hashed with the daily salt like a visitor, and is
// forgotten LiveWindow after its last poll. An absent or malformed id
// (a page from before the header existed) falls back to IP + UA.
//
// Only the still poll calls this, never the page load: one guest would
// otherwise count twice — once by IP + UA, once by tab id.
func (r *Recorder) Watch(slug, tab, ip, ua string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.now()
	r.rotate(t)
	id := r.visitor(ip, ua)
	if validTab(tab) {
		id = r.visitor("tab", tab)
	}
	lv := r.live[slug]
	if lv == nil {
		lv = map[uint64]time.Time{}
		r.live[slug] = lv
	}
	if _, ok := lv[id]; !ok && len(lv) >= liveCap {
		return
	}
	lv[id] = t
}

// validTab accepts the page's id shape: 16 lowercase hex digits.
func validTab(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Live is how many open tabs polled the venue's still within LiveWindow.
func (r *Recorder) Live(slug string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := r.now().Add(-LiveWindow)
	n := 0
	for id, at := range r.live[slug] {
		if at.Before(cut) {
			delete(r.live[slug], id) // expire as we go: the map stays small
			continue
		}
		n++
	}
	return n
}

// Drain returns the hour deltas since the last Drain (and forgets them)
// plus today's absolute visitor count for every venue whose count moved.
// The store adds the hour deltas and keeps the max of the day counts, so a
// restart (which empties the visitor set) can only under-count, never
// double-count, the day it happens on.
func (r *Recorder) Drain() ([]Hour, []Day) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hours := make([]Hour, 0, len(r.pending))
	for _, h := range r.pending {
		hours = append(hours, *h)
	}
	clear(r.pending)
	var days []Day
	for slug := range r.dirty {
		days = append(days, Day{Slug: slug, Day: r.day, Visitors: int64(len(r.seen[slug]))})
	}
	clear(r.dirty)
	return hours, days
}

// Restore puts drained rows back after a failed flush, so a transient
// store error loses nothing.
func (r *Recorder) Restore(hours []Hour, days []Day) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range hours {
		key := hourKey{h.Slug, h.Hour}
		p := r.pending[key]
		if p == nil {
			p = &Hour{Slug: h.Slug, Hour: h.Hour}
			r.pending[key] = p
		}
		p.Views += h.Views
		p.Polls += h.Polls
		p.QR += h.QR
	}
	for _, d := range days {
		if d.Day == r.day {
			r.dirty[d.Slug] = true
		}
	}
}
