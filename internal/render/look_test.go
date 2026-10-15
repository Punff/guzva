package render

import "testing"

func TestLookParams(t *testing.T) {
	if (Look{}).Params() != Defaults {
		t.Fatal("zero Look must be Defaults")
	}
	if (Look{Outline: "bogus", Blur: "sharpest", Contrast: "x"}).Params() != Defaults {
		t.Fatal("unknown values must fall back to Defaults")
	}
	// Privacy floor: no option blurs less than Defaults.
	for _, o := range BlurOptions {
		if s := (Look{Blur: o.Value}).Params().SigmaP; s < Defaults.SigmaP {
			t.Errorf("blur %q has σp %.2f < default %.2f", o.Value, s, Defaults.SigmaP)
		}
	}
	if p := (Look{Outline: "off"}).Params(); p.Edge != 0 {
		t.Errorf("outline off: edge %d", p.Edge)
	}
	soft, strong := (Look{Contrast: "soft"}).Params(), (Look{Contrast: "strong"}).Params()
	if !(soft.Light > Defaults.Light && strong.Light < Defaults.Light && soft.Dark1 < Defaults.Dark1) {
		t.Errorf("contrast thresholds not ordered: soft %+v strong %+v", soft, strong)
	}
}
