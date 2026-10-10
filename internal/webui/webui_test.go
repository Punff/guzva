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
	if len(App()) == 0 {
		t.Fatal("embedded stylesheet is empty — the build did not run")
	}
}
