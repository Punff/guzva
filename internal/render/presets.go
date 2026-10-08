package render

import "image"

// Preset is one public style. Implementations must be pure and
// deterministic: same input pixels → same output bytes, no clock, no
// randomness, no globals (§7). A new style = one file + one registry line +
// one golden test.
//
// There is no raw preset — ever (§7). The registry simply has no entry that
// would pass frames through unstyled.
type Preset interface {
	Name() string
	// Version bumps whenever Render's output changes for the same input;
	// frames record preset@version so a style change never rewrites meaning
	// (D2).
	Version() int
	// Render transforms src in place (it owns the scratch buffer) and
	// returns the result.
	Render(src *image.RGBA) *image.RGBA
}

// quant4 maps a channel value onto 4 evenly spaced levels
// {0, 85, 170, 255} with rounding (posterization).
func quant4(v uint8) uint8 {
	return uint8((int(v)*3 + 127) / 255 * 85)
}

// PosterPreset: colour posterization to 4 levels per channel (§7).
type PosterPreset struct{}

func (PosterPreset) Name() string { return "poster" }
func (PosterPreset) Version() int { return 1 }

func (PosterPreset) Render(src *image.RGBA) *image.RGBA {
	pix := src.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = quant4(pix[i])
		pix[i+1] = quant4(pix[i+1])
		pix[i+2] = quant4(pix[i+2])
		// pix[i+3] stays opaque; renders are never transparent.
	}
	return src
}

// MonoPreset: grayscale, then the same 4-level posterization (§7).
type MonoPreset struct{}

func (MonoPreset) Name() string { return "mono" }
func (MonoPreset) Version() int { return 1 }

func (MonoPreset) Render(src *image.RGBA) *image.RGBA {
	pix := src.Pix
	for i := 0; i < len(pix); i += 4 {
		lum := (int(pix[i])*77 + int(pix[i+1])*150 + int(pix[i+2])*29) >> 8
		q := quant4(uint8(lum))
		pix[i], pix[i+1], pix[i+2] = q, q, q
	}
	return src
}
