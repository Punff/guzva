// Package qa holds the QA harness's hermetic checks — the ones that need no
// server, no network and no fixtures, and can therefore run inside
// `make test` alongside the repo's own tests.
//
// The shell tests in this directory (test_ingest.sh, test_pagecache.sh,
// test_public_page.sh) exercise a real `guzva serve` over HTTP. That proves
// the system works. These prove three things a server cannot tell you:
// state colours are legible to a human, both catalogs say the same thing,
// and no user-visible string escaped the catalog.
package qa

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// repoRoot walks up to the module root (this package lives in qa/).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("repo root not found")
	return ""
}

// ---------------------------------------------------------------- colour

type rgb struct{ r, g, b float64 }

// parseHex reads #rrggbb from the design tokens. A token this test cannot
// read is a finding, not a skip: an unreadable colour is an unreadable state.
func parseHex(t *testing.T, s string) rgb {
	t.Helper()
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	if len(s) != 6 {
		t.Fatalf("colour %q is not #rrggbb", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		t.Fatalf("colour %q: %v", s, err)
	}
	return rgb{float64(v>>16) / 255, float64((v>>8)&0xff) / 255, float64(v&0xff) / 255}
}

func (c rgb) lum() float64 {
	f := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.r) + 0.7152*f(c.g) + 0.0722*f(c.b)
}

// contrast is the WCAG 2.x ratio between two colours, 1.0 … 21.0.
func contrast(a, b rgb) float64 {
	la, lb := a.lum(), b.lum()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// token reads one custom property out of web/tokens.css.
func token(t *testing.T, css, name string) rgb {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `:\s*(#[0-9a-fA-F]{6})\s*;`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("token %s not found in web/tokens.css — an unreadable state colour is a finding, not a skip", name)
	}
	return parseHex(t, m[1])
}

// TestStateChipContrast is the WCAG AA check on the state chips.
//
// DESIGN.md §3.2 makes colour redundant on purpose — every state also has a
// silhouette and a sentence — so a chip that fails contrast is not "the page
// is unreadable". It is worse than that in a specific way: the chip is the
// fastest thing a visitor reads, it is the page's one-word answer to "is it
// busy?", and at 16px on --paper the failing pairs are the ones people with
// low vision or a dim phone screen hit first. AA wants 4.5:1 for text this
// size (the chip is font-size 1rem / weight 600 — not "large text", which
// starts at 18.66px bold, so the full 4.5 applies).
func TestStateChipContrast(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "tokens.css"))
	if err != nil {
		t.Fatalf("read tokens.css: %v", err)
	}
	css := string(raw)

	paper := token(t, css, "--paper")
	paper2 := token(t, css, "--paper-2")
	ink := token(t, css, "--ink")
	muted := token(t, css, "--muted")
	accent := token(t, css, "--accent")

	// The chip's own foreground is inherited from .chip { background:
	// var(--paper) }; only the shape is tinted, via text-<state>. The chip
	// LABEL itself is ink. So both halves are checked: the label, and each
	// state colour as it is actually used (the shape on paper).
	cases := []struct {
		what   string
		fg, bg rgb
		min    float64
	}{
		{"chip label (ink on paper)", ink, paper, 4.5},
		{"body text (ink on paper-2)", ink, paper2, 4.5},
		{"muted text on paper", muted, paper, 4.5},
		{"muted text on paper-2 (hours/info cards)", muted, paper2, 4.5},
		{"state: fresh shape on paper", token(t, css, "--fresh"), paper, 3.0},
		{"state: late shape on paper", token(t, css, "--late"), paper, 3.0},
		{"state: nosignal shape on paper", token(t, css, "--stale"), paper, 3.0},
		{"state: closed/waiting shape on paper", muted, paper, 3.0},
		{"accent (logomark peak, btn--primary text)", accent, paper, 4.5},
	}
	for _, c := range cases {
		got := contrast(c.fg, c.bg)
		if got < c.min {
			t.Errorf("%s: contrast %.2f:1 is below the %.1f:1 minimum (WCAG AA)", c.what, got, c.min)
		} else {
			t.Logf("%s: %.2f:1 (min %.1f) OK", c.what, got, c.min)
		}
	}
}

// TestFocusRingIsVisible pins the focus indicator: a page that fails contrast
// still has to be usable by keyboard, and a 3px accent ring at 3:1 is the
// only thing telling a keyboard user where they are.
func TestFocusRingIsVisible(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "tokens.css"))
	if err != nil {
		t.Fatalf("read tokens.css: %v", err)
	}
	css := string(raw)
	paper := token(t, css, "--paper")
	accent := token(t, css, "--accent")
	ring := token(t, css, "--ink")

	if !strings.Contains(css, "outline: 3px solid var(--accent)") {
		t.Errorf("the :focus-visible outline rule is gone from tokens.css")
	}
	// WCAG 2.2 SC 1.4.11 (non-text contrast): a focus indicator needs 3:1
	// against what surrounds it.
	if got := contrast(accent, paper); got < 3.0 {
		t.Errorf("focus ring (accent on paper) is %.2f:1, below the 3:1 non-text minimum", got)
	}
	_ = ring
}

// ------------------------------------------------------------------- i18n

// TestCatalogKeyAndPlaceholderParity is the coverage check D7 asks for, from
// the rendered-text side rather than the key side.
//
// internal/i18n already pins that hr and en have identical KEY SETS. That is
// necessary but not sufficient: a key can exist in both catalogs and still be
// broken in one, because Get() falls back to Croatian for a missing key and
// silently repairs what the parity test would have caught a different way.
// The case nothing else tests is a key present in both whose PLACEHOLDERS
// disagree — `{time}` in hr and `{t}` in en — where every call site still
// compiles, the key still exists, and an English venue reads "The last picture
// is from " with the time silently dropped.
func TestCatalogKeyAndPlaceholderParity(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "i18n", "i18n.go"))
	if err != nil {
		t.Fatalf("read i18n.go: %v", err)
	}
	catalogs := parseCatalogs(t, src)
	if len(catalogs) != 2 {
		t.Fatalf("expected 2 catalogs, parsed %d", len(catalogs))
	}
	var hr, en map[string]string
	for _, c := range catalogs {
		switch c.lang {
		case "hr":
			hr = c.msgs
		case "en":
			en = c.msgs
		}
	}
	if hr == nil || en == nil {
		t.Fatalf("could not identify both catalogs (hr=%v en=%v)", hr != nil, en != nil)
	}

	for k, hv := range hr {
		ev, ok := en[k]
		if !ok {
			t.Errorf("key %q missing from the en catalog", k)
			continue
		}
		if got, want := placeholders(ev), placeholders(hv); got != want {
			t.Errorf("key %q: en placeholders %v, hr placeholders %v — a call site cannot satisfy both", k, got, want)
		}
	}
	for k := range en {
		if _, ok := hr[k]; !ok {
			t.Errorf("key %q missing from the hr catalog", k)
		}
	}
}

var phRe = regexp.MustCompile(`\{[^{}]*\}`)

// placeholders returns the {…} tokens of a message in order of appearance.
func placeholders(s string) string {
	return strings.Join(phRe.FindAllString(s, -1), ",")
}

type catalog struct {
	lang string
	msgs map[string]string
}

// parseCatalogs finds `var hr = &Catalog{…}` / `var en = &Catalog{…}` and
// pulls the msg map out of each by brace matching — deliberately not by
// importing the package, so the test reads the SHIPPED source including any
// key a build tag might hide.
func parseCatalogs(t *testing.T, src []byte) []catalog {
	t.Helper() //nolint:gocritic // t is used below
	s := string(src)
	var out []catalog
	for _, lang := range []string{"hr", "en"} {
		marker := "var " + lang + " = &Catalog{"
		i := strings.Index(s, marker)
		if i < 0 {
			continue
		}
		msgs := map[string]string{}
		j := strings.Index(s[i:], "msg: map[string]string{")
		if j < 0 {
			continue
		}
		body := s[i+j:]
		depth, k := 0, strings.Index(body, "{")
		if k < 0 {
			continue
		}
		for idx := k; idx < len(body); idx++ {
			switch body[idx] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					body = body[:idx]
					idx = len(body)
				}
			}
			if depth == 0 && idx >= len(body) {
				break
			}
		}
		pair := regexp.MustCompile(`(?m)^\s*"([^"]+)":\s*"((?:[^"\\]|\\.)*)"`)
		for _, m := range pair.FindAllStringSubmatch(body, -1) {
			msgs[m[1]] = strings.ReplaceAll(m[2], `\"`, `"`)
		}
		out = append(out, catalog{lang: lang, msgs: msgs})
	}
	return out
}

// TestNoUserVisibleLiteralsInTemplates catches copy that escaped the catalog.
//
// D7 says user-facing strings live in internal/i18n and nowhere else. The
// existing i18n test scans Go `Get`/`F` calls, which cannot see a literal
// typed straight into a template — the one place it is most likely to appear.
//
// Scope is deliberately internal/pages/templates only: the public page is
// the Croatian-first product surface D7 governs. The admin console is
// catalog-free English BY DESIGN ("admin/internal may stay English"), so
// scanning it would produce a wall of false positives and train everyone to
// ignore this test.
func TestNoUserVisibleLiteralsInTemplates(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join("internal", "pages", "templates")

	// Literal text that is correct to have in markup: the brand, the domain,
	// and the separator glyphs. Everything else must come from the catalog.
	allowed := map[string]bool{
		"guzva": true, "guzva.ba": true, "guzva –": true,
	}

	var hits []string
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		rel := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, lit := range textNodes(string(raw)) {
			if allowed[lit] || !looksLikeProse(lit) {
				continue
			}
			hits = append(hits, rel+": "+lit)
		}
	}
	for _, h := range hits {
		t.Errorf("user-visible text outside the catalog: %s", h)
	}
}

// textNodes returns the literal prose a visitor would see.
//
// Four things are removed before the scan, and each removal is load-bearing:
//   - HTML comments          — the templates are heavily commented prose
//   - <script>/<style> bodies — the poller and the design system
//   - {{/* … */}}            — Go template COMMENTS, which are also prose
//   - {{ … }}                — template ACTIONS, so "{{.Name}} — {{.Tagline}}"
//     does not read as the literal words "Name" and "Tagline"
func textNodes(page string) []string {
	page = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(page, " ")
	page = regexp.MustCompile(`(?s)<script.*?</script>`).ReplaceAllString(page, " ")
	page = regexp.MustCompile(`(?s)<style.*?</style>`).ReplaceAllString(page, " ")
	page = regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`).ReplaceAllString(page, " ")
	// Drop the attribute half of any remaining tag before splitting on ">".
	var out []string
	for _, chunk := range strings.Split(page, ">") {
		if i := strings.Index(chunk, "<"); i >= 0 {
			chunk = chunk[:i]
		}
		chunk = regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(chunk, " ")
		if s := strings.TrimSpace(chunk); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var wordRe = regexp.MustCompile(`[A-Za-zŠšĐđČčĆćŽž]{2,}`)

// looksLikeProse is deliberately conservative: at least two words, at least
// one capitalised, and no leftover template punctuation — so class lists, ids
// and separator glyphs never trip it. Real copy has two capitalised words.
func looksLikeProse(s string) bool {
	if strings.ContainsAny(s, "{}=") {
		return false
	}
	words := wordRe.FindAllString(s, -1)
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		if w[0] >= 'A' && w[0] <= 'Z' {
			return true
		}
	}
	return false
}
