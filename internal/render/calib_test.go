package render

import (
	"reflect"
	"testing"
)

func TestParseCalib(t *testing.T) {
	// Empty → defaults.
	if c := ParseCalib(""); !reflect.DeepEqual(c, Calib{}) {
		t.Fatalf("empty = %+v, want zero", c)
	}
	// Full document round-trips.
	c := ParseCalib(`{"t":[70,140,200],"bias":-12,"invert":true,"masks":[{"X0":10,"Y0":20,"X1":30,"Y1":40}],"preset":"poster"}`)
	if c.T != [3]uint8{70, 140, 200} || c.Bias != -12 || !c.Invert || c.Preset != "poster" {
		t.Fatalf("full doc = %+v", c)
	}
	if len(c.Masks) != 1 || c.Masks[0] != (Rect{10, 20, 30, 40}) {
		t.Fatalf("masks = %+v", c.Masks)
	}
	// Malformed → defaults, never an error the render path must handle.
	for _, bad := range []string{"{", `{"t":[1,2]}`, `{"bogus":1}`, "not json",
		`{"t":[200,100,50]}`, // non-monotonic thresholds rejected
		`{"t":[0,0,0]}`,      // zero triple means "unset", not "all black"
	} {
		if got := ParseCalib(bad); !reflect.DeepEqual(got, Calib{}) {
			t.Errorf("ParseCalib(%q) = %+v, want zero", bad, got)
		}
	}
	// Five masks → four. The cap is enforced at parse so the stored document
	// and the effective one agree.
	many := ParseCalib(`{"masks":[{"X0":1,"Y0":1,"X1":2,"Y1":2},{"X0":3,"Y0":3,"X1":4,"Y1":4},{"X0":5,"Y0":5,"X1":6,"Y1":6},{"X0":7,"Y0":7,"X1":8,"Y1":8},{"X0":9,"Y0":9,"X1":10,"Y1":10}]}`)
	if len(many.Masks) != MaxMasks {
		t.Fatalf("masks = %d, want cap %d", len(many.Masks), MaxMasks)
	}
}
