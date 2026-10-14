package render

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

// hardScene is the procedural lab frame (tools/styleproof -synthetic): dim,
// lit unevenly from the upper right, soft focus, four people — the case v3
// turned into lighting blobs.
func hardScene(t *testing.T) image.Image {
	t.Helper()
	b, err := os.ReadFile("testdata/hard-scene.jpg")
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// gray builds a w×h grey image from f(x, y).
func gray(w, h int, f func(x, y int) uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Pix[y*w+x] = f(x, y)
		}
	}
	return img
}

func decodeLevels(t *testing.T, b []byte) (*image.Paletted, map[uint8]int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("render decoded as %T, want paletted", img)
	}
	hist := map[uint8]int{}
	for _, v := range p.Pix {
		hist[v]++
	}
	return p, hist
}

// pngChunks lists the chunk types in file order.
func pngChunks(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	for i := 8; i+8 <= len(b); {
		n := int(b[i])<<24 | int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
		out = append(out, string(b[i+4:i+8]))
		i += 12 + n
	}
	return out
}

// The output contract: 320×240, paletted, ≤ 4 entries (2 for Šablon), the
// fixed ramp, only IHDR/PLTE/IDAT/IEND, under the 12 KB size cap.
func TestPipelineInvariants(t *testing.T) {
	img := hardScene(t)
	for _, levels := range []int{4, 2} {
		b, err := Pipeline(img, levels, nil, Defaults)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := decodeLevels(t, b)
		if p.Bounds().Dx() != GW || p.Bounds().Dy() != GH {
			t.Errorf("levels=%d: dims %v", levels, p.Bounds())
		}
		if len(p.Palette) != levels {
			t.Errorf("levels=%d: palette has %d entries", levels, len(p.Palette))
		}
		if p.Palette[0] != color.Color(ramp[0]) || p.Palette[levels-1] != color.Color(ramp[3]) {
			t.Errorf("levels=%d: palette ends are not ink/paper", levels)
		}
		for _, c := range pngChunks(t, b) {
			if c != "IHDR" && c != "PLTE" && c != "IDAT" && c != "IEND" {
				t.Errorf("levels=%d: ancillary chunk %q", levels, c)
			}
		}
		if len(b) > 12<<10 {
			t.Errorf("levels=%d: %d bytes > 12 KB cap", levels, len(b))
		}
	}
}

// The v3 failure, pinned: on a dim, unevenly lit scene the print must use
// at least three tones, and the lighting gradient alone must not create
// bands — a smooth ramp with no objects renders (almost) flat s2.
func TestLightingDoesNotDecideTones(t *testing.T) {
	b, err := Pipeline(hardScene(t), 4, nil, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	_, hist := decodeLevels(t, b)
	used := 0
	for _, n := range hist {
		if n > GW*GH/100 {
			used++
		}
	}
	if used < 3 {
		t.Errorf("hard scene uses %d tones (>1%% each), want ≥ 3: %v", used, hist)
	}

	ramp := gray(320, 240, func(x, y int) uint8 { return uint8(30 + x*150/320 + y*40/240) })
	b, err = Pipeline(ramp, 4, nil, Defaults)
	if err != nil {
		t.Fatal(err)
	}
	_, hist = decodeLevels(t, b)
	if hist[2] < GW*GH*97/100 {
		t.Errorf("object-free lighting ramp is not flat neutral: %v", hist)
	}
}

// Darker-than-surroundings reads as ink, lighter as paper, whatever the
// absolute brightness: the same scene at a third of the light gives the
// same picture (within a few cells).
func TestToneIsRelative(t *testing.T) {
	scene := func(scale int) image.Image {
		return gray(320, 240, func(x, y int) uint8 {
			v := 150
			if x >= 60 && x < 100 && y >= 80 && y < 200 { // dark figure
				v = 50
			}
			if x >= 200 && x < 280 && y >= 40 && y < 100 { // bright screen
				v = 250
			}
			return uint8(v * scale / 3)
		})
	}
	bright, _ := Pipeline(scene(3), 4, nil, Defaults)
	dim, _ := Pipeline(scene(1), 4, nil, Defaults)
	pb, _ := decodeLevels(t, bright)
	pd, _ := decodeLevels(t, dim)
	if got := pb.Pix[140*GW+80]; got != 0 {
		t.Errorf("figure centre = s%d, want s0", got)
	}
	if got := pb.Pix[70*GW+240]; got != 3 {
		t.Errorf("screen centre = s%d, want s3", got)
	}
	diff := 0
	for i := range pb.Pix {
		if pb.Pix[i] != pd.Pix[i] {
			diff++
		}
	}
	if diff > GW*GH/50 {
		t.Errorf("dim vs bright differ in %d cells (> 2%%)", diff)
	}
}

// Privacy dial: detail finer than the low-pass (a 1-cell checkerboard —
// eyes, text, a face's features) must not survive as structure.
func TestFinestDetailDies(t *testing.T) {
	img := gray(320, 240, func(x, y int) uint8 {
		if x >= 100 && x < 220 && y >= 60 && y < 180 && (x+y)%2 == 0 {
			return 200
		}
		if x >= 100 && x < 220 && y >= 60 && y < 180 {
			return 60
		}
		return 130
	})
	b, _ := Pipeline(img, 4, nil, Defaults)
	p, _ := decodeLevels(t, b)
	for y := 80; y < 160; y++ {
		for x := 120; x < 200; x++ {
			if p.Pix[y*GW+x] != p.Pix[120*GW+160] {
				t.Fatalf("checkerboard survived at (%d,%d)", x, y)
			}
		}
	}
}

// Thin shapes survive the cleanup (v3's mode filter erased arms and chair
// backs): a 3-cell dark bar on a mid wall stays ink end to end.
func TestThinShapesSurvive(t *testing.T) {
	img := gray(320, 240, func(x, y int) uint8 {
		if x >= 100 && x < 103 && y >= 40 && y < 200 {
			return 20
		}
		return 140
	})
	b, _ := Pipeline(img, 4, nil, Defaults)
	p, _ := decodeLevels(t, b)
	for y := 50; y < 190; y++ {
		if p.Pix[y*GW+101] > 1 {
			t.Fatalf("thin bar broken at y=%d (s%d)", y, p.Pix[y*GW+101])
		}
	}
}

// Mask cells are exactly level 0, and what is under a mask cannot leak
// out: changing the masked content changes no visible cell.
func TestMasks(t *testing.T) {
	m := []Rect{{X0: 100, Y0: 60, X1: 200, Y1: 160}, {X0: -10, Y0: -10, X1: 5, Y1: 5}}
	under := func(v uint8) image.Image {
		return gray(320, 240, func(x, y int) uint8 {
			if x >= 100 && x < 200 && y >= 60 && y < 160 {
				return v
			}
			return uint8(80 + x/4)
		})
	}
	a, _ := Pipeline(under(0), 4, m, Defaults)
	b, _ := Pipeline(under(255), 4, m, Defaults)
	if !bytes.Equal(a, b) {
		t.Error("masked content leaked into visible cells")
	}
	p, _ := decodeLevels(t, a)
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			in := (x >= 100 && x < 200 && y >= 60 && y < 160) || (x < 5 && y < 5)
			if in && p.Pix[y*GW+x] != 0 {
				t.Fatalf("mask cell (%d,%d) = s%d", x, y, p.Pix[y*GW+x])
			}
		}
	}
}

func TestNon43IsDimsReject(t *testing.T) {
	for _, r := range []image.Rectangle{image.Rect(0, 0, 240, 240), image.Rect(0, 0, 320, 180)} {
		if _, err := Pipeline(image.NewGray(r), 4, nil, Defaults); err == nil {
			t.Errorf("%v: want dims reject", r)
		} else if reason, _ := IsReject(err); reason != "dims" {
			t.Errorf("%v: reason %q", r, reason)
		}
	}
}

// Area averaging is exact: a VGA frame of 2×2 blocks averages to the block
// means; a QQVGA frame replicates each pixel into 2×2 cells.
func TestAreaAverageIsExact(t *testing.T) {
	vga := gray(640, 480, func(x, y int) uint8 { return uint8((x/2+y/2)%7*30 + (x+y)%2*2) })
	g, err := lumaGrid(vga)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]int{{0, 0}, {13, 7}, {319, 239}} {
		x, y := c[0], c[1]
		want := uint8((x+y)%7*30 + 1)
		if got := g[y*GW+x]; got != want {
			t.Errorf("VGA cell %v = %d, want %d", c, got, want)
		}
	}
	qq := gray(160, 120, func(x, y int) uint8 { return uint8(x + y) })
	g, _ = lumaGrid(qq)
	if g[21*GW+41] != uint8(20+10) {
		t.Errorf("QQVGA replicate: got %d", g[21*GW+41])
	}
}
