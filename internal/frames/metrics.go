package frames

import "sync/atomic"

// ingestOutcomes is the fixed label set for the ingest counter. Fixed on
// purpose: labels must never be built from request data.
var ingestOutcomes = []string{
	"stored", "duplicate", "quarantined", "health",
	"unauthorized", "ratelimited", "too_large", "disk_full",
	"bad_meta", "aborted", "internal",
}

// Metrics counts ingest outcomes. The readers today are the test suite and
// the per-IP flood path: a flood must not become a log flood (§6.1), so the
// drop is counted silently instead. There is deliberately NO /metrics route
// and no formatter (PLAN §14 trigger: "debugging/audit needs slog can't
// answer"); when one lands, a hand-rolled exposition of these counters is
// in git history. The render-latency histogram that used to live here was
// never read by anything either — render_ms rides the per-ingest journal
// line (§11), which the 48 h soak actually consumes.
type Metrics struct {
	counters map[string]*atomic.Int64
}

// NewMetrics zeroes all counters.
func NewMetrics() *Metrics {
	m := &Metrics{counters: make(map[string]*atomic.Int64, len(ingestOutcomes))}
	for _, o := range ingestOutcomes {
		m.counters[o] = &atomic.Int64{}
	}
	return m
}

// add increments a known outcome (unknown names are dropped rather than
// creating unbounded label values).
func (m *Metrics) add(outcome string) {
	if c, ok := m.counters[outcome]; ok {
		c.Add(1)
	}
}

// Get returns a counter for tests.
func (m *Metrics) Get(outcome string) int64 {
	if c, ok := m.counters[outcome]; ok {
		return c.Load()
	}
	return 0
}
