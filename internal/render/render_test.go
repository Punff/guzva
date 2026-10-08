package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imaliguzve/internal/proto"
)

// fixedClock pins received_at so tests are deterministic (Clock is the
// plan's one injected interface, §3).
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testEngine() *Engine { return NewEngine(fixedClock{testNow}, 2) }

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
	if out.Width != PublicWidth {
		t.Errorf("width = %d, want %d", out.Width, PublicWidth)
	}
	if out.Height != 480 { // 240 * 640/320
		t.Errorf("height = %d, want 480", out.Height)
	}
	if out.Preset != "poster" || out.PresetVer != 1 {
		t.Errorf("preset = %s@%d", out.Preset, out.PresetVer)
	}
	if !out.ReceivedAt.Equal(testNow) {
		t.Errorf("received_at = %v, want %v (clock injected)", out.ReceivedAt, testNow)
	}
	if len(out.SHA256) != 64 {
		t.Errorf("sha256 = %q", out.SHA256)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out.JPEG))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if cfg.Width != PublicWidth {
		t.Errorf("output width = %d", cfg.Width)
	}
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
	if hasAPP1(out.JPEG) {
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
		sum := sha256Hex(out.JPEG)
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
	raw := syntheticFrame(t, 320, 240, 4)
	out, err := process(t, testEngine(), raw, "poster")
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "poster", out.JPEG)
}

func TestGoldenMono(t *testing.T) {
	raw := syntheticFrame(t, 320, 240, 4)
	out, err := process(t, testEngine(), raw, "mono")
	if err != nil {
		t.Fatal(err)
	}
	goldenCheck(t, "mono", out.JPEG)
}

func goldenCheck(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".sha256")
	sum := sha256Hex(got)
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

// §7: no raw preset exists — the registry cannot publish an unstyled frame.
func TestNoRawPreset(t *testing.T) {
	e := testEngine()
	if _, ok := e.Preset("raw"); ok {
		t.Fatal("raw preset must not exist (§7)")
	}
	names := e.PresetNames()
	if len(names) != 2 {
		t.Fatalf("registry = %v, want poster+mono", names)
	}
	// Unknown style falls back to the default, never to nothing.
	if p, ok := e.Preset("definitely-not-a-style"); ok || p != nil {
		t.Fatal("unknown preset must not resolve")
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
	if bytes.Equal(poster.JPEG, mono.JPEG) {
		t.Fatal("poster and mono must differ")
	}
	if mono.Preset != "mono" || mono.PresetVer != 1 {
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
	out := Output{Preset: "poster", PresetVer: 1, Width: 640, Height: 480}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"Preset":"poster"`) {
		t.Errorf("json = %s", b)
	}
}
