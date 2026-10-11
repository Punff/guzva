package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
)

// TestCatalogsHaveIdenticalKeySets pins the translation contract: hr is the
// product language and en its full counterpart. A key added to one catalog
// but not the other silently renders Croatian on an English venue (Get's
// fallback) — legal by design, but it must be a decision, not an accident.
func TestCatalogsHaveIdenticalKeySets(t *testing.T) {
	for k := range hr.msg {
		if _, ok := en.msg[k]; !ok {
			t.Errorf("key %q missing from the en catalog", k)
		}
	}
	for k := range en.msg {
		if _, ok := hr.msg[k]; !ok {
			t.Errorf("key %q missing from the hr catalog", k)
		}
	}
}

// TestFetchedKeysExist finds every key Go code and templates fetch and
// asserts it exists in both catalogs. This is the mechanical proof of the
// one-catalog rule (DESIGN §11): the owner dash used to fetch
// owner.fresh/late/stale before they existed, and Get's key-itself fallback
// rendered the raw key on the live panel — invisible to every content test
// because nothing enumerated the fetches (maintenance W2/B2).
func TestFetchedKeysExist(t *testing.T) {
	fetched := map[string]string{} // key → first file seen in
	// Go fetches: cat.Get("…") / cat.F("…"). Catalog keys always contain a
	// dot (owner.fresh, state.closed…); that also keeps url.Values.Get
	// ("saved", "created"…) out of the scan — different receiver, same verb.
	goRe := regexp.MustCompile(`\.(?:Get|F)\("([a-z][a-z0-9]*\.[a-z0-9_.]*)"\)`)
	// Template fetches: {{.Cat.Get "…"}} / {{(.Cat.Get "…")}}
	tmplRe := regexp.MustCompile(`(?:Get|F) "([a-z][a-z0-9]*\.[a-z0-9_.]*)"`)

	root := repoRoot(t)
	scan := func(path string, re *regexp.Regexp) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if _, seen := fetched[m[1]]; !seen {
				fetched[m[1]] = filepath.Base(path)
			}
		}
	}
	for _, dir := range []string{"internal/admin", "internal/pages", "cmd/guzva"} {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, f := range files {
			scan(f, goRe)
		}
	}
	for _, dir := range []string{"internal/admin/templates", "internal/pages/templates"} {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.html"))
		if err != nil || len(files) == 0 {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, f := range files {
			scan(f, tmplRe)
		}
	}
	if len(fetched) < 10 {
		t.Fatalf("scan found only %d fetches — the scanner itself is broken", len(fetched))
	}
	for k, where := range fetched {
		if _, ok := hr.msg[k]; !ok {
			t.Errorf("key %q (fetched in %s) missing from the hr catalog — it would render as the raw key", k, where)
		}
		if _, ok := en.msg[k]; !ok {
			t.Errorf("key %q (fetched in %s) missing from the en catalog", k, where)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = <root>/internal/i18n/i18n_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

func TestDayNameYesterdayUsesCatalog(t *testing.T) {
	// The "jučer" branch goes through the catalog now (it was a hardcoded
	// string per language before W2); this also pins the year-boundary fix.
	loc := time.FixedZone("CET", 3600)
	then := time.Date(2025, 12, 31, 12, 0, 0, 0, loc)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, loc)
	if got := hr.DayName(then, now); got != "jučer" {
		t.Errorf("hr DayName(31.12→1.1) = %q, want jučer", got)
	}
	if got := en.DayName(then, now); got != "yesterday" {
		t.Errorf("en DayName(31.12→1.1) = %q, want yesterday", got)
	}
}
