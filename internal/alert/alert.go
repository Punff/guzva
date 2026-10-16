// Package alert watches the fleet and tells the operator on Telegram when
// something needs attention (PLAN §6.6, business plan P1). It is push to one
// person, not a public feature: the rules read the same rows the admin fleet
// board shows, and nothing here ever contacts a device (push-only, §1).
//
// The rules are a pure function over a Snapshot (Engine.Evaluate) so they can
// be tested with a fake clock. Monitor runs them once a minute from sched,
// diffs the result against the open-alert ledger in the store, and sends one
// message per change: "⚠ …" when a condition starts, "✓ Resolved: …" when it
// ends. A send that fails is retried on the next tick.
package alert

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"guzva/internal/venue"
)

// Thresholds. Plain constants: there is one operator and no settings UI.
const (
	// WeakRSSI is the signal below which a camera is "weak" (the install
	// gate wants ≥ −65; −72 is where uploads start failing on the bench).
	WeakRSSI = -72
	// WeakFor is how long the signal must stay weak before it is worth a
	// message (single dips are normal).
	WeakFor = time.Hour
	// RebootLoop is the number of boots within RebootWindow that means the
	// board is resetting itself.
	RebootLoop   = 3
	RebootWindow = time.Hour
	// BrownoutHold keeps a brownout alert open this long after the counter
	// last rose, so one dip is one message, not one per minute.
	BrownoutHold = 6 * time.Hour
	// ManyStale is the number of venues stale at once that points at the
	// server or the network rather than one camera.
	ManyStale = 3
	// DiskLow mirrors the ingest DEGRADED threshold (§8).
	DiskLow = 15.0
	// WarmUp is how long after a restart the history-based rules (weak
	// signal, reboot loop, brownout) are not trusted to CLEAR an alert —
	// their memory starts empty, which would read as "resolved".
	WarmUp = WeakFor + 10*time.Minute
)

// VenueState is what the rules need to know about one venue.
type VenueState struct {
	Slug, Name  string
	Open        venue.OpenClosed // at Snapshot.Now in the venue's timezone
	LastFrameAt time.Time        // zero = never had a picture
}

// DeviceState is what the rules need to know about one active camera.
type DeviceState struct {
	Serial    string
	VenueName string
	Boot      uint32
	Brownout  uint32
	RSSI      int
	LastSeen  time.Time
}

// Snapshot is the fleet at one instant.
type Snapshot struct {
	Now      time.Time
	Venues   []VenueState
	Devices  []DeviceState
	DiskFree float64 // percent; negative = unknown
}

// Condition is one alert that is true right now.
type Condition struct {
	Key     string // stable identity, e.g. "stale:jadro"
	Message string // what the operator reads
}

// historyKeys are the key prefixes whose rules depend on Engine memory.
var historyKeys = []string{"weak:", "reboot:", "brownout:"}

// HistoryBased reports whether a key's rule needs Engine memory to be judged.
func HistoryBased(key string) bool {
	for _, p := range historyKeys {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

type bootSample struct {
	at   time.Time
	boot uint32
}

// Engine evaluates the rules and keeps the little history some of them need.
// Not safe for concurrent use; sched calls it from one goroutine.
type Engine struct {
	boots     map[string][]bootSample
	brownout  map[string]uint32
	brownAt   map[string]time.Time
	weakSince map[string]time.Time
}

// NewEngine returns an Engine with empty history.
func NewEngine() *Engine {
	return &Engine{
		boots: map[string][]bootSample{}, brownout: map[string]uint32{},
		brownAt: map[string]time.Time{}, weakSince: map[string]time.Time{},
	}
}

// Evaluate returns every condition true at s.Now, sorted by key.
func (e *Engine) Evaluate(s Snapshot) []Condition {
	var out []Condition
	stale := 0
	for _, v := range s.Venues {
		// A venue is expected to send while open, or always when it has no
		// hours set. Closed venues are left alone: nobody needs a 3 a.m.
		// message about a salon that is shut.
		if v.LastFrameAt.IsZero() || v.Open == venue.Closed {
			continue
		}
		if age := s.Now.Sub(v.LastFrameAt); age > venue.NoSignalAfter {
			stale++
			out = append(out, Condition{"stale:" + v.Slug,
				fmt.Sprintf("%s: no picture for %s (last at %s UTC)", v.Name, round(age), v.LastFrameAt.UTC().Format("15:04"))})
		}
	}
	if stale >= ManyStale {
		out = append(out, Condition{"many-stale",
			fmt.Sprintf("%d venues have no picture at once — check the server and network first", stale)})
	}

	for _, d := range s.Devices {
		// Reboot loop: boots counted within the window.
		hist := append(e.boots[d.Serial], bootSample{s.Now, d.Boot})
		cut := s.Now.Add(-RebootWindow)
		for len(hist) > 0 && hist[0].at.Before(cut) {
			hist = hist[1:]
		}
		e.boots[d.Serial] = hist
		if lo := hist[0].boot; d.Boot >= lo && d.Boot-lo >= RebootLoop {
			out = append(out, Condition{"reboot:" + d.Serial,
				fmt.Sprintf("%s (%s): rebooted %d times in the last hour — power or firmware", d.VenueName, d.Serial, d.Boot-lo)})
		}

		// Brownout: the counter rose since we last looked. The first sight
		// of a device only sets the baseline (old brownouts are history).
		if prev, seen := e.brownout[d.Serial]; seen && d.Brownout > prev {
			e.brownAt[d.Serial] = s.Now
		}
		e.brownout[d.Serial] = d.Brownout
		if at, ok := e.brownAt[d.Serial]; ok && s.Now.Sub(at) < BrownoutHold {
			out = append(out, Condition{"brownout:" + d.Serial,
				fmt.Sprintf("%s (%s): power dipped (brownout #%d) — check the adapter and cable", d.VenueName, d.Serial, d.Brownout)})
		}

		// Weak signal, sustained. Only judged on fresh readings.
		fresh := !d.LastSeen.IsZero() && s.Now.Sub(d.LastSeen) < 5*time.Minute
		switch {
		case fresh && d.RSSI != 0 && d.RSSI < WeakRSSI:
			if _, ok := e.weakSince[d.Serial]; !ok {
				e.weakSince[d.Serial] = s.Now
			}
		case fresh:
			delete(e.weakSince, d.Serial)
		}
		if since, ok := e.weakSince[d.Serial]; ok && s.Now.Sub(since) >= WeakFor {
			out = append(out, Condition{"weak:" + d.Serial,
				fmt.Sprintf("%s (%s): weak WiFi for %s (%d dBm) — uploads will fail; move the camera or add an antenna", d.VenueName, d.Serial, round(s.Now.Sub(since)), d.RSSI)})
		}
	}

	if s.DiskFree >= 0 && s.DiskFree < DiskLow {
		out = append(out, Condition{"disk", fmt.Sprintf("Server disk %.0f %% free — ingest degrades below 15 %%", s.DiskFree)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func round(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f h", d.Hours())
}
