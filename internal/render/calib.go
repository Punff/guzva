package render

import (
	"encoding/json"
	"strings"
)

// ParseCalib decodes a venue's calib_json document (DESIGN.md ⚑5):
//
//	{"t":[a,b,c],"bias":0,"invert":false,"masks":[{"x0":..,"y0":..,"x1":..,"y1":..}],"preset":"poster"}
//
// Empty means defaults. Anything malformed ALSO means defaults: a hand-edited
// document must never break the render path, it just does not apply —
// failing open on the picture beats failing closed on a typo. Thresholds must
// be strictly increasing (a non-monotonic triple would invert bands); masks
// beyond the cap are dropped so the stored document and the effective one
// cannot disagree silently.
func ParseCalib(raw string) Calib {
	var doc struct {
		T      [3]uint8 `json:"t"`
		Bias   int      `json:"bias"`
		Invert bool     `json:"invert"`
		Masks  []Rect   `json:"masks"`
		Preset string   `json:"preset"`
	}
	if raw == "" {
		return Calib{}
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Calib{}
	}
	c := Calib{Bias: doc.Bias, Invert: doc.Invert, Preset: doc.Preset}
	if doc.T[0] < doc.T[1] && doc.T[1] < doc.T[2] && doc.T[2] > 0 {
		c.T = doc.T
	}
	if len(doc.Masks) > MaxMasks {
		doc.Masks = doc.Masks[:MaxMasks]
	}
	c.Masks = doc.Masks
	return c
}
