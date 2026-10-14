package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func png4(t *testing.T, c *Code, path string) {
	t.Helper()
	const s, q = 6, 4
	w := (c.Size + 2*q) * s
	img := image.NewGray(image.Rect(0, 0, w, w))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Dark(x, y) {
				for dy := 0; dy < s; dy++ {
					for dx := 0; dx < s; dx++ {
						img.SetGray((x+q)*s+dx, (y+q)*s+dy, color.Gray{})
					}
				}
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Every version 1–10 decodes back to its text with a real decoder.
func TestRoundTripZbar(t *testing.T) {
	zbar, err := exec.LookPath("zbarimg")
	if err != nil {
		t.Skip("zbarimg not installed")
	}
	dir := t.TempDir()
	seen := map[int]bool{}
	// Each length is a version's level-M byte capacity: v1 14 … v10 213.
	for _, n := range []int{14, 26, 42, 62, 84, 106, 122, 152, 180, 213} {
		text := ("https://guzva.ba/" + strings.Repeat("x", n))[:n]
		c, err := Encode(text)
		if err != nil {
			t.Fatalf("len %d: %v", len(text), err)
		}
		seen[c.Version] = true
		p := filepath.Join(dir, "q.png")
		png4(t, c, p)
		out, err := exec.Command(zbar, "--quiet", "--raw", p).Output()
		if err != nil {
			t.Fatalf("v%d (len %d, mask %d): zbar failed: %v", c.Version, len(text), c.Mask, err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != text {
			t.Fatalf("v%d: decoded %q, want %q", c.Version, got, text)
		}
	}
	for v := 1; v <= 10; v++ {
		if !seen[v] {
			t.Errorf("version %d not exercised", v)
		}
	}
}

func TestTooLong(t *testing.T) {
	if _, err := Encode(strings.Repeat("a", 300)); err != ErrTooLong {
		t.Fatalf("err = %v", err)
	}
}

func TestSVGShape(t *testing.T) {
	c, _ := Encode("https://34.139.58.143/bench/?src=qr")
	svg := c.SVG("#12202d")
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, `fill="#12202d"`) || strings.Contains(svg, "<script") {
		t.Fatalf("svg = %.120s", svg)
	}
}
