package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestAnalyse pins the exit-gate arithmetic on a synthetic 10-minute journal:
// frames every minute except a 5-minute hole, a reboot that resets the
// counters, one dark reject, and non-ingest noise that must be skipped.
func TestAnalyse(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 30, 0, time.UTC)
	var b strings.Builder
	b.WriteString("-- boot noise --\n")
	b.WriteString(`{"time":"2026-10-15T12:00:00Z","msg":"listening"}` + "\n")
	emit := func(min int, status, boot, pf int, heap int, render float64, reject string) {
		fmt.Fprintf(&b, `{"time":%q,"msg":"ingest","status":%d,"serial":"x","rssi":%d,"free_heap":%d,"boot":%d,"brownout":0,"post_fail":%d,"tls_fail":0,"render_ms":%g,"reject":%q}`+"\n",
			t0.Add(time.Duration(min)*time.Minute).Format(time.RFC3339), status, -70-min, heap, boot, pf, render, reject)
	}
	emit(0, 201, 5, 10, 90000, 30, "")
	emit(1, 201, 5, 12, 89000, 32, "")
	emit(2, 422, 5, 12, 88000, 0, "dark")
	// minutes 3..7: nothing stored
	emit(8, 201, 1, 3, 95000, 31, "") // rebooted: counters reset
	emit(9, 201, 1, 4, 94000, 29, "")
	fmt.Fprintf(&b, `{"time":"2026-10-15T12:05:00Z","msg":"ingest","status":201,"serial":"other"}`+"\n")

	ls, err := parse(strings.NewReader(b.String()), "x")
	if err != nil {
		t.Fatal(err)
	}
	r := analyse(ls)
	if r.Lines != 5 || r.Status[201] != 4 || r.Rejects["dark"] != 1 {
		t.Fatalf("counts: %+v", r)
	}
	// Minutes 12:00..12:09 = 10, judged at their ends. Stores at :00:30 and
	// :01:30 keep the ends 12:01–12:04 fresh (≤3 min); 12:05–12:08 are
	// stale (the 12:08:30 frame lands after the 12:08 end); 12:09, 12:10 fresh.
	if r.TotalMinutes != 10 || r.FreshMinutes != 6 {
		t.Fatalf("fresh %d/%d, want 6/10", r.FreshMinutes, r.TotalMinutes)
	}
	if r.LongestGap != 7*time.Minute {
		t.Fatalf("gap %s", r.LongestGap)
	}
	if r.PostFailDelta != 2+0+3+1 { // 10→12, 12→12, reset→3, 3→4
		t.Fatalf("post_fail delta %d", r.PostFailDelta)
	}
	if len(r.Boots) != 2 || r.HeapMin != 88000 || r.HeapLast != 94000 {
		t.Fatalf("boots/heap: %+v", r)
	}
	if r.RSSIMin != -79 || r.RSSIMax != -70 || r.RenderN != 4 || r.RenderP50 != 31 {
		t.Fatalf("rssi/render: %+v", r)
	}
}
