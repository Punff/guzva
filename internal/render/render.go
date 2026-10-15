// Package render implements the server-side image pipeline (PLAN §7, D1):
//
//	decode (4 MiB cap) → validate → luma grid 320×240 → band-pass
//	(privacy low-pass − lighting field) → 4 fixed tones → paletted PNG
//
// The raw JPEG exists only inside one Process call: it is never written, and
// the input buffer is zeroed on return (privacy proof: input-zeroed test).
// A rejected frame returns a *RejectError with a reason and stores nothing —
// garbage never replaces a good still (§1).
package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/jpeg"
	"time"

	"guzva/internal/proto"
)

// Tunables for the pipeline.
const (
	// Bounds on accepted source frames. QQVGA 160x120 (bench favorite) is
	// fine; the OV2640 tops out at UXGA 1600x1200. Aspect must be exactly
	// 4:3 — the grid is 4:3, and stretching anything else onto it would
	// silently change what a cell means (non-4:3 is a dims reject).
	MinDim = 96
	MaxDim = 4096

	// DarkMeanLuma: mean grid luma (0-255) below this quarantines the frame
	// as "dark" — the bench produced 1884-byte all-black frames (§7).
	DarkMeanLuma = 6
)

// FrameIn is one ingest attempt. Raw is owned by Process and is zeroed on
// return regardless of success or failure.
type FrameIn struct {
	VenueID   int64
	CaptureID string
	Preset    string // venue style; unknown names fall back to the default
	Raw       []byte // the JPEG exactly as it arrived off the wire
	// Look is the venue's picture tuning (look.go); zero value = Defaults.
	Look Look
}

// Output is the result of a successful Process.
type Output struct {
	PNG        []byte // public stylised render, GW×GH paletted PNG
	Width      int    // always GW
	Height     int    // always GH
	SHA256     string // sha256 of Raw before zeroing — idempotency guard (§1)
	RawBytes   int    // size of the raw input (metadata only)
	Preset     string
	PresetVer  int
	ReceivedAt time.Time // server clock, canonical freshness (§1)
}

// RejectError marks a frame the pipeline refused. Reason is one of the
// proto reason constants; nothing is ever stored for it.
type RejectError struct {
	Reason string
	// Mean is the measured mean luma (0-255) for rejects that measured
	// one — dark and dead only. Every other reject leaves it 0, which
	// means "unmeasured", never "black": only dark/dead call sites use
	// RejectWithMean, so a dark/dead Mean is always a real measurement.
	Mean float64
}

func (e *RejectError) Error() string { return "frame rejected: " + e.Reason }

// Reject builds a *RejectError.
func Reject(reason string) error { return &RejectError{Reason: reason} }

// RejectWithMean builds a *RejectError carrying the measured mean luma.
// Use it at the dark/dead sites so operators can tell "covered lens"
// (mean ≈ 0) from "gain clamp" (mean low but nonzero) from the journal
// alone, without raw-image access (§1).
func RejectWithMean(reason string, mean float64) error {
	return &RejectError{Reason: reason, Mean: mean}
}

// IsReject reports whether err is a *RejectError and returns its reason.
func IsReject(err error) (string, bool) {
	if r, ok := err.(*RejectError); ok {
		return r.Reason, true
	}
	return "", false
}

// RejectMean returns the measured mean luma carried by a dark/dead reject
// (0 when the error carries no measurement — only dark/dead do).
func RejectMean(err error) float64 {
	if r, ok := err.(*RejectError); ok {
		return r.Mean
	}
	return 0
}

// Engine runs the pipeline. A single global semaphore bounds concurrent
// Process calls (§6.1): one slow frame must not starve the fast ones.
type Engine struct {
	now func() time.Time // injectable so tests can pin received_at
	sem chan struct{}
}

// NewEngine builds an Engine. now nil means time.Now; maxConcurrent caps
// in-flight Process calls (0 → 1).
func NewEngine(now func() time.Time, maxConcurrent int) *Engine {
	if now == nil {
		now = time.Now
	}
	return &Engine{now: now, sem: make(chan struct{}, max(maxConcurrent, 1))}
}

// Process runs the full pipeline for one frame. The input buffer is always
// zeroed on return; on rejection an *RejectError carries the quarantine
// reason. Context cancellation while waiting for the semaphore yields
// ReasonBusy (§4).
func (e *Engine) Process(ctx context.Context, in FrameIn) (Output, error) {
	// Privacy (D1): the raw buffer is ours for the duration of this call and
	// is zeroed on the way out, success or failure.
	defer clear(in.Raw)

	// Bounded work: global render gate (§6.1).
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return Output{}, Reject(proto.ReasonBusy)
	}

	sum := sha256.Sum256(in.Raw)
	out := Output{
		SHA256:     hex.EncodeToString(sum[:]),
		RawBytes:   len(in.Raw),
		ReceivedAt: e.now().UTC(),
	}

	style, ok := styles[in.Preset]
	if !ok {
		style = styles[DefaultStyle]
	}
	out.Preset, out.PresetVer = style.Name, style.Version

	// --- validate (cheap checks before the full decode) ---
	if len(in.Raw) > proto.MaxFrameBytes {
		return Output{}, Reject(proto.ReasonTooBig)
	}
	if len(in.Raw) < 2 || in.Raw[0] != 0xff || in.Raw[1] != 0xd8 {
		return Output{}, Reject(proto.ReasonBadMagic)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(in.Raw))
	if err != nil {
		return Output{}, Reject(proto.ReasonTruncated)
	}
	if cfg.Width < MinDim || cfg.Height < MinDim || cfg.Width > MaxDim || cfg.Height > MaxDim {
		return Output{}, Reject(proto.ReasonDims)
	}
	img, err := jpeg.Decode(bytes.NewReader(in.Raw))
	if err != nil {
		return Output{}, Reject(proto.ReasonTruncated)
	}

	// --- normalize to the luma grid, computing dark/dead-sensor stats on the
	// unmasked grid in the same pass (masks must not move the mean: a venue
	// with a big mask over a bright window is not a dark room). ---
	g, err := lumaGrid(img)
	if err != nil {
		return Output{}, err
	}
	if mean, uniform := gridStats(g); mean < DarkMeanLuma {
		return Output{}, RejectWithMean(proto.ReasonDark, mean)
	} else if uniform {
		return Output{}, RejectWithMean(proto.ReasonDead, mean)
	}

	// --- the print (tisak.go).
	pngBytes, err := encode(tones(g, style.Levels, nil, in.Look.Params()), style.Levels)
	if err != nil {
		return Output{}, err
	}
	out.PNG = pngBytes
	out.Width, out.Height = GW, GH
	return out, nil
}

// gridStats returns the mean luma and whether every cell is identical.
// Computed on the unmasked grid so installer masks cannot fake a dark room.
func gridStats(g []uint8) (mean float64, uniform bool) {
	var sum uint64
	first := g[0]
	uniform = true
	for _, v := range g {
		sum += uint64(v)
		if v != first {
			uniform = false
		}
	}
	return float64(sum) / float64(len(g)), uniform && len(g) > 1
}
