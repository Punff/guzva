// Package proto holds the device<->server wire types and constants for
// protocol v1. docs/proto-v1.md is the human-readable source of truth; this
// package is its Go mirror. It is a leaf package: server and fakecam both
// depend on it, it depends on nothing but the stdlib.
package proto

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Paths and headers.
const (
	PathIngest  = "/v1/ingest"
	PathHealthz = "/healthz"

	HeaderProto = "X-Proto"
	HeaderAuth  = "Authorization"
	Version     = "v1"

	BearerPrefix = "Bearer "
)

// Bounds from PLAN §4. interval_s 30-300 (default 60).
const (
	MinIntervalS     = 30
	MaxIntervalS     = 300
	DefaultIntervalS = 60

	DefaultQuality = 18
	MinQuality     = 4 // OV2640 jpeg quality: lower = better (proto-v1.md table)
	MaxQuality     = 63

	// Part names inside the multipart body.
	PartMeta  = "meta"
	PartFrame = "frame"

	// Size caps (PLAN §4: 422/413 boundaries, §7 decode cap).
	MaxMetaBytes  = 16 << 10                                 // 16 KiB
	MaxFrameBytes = 4 << 20                                  // 4 MiB (decode cap)
	MaxBodyBytes  = MaxMetaBytes + MaxFrameBytes + (8 << 10) // + multipart framing

	// Rate limiting: how long a 429'd client should back off.
	RetryAfterS = 10
)

// Response status codes (PLAN §4).
const (
	StatusStored       = 201 // stored, or health-only beat accepted
	StatusDuplicate    = 409 // same body as 201; retry must not re-process
	StatusRejected     = 422 // quarantined: rejected + reason, nothing stored
	StatusUnauthorized = 401
	StatusTooLarge     = 413
	StatusRateLimit    = 429
	StatusDiskFull     = 507
)

// Quarantine / rejection reasons. The server guarantees no bytes are stored
// for any of these; only headers-only records are written (PLAN §7).
const (
	ReasonBadMeta      = "bad_meta"
	ReasonBadCaptureID = "bad_capture_id"
	ReasonBadMagic     = "bad_magic"
	ReasonTooBig       = "too_big"
	ReasonDims         = "dims"
	ReasonTruncated    = "truncated"
	ReasonDark         = "dark"
	ReasonDead         = "dead"
	ReasonBusy         = "busy"
	ReasonDecode       = "truncated" // decode failure == truncated/corrupt corpus
)

// CaptureIDPattern is C<boot>-<ctr>-<rand4>, monotonic from NVS, bound to
// device identity (PLAN §4).
var CaptureIDPattern = regexp.MustCompile(`^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$`)

// ValidCaptureID reports whether an id matches the v1 shape.
func ValidCaptureID(id string) bool { return CaptureIDPattern.MatchString(id) }

// Counters are cumulative device health counters, part of meta (PLAN §4).
// boot and brownout persist in NVS; the rest are RAM-resident on the device.
type Counters struct {
	Boot        uint32 `json:"boot"`
	Brownout    uint32 `json:"brownout"`
	JoinFail    uint32 `json:"join_fail"`
	PostFail    uint32 `json:"post_fail"`
	TLSFail     uint32 `json:"tls_fail"`
	CaptureFail uint32 `json:"capture_fail"`
}

// Meta is the JSON in the `meta` part. Unknown additive fields are ignored.
type Meta struct {
	CaptureID     string   `json:"capture_id"`
	FW            string   `json:"fw"`
	RSSI          int      `json:"rssi"`
	FreeHeap      uint32   `json:"free_heap"`
	CfgVerApplied int      `json:"cfg_ver_applied"`
	Counters      Counters `json:"counters"`
	Reason        string   `json:"reason,omitempty"`
}

// CfgSpec is one settable desired_cfg key with its inclusive clamp range
// (docs/proto-v1.md §desired_cfg keys). The device clamps again on apply —
// this table exists so the server rejects typos and out-of-range values
// before they reach a field unit.
type CfgSpec struct {
	Min, Max int
}

// CfgKeys is the full 22-key desired_cfg table: cadence + OV2640 sensor
// feature set + device-side LED. "ver" is server-managed (Settable=false):
// it increments on every change and is never set directly. The keys, ranges
// and names are pinned to the docs/proto-v1.md table by a test — change
// both together.
var CfgKeys = map[string]CfgSpec{
	"ver":        {Min: 0, Max: 1 << 30}, // server-managed, see Settable below
	"interval_s": {Min: MinIntervalS, Max: MaxIntervalS},
	"quality":    {Min: MinQuality, Max: MaxQuality},
	"framesize":  {Min: 0, Max: 10}, // FRAMESIZE_QVGA…UXGA (OV2640 max)
	"brightness": {Min: -2, Max: 2},
	"contrast":   {Min: -2, Max: 2},
	"saturation": {Min: -2, Max: 2},
	"sharpness":  {Min: -2, Max: 2},
	"denoise":    {Min: 0, Max: 1},
	"special_fx": {Min: 0, Max: 6},
	"wb_mode":    {Min: 0, Max: 4},
	"awb":        {Min: 0, Max: 1},
	"aec":        {Min: 0, Max: 1},
	"agc":        {Min: 0, Max: 1},
	"ae_level":   {Min: -2, Max: 2},
	"agc_gain":   {Min: 0, Max: 30},
	"hmirror":    {Min: 0, Max: 1},
	"vflip":      {Min: 0, Max: 1},
	"dcw":        {Min: 0, Max: 1},
	"lenc":       {Min: 0, Max: 1},
	"colorbar":   {Min: 0, Max: 1},
	"led_pwm":    {Min: 0, Max: 255},
}

// SettableCfgKeys returns the keys an operator may set (all but "ver").
// The returned slice is sorted for stable CLI output.
func SettableCfgKeys() []string {
	out := make([]string, 0, len(CfgKeys))
	for k := range CfgKeys {
		if k != "ver" {
			out = append(out, k)
		}
	}
	sort.Strings(out) // stable CLI output
	return out
}

// ValidateCfgKey checks one operator-supplied key=value pair against the
// table. "ver" is rejected: the server owns the version counter.
func ValidateCfgKey(key string, val int) error {
	if key == "ver" {
		return fmt.Errorf("ver is server-managed")
	}
	spec, ok := CfgKeys[key]
	if !ok {
		return fmt.Errorf("unknown key %q (known: %v)", key, SettableCfgKeys())
	}
	if val < spec.Min || val > spec.Max {
		return fmt.Errorf("%s=%d out of range [%d,%d]", key, val, spec.Min, spec.Max)
	}
	return nil
}

// DesiredCfg is the device-plane config piggybacked on every response
// (downlink on the uplink: no extra round-trip on weak links).
//
// The wire form is one flat JSON object: the cadence trio always, plus any
// operator-set sensor keys in Extra. The device applies keys independently
// (apply-if-found), so a subset — including explicit zeros — is legal and
// means "leave everything else alone".
type DesiredCfg struct {
	Ver       int `json:"ver"`
	IntervalS int `json:"interval_s"`
	Quality   int `json:"quality"`
	// Extra holds the sensor keys (framesize, brightness, … led_pwm) the
	// operator has set. Never contains ver/interval_s/quality — the store
	// routes those to their columns; MarshalJSON skips them defensively.
	Extra map[string]int `json:"-"`
}

// MarshalJSON flattens the cadence trio and Extra into the single object
// proto-v1 specifies. Map marshalling sorts keys, so output is stable.
func (d DesiredCfg) MarshalJSON() ([]byte, error) {
	m := make(map[string]int, len(d.Extra)+3)
	m["ver"] = d.Ver
	m["interval_s"] = d.IntervalS
	m["quality"] = d.Quality
	for k, v := range d.Extra {
		if k == "ver" || k == "interval_s" || k == "quality" {
			continue
		}
		m[k] = v
	}
	return json.Marshal(m)
}

// UnmarshalJSON parses the flat object back (tests and tooling round-trip;
// the device never sends this shape).
func (d *DesiredCfg) UnmarshalJSON(b []byte) error {
	var m map[string]int
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	d.Ver, d.IntervalS, d.Quality = m["ver"], m["interval_s"], m["quality"]
	delete(m, "ver")
	delete(m, "interval_s")
	delete(m, "quality")
	if len(m) == 0 {
		d.Extra = nil
		return nil
	}
	d.Extra = m
	return nil
}

// Response is the body of 201, 409 and 422 (422 adds Reason). 201 and 409
// carry exactly these fields; server_time is stamped per reply.
type Response struct {
	DesiredCfg DesiredCfg `json:"desired_cfg"`
	NextS      int        `json:"next_s"`
	ServerTime time.Time  `json:"server_time"`
	Reason     string     `json:"reason,omitempty"`
}

// ErrorBody is the body of 401/413/429/507.
type ErrorBody struct {
	Error       string `json:"error"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
}

// ClampInterval forces interval_s into [Min,Max], defaulting invalid input.
func ClampInterval(v int) int {
	if v < MinIntervalS || v > MaxIntervalS {
		return DefaultIntervalS
	}
	return v
}

// ClampQuality forces quality into [Min,Max], defaulting invalid input.
func ClampQuality(v int) int {
	if v < MinQuality || v > MaxQuality {
		return DefaultQuality
	}
	return v
}
