package frames

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// renderBuckets: render latency histogram upper bounds in milliseconds (§6.6
// hand-rolled /metrics — no dependency, no cardinality surprises).
var renderBuckets = [...]float64{10, 25, 50, 100, 200, 500, 1000}

// ingestOutcomes is the fixed label set for the ingest counter. Fixed on
// purpose: labels must never be built from request data.
var ingestOutcomes = []string{
	"stored", "duplicate", "quarantined", "health",
	"unauthorized", "ratelimited", "too_large", "disk_full",
	"bad_meta", "internal",
}

// Metrics counts ingest outcomes. All operations are lock-free atomics: the
// hot path must not serialize (§6.1 bounded work).
type Metrics struct {
	counters  map[string]*atomic.Int64
	renderMs  [len(renderBuckets) + 1]atomic.Int64 // last bucket is +Inf
	renderSum atomic.Int64                         // total render ms
	renderN   atomic.Int64                         // render count
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

// observeRender folds one pipeline duration into the histogram.
func (m *Metrics) observeRender(d time.Duration) {
	ms := float64(d.Milliseconds())
	m.renderSum.Add(int64(ms))
	m.renderN.Add(1)
	idx := len(renderBuckets) // +Inf
	for i, ub := range renderBuckets {
		if ms <= ub {
			idx = i
			break
		}
	}
	m.renderMs[idx].Add(1)
}

// Prometheus renders the text exposition format (hand-rolled, §2).
func (m *Metrics) Prometheus() string {
	var b strings.Builder
	b.WriteString("# TYPE imaliguzve_ingest_total counter\n")
	for _, o := range ingestOutcomes {
		fmt.Fprintf(&b, "imaliguzve_ingest_total{outcome=%q} %d\n", o, m.counters[o].Load())
	}
	b.WriteString("# TYPE imaliguzve_render_ms histogram\n")
	var cum int64
	for i, ub := range renderBuckets {
		cum += m.renderMs[i].Load()
		fmt.Fprintf(&b, "imaliguzve_render_ms_bucket{le=\"%g\"} %d\n", ub, cum)
	}
	cum += m.renderMs[len(renderBuckets)].Load()
	fmt.Fprintf(&b, "imaliguzve_render_ms_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(&b, "imaliguzve_render_ms_sum %d\n", m.renderSum.Load())
	fmt.Fprintf(&b, "imaliguzve_render_ms_count %d\n", m.renderN.Load())
	return b.String()
}
