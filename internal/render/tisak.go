// Tisak v4 pipeline (docs/print-v4.md §4, poster@4 / mono@4).
//
// Pure, integer-only, stdlib-only: same input → same bytes, on every
// architecture. Input: a decoded 4:3 frame; output: a 320×240 paletted PNG.
//
//  1. luma only, area-averaged to 320×240 (identity for QVGA) — colour dies
//     first and is never reconstructed; EXIF never survives the decode.
//  2. (retired) privacy masks — removed from the product in v11 (D11); the
//     stage numbers below keep print-v4.md's.
//  3. privacy low-pass P, Gaussian σp — the privacy dial. Features smaller
//     than about a third of a head are gone after this stage.
//  4. lighting field B, Gaussian σb on a ×8 downsample — the room's light.
//  5. Weber contrast C = (P − B) / (B + k): tone means "darker or lighter
//     than its surroundings", independent of how bright the room is. This is
//     what keeps window light from deciding the bands (v3's failure).
//  6. fixed thresholds in C-space → 4 tones (neutral → s2, darker → s1/s0,
//     brighter → s3). The same constants for every venue, never per frame.
//  7. isolated-cell cleanup: a cell none of whose 8 neighbours agree with it
//     takes their majority. Unlike v3's mode filter this keeps thin shapes.
//  8. paletted 2-bit PNG with the fixed ramp.
//
// Privacy rule for installers (print-v4 §4): head width in cells ÷ blur FWHM
// (2.355·σp) ≤ ~4 at the nearest seat; otherwise move the camera.
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Grid geometry: sensor-native QVGA, displayed at integer scale.
const (
	GW = 320
	GH = 240
)

// Still ramp (DESIGN.md §5.4). Changing a colour ships a new preset version:
// old frames on disk keep their meaning (D2).
var ramp = [4]color.RGBA{
	{0x12, 0x20, 0x2d, 0xff}, // s0 ink — people, hair, shadows
	{0x33, 0x60, 0x7f, 0xff}, // s1 dark mid
	{0x7e, 0xa5, 0xc0, 0xff}, // s2 light mid — neutral surroundings
	{0xf2, 0xed, 0xe2, 0xff}, // s3 paper — lights, windows, bright objects
}

// Params are the filter constants. Production always uses Defaults; the
// struct exists so tools/styleproof can sweep variants offline. There are
// no per-venue overrides (print-v4 decision 3).
type Params struct {
	SigmaP float64 // privacy low-pass, grid cells
	SigmaB float64 // lighting field, grid cells (computed at ×8 downsample)
	K      int     // Weber denominator floor, luma units: tames noise in the dark
	// Contrast thresholds in 1/256 units of C: s0 below Dark2, s1 below
	// Dark1, s3 at or above Light, s2 otherwise. Šablon inks below Dark1.
	Dark2, Dark1, Light int
	Cleanup             bool
	// Edge is the outline threshold: cells where the Weber-normalised
	// gradient of the privacy-filtered image is a local maximum at or above
	// Edge (Q8) are inked. 0 = no outlines. The lines come from P, after the
	// privacy low-pass, so they trace shapes that survived it — they cannot
	// put back detail the blur removed.
	Edge int
}

// Defaults are the frozen poster@4 / mono@4 constants. Starting values from
// the synthetic lab (print-v4 §5); re-freeze from the real corpus as a
// preset version bump, never as a setting.
var Defaults = Params{
	SigmaP: 1.75, SigmaB: 48, K: 24,
	Dark2: -90, Dark1: -28, Light: 36,
	Cleanup: true,
	Edge:    20,
}

// lumaGrid decodes to luminance and area-averages onto the grid (stage 1).
// Only 4:3 inputs are accepted: stretching another aspect would silently
// change what a cell means (422 dims). Exact integer math with Q8 edge
// weights, so any 4:3 size averages (or replicates, below QVGA) exactly.
func lumaGrid(img image.Image) ([]uint8, error) {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw < 1 || sh < 1 || sw*3 != sh*4 {
		return nil, Reject("dims")
	}
	y, stride := lumaPlane(img, b, sw, sh)

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
				for px := x0 / fp; px*fp < x1; px++ {
					ox := overlap(px*fp, px*fp+fp, x0, x1)
					sum += uint64(y[py*stride+px]) * uint64(ox) * uint64(oy)
				}
			}
			g[gy*GW+gx] = uint8(sum / uint64(cellW) / uint64(cellH))
		}
	}
	return g, nil
}

// lumaPlane returns full-resolution luma and its stride. YCbCr decodes keep
// their Y plane untouched; anything else goes through color.GrayModel.
func lumaPlane(img image.Image, b image.Rectangle, sw, sh int) ([]uint8, int) {
	if yc, ok := img.(*image.YCbCr); ok {
		off := (b.Min.Y-yc.Rect.Min.Y)*yc.YStride + (b.Min.X - yc.Rect.Min.X)
		return yc.Y[off : off+(sh-1)*yc.YStride+sw], yc.YStride
	}
	y := make([]uint8, sw*sh)
	for j := 0; j < sh; j++ {
		for i := 0; i < sw; i++ {
			y[j*sw+i] = color.GrayModel.Convert(img.At(b.Min.X+i, b.Min.Y+j)).(color.Gray).Y
		}
	}
	return y, sw
}

// overlap is the length of the intersection of [a0,a1) and [b0,b1).
func overlap(a0, a1, b0, b1 int) int {
	return max(min(a1, b1)-max(a0, b0), 0)
}

// kernel is a one-sided integer Gaussian, k[0] the centre, radius ⌈3σ⌉.
// Weights are rounded to Q8 once; the pipeline itself never touches floats.
func kernel(sigma float64) []int64 {
	r := int(math.Ceil(3 * sigma))
	k := make([]int64, r+1)
	for i := range k {
		k[i] = int64(math.Round(256 * math.Exp(-float64(i*i)/(2*sigma*sigma))))
	}
	return k
}

// blurSep is a separable convolution over a w×h plane. Out-of-range taps
// contribute nothing (zero weight, not edge-clamped), which together with
// normalized convolution (blur(v·w) / blur(w)) means borders bias nothing.
func blurSep(v []int64, w, h int, k []int64) []int64 {
	r := len(k) - 1
	tmp := make([]int64, len(v))
	for y := 0; y < h; y++ {
		row := v[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			s := k[0] * row[x]
			for i := 1; i <= r; i++ {
				if x-i >= 0 {
					s += k[i] * row[x-i]
				}
				if x+i < w {
					s += k[i] * row[x+i]
				}
			}
			tmp[y*w+x] = s
		}
	}
	out := make([]int64, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := k[0] * tmp[y*w+x]
			for i := 1; i <= r; i++ {
				if y-i >= 0 {
					s += k[i] * tmp[(y-i)*w+x]
				}
				if y+i < h {
					s += k[i] * tmp[(y+i)*w+x]
				}
			}
			out[y*w+x] = s
		}
	}
	return out
}

// normBlur is a normalized convolution: the weighted mean of the visible
// cells under the kernel, in Q4 luma (×16). A zero weight sum (impossible
// with every cell visible) falls back to mid-grey.
func normBlur(vals []int64, wts []int64, w, h int, k []int64) []int64 {
	num := blurSep(vals, w, h, k)
	den := blurSep(wts, w, h, k)
	out := make([]int64, len(vals))
	for i := range out {
		if den[i] == 0 {
			out[i] = 128 * 16
			continue
		}
		out[i] = (num[i]*16 + den[i]/2) / den[i]
	}
	return out
}

// background estimates the lighting field B (stage 4) in Q4 luma: a ×8
// block mean (visible cells only), odd-reflection padding, a normalized
// Gaussian of σb/8 blocks, then bilinear back up to the grid.
//
// Why the padding: without it the blur near a frame edge averages only the
// interior, so on a lighting gradient the dim edge reads "darker than its
// surroundings" and the print grows a dark rim. Odd reflection
// (m[−i] = 2·m[0] − m[i]) continues a linear gradient straight past the
// edge. It is linear per axis, so padding x then y handles the corners.
// Block means average 64 cells, so the reflection does not amplify noise.
func background(g []uint8, sigmaB float64) []int64 {
	const f = 8
	bw, bh := GW/f, GH/f
	sum := make([]int64, bw*bh)
	cnt := make([]int64, bw*bh)
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			i := (y/f)*bw + x/f
			sum[i] += int64(g[y*GW+x])
			cnt[i]++
		}
	}
	mean := make([]int64, bw*bh) // Q4
	for i := range mean {
		if cnt[i] > 0 {
			mean[i] = (sum[i]*16 + cnt[i]/2) / cnt[i]
		}
	}
	k := kernel(sigmaB / f)
	r := min(len(k)-1, bw-1, bh-1)
	pw, ph := bw+2*r, bh+2*r

	// Pad x, then y over the padded rows.
	mx, cx := make([]int64, pw*bh), make([]int64, pw*bh)
	for y := 0; y < bh; y++ {
		padAxis(mean[y*bw:(y+1)*bw], cnt[y*bw:(y+1)*bw], mx[y*pw:(y+1)*pw], cx[y*pw:(y+1)*pw], 1, 1, bw, r)
	}
	m, c := make([]int64, pw*ph), make([]int64, pw*ph)
	for x := 0; x < pw; x++ {
		padAxis(mx[x:], cx[x:], m[x:], c[x:], pw, pw, bh, r)
	}
	num := make([]int64, len(m))
	for i := range m {
		num[i] = max(m[i], 0) * c[i]
	}
	ns, ds := blurSep(num, pw, ph, k), blurSep(c, pw, ph, k)
	blk := make([]int64, bw*bh)
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			i := (y+r)*pw + x + r
			if ds[i] == 0 {
				blk[y*bw+x] = 128 * 16
			} else {
				blk[y*bw+x] = (ns[i] + ds[i]/2) / ds[i]
			}
		}
	}

	// Bilinear: cell x's centre sits at (2x+1−f)/(2f) blocks from block 0's
	// centre; in 1/16-block units that is 2x−7 for f=8.
	out := make([]int64, GW*GH)
	axis := func(c, n int) (i0, i1 int, fr int64) {
		t := 2*c - (f - 1)
		if t <= 0 {
			return 0, 0, 0
		}
		i0, fr = t/16, int64(t%16)
		if i0 >= n-1 {
			return n - 1, n - 1, 0
		}
		return i0, i0 + 1, fr
	}
	for y := 0; y < GH; y++ {
		y0, y1, fy := axis(y, bh)
		for x := 0; x < GW; x++ {
			x0, x1, fx := axis(x, bw)
			top := blk[y0*bw+x0]*(16-fx) + blk[y0*bw+x1]*fx
			bot := blk[y1*bw+x0]*(16-fx) + blk[y1*bw+x1]*fx
			out[y*GW+x] = (top*(16-fy) + bot*fy + 128) / 256
		}
	}
	return out
}

// padAxis copies n strided samples of (m, c) into the n+2r strided output
// with r samples of odd reflection on each side. Weights reflect evenly,
// capped by the edge weight.
func padAxis(m, c, om, oc []int64, in, out, n, r int) {
	for i := -r; i < n+r; i++ {
		j, e := i, 0
		switch {
		case i < 0:
			j, e = -i, 0
		case i >= n:
			j, e = 2*(n-1)-i, n-1
		}
		o := (i + r) * out
		if j == i {
			om[o], oc[o] = m[j*in], c[j*in]
			continue
		}
		om[o] = 2*m[e*in] - m[j*in]
		oc[o] = min(c[e*in], c[j*in])
	}
}

// tones runs stages 2–7 over the luma grid and returns the level grid.
func tones(g []uint8, levels int, p Params) []uint8 {
	vals := make([]int64, len(g))
	wts := make([]int64, len(g))
	for i, v := range g {
		vals[i], wts[i] = int64(v), 1
	}
	lp := normBlur(vals, wts, GW, GH, kernel(p.SigmaP))
	bg := background(g, p.SigmaB)

	q := make([]uint8, len(g))
	for i := range q {
		c := (lp[i] - bg[i]) * 256 / (bg[i] + int64(p.K)*16) // Q8 Weber contrast
		switch {
		case levels == 2:
			if c >= int64(p.Dark1) {
				q[i] = 1
			}
		case c < int64(p.Dark2):
			q[i] = 0
		case c < int64(p.Dark1):
			q[i] = 1
		case c < int64(p.Light):
			q[i] = 2
		default:
			q[i] = 3
		}
	}
	if p.Cleanup {
		q = despeckle(q, levels)
	}
	if p.Edge > 0 {
		outline(q, lp, bg, levels, p)
	}
	return q
}

// outline inks object boundaries (stage 7b): a Weber-normalised central-
// difference gradient of P, thinned by non-maximum suppression along its
// dominant axis so lines stay one cell wide, kept where ≥ p.Edge.
//
// Lines are ink, except inside ink: two dark things touching (dark clothes
// on a dark sofa) is exactly how a person melts into the background, and
// an ink line there is invisible — so on s0 the line is drawn light (s2,
// or paper in Šablon).
func outline(q []uint8, lp, bg []int64, levels int, p Params) {
	light := uint8(2)
	if levels == 2 {
		light = 1
	}
	mag := make([]int64, len(lp))
	horiz := make([]bool, len(lp)) // dominant gradient runs along x
	for y := 1; y < GH-1; y++ {
		for x := 1; x < GW-1; x++ {
			i := y*GW + x
			gx, gy := lp[i+1]-lp[i-1], lp[i+GW]-lp[i-GW]
			ax, ay := max(gx, -gx), max(gy, -gy)
			mag[i] = (ax + ay) * 256 / (bg[i] + int64(p.K)*16)
			horiz[i] = ax >= ay
		}
	}
	for y := 1; y < GH-1; y++ {
		for x := 1; x < GW-1; x++ {
			i := y*GW + x
			m := mag[i]
			if m < int64(p.Edge) {
				continue
			}
			a, b := mag[i-GW], mag[i+GW]
			if horiz[i] {
				a, b = mag[i-1], mag[i+1]
			}
			if m >= a && m > b {
				if q[i] == 0 {
					q[i] = light
				} else {
					q[i] = 0
				}
			}
		}
	}
}

// despeckle replaces each cell that no neighbour agrees with by the
// neighbours' majority (ties → the lower level, for determinism). Thin
// lines survive: a one-cell-wide arm still has two agreeing neighbours.
func despeckle(q []uint8, levels int) []uint8 {
	out := append([]uint8(nil), q...)
	for y := 0; y < GH; y++ {
		for x := 0; x < GW; x++ {
			c := q[y*GW+x]
			var n [4]int
			same := false
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					xx, yy := x+dx, y+dy
					if (dx == 0 && dy == 0) || xx < 0 || yy < 0 || xx >= GW || yy >= GH {
						continue
					}
					v := q[yy*GW+xx]
					if v == c {
						same = true
						break
					}
					n[v]++
				}
				if same {
					break
				}
			}
			if same {
				continue
			}
			best := 0
			for lv := 1; lv < levels; lv++ {
				if n[lv] > n[best] {
					best = lv
				}
			}
			out[y*GW+x] = uint8(best)
		}
	}
	return out
}

// encode renders the level grid as a paletted PNG. Go picks 2-bit depth
// for ≤4 colours and writes no ancillary chunks (asserted in the tests).
// Šablon uses the ramp's two ends: ink and paper.
func encode(q []uint8, levels int) ([]byte, error) {
	pal := color.Palette{ramp[0], ramp[1], ramp[2], ramp[3]}
	if levels == 2 {
		pal = color.Palette{ramp[0], ramp[3]}
	}
	img := image.NewPaletted(image.Rect(0, 0, GW, GH), pal)
	copy(img.Pix, q)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode tisak png: %w", err)
	}
	return buf.Bytes(), nil
}

// Pipeline renders img with the given parameters (styleproof's entry
// point; the Engine always passes Defaults).
func Pipeline(img image.Image, levels int, p Params) ([]byte, error) {
	g, err := lumaGrid(img)
	if err != nil {
		return nil, err
	}
	return encode(tones(g, levels, p), levels)
}
