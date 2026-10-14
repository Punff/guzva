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

// TisakPreset: the 4-tone print (poster@4).
//
// Versions: v1 colour JPEG, v2 stamped PNG, v3 160×120 blur/threshold/mode,
// v4 the 320×240 band-pass with Weber-contrast tones (docs/print-v4.md).
// D2's rule: bump whenever the output changes for the same input, so old
// sidecars keep their meaning.
type TisakPreset struct{}

func (TisakPreset) Name() string { return "poster" }
func (TisakPreset) Version() int { return 4 }
func (TisakPreset) Levels() int  { return 4 }

// SablonPreset: the 2-tone stencil (mono@4) — the same contrast map, inked
// where the scene is darker than its surroundings.
type SablonPreset struct{}

func (SablonPreset) Name() string { return "mono" }
func (SablonPreset) Version() int { return 4 }
func (SablonPreset) Levels() int  { return 2 }
