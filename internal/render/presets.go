package render

// Preset is one public style. Implementations must be pure and
// deterministic: same input grid → same output levels, no clock, no
// randomness, no globals (§7). A new style = one registry line + one golden
// test.
//
// There is no raw preset — ever (§7). The registry simply has no entry that
// would pass frames through unstyled.
//
// Display names live in the UI (Tisak/Šablon); the registry keeps the stable
// IDs "poster"/"mono" so old rows keep their meaning (DESIGN.md §5.8).
type Preset interface {
	Name() string
	// Version bumps whenever the output changes for the same input; frames
	// record preset@version so a style change never rewrites meaning (D2).
	Version() int
	// Levels is the tone count: 4 for the print, 2 for the stencil.
	Levels() int
}

// TisakPreset: the 4-tone print (DESIGN.md §5, poster@2).
//
// Version 2, not 1: the previous poster v1 rendered a 640-wide colour JPEG,
// and D2 requires a bump whenever the output changes for the same input.
// DESIGN.md §5.8 numbers the new pipeline "poster@1", but the PLAN contract
// outranks the design doc's numbering — old rows on disk are v1 JPEGs and
// new rows are v2 PNGs, and the sidecar must tell them apart.
type TisakPreset struct{}

func (TisakPreset) Name() string { return "poster" }
func (TisakPreset) Version() int { return 2 }
func (TisakPreset) Levels() int  { return 4 }

// SablonPreset: the 2-tone stencil (DESIGN.md §5, mono@2). Same version
// reasoning as Tisak: the old mono v1 was a 640-wide greyscale JPEG.
type SablonPreset struct{}

func (SablonPreset) Name() string { return "mono" }
func (SablonPreset) Version() int { return 2 }
func (SablonPreset) Levels() int  { return 2 }
