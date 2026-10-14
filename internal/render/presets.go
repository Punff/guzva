package render

// Style is one public print style. There is no raw style — ever (§7): the
// table simply has no entry that would pass frames through unstyled.
//
// Names are the stable IDs stored on venues and frames ("poster"/"mono");
// display names live in the UI (Tisak/Šablon, DESIGN.md §5.8).
//
// Version bumps whenever the output changes for the same input; frames
// record name@version so a style change never rewrites meaning (D2).
// History: v1 colour JPEG, v2 stamped PNG, v3 160×120 blur/threshold/mode,
// v4 the 320×240 band-pass with Weber-contrast tones (docs/print-v4.md),
// v5 adds 1-cell outlines so distant objects don't melt into the
// background (tuned on real bench frames, 14 Oct 2026), v6 a slightly
// lighter privacy blur (σp 2 → 1.75, user decision 14 Oct 2026).
type Style struct {
	Name    string
	Version int
	Levels  int // 4 for the print (Tisak), 2 for the stencil (Šablon)
}

var styles = map[string]Style{
	"poster": {Name: "poster", Version: 6, Levels: 4},
	"mono":   {Name: "mono", Version: 6, Levels: 2},
}

// DefaultStyle is what an unknown or empty venue style renders as.
const DefaultStyle = "poster"

// LookupStyle returns the named style.
func LookupStyle(name string) (Style, bool) {
	s, ok := styles[name]
	return s, ok
}
