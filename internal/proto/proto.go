// Package proto holds the device<->server wire types and constants for
// protocol v1. docs/proto-v1.md is the human-readable source of truth; this
// package is its Go mirror. It is a leaf package: server and fakecam both
// depend on it, it depends on nothing but the stdlib.
package proto

import (
	"regexp"
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
	MinQuality     = 5 // OV2640 jpeg quality: lower = better
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

// DesiredCfg is the device-plane config piggybacked on every response
// (downlink on the uplink: no extra round-trip on weak links).
type DesiredCfg struct {
	Ver       int `json:"ver"`
	IntervalS int `json:"interval_s"`
	Quality   int `json:"quality"`
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
