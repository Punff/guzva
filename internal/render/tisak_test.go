package render

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

// scene renders a procedural 4:3 "room": gradients, a bright window, dark
// furniture blocks, and head-sized dark blobs — flat enough to be legible,
// textured enough to exercise the mode filter. Seeded, so every run draws the
// same room. NEVER a real venue frame: real frames must never be committed.
func scene(t *testing.T, w, h int, seed int64) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Vertical gradient (bright ceiling → dark floor) + window block.
			v := 200 - y*160/h
			if x > w*3/4 && y < h/3 {
				v = 235 // window
			}
			if x > w/4 && x < w/2 && y > h/2 {
				v = 40 // furniture
			}
			v += rnd.Intn(16) - 8 // sensor noise
			img.SetRGBA(x, y, color.RGBA{uint8(clamp(v)), uint8(clamp(v)), uint8(clamp(v + 6)), 255})
		}
	}
	// Two head-sized dark blobs (people).
	for _, hx := range []int{w / 3, w * 2 / 3} {
		for dy := -h / 20; dy <= h/20; dy++ {
			for dx := -w / 60; dx <= w/60; dx++ {
				x, y := hx+dx, h/2+dy
				if x >= 0 && x < w && y >= 0 && y < h {
					img.SetRGBA(x, y, color.RGBA{30, 28, 26, 255})
				}
			}
		}
	}
	return img
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// decodePNG parses our own output back into levels + palette, so the
// invariants assert on bytes, not on our encoder's good intentions.
func decodePNG(t *testing.T, b []byte) (levels []uint8, pal color.Palette) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode own png: %v", err)
	}
	p, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("output is %T, want *image.Paletted", img)
	}
	return p.Pix, p.Palette
}

func TestPipelineInvariants(t *testing.T) {
	out, err := Pipeline(scene(t, 640, 480, 7), 4, Calib{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty output")
	}
	levels, pal := decodePNG(t, out)

	// Exactly 160×120.
	if len(levels) != GW*GH {
		t.Fatalf("grid = %d cells, want %d", len(levels), GW*GH)
	}
	// ≤ 4 palette entries, in the fixed ramp order.
	if len(pal) > 4 {
		t.Fatalf("palette has %d entries, want ≤ 4", len(pal))
	}
	for i, c := range pal {
		r, g, b, _ := c.RGBA()
		want := ramp[i]
		if uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B {
			t.Fatalf("palette[%d] = #%02x%02x%02x, want ramp #%02x%02x%02x",
				i, r>>8, g>>8, b>>8, want.R, want.G, want.B)
		}
	}
	// Every cell references a palette entry that exists.
	for i, lv := range levels {
		if int(lv) >= len(pal) {
			t.Fatalf("cell %d references palette entry %d of %d", i, lv, len(pal))
		}
	}
	// No ancillary chunks: IHDR, PLTE, IDAT, IEND only. EXIF, text and
	// profiles cannot survive — asserted on the raw bytes.
	for _, c := range pngChunks(t, out) {
		switch c {
		case "IHDR", "PLTE", "IDAT", "IEND":
		default:
			t.Fatalf("ancillary chunk %q in output", c)
		}
	}
}

// pngChunks lists chunk types in order from the raw bytes.
func pngChunks(t *testing.T, b []byte) []string {
	t.Helper()
	if len(b) < 8 || string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("not a png (magic %q)", b[:min(8, len(b))])
	}
	var out []string
	r := bytes.NewReader(b[8:])
	for {
		var length uint32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			t.Fatalf("chunk length: %v", err)
		}
		typ := make([]byte, 4)
		if _, err := r.Read(typ); err != nil {
			t.Fatalf("chunk type: %v", err)
		}
		out = append(out, string(typ))
		if _, err := r.Seek(int64(length)+4, 1); err != nil {
			t.Fatalf("chunk skip: %v", err)
		}
		if string(typ) == "IEND" {
			return out
		}
	}
}

func TestPipelineSizeBound(t *testing.T) {
	// A busy scene is the worst case for a palette PNG; the bound guards
	// accidental dither or noise leaking into the output.
	busy := scene(t, 640, 480, 99)
	for y := 0; y < 480; y += 3 {
		for x := 0; x < 640; x += 3 {
			busy.(*image.RGBA).Set(x, y, color.RGBA{10, 10, 10, 255})
		}
	}
	for _, tc := range []struct {
		name   string
		img    image.Image
		levels int
	}{
		{"tisak-clean", scene(t, 640, 480, 7), 4},
		{"tisak-busy", busy, 4},
		{"sablon", scene(t, 640, 480, 7), 2},
	} {
		out, err := Pipeline(tc.img, tc.levels, Calib{})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes", tc.name, len(out))
		// DESIGN.md §5.11: a render over 6 KB fails the test.
		if len(out) > 6<<10 {
			t.Errorf("%s = %d bytes, over the 6 KB bound", tc.name, len(out))
		}
	}
}

func TestPipelineDeterministic(t *testing.T) {
	img := scene(t, 320, 240, 3)
	first, err := Pipeline(img, 4, Calib{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		got, err := Pipeline(scene(t, 320, 240, 3), 4, Calib{})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestMasksAreLevelZeroWhateverTheInput(t *testing.T) {
	// A bright scene with a mask over the brightest part: after the pipeline
	// every mask cell must read level 0, including the 1-cell border the blur
	// would otherwise smear a gradient across.
	img := scene(t, 640, 480, 7)
	m := []Rect{{X0: 100, Y0: 80, X1: 140, Y1: 110}}
	out, err := Pipeline(img, 4, Calib{Masks: m})
	if err != nil {
		t.Fatal(err)
	}
	levels, _ := decodePNG(t, out)
	for y := 80; y < 110; y++ {
		for x := 100; x < 140; x++ {
			if levels[y*GW+x] != 0 {
				t.Fatalf("mask cell (%d,%d) = level %d, want 0", x, y, levels[y*GW+x])
			}
		}
	}
	// Masks beyond the cap are ignored, not shifted: the fifth rect must not
	// eat the fourth's slot. Assert on a white grid so pre-existing zeros
	// cannot hide a miss.
	white := make([]uint8, GW*GH)
	for i := range white {
		white[i] = 255
	}
	many := []Rect{{X0: 1, Y0: 1, X1: 2, Y1: 2}, {X0: 3, Y0: 3, X1: 4, Y1: 4},
		{X0: 5, Y0: 5, X1: 6, Y1: 6}, {X0: 7, Y0: 7, X1: 8, Y1: 8},
		{X0: 9, Y0: 9, X1: 10, Y1: 10}}
	applyMasks(white, many)
	for _, c := range [][2]int{{1, 1}, {3, 3}, {5, 5}, {7, 7}} {
		if white[c[1]*GW+c[0]] != 0 {
			t.Fatalf("mask %v not applied", c)
		}
	}
	if white[9*GW+9] != 255 {
		t.Fatal("fifth mask applied — the cap moved")
	}
}

func TestNon43IsDimsReject(t *testing.T) {
	for _, dim := range [][2]int{{640, 360}, {800, 500}, {100, 100}} {
		img := image.NewRGBA(image.Rect(0, 0, dim[0], dim[1]))
		if _, err := Pipeline(img, 4, Calib{}); err == nil {
			t.Fatalf("%dx%d accepted, want dims reject", dim[0], dim[1])
		} else if r, ok := IsReject(err); !ok || r != "dims" {
			t.Fatalf("%dx%d: reason %q, want dims", dim[0], dim[1], r)
		}
	}
	// Exact 4:3 at several widths, including odd ones (Q8 edge weights).
	for _, w := range []int{160, 320, 480, 640, 800, 1600} {
		img := scene(t, w, w*3/4, 5)
		if _, err := Pipeline(img, 4, Calib{}); err != nil {
			t.Fatalf("%dx%d rejected: %v", w, w*3/4, err)
		}
	}
}

func TestQuantThresholdsAndInvert(t *testing.T) {
	g := []uint8{0, 63, 64, 127, 128, 191, 192, 255}
	q := quant(g, 4, defaultThresholds, 0, false)
	for i, want := range []uint8{0, 0, 1, 1, 2, 2, 3, 3} {
		if q[i] != want {
			t.Fatalf("luma %d → level %d, want %d", g[i], q[i], want)
		}
	}
	qi := quant(g, 4, defaultThresholds, 0, true)
	for i, want := range []uint8{3, 3, 2, 2, 1, 1, 0, 0} {
		if qi[i] != want {
			t.Fatalf("inverted luma %d → level %d, want %d", g[i], qi[i], want)
		}
	}
	// Bias shifts the bands without moving the thresholds.
	qb := quant([]uint8{60}, 4, defaultThresholds, 10, false)
	if qb[0] != 1 {
		t.Fatalf("biased luma 60+10 → level %d, want 1", qb[0])
	}
	// Mono: one threshold, two levels.
	qm := quant(g, 2, defaultThresholds, 0, false)
	for i, want := range []uint8{0, 0, 0, 0, 1, 1, 1, 1} {
		if qm[i] != want {
			t.Fatalf("mono luma %d → level %d, want %d", g[i], qm[i], want)
		}
	}
}

func TestAreaAverageIsExact(t *testing.T) {
	// Left half white, right half black at 640×480: the grid's middle column
	// boundary falls exactly between cells, so cells are pure 255 or pure 0 —
	// any smear means the averaging windows overlap.
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			v := uint8(0)
			if x < 320 {
				v = 255
			}
			img.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	g, err := lumaGrid(img)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			want := uint8(0)
			if x < GW/2 {
				want = 255
			}
			if g[y*GW+x] != want {
				t.Fatalf("cell (%d,%d) = %d, want %d", x, y, g[y*GW+x], want)
			}
		}
	}
}
