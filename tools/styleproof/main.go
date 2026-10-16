// Command styleproof is the Print v4 lab (docs/print-v4.md §5): it renders
// local JPEG frames through filter variants and writes a contact sheet with
// the numbers the selection criteria need (bytes, tones used, frame-to-frame
// stability). Local files in, local files out — a raw frame downloaded from
// the bench never leaves this machine, and nothing here talks to a server.
//
//	go run ./tools/styleproof -out /tmp/sheet corpus/*.jpg
//	go run ./tools/styleproof -out /tmp/sheet -sweep corpus/*.jpg
//	go run ./tools/styleproof -synthetic internal/render/testdata/hard-scene.jpg
//
// The corpus is never committed (.gitignore: corpus/). -synthetic writes the
// procedural hard scene (dim, uneven light, soft focus, four people) that the
// render goldens use.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"guzva/internal/render"
)

func main() {
	out := flag.String("out", "", "output directory for the contact sheet")
	sweep := flag.Bool("sweep", false, "sweep σp × σb × thresholds instead of Defaults only")
	synth := flag.String("synthetic", "", "write the procedural hard scene JPEG to this path and exit")
	flag.Func("p", "extra variant σp,σb,k,dark2,dark1,light[,edge] (repeatable)", func(v string) error {
		p := render.Defaults
		n, err := fmt.Sscanf(v, "%g,%g,%d,%d,%d,%d,%d", &p.SigmaP, &p.SigmaB, &p.K, &p.Dark2, &p.Dark1, &p.Light, &p.Edge)
		if n < 6 {
			return err
		}
		extra = append(extra, variant{"p" + v, 4, p})
		return nil
	})
	flag.Parse()
	var err error
	if *synth != "" {
		err = writeSynthetic(*synth)
	} else {
		err = run(*out, *sweep, flag.Args())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "styleproof:", err)
		os.Exit(1)
	}
}

var extra []variant

type variant struct {
	name   string
	levels int
	p      render.Params
}

func variants(sweep bool) []variant {
	if !sweep {
		return append([]variant{{"tisak", 4, render.Defaults}, {"sablon", 2, render.Defaults}}, extra...)
	}
	type th struct{ d2, d1, l int }
	var vs []variant
	for _, sp := range []float64{1.5, 2, 2.5, 3} {
		for _, sb := range []float64{32, 48, 64} {
			for _, t := range []th{{-90, -28, 36}, {-120, -40, 50}, {-70, -20, 28}} {
				p := render.Defaults
				p.SigmaP, p.SigmaB, p.Dark2, p.Dark1, p.Light = sp, sb, t.d2, t.d1, t.l
				vs = append(vs, variant{fmt.Sprintf("p%g-b%g-t%d.%d.%d", sp, sb, t.d2, t.d1, t.l), 4, p})
			}
		}
	}
	return vs
}

type result struct {
	file  string
	bytes int
	tones int
	q     []uint8
}

func run(out string, sweep bool, ins []string) error {
	if out == "" || len(ins) == 0 {
		return fmt.Errorf("usage: styleproof -out DIR [-sweep] frame.jpg...")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	sort.Strings(ins)
	vs := variants(sweep)
	res := make([][]result, len(vs)) // [variant][frame]
	for fi, in := range ins {
		raw, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("%s: %w", in, err)
		}
		for vi, v := range vs {
			b, err := render.Pipeline(img, v.levels, v.p)
			if err != nil {
				return fmt.Errorf("%s/%s: %w", in, v.name, err)
			}
			name := fmt.Sprintf("f%02d-%s.png", fi, v.name)
			if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
				return err
			}
			q, tones := levels(b)
			res[vi] = append(res[vi], result{name, len(b), tones, q})
		}
	}
	return writeSheet(out, ins, vs, res)
}

// levels decodes a render back to its level grid and counts distinct tones.
func levels(b []byte) ([]uint8, int) {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0
	}
	p := img.(*image.Paletted)
	seen := map[uint8]bool{}
	for _, v := range p.Pix {
		seen[v] = true
	}
	return p.Pix, len(seen)
}

// stability is criterion 4: the mean fraction of cells that change level
// between consecutive frames (meaningful on a static-scene sequence).
func stability(rs []result) float64 {
	if len(rs) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(rs); i++ {
		n := 0
		for j := range rs[i].q {
			if rs[i].q[j] != rs[i-1].q[j] {
				n++
			}
		}
		sum += float64(n) / float64(len(rs[i].q))
	}
	return sum / float64(len(rs)-1)
}

func p95(rs []result) int {
	b := make([]int, len(rs))
	for i, r := range rs {
		b[i] = r.bytes
	}
	sort.Ints(b)
	return b[min(len(b)-1, int(math.Ceil(0.95*float64(len(b))))-1)]
}

func writeSheet(out string, ins []string, vs []variant, res [][]result) error {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>styleproof</title><style>
body{font:13px system-ui;margin:16px;background:#f2ede2;color:#12202d}
table{border-collapse:collapse}td,th{padding:4px;vertical-align:top;text-align:left}
img{image-rendering:pixelated;width:320px;height:240px;display:block}
small{color:#33607f}</style>
<p>Local only. Criteria (print-v4 §5): p95 ≤ 12 KB, stability ≤ ~3 % on a static sequence, then the human legibility + recognition tests.</p><table><tr><th>variant</th>`)
	for _, in := range ins {
		abs, _ := filepath.Abs(in)
		fmt.Fprintf(&b, `<th><img src="file://%s"><small>%s</small></th>`, html.EscapeString(abs), html.EscapeString(filepath.Base(in)))
	}
	b.WriteString("</tr>")
	for vi, v := range vs {
		fmt.Fprintf(&b, `<tr><th>%s<br><small>p95 %d B<br>Δ %.2f %%</small></th>`,
			html.EscapeString(v.name), p95(res[vi]), 100*stability(res[vi]))
		for _, r := range res[vi] {
			fmt.Fprintf(&b, `<td><img src="%s"><small>%d B · %d tones</small></td>`, r.file, r.bytes, r.tones)
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return os.WriteFile(filepath.Join(out, "index.html"), []byte(b.String()), 0o644)
}

// writeSynthetic renders the procedural hard scene: a dim room lit unevenly
// from the upper right, soft focus, sensor noise, four people at a counter,
// a poster and a bright screen — the case v3 turned into lighting blobs.
// Luma = illumination × reflectance, so the scene has a real lighting field
// for the filter to remove.
func writeSynthetic(path string) error {
	const w, h = 320, 240
	refl := make([]float64, w*h)
	fill := func(x0, y0, x1, y1 int, r float64) {
		for y := max(y0, 0); y < min(y1, h); y++ {
			for x := max(x0, 0); x < min(x1, w); x++ {
				refl[y*w+x] = r
			}
		}
	}
	disc := func(cx, cy, rad int, r float64, upper bool) {
		for y := cy - rad; y <= cy+rad; y++ {
			for x := cx - rad; x <= cx+rad; x++ {
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= rad*rad && (!upper || y <= cy) && x >= 0 && y >= 0 && x < w && y < h {
					refl[y*w+x] = r
				}
			}
		}
	}
	fill(0, 0, w, h, 0.55)        // wall
	fill(0, 150, w, h, 0.30)      // floor
	fill(0, 142, w, 152, 0.42)    // counter edge
	fill(28, 36, 100, 104, 0.80)  // poster
	fill(210, 40, 302, 110, 0.20) // screen frame
	fill(216, 46, 296, 104, 1.00) // screen
	people := []struct {
		x, top int
		shirt  float64
	}{{78, 120, 0.22}, {170, 118, 0.38}, {248, 142, 0.50}, {290, 146, 0.70}}
	for _, p := range people {
		fill(p.x-24, p.top+18, p.x+24, h, p.shirt) // body
		disc(p.x, p.top+6, 14, 0.62, false)        // face
		disc(p.x, p.top+4, 15, 0.10, true)         // hair
	}
	// Illumination: dim overall, stronger toward the upper right.
	lum := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			il := 0.22 + 0.38*float64(x)/w + 0.18*(1-float64(y)/h)
			lum[y*w+x] = 255 * il * refl[y*w+x]
		}
	}
	lum = boxBlur(boxBlur(lum, w, h, 2), w, h, 2) // soft focus
	rnd := rand.New(rand.NewSource(7))
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i, v := range lum {
		img.Pix[i] = uint8(math.Max(0, math.Min(255, v+rnd.NormFloat64()*5)))
	}
	// Re-wrap as RGBA so the JPEG is YCbCr like the camera's.
	rgba := image.NewRGBA(img.Bounds())
	for i, v := range img.Pix {
		rgba.Set(i%w, i/w, color.Gray{v})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 70}); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func boxBlur(v []float64, w, h, r int) []float64 {
	out := make([]float64, len(v))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var s float64
			n := 0
			for dy := -r; dy <= r; dy++ {
				for dx := -r; dx <= r; dx++ {
					xx, yy := x+dx, y+dy
					if xx >= 0 && yy >= 0 && xx < w && yy < h {
						s += v[yy*w+xx]
						n++
					}
				}
			}
			out[y*w+x] = s / float64(n)
		}
	}
	return out
}
