package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"guzva/internal/proto"
)

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testEngine() *Engine { return NewEngine(func() time.Time { return testNow }, 2) }

// syntheticFrame renders a smooth gradient + seeded noise (never uniform,
// never dark) and JPEG-encodes it. Fixed seed → stable bytes per run.
func syntheticFrame(t *testing.T, w, h int, seed int64) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := rnd.Intn(24) - 12
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(clampInt(60 + x*120/w + n)),
				G: uint8(clampInt(80 + y*100/h + n)),
				B: uint8(clampInt(120 + (x+y)*40/(w+h) + n)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func clampInt(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func uniformFrame(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewUniform(c)
	var buf bytes.Buffer
	// Encode via a sized RGBA so bounds match.
	full := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			full.Set(x, y, img.At(x, y))
		}
	}
	if err := jpeg.Encode(&buf, full, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode uniform: %v", err)
	}
	return buf.Bytes()
}

func process(t *testing.T, e *Engine, raw []byte, preset string) (Output, error) {
	t.Helper()
	cp := append([]byte(nil), raw...) // Process zeroes its input
	return e.Process(context.Background(), FrameIn{
		VenueID: 1, CaptureID: "C1-1-abcd", Preset: preset, Raw: cp,
	})
}

func TestProcessNormalizesAndStyles(t *testing.T) {
	e := testEngine()
	raw := syntheticFrame(t, 320, 240, 1)
	cp := append([]byte(nil), raw...)
	out, err := e.Process(context.Background(), FrameIn{
		VenueID: 1, CaptureID: "C1-1-abcd", Preset: "poster", Raw: cp,
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if out.Width != GW || out.Height != GH {
		t.Errorf("dims = %dx%d, want %dx%d", out.Width, out.Height, GW, GH)
	}
	if out.Preset != "poster" || out.PresetVer != 6 {
		t.Errorf("preset = %s@%d", out.Preset, out.PresetVer)
	}
	if !out.ReceivedAt.Equal(testNow) {
		t.Errorf("received_at = %v, want %v (clock injected)", out.ReceivedAt, testNow)
	}
	if len(out.SHA256) != 64 {
		t.Errorf("sha256 = %q", out.SHA256)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out.PNG))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if cfg.Width != GW || cfg.Height != GH {
		t.Errorf("output dims = %dx%d", cfg.Width, cfg.Height)
	}
	// The burned-in HH:MM stamp is gone (removed 12 Oct 2026 — the page's
	// print label carries the time); nothing to assert here beyond dims.
}

// Privacy (§7 input-zeroed): the raw buffer is zeroed on return, success or
// failure.
func TestInputZeroed(t *testing.T) {
	e := testEngine()
	raw := syntheticFrame(t, 320, 240, 2)
	cp := append([]byte(nil), raw...)
	if _, err := e.Process(context.Background(), FrameIn{Preset: "poster", Raw: cp}); err != nil {
		t.Fatalf("process: %v", err)
	}
	assertAllZero(t, cp, "success path")

	bad := []byte{0x00, 0x01, 0x02, 0x03}
	if _, err := e.Process(context.Background(), FrameIn{Preset: "poster", Raw: bad}); err == nil {
		t.Fatal("bad magic should reject")
	}
	assertAllZero(t, bad, "reject path")
}

func assertAllZero(t *testing.T, b []byte, path string) {
	t.Helper()
	for i, c := range b {
		if c != 0 {
			t.Fatalf("%s: input not zeroed at byte %d", path, i)
		}
	}
}

// Privacy (§7 EXIF-stripped): an EXIF-carrying input yields an output with
// no APP1 segment — the pipeline keeps only decoded pixels.
func TestEXIFStripped(t *testing.T) {
	raw := syntheticFrame(t, 320, 240, 3)
	withExif := injectAPP1(t, raw)
	if !hasAPP1(withExif) {
		t.Fatal("fixture should carry APP1")
	}
	e := testEngine()
	out, err := process(t, e, withExif, "poster")
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if hasAPP1(out.PNG) {
		t.Fatal("output still carries EXIF/APP1")
	}
}

// injectAPP1 splices a minimal Exif APP1 segment after SOI.
func injectAPP1(t *testing.T, jpegBytes []byte) []byte {
	t.Helper()
	payload := append([]byte("Exif\x00\x00"), make([]byte, 32)...)
	segLen := len(payload) + 2
	seg := []byte{0xff, 0xe1, byte(segLen >> 8), byte(segLen)}
	seg = append(seg, payload...)
	out := append([]byte{}, jpegBytes[:2]...)
	out = append(out, seg...)
	return append(out, jpegBytes[2:]...)
}

func hasAPP1(b []byte) bool {
	for i := 0; i+4 <= len(b); i++ {
		if b[i] == 0xff && b[i+1] == 0xe1 {
			return true
		}
	}
	return false
}

// §7 determinism: 100 runs of the pure pipeline produce identical bytes.
func TestDeterminism100(t *testing.T) {
	e := testEngine()
	raw := syntheticFrame(t, 320, 240, 4)
	var want string
	for i := 0; i < 100; i++ {
		out, err := process(t, e, raw, "poster")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		sum := sha256Hex(out.PNG)
		if i == 0 {
			want = sum
		} else if sum != want {
			t.Fatalf("run %d: non-deterministic output", i)
		}
	}
}

// §7 golden render: catches unintended style changes. Regenerate with
// UPDATE_GOLDEN=1 go test ./internal/render.
func TestGoldenPoster(t *testing.T) {
	raw, _ := os.ReadFile("testdata/hard-scene.jpg")
	out, err := process(t, testEngine(), raw, "poster")
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "poster", out.PNG)
}

func TestGoldenMono(t *testing.T) {
	raw, _ := os.ReadFile("testdata/hard-scene.jpg")
	out, err := process(t, testEngine(), raw, "mono")
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "mono", out.PNG)
}

// pixelSum fingerprints the DECODED render, not the PNG bytes.
//
// The goldens used to hash the encoded PNG. That made the gate depend on the
// stdlib's deflate stream: Go 1.27 changed it, CI (pinned to go.mod's
// go1.26.0 by setup-go) hashed different bytes than the committed goldens,
// and run #1 went red on a byte-identical picture. The deflate stream is
// transport, not style; the style is the decoded image. So the fingerprint
// is the dimensions, the palette, and every decoded pixel colour — stable
// across any toolchain that renders the same picture, still failing on any
// real style change.
//
// The palette is in the fingerprint deliberately: Pix holds palette INDICES,
// so index bytes alone cannot see a ramp colour change, and the ramp IS the
// style (changing a colour ships a new preset version, D2). Stride is left
// out on purpose — row packing is transport too, the same lesson as deflate.
func pixelSum(t *testing.T, pngBytes []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode golden render: %v", err)
	}
	// The pipeline emits a paletted PNG (PLAN §7): 320×240, ≤ four ramp colours,
	// a few hundred bytes on disk. Pinning the type keeps the protection the
	// byte hash happened to give — a silent encoder switch to RGBA would leave
	// every pixel equal while quadrupling the still's size.
	p, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("golden render decoded as %T, want *image.Paletted (PLAN §7)", img)
	}
	b := img.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d", b.Dx(), b.Dy())
	for _, c := range p.Palette {
		r, g, bl, a := c.RGBA()
		fmt.Fprintf(h, " %d,%d,%d,%d", r, g, bl, a)
	}
	h.Write([]byte("|"))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			fmt.Fprintf(h, " %d,%d,%d,%d", r, g, bl, a)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func goldenCheck(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".sha256")
	sum := pixelSum(t, got)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(sum+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("golden %s missing — run UPDATE_GOLDEN=1 go test ./internal/render once", path)
	}
	if strings.TrimSpace(string(want)) != sum {
		t.Errorf("golden %s mismatch:\n got %s\nwant %s\n(regenerate with UPDATE_GOLDEN=1 if intended)",
			name, sum, strings.TrimSpace(string(want)))
	}
}

func TestRejects(t *testing.T) {
	e := testEngine()
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"bad_magic", []byte{0x00, 0x01, 0x02, 0x03}, "bad_magic"},
		{"empty", nil, "bad_magic"},
		{"truncated", syntheticFrame(t, 320, 240, 5)[:500], "truncated"},
		{"dims", uniformFrame(t, 64, 64, color.RGBA{R: 90, G: 140, B: 200, A: 255}), "dims"},
		{"dark", uniformFrame(t, 320, 240, color.RGBA{A: 255}), "dark"}, // bench black frames → dark (§7)
		{"dead", uniformFrame(t, 320, 240, color.RGBA{R: 128, G: 128, B: 128, A: 255}), "dead"},
		{"too_big", bytes.Repeat([]byte{0xff, 0xd8}, (proto.MaxFrameBytes/2)+1), "too_big"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := process(t, e, c.raw, "poster")
			reason, ok := IsReject(err)
			if !ok {
				t.Fatalf("err = %v, want reject %s", err, c.want)
			}
			if reason != c.want {
				t.Errorf("reason = %s, want %s", reason, c.want)
			}
		})
	}
}

// Q-01: dark/dead rejects carry the measured mean luma; every other
// reject carries 0 ("unmeasured"). The journal logs mean_luma only for
// dark/dead, so the key's presence proves a frame was measured.
func TestRejectMean(t *testing.T) {
	e := testEngine()
	// dark: uniform black → measured below the threshold.
	_, err := process(t, e, uniformFrame(t, 320, 240, color.RGBA{A: 255}), "poster")
	reason, ok := IsReject(err)
	if !ok || reason != proto.ReasonDark {
		t.Fatalf("err = %v, want dark reject", err)
	}
	if m := RejectMean(err); m >= DarkMeanLuma {
		t.Errorf("dark Mean = %v, want < %v", m, DarkMeanLuma)
	}
	// dead: uniform mid-gray → measured ≈ 128 (a real number flows,
	// not a constant).
	_, err = process(t, e, uniformFrame(t, 320, 240, color.RGBA{R: 128, G: 128, B: 128, A: 255}), "poster")
	reason, ok = IsReject(err)
	if !ok || reason != proto.ReasonDead {
		t.Fatalf("err = %v, want dead reject", err)
	}
	if m := RejectMean(err); m < 100 || m > 156 {
		t.Errorf("dead Mean = %v, want ≈128", m)
	}
	// Unmeasured rejects carry 0.
	unmeasured := map[string][]byte{
		"bad_magic": {0x00, 0x01},
		"dims":      uniformFrame(t, 64, 64, color.RGBA{R: 90, G: 140, B: 200, A: 255}),
	}
	for name, raw := range unmeasured {
		_, err := process(t, e, raw, "poster")
		if _, ok := IsReject(err); !ok {
			t.Fatalf("%s: err = %v, want reject", name, err)
		}
		if m := RejectMean(err); m != 0 {
			t.Errorf("%s: Mean = %v, want 0 (unmeasured)", name, m)
		}
	}
}

// §7: no raw style exists — exactly poster + mono, nothing passes through.
func TestNoRawStyle(t *testing.T) {
	if len(styles) != 2 {
		t.Fatalf("styles = %v, want exactly poster+mono", styles)
	}
	for _, name := range []string{"poster", "mono"} {
		if _, ok := LookupStyle(name); !ok {
			t.Errorf("missing style %q", name)
		}
	}
	if _, ok := LookupStyle("raw"); ok {
		t.Fatal("raw style must not exist (§7)")
	}
	// An unknown venue style renders as the default, never unstyled.
	out, err := process(t, testEngine(), syntheticFrame(t, 320, 240, 3), "raw")
	if err != nil || out.Preset != DefaultStyle {
		t.Fatalf("unknown style → %q err=%v, want %q", out.Preset, err, DefaultStyle)
	}
}

func TestPresetsDifferAndAgreeOnShape(t *testing.T) {
	e := testEngine()
	raw := syntheticFrame(t, 320, 240, 6)
	poster, err := process(t, e, raw, "poster")
	if err != nil {
		t.Fatal(err)
	}
	mono, err := process(t, e, raw, "mono")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(poster.PNG, mono.PNG) {
		t.Fatal("poster and mono must differ")
	}
	if mono.Preset != "mono" || mono.PresetVer != 6 {
		t.Errorf("mono metadata = %s@%d", mono.Preset, mono.PresetVer)
	}
}

// §7 race hammer: run the pipeline concurrently (pair with go test -race).
func TestRaceHammer(t *testing.T) {
	e := testEngine()
	raw := syntheticFrame(t, 320, 240, 7)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 3; j++ {
				out, err := process(t, e, raw, "poster")
				if err != nil {
					t.Errorf("worker %d: %v", i, err)
					return
				}
				_ = out
			}
		}(i)
	}
	wg.Wait()
}

// sha256Hex is a tiny test helper (the production one lives in frames).
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// JSON round trip guard for Output used in debug logging elsewhere.
func TestOutputJSONShape(t *testing.T) {
	out := Output{Preset: "poster", PresetVer: 2, Width: GW, Height: GH}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"Preset":"poster"`) {
		t.Errorf("json = %s", b)
	}
}
