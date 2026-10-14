// Command fixtures writes the synthetic JPEG corpus the qa/ shell tests
// ingest. Hermetic by construction: stdlib image/jpeg only, no ImageMagick,
// no python, no fixture blobs in git. The good frame mirrors
// tools/fakecam's makeJPEG (the same encoder settings, the same gradient +
// noise shape) so a "201" here means the same thing a "201" from the bench
// harness means.
//
// Every frame exists to trip exactly one contract from docs/proto-v1.md §
// responses; the expected outcome is printed next to each so the shell tests
// and a human reading the directory agree on what each file is for.
//
//	go run ./qa/fixtures -out /tmp/opencode/guzva-qa/fix
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "", "directory to write the corpus into (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "fixtures: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "fixtures:", err)
		os.Exit(1)
	}
	for _, f := range corpus() {
		p := filepath.Join(*out, f.name)
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "fixtures:", err)
			os.Exit(1)
		}
		fmt.Printf("%-22s %8d B  → %s\n", f.name, len(f.data), f.expect)
	}
}

type fixture struct {
	name   string
	data   []byte
	expect string
}

// corpus is the full reject matrix. Dimensions are chosen against the
// contracts in internal/render/render.go: MinDim 96, MaxDim 4096, aspect
// exactly 4:3, mean luma ≥ DarkMeanLuma (6), and not-uniform unless we want
// `dead`.
func corpus() []fixture {
	return []fixture{
		// 320×240 QVGA is the profile the firmware bakes in (PLAN, the
		// 13 Oct 16:40 decision: framesize=5). The one shape that must work.
		{"good.jpg", makeJPEG(320, 240, 1), "201 stored"},
		// A second good frame at a different seed — proves the pipeline is
		// not accidentally keyed on identical bytes.
		{"good2.jpg", makeJPEG(320, 240, 2), "201 stored"},
		// §7's quarantine case, byte-for-byte the shape the bench produced:
		// uniform black decodes to mean luma 0, under DarkMeanLuma.
		{"dark-black.jpg", makeJPEG(320, 240, -1), "422 dark"},
		// Just under the threshold: mean luma ~5. The boundary the
		// "covered lens" vs "dim room" distinction depends on.
		{"dark-luma5.jpg", flat(320, 240, 5), "422 dark (mean 5 < 6)"},
		// Exactly at the threshold: mean 6 is NOT dark (Classify is `mean <
		// DarkMeanLuma`), and it is uniform → dead. One image, two contracts.
		{"dead-gray.jpg", flat(320, 240, 100), "422 dead (uniform, mean ≥ 6)"},
		// THE 11:44 INCIDENT. framesize=0 on the pinned esp32-camera build
		// is 96×96: 1:1, above MinDim so it clears the size gate, and
		// rejected only by the 4:3 rule. This is the frame that wedged the
		// bench for ~5 h.
		{"dims-1to1.jpg", makeJPEG(96, 96, 3), "422 dims (1:1)"},
		// A different non-4:3: 16:9 is the shape a "fix" would produce if
		// someone set framesize for a wide sensor crop.
		{"dims-16to9.jpg", makeJPEG(320, 180, 4), "422 dims (16:9)"},
		// Below MinDim (95 < 96) — the size half of the dims contract.
		{"dims-tiny.jpg", makeJPEG(95, 95, 5), "422 dims (< MinDim)"},
		// Above MaxDim — the other end of the same gate.
		{"dims-huge.jpg", makeJPEG(4097, 4097, 6), "413/422 (over MaxDim)"},
		// Truncated: valid SOI magic, then nothing — DecodeConfig fails.
		{"truncated.jpg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}, "422 truncated"},
		// Truncated harder: a real JPEG with its tail cut off.
		{"truncated-tail.jpg", cut(makeJPEG(320, 240, 7), 200), "422 truncated"},
		// bad_magic: right size, wrong header. This is what a TLS-terminating
		// proxy or a captive portal returns, so it must never be stored.
		{"bad-magic.jpg", []byte("<html>captive portal</html>"), "422 bad_magic"},
		// Empty frame part: len(Raw) < 2 → bad_magic.
		{"empty.jpg", nil, "422 bad_magic (len < 2)"},
		// Over the 4 MiB frame cap. Not a valid image on purpose: the body
		// cap must trip on SIZE, before any decode.
		{"oversize.jpg", bytes.Repeat([]byte{0xff, 0xd8}, (5<<20)/2), "413 too_large"},
	}
}

// makeJPEG mirrors tools/fakecam's makeJPEG: a left-to-right red ramp, a
// top-to-bottom green ramp and a diagonal blue ramp, each with ±12 of noise
// so the grid is never uniform. seed < 0 produces uniform black.
func makeJPEG(w, h int, seed int64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if seed < 0 {
				img.Set(x, y, color.RGBA{A: 255})
				continue
			}
			n := rnd.Intn(24) - 12
			img.Set(x, y, color.RGBA{
				R: uint8(clamp(60 + x*120/w + n)),
				G: uint8(clamp(80 + y*100/h + n)),
				B: uint8(clamp(120 + (x+y)*40/(w+h) + n)),
				A: 255,
			})
		}
	}
	return encode(img)
}

// flat is a single uniform luma across the whole frame. Uniform + bright
// enough is the `dead` sensor case; the exact value picks which of dark/dead
// the pipeline lands on, which is how dark-luma5 and dead-gray separate the
// two contracts at their shared boundary.
func flat(w, h, v int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{R: uint8(v), G: uint8(v), B: uint8(v), A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return encode(img)
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func cut(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
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
