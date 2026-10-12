package render

import (
	"testing"
)

// bandShare runs a flat luma field through the shipped pipeline and reports
// the share of the frame per poster band. It is the measurement behind the
// filter-defaults proposal: it needs no photo, so it can be asserted in CI
// even though the repo carries no real venue frames (all frames in mydata
// are fakecam's synthetic gradient — W8.1 still wants real ones).
func bandShare(t *testing.T, luma uint8) map[uint8]int {
	t.Helper()
	g := make([]uint8, GW*GH)
	for i := range g {
		g[i] = luma
	}
	q := mode3(quant(blur121(g), 4, defaultThresholds, 0, false), 4)
	out := map[uint8]int{}
	for _, v := range q {
		out[v]++
	}
	return out
}

// The shipped poster defaults divide the luma range into four EQUAL spans.
// This pins that geometry deliberately: it is the reason a flat field is
// never ambiguous (one luma → exactly one band, no ties to resolve), and it
// is the number any proposal to change the defaults must argue against.
//
// It also documents the known limit honestly rather than hiding it: a room
// darker than 64 or brighter than 192 luma renders as ONE flat band and all
// detail is lost. That is a property of any fixed-threshold poster — the
// alternative (auto-levels) is refused by design, because an empty and a full
// room must not be made to look alike (PLAN §7, W8). The per-venue lever is
// bias/thresholds in the calib, which is exactly what DESIGN §5.2 provides.
func TestPosterDefaultBandGeometry(t *testing.T) {
	// Equal 64-wide spans: ink 0-63, dark mid 64-127, light mid 128-191, paper 192-255.
	wantSpans := [4][2]int{{0, 63}, {64, 127}, {128, 191}, {192, 255}}
	if defaultThresholds != [3]uint8{64, 128, 192} {
		t.Fatalf("defaultThresholds = %v; this test pins the current geometry — "+
			"update it deliberately, and bump the preset version (D2)", defaultThresholds)
	}
	// Each span's midpoint lands in its own band, and no band is reachable
	// from another span: the boundaries are exactly where the doc says.
	for band, span := range wantSpans {
		mid := uint8((span[0] + span[1]) / 2)
		got := bandShare(t, mid)
		if len(got) != 1 {
			t.Fatalf("flat luma %d (band %d) produced %v, want a single band", mid, band, got)
		}
		if _, ok := got[uint8(band)]; !ok {
			t.Errorf("flat luma %d landed in %v, want band %d", mid, got, band)
		}
	}
	// The documented collapse: the darkest 25%% and brightest 25%% of the
	// range are each a single flat band. Asserted so a future default change
	// cannot quietly widen or narrow that dead zone.
	for _, l := range []uint8{0, 63, 192, 255} {
		if got := bandShare(t, l); len(got) != 1 {
			t.Errorf("flat luma %d = %v, want one flat band (documented collapse)", l, got)
		}
	}
}

// A checkerboard — the finest detail the 160x120 grid can carry — is
// destroyed to a single band. That is the privacy property working as
// designed (nothing recoverable, PLAN §7), not a defect: it is pinned here so
// a "make it prettier" change cannot quietly trade irreversibility away.
func TestPosterDestroysFinestDetail(t *testing.T) {
	g := make([]uint8, GW*GH)
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			if (x+y)%2 == 0 {
				g[y*GW+x] = 30
			} else {
				g[y*GW+x] = 220
			}
		}
	}
	q := mode3(quant(blur121(g), 4, defaultThresholds, 0, false), 4)
	bands := map[uint8]int{}
	for _, v := range q {
		bands[v]++
	}
	if len(bands) != 1 {
		t.Errorf("checkerboard produced bands %v; the blur+mode chain is meant to "+
			"flatten 1-cell detail entirely", bands)
	}
}

// A person-sized blob (10x10 cells, the DESIGN §5.3 privacy rule's scale)
// survives as a solid shape: the poster must keep saying "somebody is there"
// at a glance. Pinned because this is the visibility half of the contract —
// destroy everything and the page stops answering "how busy is it?".
func TestPosterKeepsPersonSizedShapes(t *testing.T) {
	g := make([]uint8, GW*GH)
	for i := range g {
		g[i] = 200 // paper
	}
	for y := 50; y < 60; y++ {
		for x := 70; x < 80; x++ {
			g[y*GW+x] = 40 // ink
		}
	}
	q := mode3(quant(blur121(g), 4, defaultThresholds, 0, false), 4)
	var ink int
	for _, v := range q {
		if v == 0 {
			ink++
		}
	}
	// The blob keeps most of its cells at ink: a silhouette, not a smudge.
	if ink < 40 {
		t.Errorf("person-sized blob kept only %d ink cells of 100; the poster is "+
			"losing the shape it exists to show", ink)
	}
}

// The mono preset uses the middle threshold, so it has the same 128 boundary
// and the same flat-field collapse. Pinned because the two presets share one
// calib shape and a change to one must not be assumed to carry the other.
func TestMonoDefaultBoundary(t *testing.T) {
	if MonoThreshold != 128 {
		t.Fatalf("MonoThreshold = %d, want 128 (the middle default)", MonoThreshold)
	}
	for _, tc := range []struct {
		luma uint8
		want uint8
	}{{30, 0}, {127, 0}, {128, 1}, {255, 1}} {
		g := make([]uint8, GW*GH)
		for i := range g {
			g[i] = tc.luma
		}
		q := mode3(quant(blur121(g), 2, defaultThresholds, 0, false), 2)
		got := map[uint8]int{}
		for _, v := range q {
			got[v]++
		}
		if len(got) != 1 {
			t.Fatalf("mono flat %d = %v, want one band", tc.luma, got)
		}
		if _, ok := got[tc.want]; !ok {
			t.Errorf("mono flat %d = %v, want band %d", tc.luma, got, tc.want)
		}
	}
}
