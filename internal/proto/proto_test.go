package proto

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestCfgKeysMatchDoc pins the Go table to the docs/proto-v1.md desired_cfg
// table: same keys, same ranges. The doc is the source of truth (§protocol);
// if this fails, change both together.
func TestCfgKeysMatchDoc(t *testing.T) {
	raw, err := os.ReadFile("../../docs/proto-v1.md")
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}

	// Parse the `| \`key\` | type | range | ...` rows of the desired_cfg table.
	doc := map[string][2]int{} // key → {min, max}; hasRange=false → {0,0} sentinel handled below
	docHasRange := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "| Key | Type | Range") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break // table ended
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		key := strings.TrimSpace(strings.Trim(cells[1], "` "))
		if key == "Key" || strings.Trim(key, "-") == "" {
			continue
		}
		rng := strings.TrimSpace(cells[3])
		if rng == "—" || rng == "-" {
			docHasRange[key] = false
			doc[key] = [2]int{}
			continue
		}
		rng = strings.ReplaceAll(rng, "\u2212", "-") // doc uses U+2212 minus
		var lo, hi int
		sep := "–"
		if !strings.Contains(rng, sep) {
			sep = "/" // "0/1" style
		}
		parts := strings.SplitN(rng, sep, 2)
		if len(parts) != 2 {
			t.Fatalf("unparsed range %q for %s", rng, key)
		}
		var err error
		if lo, err = strconv.Atoi(strings.TrimSpace(parts[0])); err != nil {
			t.Fatalf("doc range %q for %s: %v", rng, key, err)
		}
		if hi, err = strconv.Atoi(strings.TrimSpace(parts[1])); err != nil {
			t.Fatalf("doc range %q for %s: %v", rng, key, err)
		}
		doc[key] = [2]int{lo, hi}
		docHasRange[key] = true
	}
	if len(doc) != 22 {
		t.Fatalf("parsed %d doc keys, want 22 (table changed shape?)", len(doc))
	}

	for key, want := range doc {
		spec, ok := CfgKeys[key]
		if !ok {
			t.Errorf("CfgKeys missing documented key %q", key)
			continue
		}
		if docHasRange[key] && (spec.Min != want[0] || spec.Max != want[1]) {
			t.Errorf("%s range = [%d,%d], doc says [%d,%d]", key, spec.Min, spec.Max, want[0], want[1])
		}
	}
	for key := range CfgKeys {
		if _, ok := doc[key]; !ok {
			t.Errorf("CfgKeys has %q which the doc table does not document", key)
		}
	}
	if len(CfgKeys) != 22 {
		t.Errorf("CfgKeys has %d entries, want 22", len(CfgKeys))
	}
}

func TestValidateCfgKey(t *testing.T) {
	// ver is server-managed: never settable by an operator.
	if err := ValidateCfgKey("ver", 3); err == nil {
		t.Error("ver must be rejected")
	}
	if err := ValidateCfgKey("interval_s", 45); err != nil {
		t.Errorf("valid set rejected: %v", err)
	}
	if err := ValidateCfgKey("interval_s", 10); err == nil {
		t.Error("interval_s=10 (below 30) must be rejected")
	}
	if err := ValidateCfgKey("led_pwm", 256); err == nil {
		t.Error("led_pwm=256 (above 255) must be rejected")
	}
	if err := ValidateCfgKey("brightness", -2); err != nil {
		t.Errorf("brightness=-2 must be accepted: %v", err)
	}
	if err := ValidateCfgKey("brightness", -3); err == nil {
		t.Error("brightness=-3 must be rejected")
	}
	if err := ValidateCfgKey("typo_key", 1); err == nil {
		t.Error("unknown key must be rejected")
	}
	if got := SettableCfgKeys(); len(got) != 21 {
		t.Errorf("SettableCfgKeys = %d keys, want 21 (22 minus ver)", len(got))
	}
}

func TestDesiredCfgJSONFlatRoundtrip(t *testing.T) {
	in := DesiredCfg{
		Ver: 7, IntervalS: 45, Quality: 12,
		Extra: map[string]int{"framesize": 5, "led_pwm": 0, "brightness": -1},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Flat single object; explicit zero (led_pwm=0) must survive.
	var m map[string]int
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("wire form not a flat object: %s (%v)", b, err)
	}
	want := map[string]int{"ver": 7, "interval_s": 45, "quality": 12,
		"framesize": 5, "led_pwm": 0, "brightness": -1}
	if len(m) != len(want) {
		t.Errorf("wire form = %s, want keys %v", b, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("wire[%s] = %d, want %d (%s)", k, m[k], v, b)
		}
	}

	var out DesiredCfg
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ver != 7 || out.IntervalS != 45 || out.Quality != 12 ||
		out.Extra["framesize"] != 5 || out.Extra["led_pwm"] != 0 || out.Extra["brightness"] != -1 {
		t.Errorf("roundtrip = %+v", out)
	}

	// A trio-only response (no extras) round-trips with Extra == nil.
	only, err := json.Marshal(DesiredCfg{Ver: 1, IntervalS: 60, Quality: 18})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(only, &out); err != nil {
		t.Fatal(err)
	}
	if out.Extra != nil {
		t.Errorf("trio-only Extra = %v, want nil", out.Extra)
	}
}
