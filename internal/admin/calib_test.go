package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"guzva/internal/webui"
)

// The calibration mask overlay is built by calib.js at RUNTIME, so a utility
// class set from JS is invisible to the Tailwind scanner and silently matches
// nothing — the W2/B1 shape. It shipped exactly that way: the overlay SVG
// rendered as a static block BELOW the picture, stretching the preview box
// with a dead zone while the masks drew in the wrong place. The positioning
// must therefore ship as explicit CSS, and no runtime class string may creep
// back in.
func TestCalibOverlayNeedsNoRuntimeClasses(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("static", "calib.js"))
	if err != nil {
		t.Fatalf("read calib.js: %v", err)
	}
	css := string(webui.App())

	// Positive pin: the overlay is positioned over the picture by an
	// explicit selector, in the console stylesheet.
	if !strings.Contains(css, "#calib-preview svg") {
		t.Error("app.css lacks the #calib-preview svg rule — the mask overlay has no positioning")
	}
	for _, prop := range []string{"position:absolute", "inset:0", "width:100%", "height:100%"} {
		if !strings.Contains(strings.ReplaceAll(css, " ", ""), prop) {
			t.Errorf("app.css #calib-preview rule lacks %q", prop)
		}
	}

	// Guard: every class calib.js sets at runtime must exist in the compiled
	// console sheet. Today it sets none; if a future edit adds one, this
	// fails before the overlay silently breaks again.
	re := regexp.MustCompile(`setAttribute\(\s*['"]class['"]\s*,\s*['"]([^'"]+)['"]\)`)
	for _, m := range re.FindAllSubmatch(js, -1) {
		for _, cls := range strings.Fields(string(m[1])) {
			if strings.Contains(css, "."+cls+"{") || strings.Contains(css, "."+cls+":") {
				continue
			}
			t.Errorf("calib.js sets runtime class %q, missing from app.css — the scanner cannot see it", cls)
		}
	}
}
