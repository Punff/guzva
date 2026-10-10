// Tisak pipeline (DESIGN.md §5.2, poster@1 / mono@1).
//
// All stages are pure, integer-only, stdlib-only. Input: a decoded frame;
// output: a 160×120 paletted PNG. Same inputs → same bytes, always: no clock,
// no randomness, no globals. (The venue-local HH:MM for the stamp and the
// venue calib arrive as arguments — see Calib.)
//
// Stage order matters for privacy, not just looks:
//  1. luminance only — colour (skin, hair, clothes, branding) is discarded
//     first and never converted back. EXIF never survives the decode.
//  2. area-average to the 160×120 grid — the mosaic IS the grid.
//  3. privacy masks — constant luma over installer-drawn rects, BEFORE blur
//     so nothing inside leaks outward through the blur kernel (and re-applied
//     after, so the edge bleed blur leaves behind cannot smuggle a gradient
//     back in — belt and braces, both directions).
//  4. 3×3 binomial blur — sub-cell detail and sensor noise die here.
//  5. quantise to fixed thresholds — flat bands, ~2 bits of tone. Fixed
//     thresholds, never per-frame auto-levels: auto-levels would pump
//     brightness between frames and make an empty room and a full one render
//     the same tone mix.
//  6. 3×3 mode filter — speckle where a smooth area sits on a threshold.
//  7. HH:MM stamp, burned into the bottom-left on its own plate — a
//     screenshot shared in a chat dates itself (§5.6).
//  8. paletted PNG, fixed palette, best compression — tiny, lossless, no
//     JPEG ringing on flat art, no ancillary chunks.
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// Grid geometry. 160×120 = 19,200 cells; a VGA frame has 16× more samples,
// QVGA (the recommended capture, DESIGN.md ⚑10) has 4×.
const (
	GW = 160
	GH = 120
)

// Still ramp (DESIGN.md §5.4). Fixed in both UI themes; the order is part of
// the preset version — changing a colour ships a new preset version, because
// old frames on disk keep their meaning.
var ramp = [4]color.RGBA{
	{0x12, 0x20, 0x2d, 0xff}, // s0 ink — people, hair, shadows, stamp plate
	{0x33, 0x60, 0x7f, 0xff}, // s1 dark mid
	{0x7e, 0xa5, 0xc0, 0xff}, // s2 light mid
	{0xf2, 0xed, 0xe2, 0xff}, // s3 paper — walls, windows, stamp digits
}

// Default thresholds (DESIGN.md §5.2 stage 5). A venue calib may move them,
// but they are still fixed per venue — never per frame.
var defaultThresholds = [3]uint8{64, 128, 192}

// MonoThreshold is the single Šablon threshold: the middle default, so one
// calib shape serves both presets.
const MonoThreshold = 128

// Rect is a privacy mask in grid cells, half-open [X0,X1)×[Y0,Y1).
// Installer-drawn, static geometry only — never detection output (D9).
type Rect struct {
	X0, Y0, X1, Y1 int
}

// Calib is the per-venue render calibration (DESIGN.md ⚑5): threshold moves,
// a luma bias, an optional inversion for negative rooms, the static masks,
// and which preset they were chosen for. All numbers and geometry —
// consistent with §1, because no raw frame is ever stored to produce them.
type Calib struct {
	T      [3]uint8 // quantise thresholds; zero value means defaults
	Bias   int      // added to luma before thresholding (-255..255)
	Invert bool     // render the negative (dark rooms with bright doors)
	Masks  []Rect   // ≤ MaxMasks static privacy rects, grid coords
	Preset string   // preset the calib was chosen against ("poster"/"mono")
}

// MaxMasks bounds the installer-drawn geometry (DESIGN.md §5.2 stage 3).
const MaxMasks = 4

// thresholds resolves the zero value to the defaults.
func (c Calib) thresholds() [3]uint8 {
	if c.T == [3]uint8{} {
		return defaultThresholds
	}
	return c.T
}

// clampRect clips a mask to the grid. Degenerate rects (empty or fully
// outside) vanish — an installer drag that misses the grid must not shift
// every other mask's meaning.
func clampRect(r Rect) (Rect, bool) {
	if r.X0 < 0 {
		r.X0 = 0
	}
	if r.Y0 < 0 {
		r.Y0 = 0
	}
	if r.X1 > GW {
		r.X1 = GW
	}
	if r.Y1 > GH {
		r.Y1 = GH
	}
	return r, r.X0 < r.X1 && r.Y0 < r.Y1
}

// lumaGrid decodes to luminance and area-averages onto the grid (stages 1–2).
//
// Colour dies first: the Y plane of a decoded YCbCr is taken directly (it is
// full-resolution regardless of chroma subsampling); anything else goes
// through color.GrayModel. Either way, no colour value survives this function.
//
// Only 4:3 inputs are accepted — the grid is 4:3, and stretching any other
// aspect onto it would silently change what a cell means. Anything else is a
// 422 dims rejection, not a guess.
//
// Averaging is exact integer math (Q8 fixed-point edge weights): each cell
// accumulates overlap-weighted source luma, so 160/320/640/800/1600-wide
// inputs average whole blocks and odd widths still divide exactly.
func lumaGrid(img image.Image) ([]uint8, error) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 {
		return nil, Reject("dims")
	}
	if sw*3 != sh*4 {
		return nil, Reject("dims")
	}

	y, yw, xw := lumaPlane(img, b, sw, sh)

	g := make([]uint8, GW*GH)
	const fp = 256 // Q8
	cellW, cellH := sw*fp/GW, sh*fp/GH
	for gy := 0; gy < GH; gy++ {
		y0, y1 := gy*sh*fp/GH, (gy+1)*sh*fp/GH
		for gx := 0; gx < GW; gx++ {
			x0, x1 := gx*sw*fp/GW, (gx+1)*sw*fp/GW
			var sum uint64
			for py := y0 / fp; py*fp < y1; py++ {
				oy := overlap(py*fp, py*fp+fp, y0, y1)
				if oy == 0 {
					continue
				}
				for px := x0 / fp; px*fp < x1; px++ {
					ox := overlap(px*fp, px*fp+fp, x0, x1)
					if ox == 0 {
						continue
					}
					sum += uint64(y[py*yw+px*xw]) * uint64(ox) * uint64(oy)
				}
			}
			g[gy*GW+gx] = uint8(sum / uint64(cellW) / uint64(cellH))
		}
	}
	return g, nil
}

// lumaPlane extracts full-resolution luma plus strides. YCbCr decodes keep
// their Y plane (no resampling, no colour math); everything else is reduced
// through color.GrayModel into a scratch plane.
func lumaPlane(img image.Image, b image.Rectangle, sw, sh int) (y []uint8, yw, xw int) {
	if yc, ok := img.(*image.YCbCr); ok {
		// Y is full-resolution in Go's representation regardless of the
		// chroma subsampling, but its origin follows the image bounds.
		strideY := yc.YStride
		off := (b.Min.Y-yc.Rect.Min.Y)*strideY + (b.Min.X - yc.Rect.Min.X)
		return yc.Y[off : off+(sh-1)*strideY+sw], strideY, 1
	}
	y = make([]uint8, sw*sh)
	for j := 0; j < sh; j++ {
		for i := 0; i < sw; i++ {
			y[j*sw+i] = color.GrayModel.Convert(img.At(b.Min.X+i, b.Min.Y+j)).(color.Gray).Y
		}
	}
	return y, sw, 1
}

// overlap is the length of the intersection of [a0,a1) and [b0,b1).
func overlap(a0, a1, b0, b1 int) int {
	lo := max(a0, b0)
	hi := min(a1, b1)
	if hi <= lo {
		return 0
	}
	return hi - lo
}

// applyMasks overwrites mask rects with luma 0 (stage 3). Masks beyond
// MaxMasks are ignored — silently accepting a fifth rect would mean two
// installers disagree about what is covered.
func applyMasks(g []uint8, masks []Rect) {
	n := 0
	for _, m := range masks {
		if n >= MaxMasks {
			break
		}
		n++
		r, ok := clampRect(m)
		if !ok {
			continue
		}
		for y := r.Y0; y < r.Y1; y++ {
			for x := r.X0; x < r.X1; x++ {
				g[y*GW+x] = 0
			}
		}
	}
}

// blur121 is the 3×3 binomial blur, [1 2 1]²/16 (stage 4, σ ≈ 0.85 cells).
// Separable, edge-clamped, round-half-up — deterministic to the bit.
func blur121(g []uint8) []uint8 {
	tmp := make([]uint8, len(g))
	out := make([]uint8, len(g))
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			xm, xp := max(x-1, 0), min(x+1, GW-1)
			tmp[y*GW+x] = uint8((int(g[y*GW+xm]) + 2*int(g[y*GW+x]) + int(g[y*GW+xp]) + 2) >> 2)
		}
	}
	for y := 0; y < GH; y++ {
		ym, yp := max(y-1, 0)*GW, min(y+1, GH-1)*GW
		for x := 0; x < GW; x++ {
			out[y*GW+x] = uint8((int(tmp[ym+x]) + 2*int(tmp[y*GW+x]) + int(tmp[yp+x]) + 2) >> 2)
		}
	}
	return out
}

// quant maps luma to levels 0..levels-1 (stage 5). Bias shifts the luma
// before the fixed thresholds; invert renders the negative. Poster uses all
// three thresholds; mono uses the middle one.
func quant(g []uint8, levels int, t [3]uint8, bias int, invert bool) []uint8 {
	q := make([]uint8, len(g))
	if levels == 2 {
		for i, v := range g {
			lv := 0
			if int(v)+bias >= int(t[1]) {
				lv = 1
			}
			if invert {
				lv = 1 - lv
			}
			q[i] = uint8(lv)
		}
		return q
	}
	for i, v := range g {
		l := int(v) + bias
		lv := 0
		switch {
		case l >= int(t[2]):
			lv = 3
		case l >= int(t[1]):
			lv = 2
		case l >= int(t[0]):
			lv = 1
		}
		if invert {
			lv = 3 - lv
		}
		q[i] = uint8(lv)
	}
	return q
}

// mode3 is the 3×3 majority filter (stage 6): ties keep the centre, so a
// checkerboard the grid cannot resolve stays put instead of strobing between
// frames. Edge-clamped like the blur.
func mode3(q []uint8, levels int) []uint8 {
	out := make([]uint8, len(q))
	var counts [4]int
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			for i := range counts {
				counts[i] = 0
			}
			for dy := -1; dy <= 1; dy++ {
				yy := min(max(y+dy, 0), GH-1)
				for dx := -1; dx <= 1; dx++ {
					xx := min(max(x+dx, 0), GW-1)
					counts[q[yy*GW+xx]]++
				}
			}
			best, bestN := int(q[y*GW+x]), counts[q[y*GW+x]]
			for lv := 0; lv < levels; lv++ {
				if counts[lv] > bestN {
					best, bestN = lv, counts[lv]
				}
			}
			out[y*GW+x] = uint8(best)
		}
	}
	return out
}

// Stamp geometry (DESIGN.md §5.2 stage 7). "HH:MM" is 5 glyphs × 5 columns +
// 4 single-column gaps = 29 wide; the plate adds 2 cells of S0 around the 7
// rows of digits. Bottom-left with a 2-cell margin, so the stamp never touches
// the frame edge and the rect is stable enough to assert against.
const (
	StampCols, StampRows     = 29, 7
	StampPlateW, StampPlateH = 29, 11
	StampX, StampY           = 2, GH - 2 - StampPlateH
)

// stampRect is the plate rectangle, for the "stamp cells only in the stamp
// rectangle" invariant.
func stampRect() Rect { return Rect{StampX, StampY, StampX + StampPlateW, StampY + StampPlateH} }

// font57 is the 5×7 digit font, column-major, LSB = top row. Digits 0-9 and
// the colon; nothing else can appear in an HH:MM stamp, so nothing else is
// stored — a full font would be bytes spent on glyphs that can never render.
var font57 = map[byte][5]uint8{
	'0': {0x3e, 0x51, 0x49, 0x45, 0x3e},
	'1': {0x00, 0x42, 0x7f, 0x40, 0x00},
	'2': {0x42, 0x61, 0x51, 0x49, 0x46},
	'3': {0x21, 0x41, 0x45, 0x4b, 0x31},
	'4': {0x18, 0x14, 0x12, 0x7f, 0x10},
	'5': {0x27, 0x45, 0x45, 0x45, 0x39},
	'6': {0x3c, 0x4a, 0x49, 0x49, 0x30},
	'7': {0x01, 0x71, 0x09, 0x05, 0x03},
	'8': {0x36, 0x49, 0x49, 0x49, 0x36},
	'9': {0x06, 0x49, 0x49, 0x29, 0x1e},
	':': {0x00, 0x14, 0x00, 0x00, 0x00},
}

// stamp burns hhmm ("HH:MM", venue-local, already formatted by the caller)
// into the level grid (stage 7): S3 digits on an S0 plate. It runs on levels,
// not luma, so the digits are exactly palette entries — never a dithered
// almost-white that a screenshot would smear.
//
// A malformed string stamps nothing rather than something wrong: a half-
// burned time would date every shared screenshot incorrectly, which is worse
// than an undated one.
func stamp(q []uint8, hhmm string) {
	if len(hhmm) != 5 || hhmm[2] != ':' {
		return
	}
	for _, c := range []byte{hhmm[0], hhmm[1], hhmm[2], hhmm[3], hhmm[4]} {
		if _, ok := font57[c]; !ok {
			return
		}
	}
	// Plate first: S0 everywhere in the rect, so the digits sit on a flat
	// 14:1 field whatever the room behind them is doing.
	for y := StampY; y < StampY+StampPlateH; y++ {
		for x := StampX; x < StampX+StampPlateW; x++ {
			q[y*GW+x] = 0
		}
	}
	// Digits: 5 columns each, 1-column gaps, 2 rows of plate above and below.
	for gi, c := range []byte(hhmm) {
		gx := StampX + gi*6
		cols := font57[c]
		for cx := 0; cx < 5; cx++ {
			col := cols[cx]
			for ry := 0; ry < 7; ry++ {
				if col&(1<<ry) != 0 {
					q[(StampY+2+ry)*GW+gx+cx] = 3
				}
			}
		}
	}
}

// encode renders the level grid as a paletted PNG (stage 8): the four fixed
// ramp entries in order, best compression. Go picks 2-bit depth for ≤4
// colours; the encoder writes no ancillary chunks by construction (EXIF,
// text and profiles cannot survive — asserted byte-level in the tests).
func encode(q []uint8) ([]byte, error) {
	pal := color.Palette{ramp[0], ramp[1], ramp[2], ramp[3]}
	img := image.NewPaletted(image.Rect(0, 0, GW, GH), pal)
	copy(img.Pix, q)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode tisak png: %w", err)
	}
	return buf.Bytes(), nil
}

// Pipeline runs all eight stages over img: grid → masks → blur → quant →
// mode → masks again → stamp → encode. Masks are applied twice (stage 3 and
// again after the blur): the first application keeps masked content out of
// the blur kernel, the second kills the 1-cell gradient bleed the blur leaves
// at the rect border — so every mask cell is exactly level 0 whatever the
// input is, which the invariant test pins.
func Pipeline(img image.Image, levels int, cal Calib, hhmm string) ([]byte, error) {
	g, err := lumaGrid(img)
	if err != nil {
		return nil, err
	}
	return renderGrid(g, levels, cal, hhmm)
}

// renderGrid runs stages 3–8 from an already-built grid, so callers that
// needed the grid for validation stats (dark/dead-sensor) do not pay for it
// twice. The grid is the caller's to keep mutating afterwards — renderGrid
// copies before the blur overwrites anything.
func renderGrid(g []uint8, levels int, cal Calib, hhmm string) ([]byte, error) {
	work := append([]uint8(nil), g...)
	applyMasks(work, cal.Masks)
	work = blur121(work)
	q := quant(work, levels, cal.thresholds(), cal.Bias, cal.Invert)
	q = mode3(q, levels)
	applyMasks(q, cal.Masks)
	stamp(q, hhmm)
	return encode(q)
}
