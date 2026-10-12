// Command styleproof renders a local JPEG into the candidate contact sheet
// the install wizard shows live (DESIGN.md §5.11, §8.4): bias −24/0/+24 ×
// Tisak/Šablon, plus the default — so threshold and preset choices are made
// from real frames, not guessed from the design doc's synthetic numbers.
//
// It reads only local files and writes only local files: a venue's raw frame
// downloaded for calibration never leaves this machine, and nothing here
// touches the server. Usage:
//
//	go run ./tools/styleproof -in frame.jpg -out sheet/
//	go run ./tools/styleproof -in frame.jpg -out sheet/ -masks 10,20,30,40
//
// The sheet is one PNG per candidate plus an index.html contact sheet with
// the exact calib_json for each — copy the chosen document into the venue's
// calibration form.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"

	"guzva/internal/render"
)

func main() {
	in := flag.String("in", "", "local JPEG frame to proof (never uploaded anywhere)")
	out := flag.String("out", "", "output directory for the contact sheet")
	masks := flag.String("masks", "", "optional static masks as x0,y0,x1,y1 triples, ';'-separated, in grid cells")
	flag.Parse()
	if err := run(*in, *out, *masks); err != nil {
		fmt.Fprintln(os.Stderr, "styleproof:", err)
		os.Exit(1)
	}
}

func run(in, out, masksRaw string) error {
	if in == "" || out == "" {
		return fmt.Errorf("usage: styleproof -in frame.jpg -out sheet/ [-masks x0,y0,x1,y1[;...]]")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	var masks []render.Rect
	if masksRaw != "" {
		for _, m := range strings.Split(masksRaw, ";") {
			var r render.Rect
			if _, err := fmt.Sscanf(m, "%d,%d,%d,%d", &r.X0, &r.Y0, &r.X1, &r.Y1); err != nil {
				return fmt.Errorf("masks: %q: %w", m, err)
			}
			masks = append(masks, r)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	type cand struct {
		name   string
		levels int
		cal    render.Calib
	}
	cands := []cand{
		{"tisak-bias0", 4, render.Calib{Masks: masks}},
		{"tisak-bias-24", 4, render.Calib{Bias: -24, Masks: masks}},
		{"tisak-bias+24", 4, render.Calib{Bias: 24, Masks: masks}},
		{"sablon-bias0", 2, render.Calib{Masks: masks}},
		{"sablon-bias-24", 2, render.Calib{Bias: -24, Masks: masks}},
		{"sablon-bias+24", 2, render.Calib{Bias: 24, Masks: masks}},
	}
	var rows []candRow
	for _, c := range cands {
		pngBytes, err := render.Pipeline(img, c.levels, c.cal)
		if err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		fn := c.name + ".png"
		if err := os.WriteFile(filepath.Join(out, fn), pngBytes, 0o644); err != nil {
			return err
		}
		doc := calibDoc(c.levels, c.cal)
		rows = append(rows, candRow{c.name, fn, len(pngBytes), doc})
		fmt.Printf("%-14s %5d bytes  %s\n", c.name, len(pngBytes), doc)
	}
	return writeIndex(out, rows, in)
}

func calibDoc(levels int, cal render.Calib) string {
	preset := "poster"
	if levels == 2 {
		preset = "mono"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"bias":%d,"invert":%v,"preset":%q`, cal.Bias, cal.Invert, preset)
	if cal.T != [3]uint8{} {
		fmt.Fprintf(&b, `,"t":[%d,%d,%d]`, cal.T[0], cal.T[1], cal.T[2])
	}
	if len(cal.Masks) > 0 {
		b.WriteString(`,"masks":[`)
		for i, m := range cal.Masks {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"X0":%d,"Y0":%d,"X1":%d,"Y1":%d}`, m.X0, m.Y0, m.X1, m.Y1)
		}
		b.WriteString("]")
	}
	b.WriteString("}")
	return b.String()
}

type candRow struct {
	name string
	file string
	size int
	doc  string
}

func writeIndex(out string, rows []candRow, src string) error {
	// Re-render each candidate at 3× for the sheet without touching the
	// candidate files themselves (those stay byte-identical 160×120).
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>styleproof — ` + htmlEsc(filepath.Base(src)) + `</title>
<style>body{font-family:system-ui,sans-serif;background:#f2ede2;color:#12202d;max-width:60rem;margin:0 auto;padding:1rem}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:1rem}
figure{border:2px solid #12202d;background:#fff;margin:0;padding:.5rem;box-shadow:4px 4px 0 #12202d}
img{width:100%;image-rendering:pixelated;display:block;background:#f2ede2}
code{font-size:.72rem;word-break:break-all;display:block;margin-top:.4rem}</style>
</head><body><h1>styleproof</h1><p>`)
	b.WriteString(htmlEsc(src))
	b.WriteString(` — tap the best candidate and copy its calib_json into the venue calibration form. Nothing here leaves this machine.</p><div class="grid">`)
	for _, r := range rows {
		b.WriteString(`<figure><img src="` + r.file + `" width="160" height="120" alt="` + r.name + `"><figcaption><strong>`)
		b.WriteString(r.name)
		b.WriteString(`</strong> ` + itoa(r.size) + ` bytes<code>` + htmlEsc(r.doc) + `</code></figcaption></figure>`)
	}
	b.WriteString(`</div></body></html>`)
	return os.WriteFile(filepath.Join(out, "index.html"), []byte(b.String()), 0o644)
}

func htmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func itoa(n int) string { return fmt.Sprint(n) }
