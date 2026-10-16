package webui

import (
	"strings"
	"testing"
)

// A fixed URL for changing bytes is how the console rendered unstyled once:
// the templates moved to .card/.btn/.inp while browsers held a stylesheet
// from before those classes existed, and nothing failed — the page simply had
// no styling. The URL must change when the bytes change.
func TestAppPathIsContentHashed(t *testing.T) {
	p := AppPath()
	if !strings.HasPrefix(p, "/admin/app.") || !strings.HasSuffix(p, ".css") {
		t.Fatalf("AppPath() = %q, want /admin/app.<hash>.css", p)
	}
	if strings.Contains(p, "app.css") {
		t.Errorf("AppPath() = %q is not hashed — a fixed URL can go stale", p)
	}
	sum := strings.TrimSuffix(strings.TrimPrefix(p, "/admin/app."), ".css")
	if len(sum) != 8 {
		t.Errorf("hash %q is %d chars, want 8", sum, len(sum))
	}
	// The bytes must actually be the ones served.
	if len(appCSS) == 0 {
		t.Fatal("embedded stylesheet is empty — the build did not run")
	}
}

// W2/B1: the chip templates assemble state class names at render time
// (`class="text-{{.StateClass}}"`), so no scanner can generate them as
// utilities — they must ship as explicit CSS from web/tokens.css. Before
// W2 they did not exist at all and every chip on every surface rendered its
// silhouette in inherited ink. Pin each state class in BOTH stylesheets;
// the public page and the consoles share the same vocabulary.
func TestStateColourClassesShip(t *testing.T) {
	states := []string{"fresh", "late", "stale", "nosignal", "closed", "waiting", "unknown"}
	for name, css := range map[string]string{"app.css": string(appCSS), "public.css": string(Public())} {
		for _, s := range states {
			if !strings.Contains(css, "text-"+s) {
				t.Errorf("%s lacks .text-%s — the %s chip would render in ink (W2/B1)", name, s, s)
			}
		}
	}
}
