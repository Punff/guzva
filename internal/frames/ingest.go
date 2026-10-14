// Package frames holds the ingest handler logic of PLAN §6.1:
//
//	ip-floor → auth → per-device limit → body cap → disk gate → parse meta →
//	idempotency check → render.Process → write file + row in one logical
//	step → update freshness → respond with next_s
//
// Bounded work: the render engine's global semaphore caps concurrent
// Process calls; the http.Server's Read/Write timeouts (set in main) stop
// slow clients tying up resources. Request bodies are never logged (§1).
package frames

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"guzva/internal/auth"
	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/store"
)

// MinFreePct: below this the disk is DEGRADED and ingest answers 507 (§8).
const MinFreePct = 15.0

// RenderTimeout bounds one ingest's pipeline wait (queue + render). Exceed
// it and the frame is rejected as busy — the device drops it and retries on
// its own tick (§5: no frame queue).
const RenderTimeout = 15 * time.Second

// Server is the ingest plane. Handlers never touch SQL (§3): everything
// goes through *store.
type Server struct {
	st          *store.Store
	eng         *render.Engine
	lim         *auth.Limiter
	dataDir     string
	retainHours int // §6.3 config retain_hours (default 48)
	now         func() time.Time
	log         *slog.Logger
	m           *Metrics
	// diskFree is DiskFreePct, injectable so tests can force the 507 gate.
	diskFree func(dir string) (float64, error)

	// OnStored runs after a frame is durably stored and before the response
	// goes out (§6.1: replace latest.png → rewrite index.html → respond).
	// It is the pages hook; an error is logged, never turned into a failure
	// (the frame IS stored — the next tick rewrites the page).
	OnStored func(ctx context.Context, venueID int64)
}

// New builds the ingest server. dataDir is the root under which venue
// renders live (venues/<id>/stills/…); retainHours is the ring size (§6.3).
func New(st *store.Store, eng *render.Engine, lim *auth.Limiter, dataDir string, retainHours int, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if retainHours <= 0 {
		retainHours = 48
	}
	return &Server{
		st: st, eng: eng, lim: lim, dataDir: dataDir, retainHours: retainHours,
		now: time.Now, log: log, m: NewMetrics(), diskFree: DiskFreePct,
	}
}

// Metrics exposes the ingest outcome counters (tests + the silent flood
// path). There is no /metrics endpoint — see metrics.go for why.
func (s *Server) Metrics() *Metrics { return s.m }

// reqError is a parse-stage rejection carrying its protocol status.
type reqError struct {
	status int
	reason string
}

func (e *reqError) Error() string { return fmt.Sprintf("%d %s", e.status, e.reason) }

func reject(status int, reason string) error { return &reqError{status: status, reason: reason} }

var errTooLarge = errors.New("part too large")

// HandleIngest implements POST /v1/ingest (docs/proto-v1.md).
func (s *Server) HandleIngest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	// Declared up front so respond can log serial/rssi/counters (+ render_ms
	// when a frame was actually processed) on every device-visible response.
	var (
		dev      *store.Device
		meta     proto.Meta
		frameRaw []byte
		renderMS float64 // only meaningful for a response that rendered
	)

	// respond is THE exit path for 201/409/422: it logs one line (Info for
	// accepted outcomes, Warn for rejects) and then writes the canonical
	// body. All of these statuses carry next_s (§4). The reject reason
	// goes into the journal too — a 422 that only lives in quarantine.jsonl
	// is invisible to whoever is on support.
	respond := func(status int, reason string, meanLuma float64) {
		s.logOutcome(status, dev, meta, r.TLS, renderMS, reason, meanLuma)
		s.writeResp(w, status, s.response(reason))
	}

	// Per-IP floor first — a deliberate refinement of the §6.1 order:
	// unauthenticated floods must not reach the database at all.
	if !s.lim.AllowIP(auth.ClientIP(r)) {
		s.m.add("ratelimited")
		// No log line on purpose: dropping floods must not become a log
		// flood (metrics count them).
		s.write429(w)
		return
	}

	// Auth (§6.1 step 1). Body/headers are never logged (§1).
	var err error
	dev, err = auth.Authenticate(ctx, s.st, r)
	if err != nil {
		s.m.add("unauthorized")
		s.log.Warn("ingest rejected", "err", err.Error(), "ip", auth.ClientIP(r))
		s.writeErr(w, proto.StatusUnauthorized, "unauthorized")
		return
	}

	// Per-device token bucket (§6.1 step 2).
	if !s.lim.AllowDevice(dev.ID) {
		s.m.add("ratelimited")
		s.logOutcome(proto.StatusRateLimit, dev, meta, r.TLS, 0, "", 0)
		s.write429(w)
		return
	}

	// Hard body cap (§6.1 step 3).
	r.Body = http.MaxBytesReader(w, r.Body, proto.MaxBodyBytes)

	// Disk gate before any render work (§8): <15 % free → DEGRADED.
	if pct, err := s.diskFree(s.dataDir); err == nil && pct < MinFreePct {
		s.m.add("disk_full")
		s.log.Error("disk degraded", "free_pct", pct)
		s.logOutcome(proto.StatusDiskFull, dev, meta, r.TLS, 0, "", 0)
		s.writeErr(w, proto.StatusDiskFull, "disk_degraded")
		return
	}

	// Parse meta + optional frame.
	meta, frameRaw, err = s.readMultipart(r)
	if err != nil {
		var re *reqError
		if errors.As(err, &re) {
			if re.status == proto.StatusTooLarge {
				s.m.add("too_large")
				s.logOutcome(proto.StatusTooLarge, dev, meta, r.TLS, 0, "", 0)
				s.writeErr(w, proto.StatusTooLarge, "too_large")
				return
			}
			// 422 parse-stage reject: quarantined record + parity with the
			// render-stage reject (W3/B7). The device authenticated and
			// POSTed, so it is alive — but the beat is written only when
			// the meta actually parsed (bad_capture_id parses fully, so
			// its fw/rssi/counters are real; bad_meta leaves zeros, and a
			// zero beat would overwrite good readings with lies on the
			// fleet board). A quarantined row goes in only when there is
			// a capture_id to key it, so a retried reject answers 409 like
			// every other processed capture.
			reason := reasonOf(err)
			// bad_capture_id is the one parse reject that follows a successful
			// unmarshal, so its fw/rssi/counters are real; bad_meta leaves
			// zeros, and a zero beat would overwrite good readings with lies
			// on the fleet board (the meta-parsed check, not field sniffing).
			if reason == proto.ReasonBadCaptureID {
				if berr := s.st.UpdateDeviceBeat(ctx, dev.ID, beatOf(meta, s.now().UTC())); berr != nil {
					s.log.Error("beat update failed", "err", berr.Error())
				}
			}
			s.quarantine(ctx, dev, meta.CaptureID, reason, len(frameRaw), "", meta)
			if meta.CaptureID != "" {
				// Idempotency applies to rejected captures too: a retry of the
				// same (bogus) id answers 409 with the same body, exactly like
				// a retry of a stored one — the parse stage runs before the
				// general pre-check, so it carries its own.
				if exists, xerr := s.st.FrameExists(ctx, dev.VenueID, meta.CaptureID); xerr == nil && exists {
					s.m.add("duplicate")
					respond(proto.StatusDuplicate, "", 0)
					return
				}
				if _, qerr := s.st.InsertFrame(ctx, store.Frame{
					VenueID: dev.VenueID, CaptureID: meta.CaptureID,
					ReceivedAt: s.now().UTC(), Bytes: len(frameRaw),
					Status: "quarantined", Reason: reason,
				}); qerr != nil && !errors.Is(qerr, store.ErrDuplicate) {
					s.internal(w, "quarantine row", qerr)
					return
				}
			}
			s.m.add("quarantined")
			respond(proto.StatusRejected, reason, 0)
			return
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) || errors.Is(err, errTooLarge) {
			s.m.add("too_large")
			s.logOutcome(proto.StatusTooLarge, dev, meta, r.TLS, 0, "", 0)
			s.writeErr(w, proto.StatusTooLarge, "too_large")
			return
		}
		// The body broke off mid-upload: the device's link dropped or it gave
		// up on a stalled write. That is a transport event, not a malformed
		// request — no quarantine record (there is no capture to key it),
		// and the device retries on its own. Logged so a flaky link shows up
		// as what it is (it used to read as 422 bad_meta with empty fields).
		if aborted(err) {
			s.m.add("aborted")
			s.log.Warn("ingest aborted mid-upload", "serial", dev.Serial, "venue", dev.VenueID,
				"err", err.Error())
			s.writeErr(w, http.StatusRequestTimeout, "aborted")
			return
		}
		s.m.add("bad_meta")
		s.quarantine(ctx, dev, meta.CaptureID, proto.ReasonBadMeta, len(frameRaw), "", meta)
		respond(proto.StatusRejected, proto.ReasonBadMeta, 0)
		return
	}

	now := s.now().UTC()
	venue, err := s.st.VenueByID(ctx, dev.VenueID)
	if err != nil {
		s.internal(w, "load venue", err)
		return
	}

	beat := beatOf(meta, now)

	// Idempotency (§1): a retry answers 409 with the same body as a 201.
	exists, err := s.st.FrameExists(ctx, dev.VenueID, meta.CaptureID)
	if err != nil {
		s.internal(w, "idempotency check", err)
		return
	}
	if exists {
		if err := s.st.UpdateDeviceBeat(ctx, dev.ID, beat); err != nil {
			s.log.Error("beat update failed", "err", err.Error())
		}
		s.m.add("duplicate")
		respond(proto.StatusDuplicate, "", 0)
		return
	}

	// Health-only beat: the frame part is ABSENT (camera-degraded mode,
	// §4). A PRESENT-but-empty part is different: the device claims a
	// capture yet sent zero bytes — that falls through to render and
	// rejects as bad_magic, quarantined like any other corrupt frame.
	// (readMultipart leaves frameRaw nil when the part is absent;
	// io.ReadAll returns non-nil for an empty part.)
	if frameRaw == nil {
		if err := s.st.UpdateDeviceBeat(ctx, dev.ID, beat); err != nil {
			s.internal(w, "beat update", err)
			return
		}
		s.m.add("health")
		respond(proto.StatusStored, "", 0)
		return
	}

	rawSHA := sha256hex(frameRaw)

	// Render (§6.1 step 5): bounded by the engine's global semaphore.
	pctx, cancel := context.WithTimeout(ctx, RenderTimeout)
	start := s.now()
	out, perr := s.eng.Process(pctx, render.FrameIn{
		VenueID:   dev.VenueID,
		CaptureID: meta.CaptureID,
		Preset:    venue.Style,
		Raw:       frameRaw,
		Masks:     venue.Masks,
	})
	cancel()
	renderMS = float64(s.now().Sub(start).Microseconds()) / 1000 // §11: gate-visible
	// Guard against a zero reading from a sub-millisecond render or a pinned
	// test clock: gate-visible needs a positive number, and a fast render is
	// a real measurement, not "absent".
	if renderMS <= 0 {
		renderMS = 0.1
	}

	if perr != nil {
		if reason, ok := render.IsReject(perr); ok {
			// Quarantined: headers-only record, nothing stored (§7).
			s.quarantine(ctx, dev, meta.CaptureID, reason, len(frameRaw), rawSHA, meta)
			if _, err := s.st.InsertFrame(ctx, store.Frame{
				VenueID: dev.VenueID, CaptureID: meta.CaptureID, SHA256: rawSHA,
				ReceivedAt: now, Bytes: len(frameRaw), Status: "quarantined", Reason: reason,
			}); err != nil {
				if !errors.Is(err, store.ErrDuplicate) {
					s.internal(w, "quarantine row", err)
					return
				}
				// Already seen: idempotent 409.
				s.m.add("duplicate")
				respond(proto.StatusDuplicate, "", 0)
				return
			}
			if err := s.st.UpdateDeviceBeat(ctx, dev.ID, beat); err != nil {
				s.log.Error("beat update failed", "err", err.Error())
			}
			s.m.add("quarantined")
			s.log.Warn("frame quarantined",
				"venue", dev.VenueID, "capture_id", meta.CaptureID, "reason", reason)
			respond(proto.StatusRejected, reason, render.RejectMean(perr))
			return
		}
		s.internal(w, "render", perr)
		return
	}

	// Store: row + freshness in one logical step first (§6.1), then move
	// the render into place. Ordering matters under (venue_id, capture_id)
	// races: the loser removes only its own temp file, never the winner's
	// published render.
	rel := filepath.Join("venues", fmt.Sprint(dev.VenueID), "stills", meta.CaptureID+".png")
	abs := filepath.Join(s.dataDir, rel)
	tmp, err := writeTemp(abs, out.PNG, 0o644, 0o750)
	if err != nil {
		s.internal(w, "write render", err)
		return
	}
	frameID, ferr := s.st.InsertFrame(ctx, store.Frame{
		VenueID: dev.VenueID, CaptureID: meta.CaptureID, SHA256: out.SHA256,
		ReceivedAt: out.ReceivedAt, Bytes: out.RawBytes,
		Width: out.Width, Height: out.Height,
		Preset: out.Preset, PresetVer: out.PresetVer,
		Path: rel, Status: "ok",
	})
	if ferr != nil {
		os.Remove(tmp)
		if errors.Is(ferr, store.ErrDuplicate) {
			s.m.add("duplicate")
			respond(proto.StatusDuplicate, "", 0)
			return
		}
		s.internal(w, "insert frame", ferr)
		return
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		// Compensate: no row without its file (§1 — garbage never ships).
		if derr := s.st.DeleteFrames(ctx, []int64{frameID}); derr != nil {
			s.log.Error("compensate missing render failed", "err", derr.Error())
		}
		s.internal(w, "publish render", err)
		return
	}
	if err := s.st.UpdateDeviceBeat(ctx, dev.ID, beat); err != nil {
		s.log.Error("beat update failed", "err", err.Error())
	}
	if s.OnStored != nil {
		s.OnStored(ctx, dev.VenueID)
	}
	s.m.add("stored")
	respond(proto.StatusStored, "", 0)
}

// logOutcome is the per-ingest journal line: one line for every response
// the device can see, carrying what the 48 h soak measures (status, rssi,
// free heap, capture counters) plus the negotiated TLS suite — the ESP32's
// mbedtls is picky and the cipher tells us which side of the wire to look
// at when a response goes missing. Info for accepted outcomes (201/409),
// Warn for rejects (422/429/507). Bodies and tokens are never logged (§1).
func (s *Server) logOutcome(status int, dev *store.Device, meta proto.Meta, tlsState *tls.ConnectionState, renderMS float64, rejectReason string, meanLuma float64) {
	serial, venue := "?", int64(0)
	if dev != nil {
		serial, venue = dev.Serial, dev.VenueID
	}
	attrs := []any{
		"status", status,
		"serial", serial,
		"venue", venue,
		"capture_id", meta.CaptureID,
		"rssi", meta.RSSI,
		"free_heap", meta.FreeHeap,
		"boot", meta.Counters.Boot,
		"brownout", meta.Counters.Brownout,
		"post_fail", meta.Counters.PostFail,
		"tls_fail", meta.Counters.TLSFail,
	}
	if tlsState != nil {
		attrs = append(attrs, "tls_ver", fmt.Sprintf("0x%04x", tlsState.Version),
			"tls_suite", tlsCipherName(tlsState.CipherSuite))
	}
	// render_ms: only meaningful when a frame was processed; emitted on the
	// stored/quarantined responses so the Phase 1 exit gate can read render
	// latency without resurrecting /metrics (§11).
	if renderMS > 0 {
		attrs = append(attrs, "render_ms", math.Round(renderMS*10)/10)
	}
	// The server's own reject reason (bad_meta, dark, …) — meta.Reason is the
	// device's provision-diag field, which is something else entirely.
	if rejectReason != "" {
		attrs = append(attrs, "reject", rejectReason)
		// Measured mean luma, dark/dead only: "covered lens" (mean ≈ 0)
		// vs "gain clamp" (mean low but nonzero) must be distinguishable
		// from the journal alone, without raw-image access (§1). The key
		// is spelled mean_luma — the one place it ever appears. Only
		// dark/dead carry a measurement (RejectWithMean), so the key's
		// presence IS the proof a frame was measured; every other caller
		// passes 0 and never logs the key, where 0 means "unmeasured".
		if rejectReason == proto.ReasonDark || rejectReason == proto.ReasonDead {
			attrs = append(attrs, "mean_luma", math.Round(meanLuma*10)/10)
		}
	}
	if meta.Reason != "" {
		attrs = append(attrs, "reason", meta.Reason)
	}
	switch {
	case status >= 400:
		s.log.Warn("ingest", attrs...)
	default:
		s.log.Info("ingest", attrs...)
	}
}

// tlsCipherName names the suites an ESP32 can actually negotiate; anything
// else is logged as its hex id.
func tlsCipherName(id uint16) string {
	switch id {
	case tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256:
		return "chacha20-poly1305"
	case tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256:
		return "aes128-gcm"
	case tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384:
		return "aes256-gcm"
	case tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA:
		return "aes128-cbc-sha"
	case tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA:
		return "aes256-cbc-sha"
	default:
		return fmt.Sprintf("0x%04x", id)
	}
}

// readMultipart streams meta (≤16 KiB) and the optional frame (≤4 MiB);
// unknown parts are ignored for additive compatibility (§4).
func (s *Server) readMultipart(r *http.Request) (proto.Meta, []byte, error) {
	var meta proto.Meta
	mr, err := r.MultipartReader()
	if err != nil {
		return meta, nil, reject(proto.StatusRejected, proto.ReasonBadMeta)
	}
	var metaRaw, frameRaw []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return meta, frameRaw, err // classified by the caller
		}
		name := part.FormName()
		switch name {
		case proto.PartMeta:
			if metaRaw != nil {
				part.Close()
				return meta, frameRaw, reject(proto.StatusRejected, proto.ReasonBadMeta)
			}
			metaRaw, err = readCap(part, proto.MaxMetaBytes)
		case proto.PartFrame:
			if frameRaw != nil {
				part.Close()
				return meta, frameRaw, reject(proto.StatusRejected, proto.ReasonBadMeta)
			}
			frameRaw, err = readCap(part, proto.MaxFrameBytes)
		default:
			err = nil // ignore unknown parts
		}
		part.Close()
		if err != nil {
			return meta, frameRaw, err
		}
	}
	if metaRaw == nil {
		return meta, nil, reject(proto.StatusRejected, proto.ReasonBadMeta)
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return meta, frameRaw, reject(proto.StatusRejected, proto.ReasonBadMeta)
	}
	if !proto.ValidCaptureID(meta.CaptureID) {
		return meta, frameRaw, reject(proto.StatusRejected, proto.ReasonBadCaptureID)
	}
	return meta, frameRaw, nil
}

func readCap(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// beatOf is the health portion of one parsed meta.
func beatOf(meta proto.Meta, at time.Time) store.Beat {
	return store.Beat{
		FW: meta.FW, RSSI: meta.RSSI, FreeHeap: int(meta.FreeHeap),
		Counters: meta.Counters, Profile: meta.Profile, Reason: meta.Reason, At: at,
	}
}

// response builds the canonical ingest body (§4). 201 and 409 share this
// construction, so a retry sees the same payload shape as a store.
func (s *Server) response(reason string) proto.Response {
	return proto.Response{NextS: proto.IntervalS, ServerTime: s.now().UTC(), Reason: reason}
}

// quarantine writes the two headers-only records of §7: a frames row with
// status='quarantined' (handled by the caller) and the tailable JSONL file.
// Frame bytes never appear in either.
func (s *Server) quarantine(ctx context.Context, dev *store.Device, captureID, reason string, rawBytes int, rawSHA string, meta proto.Meta) {
	rec := map[string]any{
		"at":         s.now().UTC().Format(time.RFC3339),
		"venue_id":   dev.VenueID,
		"device_id":  dev.ID,
		"serial":     dev.Serial,
		"capture_id": captureID,
		"reason":     reason,
		"raw_bytes":  rawBytes,
		"sha256":     rawSHA,
		"fw":         meta.FW,
		"rssi":       meta.RSSI,
	}
	b, _ := json.Marshal(rec)
	f, err := os.OpenFile(filepath.Join(s.dataDir, "quarantine.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		s.log.Error("quarantine log open failed", "err", err.Error())
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		s.log.Error("quarantine log write failed", "err", err.Error())
	}
	// The slog Warn above is the whole audit trail (§7) — v3.1 has no
	// health_events table (§6.2), and frame bytes never appear in it.
}

// writeTemp creates the target directory and writes data to a unique temp
// file next to abs (same filesystem), returning its path. The caller renames
// it into place after the DB row exists, or removes it.
func writeTemp(abs string, data []byte, fileMode, dirMode os.FileMode) (string, error) {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".ingest-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Chmod(tmp, fileMode); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// aborted reports a body read that ended because the connection did: a
// truncated multipart stream or a read deadline.
func aborted(err error) bool {
	var ne net.Error
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}

func reasonOf(err error) string {
	var re *reqError
	if errors.As(err, &re) {
		return re.reason
	}
	return proto.ReasonBadMeta
}

// internal answers an unexpected server error. Devices treat 5xx as
// "retry next tick" (§4 lists only expected codes; anything else is a bug).
func (s *Server) internal(w http.ResponseWriter, op string, err error) {
	s.m.add("internal")
	// Log the error, never the request body (§1).
	s.log.Error("ingest internal error", "op", op, "err", err.Error())
	s.writeErr(w, http.StatusInternalServerError, "internal")
}

func (s *Server) writeResp(w http.ResponseWriter, status int, resp proto.Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(proto.HeaderProto, proto.Version)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeErr(w http.ResponseWriter, status int, kind string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set(proto.HeaderProto, proto.Version)
	if status == proto.StatusRateLimit {
		w.Header().Set("Retry-After", fmt.Sprint(proto.RetryAfterS))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.ErrorBody{Error: kind, RetryAfterS: proto.RetryAfterS})
}

func (s *Server) write429(w http.ResponseWriter) {
	s.writeErr(w, proto.StatusRateLimit, "rate_limited")
}
