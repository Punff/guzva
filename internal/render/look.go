package render

// Look is a venue's picture tuning, chosen from named presets in the admin
// console — never raw numbers, so a setting cannot break the picture or
// weaken privacy: every Blur option is at least as strong as Defaults.
// The zero value is Defaults.
type Look struct {
	Outline  string `json:"outline,omitempty"`  // off | light | normal (default) | strong
	Blur     string `json:"blur,omitempty"`     // standard (default) | softer | softest
	Contrast string `json:"contrast,omitempty"` // soft | normal (default) | strong
}

// LookOption is one choice for the console's selects.
type LookOption struct{ Value, Label string }

// The option lists, default first. Labels are console copy (English).
var (
	OutlineOptions = []LookOption{
		{"normal", "Normal"}, {"light", "Light"}, {"strong", "Strong"}, {"off", "Off"},
	}
	BlurOptions = []LookOption{
		{"standard", "Standard"}, {"softer", "Softer (more private)"}, {"softest", "Softest (most private)"},
	}
	ContrastOptions = []LookOption{
		{"normal", "Normal"}, {"soft", "Soft (busy, contrasty rooms)"}, {"strong", "Strong (flat, dim rooms)"},
	}
)

// Normalize maps unknown or empty values to the defaults, so a stored
// document from an older binary (or a hand edit) can never fail a render.
func (l Look) Normalize() Look {
	pick := func(v string, opts []LookOption) string {
		for _, o := range opts {
			if o.Value == v {
				return v
			}
		}
		return opts[0].Value
	}
	return Look{
		Outline:  pick(l.Outline, OutlineOptions),
		Blur:     pick(l.Blur, BlurOptions),
		Contrast: pick(l.Contrast, ContrastOptions),
	}
}

// Params resolves the look onto the frozen Defaults.
func (l Look) Params() Params {
	l = l.Normalize()
	p := Defaults
	switch l.Outline {
	case "off":
		p.Edge = 0
	case "light":
		p.Edge = 28
	case "strong":
		p.Edge = 14
	}
	switch l.Blur {
	case "softer":
		p.SigmaP = 2.25
	case "softest":
		p.SigmaP = 2.75
	}
	// Contrast scales the tone thresholds: soft needs a bigger difference
	// from the surroundings before a cell leaves neutral; strong a smaller.
	scale := func(v, num, den int) int { return v * num / den }
	switch l.Contrast {
	case "soft":
		p.Dark2, p.Dark1, p.Light = scale(p.Dark2, 4, 3), scale(p.Dark1, 4, 3), scale(p.Light, 4, 3)
	case "strong":
		p.Dark2, p.Dark1, p.Light = scale(p.Dark2, 7, 10), scale(p.Dark1, 7, 10), scale(p.Light, 7, 10)
	}
	return p
}
