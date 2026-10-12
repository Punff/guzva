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

// TisakPreset: the 4-tone print (DESIGN.md §5, poster@3).
//
// Version 3: v1 was the 640-wide colour JPEG era, v2 the stamped PNG
// pipeline. v3 drops the burned-in HH:MM stamp (12 Oct 2026 — the page's
// print label carries the time; a rendered clock next to the label's clock
// read as two clocks). D2's rule: a bump whenever the output changes for
// the same input, so old sidecars keep their meaning.
type TisakPreset struct{}

func (TisakPreset) Name() string { return "poster" }
func (TisakPreset) Version() int { return 3 }
func (TisakPreset) Levels() int  { return 4 }

// SablonPreset: the 2-tone stencil (DESIGN.md §5, mono@3). Same version
// reasoning as Tisak; v3 drops the stamp with it.
type SablonPreset struct{}

func (SablonPreset) Name() string { return "mono" }
func (SablonPreset) Version() int { return 3 }
func (SablonPreset) Levels() int  { return 2 }
