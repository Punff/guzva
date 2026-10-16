// Command soak turns the ingest journal into the Phase 1 exit numbers
// (PLAN §15): fresh-minute %, status mix, rssi, heap trend, counter deltas
// (boot, brownout, post_fail, tls_fail) and render latency. It reads the
// JSON journal lines on stdin — the one-line-per-ingest-outcome record that
// logOutcome writes — so it never needs DB or image access (§1):
//
//	ssh root@guzva 'journalctl -u guzva -o cat --since "-48h"' \
//	    | go run ./tools/soak -serial bd2a58b
//
// (or `make soak`). Fresh-minute % uses the page's own rule: a minute is
// fresh when a frame was stored within venue.StaleAfter before its end.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"guzva/internal/venue"
)

type line struct {
	Time     time.Time `json:"time"`
	Msg      string    `json:"msg"`
	Status   int       `json:"status"`
	Serial   string    `json:"serial"`
	RSSI     int       `json:"rssi"`
	FreeHeap uint32    `json:"free_heap"`
	Boot     uint32    `json:"boot"`
	Brownout uint32    `json:"brownout"`
	PostFail uint32    `json:"post_fail"`
	TLSFail  uint32    `json:"tls_fail"`
	RenderMS float64   `json:"render_ms"`
	Reject   string    `json:"reject"`
}

// report is everything soak prints; kept separate from the printing so the
// test pins the arithmetic, not the layout.
type report struct {
	From, To      time.Time
	Lines         int
	Status        map[int]int
	Rejects       map[string]int
	FreshMinutes  int
	TotalMinutes  int
	LongestGap    time.Duration
	RSSIMin       int
	RSSIMed       int
	RSSIMax       int
	HeapFirst     uint32
	HeapLast      uint32
	HeapMin       uint32
	Boots         []uint32 // distinct boot counters seen, in order
	BrownoutDelta uint32
	PostFailDelta uint32
	TLSFailDelta  uint32
	RenderP50     float64
	RenderP95     float64
	RenderN       int
}

func parse(r io.Reader, serial string) ([]line, error) {
	var out []line
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 || b[0] != '{' {
			continue // journald noise, systemd lines
		}
		var l line
		if json.Unmarshal(b, &l) != nil || l.Msg != "ingest" {
			continue
		}
		if serial != "" && l.Serial != serial {
			continue
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

// delta sums counter growth across reboots: a counter that goes down was
// reset (NVS wipe/reflash), so growth restarts from the new value.
func delta(vals []uint32) uint32 {
	var d uint32
	for i := 1; i < len(vals); i++ {
		if vals[i] >= vals[i-1] {
			d += vals[i] - vals[i-1]
		} else {
			d += vals[i]
		}
	}
	return d
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p*float64(len(sorted)-1) + 0.5)
	return sorted[i]
}

func analyse(ls []line) report {
	rep := report{Status: map[int]int{}, Rejects: map[string]int{}, Lines: len(ls)}
	if len(ls) == 0 {
		return rep
	}
	sort.SliceStable(ls, func(i, j int) bool { return ls[i].Time.Before(ls[j].Time) })
	rep.From, rep.To = ls[0].Time, ls[len(ls)-1].Time

	var stored []time.Time
	var rssi []int
	var renders []float64
	var brown, pf, tf []uint32
	rep.HeapFirst, rep.HeapMin = ls[0].FreeHeap, ls[0].FreeHeap
	for _, l := range ls {
		rep.Status[l.Status]++
		if l.Reject != "" {
			rep.Rejects[l.Reject]++
		}
		if l.Status == 201 {
			stored = append(stored, l.Time)
		}
		if l.RSSI != 0 {
			rssi = append(rssi, l.RSSI)
		}
		if l.FreeHeap != 0 {
			rep.HeapLast = l.FreeHeap
			if l.FreeHeap < rep.HeapMin {
				rep.HeapMin = l.FreeHeap
			}
		}
		if l.RenderMS > 0 {
			renders = append(renders, l.RenderMS)
		}
		if n := len(rep.Boots); n == 0 || rep.Boots[n-1] != l.Boot {
			rep.Boots = append(rep.Boots, l.Boot)
		}
		brown, pf, tf = append(brown, l.Brownout), append(pf, l.PostFail), append(tf, l.TLSFail)
	}
	rep.BrownoutDelta, rep.PostFailDelta, rep.TLSFailDelta = delta(brown), delta(pf), delta(tf)

	if len(rssi) > 0 {
		sort.Ints(rssi)
		rep.RSSIMin, rep.RSSIMed, rep.RSSIMax = rssi[0], rssi[len(rssi)/2], rssi[len(rssi)-1]
	}
	sort.Float64s(renders)
	rep.RenderN, rep.RenderP50, rep.RenderP95 = len(renders), pct(renders, .50), pct(renders, .95)

	// Walk the window minute by minute; a minute is fresh when the latest
	// stored frame at its end is younger than StaleAfter.
	start, end := rep.From.Truncate(time.Minute), rep.To.Truncate(time.Minute)
	j := -1
	for m := start; !m.After(end); m = m.Add(time.Minute) {
		edge := m.Add(time.Minute)
		for j+1 < len(stored) && stored[j+1].Before(edge) {
			j++
		}
		rep.TotalMinutes++
		if j >= 0 && edge.Sub(stored[j]) <= venue.StaleAfter {
			rep.FreshMinutes++
		}
	}
	for i := 1; i < len(stored); i++ {
		if g := stored[i].Sub(stored[i-1]); g > rep.LongestGap {
			rep.LongestGap = g
		}
	}
	return rep
}

func print(w io.Writer, r report) {
	if r.Lines == 0 {
		fmt.Fprintln(w, "no ingest lines")
		return
	}
	fmt.Fprintf(w, "window      %s → %s (%s)\n", r.From.UTC().Format(time.RFC3339),
		r.To.UTC().Format(time.RFC3339), r.To.Sub(r.From).Round(time.Minute))
	fmt.Fprintf(w, "fresh min   %d / %d = %.1f %%   (missed %.1f %%; pilot gate <5 %%)\n",
		r.FreshMinutes, r.TotalMinutes, 100*float64(r.FreshMinutes)/float64(r.TotalMinutes),
		100-100*float64(r.FreshMinutes)/float64(r.TotalMinutes))
	fmt.Fprintf(w, "longest gap %s between stored frames\n", r.LongestGap.Round(time.Second))
	codes := make([]int, 0, len(r.Status))
	for c := range r.Status {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	fmt.Fprint(w, "status     ")
	for _, c := range codes {
		fmt.Fprintf(w, " %d×%d", c, r.Status[c])
	}
	fmt.Fprintln(w)
	if len(r.Rejects) > 0 {
		fmt.Fprintf(w, "rejects     %v\n", r.Rejects)
	}
	fmt.Fprintf(w, "rssi        min %d / median %d / max %d dBm (install gate ≥ −65)\n", r.RSSIMin, r.RSSIMed, r.RSSIMax)
	fmt.Fprintf(w, "free heap   first %d / last %d / min %d\n", r.HeapFirst, r.HeapLast, r.HeapMin)
	fmt.Fprintf(w, "boots       %v (%d reboot(s))\n", r.Boots, len(r.Boots)-1)
	fmt.Fprintf(w, "counters    +%d brownout, +%d post_fail, +%d tls_fail\n", r.BrownoutDelta, r.PostFailDelta, r.TLSFailDelta)
	fmt.Fprintf(w, "render ms   p50 %.1f / p95 %.1f (n=%d)\n", r.RenderP50, r.RenderP95, r.RenderN)
}

func main() {
	serial := flag.String("serial", "", "only this device serial (default: all)")
	flag.Parse()
	ls, err := parse(os.Stdin, *serial)
	if err != nil {
		fmt.Fprintln(os.Stderr, "soak:", err)
		os.Exit(1)
	}
	print(os.Stdout, analyse(ls))
}
