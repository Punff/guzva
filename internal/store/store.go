// Package store is the only package in the system that speaks SQL (PLAN §3).
// SQLite in WAL mode behind one serialized connection (single writer by
// construction — no SQLITE_BUSY, no connection pool surprises on a small
// VPS). Migrations are embedded and tracked with PRAGMA user_version.
//
// Interface rule from the plan: no Repository abstraction until Postgres is
// actually needed — this is a concrete type on purpose.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imaliguzve/internal/proto"
	"imaliguzve/internal/venue"

	_ "modernc.org/sqlite" // CGO-free SQLite driver → one static binary
)

// Sentinel errors mapped from constraints; handlers translate them to
// protocol status codes without ever looking at SQL strings.
var (
	ErrNotFound    = errors.New("store: not found")
	ErrDuplicate   = errors.New("store: duplicate capture") // (venue_id, capture_id)
	ErrSlugTaken   = errors.New("store: slug already exists")
	ErrSerialTaken = errors.New("store: serial already exists")
)

// Store wraps the SQLite handle.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("store: create db dir: %w", err)
		}
	}
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// Single connection: every statement is fast and indexed; serializing
	// them removes locking entirely (§6.2 single writer).
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db, path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for tests only (same package).
func (s *Store) dbh() *sql.DB { return s.db }

// --- migrations -----------------------------------------------------------

// schema v1: the full v3 table set from PLAN §6.2. Tables whose features
// arrive in later phases (viewer_hits, intents, OTA state, claim codes) are
// deliberately absent until then (§9).
const schemaV1 = `
CREATE TABLE venues (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  slug          TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  style         TEXT NOT NULL DEFAULT 'poster',
  frame_key     TEXT NOT NULL,
  last_frame_at INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

CREATE TABLE hours (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  venue_id  INTEGER NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
  dow       INTEGER NOT NULL,
  open_min  INTEGER NOT NULL,
  close_min INTEGER NOT NULL,
  UNIQUE (venue_id, dow, open_min, close_min)
);
CREATE INDEX hours_venue ON hours(venue_id);

CREATE TABLE devices (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  venue_id         INTEGER NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
  serial           TEXT NOT NULL UNIQUE,
  token_hash       TEXT NOT NULL UNIQUE,
  status           TEXT NOT NULL DEFAULT 'active',
  fw               TEXT NOT NULL DEFAULT '',
  rssi             INTEGER NOT NULL DEFAULT 0,
  free_heap        INTEGER NOT NULL DEFAULT 0,
  boot             INTEGER NOT NULL DEFAULT 0,
  brownout         INTEGER NOT NULL DEFAULT 0,
  join_fail        INTEGER NOT NULL DEFAULT 0,
  post_fail        INTEGER NOT NULL DEFAULT 0,
  tls_fail         INTEGER NOT NULL DEFAULT 0,
  capture_fail     INTEGER NOT NULL DEFAULT 0,
  cfg_ver          INTEGER NOT NULL DEFAULT 1,
  cfg_interval_s   INTEGER NOT NULL DEFAULT 60,
  cfg_quality      INTEGER NOT NULL DEFAULT 18,
  cfg_applied_ver  INTEGER NOT NULL DEFAULT 0,
  last_seen_at     INTEGER NOT NULL DEFAULT 0,
  last_reason      TEXT NOT NULL DEFAULT '',
  created_at       INTEGER NOT NULL
);
CREATE INDEX devices_venue ON devices(venue_id);

CREATE TABLE frames (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  venue_id     INTEGER NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
  capture_id   TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  received_at  INTEGER NOT NULL,
  bytes        INTEGER NOT NULL DEFAULT 0,
  width        INTEGER NOT NULL DEFAULT 0,
  height       INTEGER NOT NULL DEFAULT 0,
  preset       TEXT NOT NULL DEFAULT '',
  preset_ver   INTEGER NOT NULL DEFAULT 0,
  path         TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'ok',
  reason       TEXT NOT NULL DEFAULT '',
  UNIQUE (venue_id, capture_id)
);
CREATE INDEX frames_received ON frames(received_at);
CREATE INDEX frames_venue_received ON frames(venue_id, received_at DESC);

CREATE TABLE settings_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id   INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  ver         INTEGER NOT NULL,
  interval_s  INTEGER NOT NULL,
  quality     INTEGER NOT NULL,
  actor       TEXT NOT NULL DEFAULT 'system',
  at          INTEGER NOT NULL
);
CREATE INDEX settings_log_device ON settings_log(device_id, at DESC);

CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  venue_id   INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE health_events (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  at        INTEGER NOT NULL,
  kind      TEXT NOT NULL,
  venue_id  INTEGER NOT NULL DEFAULT 0,
  device_id INTEGER NOT NULL DEFAULT 0,
  detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX health_events_at ON health_events(at);
`

func (s *Store) migrate(ctx context.Context) error {
	var ver int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	switch ver {
	case 0:
		if _, err := s.db.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("store: migration v1: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return fmt.Errorf("store: set user_version: %w", err)
		}
	case 1:
		// current
	default:
		return fmt.Errorf("store: unknown schema version %d (binary older than db?)", ver)
	}
	return nil
}

// --- time helpers ---------------------------------------------------------
// Timestamps are integer unix seconds: small, sortable, canonical server
// time (§1: device clock is advisory, so we never store device time).

func ts(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromTS(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// --- venues ---------------------------------------------------------------

// Venue is the site-plane entity (§1: settings planes).
type Venue struct {
	ID          int64
	Slug        string
	Name        string
	Style       string // preset name applied to new frames (site-plane)
	FrameKey    string // unguessable key guarding the public frame URL (§6.4)
	LastFrameAt time.Time
	CreatedAt   time.Time
}

// CreateVenue inserts a venue and returns its id.
func (s *Store) CreateVenue(ctx context.Context, v Venue) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO venues (slug, name, style, frame_key, last_frame_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.Slug, v.Name, v.Style, v.FrameKey, ts(v.LastFrameAt), ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrSlugTaken
		}
		return 0, fmt.Errorf("store: create venue: %w", err)
	}
	return res.LastInsertId()
}

const venueCols = `id, slug, name, style, frame_key, last_frame_at, created_at`

func scanVenue(row interface{ Scan(...any) error }) (*Venue, error) {
	var v Venue
	var last, created int64
	err := row.Scan(&v.ID, &v.Slug, &v.Name, &v.Style, &v.FrameKey, &last, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan venue: %w", err)
	}
	v.LastFrameAt = fromTS(last)
	v.CreatedAt = fromTS(created)
	return &v, nil
}

// VenueBySlug loads a venue by its public slug.
func (s *Store) VenueBySlug(ctx context.Context, slug string) (*Venue, error) {
	return scanVenue(s.db.QueryRowContext(ctx,
		`SELECT `+venueCols+` FROM venues WHERE slug = ?`, slug))
}

// VenueByID loads a venue by id. Every store method that reads venue data
// takes a venue_id — handlers get it from the session, never the URL (§3,
// risk 7).
func (s *Store) VenueByID(ctx context.Context, id int64) (*Venue, error) {
	return scanVenue(s.db.QueryRowContext(ctx,
		`SELECT `+venueCols+` FROM venues WHERE id = ?`, id))
}

// SetLastFrameAt advances canonical freshness for the venue. Only called for
// stored (ok) frames: quarantined frames and health beats never make a venue
// look fresh (§1 never publish a failure).
func (s *Store) SetLastFrameAt(ctx context.Context, venueID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET last_frame_at = ? WHERE id = ?`, ts(at), venueID)
	return err
}

// --- hours ----------------------------------------------------------------

// SetHours replaces the venue's weekly schedule in one transaction.
func (s *Store) SetHours(ctx context.Context, venueID int64, h venue.Hours) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM hours WHERE venue_id = ?`, venueID); err != nil {
		return err
	}
	for dow, ranges := range h {
		for _, r := range ranges {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO hours (venue_id, dow, open_min, close_min)
				VALUES (?, ?, ?, ?)`, venueID, dow, r.OpenMin, r.CloseMin); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Hours loads the venue's schedule (pure value; classification happens in
// the venue package).
func (s *Store) Hours(ctx context.Context, venueID int64) (venue.Hours, error) {
	var h venue.Hours
	rows, err := s.db.QueryContext(ctx,
		`SELECT dow, open_min, close_min FROM hours WHERE venue_id = ? ORDER BY dow, open_min`, venueID)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var dow, openMin, closeMin int
		if err := rows.Scan(&dow, &openMin, &closeMin); err != nil {
			return h, err
		}
		if dow >= 0 && dow <= 6 {
			h[dow] = append(h[dow], venue.Range{OpenMin: openMin, CloseMin: closeMin})
		}
	}
	return h, rows.Err()
}

// --- devices --------------------------------------------------------------

// Device is the device-plane entity.
type Device struct {
	ID            int64
	VenueID       int64
	Serial        string
	Status        string // active | revoked
	FW            string
	RSSI          int
	FreeHeap      int
	Counters      proto.Counters
	CfgVer        int
	CfgIntervalS  int
	CfgQuality    int
	CfgAppliedVer int
	LastSeenAt    time.Time
	LastReason    string
	CreatedAt     time.Time
}

// DesiredCfg is what the server tells the device to run (piggybacked on
// every ingest response).
func (d *Device) DesiredCfg() proto.DesiredCfg {
	return proto.DesiredCfg{Ver: d.CfgVer, IntervalS: d.CfgIntervalS, Quality: d.CfgQuality}
}

const deviceCols = `id, venue_id, serial, status, fw, rssi, free_heap,
	boot, brownout, join_fail, post_fail, tls_fail, capture_fail,
	cfg_ver, cfg_interval_s, cfg_quality, cfg_applied_ver, last_seen_at, last_reason, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var seen, created int64
	err := row.Scan(&d.ID, &d.VenueID, &d.Serial, &d.Status, &d.FW, &d.RSSI, &d.FreeHeap,
		&d.Counters.Boot, &d.Counters.Brownout, &d.Counters.JoinFail, &d.Counters.PostFail,
		&d.Counters.TLSFail, &d.Counters.CaptureFail,
		&d.CfgVer, &d.CfgIntervalS, &d.CfgQuality, &d.CfgAppliedVer,
		&seen, &d.LastReason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan device: %w", err)
	}
	d.LastSeenAt = fromTS(seen)
	d.CreatedAt = fromTS(created)
	return &d, nil
}

// CreateDevice inserts a device bound to a venue; tokenHash is the sha256 of
// the bearer token — the raw token is never stored (§4).
func (s *Store) CreateDevice(ctx context.Context, d Device, tokenHash string) (int64, error) {
	status := d.Status
	if status == "" {
		status = "active"
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO devices (venue_id, serial, token_hash, status, cfg_ver, cfg_interval_s, cfg_quality, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.VenueID, d.Serial, tokenHash, status, d.CfgVer, d.CfgIntervalS, d.CfgQuality, ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrSerialTaken
		}
		return 0, fmt.Errorf("store: create device: %w", err)
	}
	return res.LastInsertId()
}

// DeviceByTokenHash resolves a bearer token by its sha256. Returns
// ErrNotFound for unknown tokens; callers must check Status == "revoked"
// and answer 401 in both cases (§4).
func (s *Store) DeviceByTokenHash(ctx context.Context, tokenHash string) (*Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE token_hash = ?`, tokenHash))
}

// Beat is the health portion of one ingest (always applied, even for
// health-only beats and quarantined frames — liveness is not freshness).
type Beat struct {
	FW            string
	RSSI          int
	FreeHeap      int
	Counters      proto.Counters
	CfgAppliedVer int
	Reason        string
	At            time.Time
}

// UpdateDeviceBeat records the health headers of one ingest.
func (s *Store) UpdateDeviceBeat(ctx context.Context, deviceID int64, b Beat) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE devices SET fw = ?, rssi = ?, free_heap = ?,
			boot = ?, brownout = ?, join_fail = ?, post_fail = ?, tls_fail = ?, capture_fail = ?,
			cfg_applied_ver = ?, last_seen_at = ?, last_reason = ?
		WHERE id = ?`,
		b.FW, b.RSSI, b.FreeHeap,
		b.Counters.Boot, b.Counters.Brownout, b.Counters.JoinFail,
		b.Counters.PostFail, b.Counters.TLSFail, b.Counters.CaptureFail,
		b.CfgAppliedVer, ts(b.At), b.Reason, deviceID)
	return err
}

// SetDeviceCfg bumps the desired-config version (device plane, §1) and logs
// the change to settings_log — the audit trail behind the amber
// read-back-mismatch state.
func (s *Store) SetDeviceCfg(ctx context.Context, deviceID int64, ver, intervalS, quality int, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE devices SET cfg_ver = ?, cfg_interval_s = ?, cfg_quality = ? WHERE id = ?`,
		ver, intervalS, quality, deviceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings_log (device_id, ver, interval_s, quality, actor, at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		deviceID, ver, intervalS, quality, actor, ts(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

// --- frames ---------------------------------------------------------------

// Frame is one ingest record. status "ok" rows point at a stylised render
// on disk; "quarantined" rows are headers-only (path empty, §7).
type Frame struct {
	ID         int64
	VenueID    int64
	CaptureID  string
	SHA256     string
	ReceivedAt time.Time
	Bytes      int
	Width      int
	Height     int
	Preset     string
	PresetVer  int
	Path       string // relative to the data dir
	Status     string // ok | quarantined
	Reason     string
}

// InsertFrame stores the frame row and advances venue freshness in one
// logical step (§6.1): either both happen or neither does. A repeated
// (venue_id, capture_id) returns ErrDuplicate so the handler can answer 409
// with the same body as a 201 (§1 idempotency). On success the new row's id
// is returned so the caller can finish moving its render into place.
func (s *Store) InsertFrame(ctx context.Context, f Frame) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM frames WHERE venue_id = ? AND capture_id = ?`,
		f.VenueID, f.CaptureID).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrDuplicate
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO frames (venue_id, capture_id, sha256, received_at, bytes, width, height,
			preset, preset_ver, path, status, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.VenueID, f.CaptureID, f.SHA256, ts(f.ReceivedAt), f.Bytes, f.Width, f.Height,
		f.Preset, f.PresetVer, f.Path, f.Status, f.Reason)
	if err != nil {
		return 0, err
	}
	if f.Status == "ok" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE venues SET last_frame_at = ? WHERE id = ?`, ts(f.ReceivedAt), f.VenueID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return id, err
}

// FrameExists reports whether (venue_id, capture_id) was already processed —
// the idempotency pre-check of §6.1, run before the expensive render. The
// UNIQUE constraint (plus InsertFrame's in-transaction check) remains the
// authority under races.
func (s *Store) FrameExists(ctx context.Context, venueID int64, captureID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM frames WHERE venue_id = ? AND capture_id = ?`,
		venueID, captureID).Scan(&n)
	return n > 0, err
}

// LatestFrame returns the newest good render for the venue, or ErrNotFound.
// This is last-good: what the public page serves even when fresh captures
// fail (§1).
func (s *Store) LatestFrame(ctx context.Context, venueID int64) (*Frame, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, venue_id, capture_id, sha256, received_at, bytes, width, height,
			preset, preset_ver, path, status, reason
		FROM frames WHERE venue_id = ? AND status = 'ok'
		ORDER BY received_at DESC, id DESC LIMIT 1`, venueID)
	return scanFrame(row)
}

func scanFrame(row interface{ Scan(...any) error }) (*Frame, error) {
	var f Frame
	var received int64
	err := row.Scan(&f.ID, &f.VenueID, &f.CaptureID, &f.SHA256, &received, &f.Bytes, &f.Width,
		&f.Height, &f.Preset, &f.PresetVer, &f.Path, &f.Status, &f.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan frame: %w", err)
	}
	f.ReceivedAt = fromTS(received)
	return &f, nil
}

// StaleFrame is a pruner candidate: id + file path (relative to data dir).
type StaleFrame struct {
	ID   int64
	Path string
}

// StaleFrames lists ok frames older than the configured retain window
// (§6.3: config retain_hours, default 48), never including each venue's
// newest good frame (last-good is kept forever). One SQL select; the caller
// does the file deletes.
func (s *Store) StaleFrames(ctx context.Context, now time.Time, retainHours int) ([]StaleFrame, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.path
		FROM frames f
		WHERE f.status = 'ok'
		  AND f.received_at < (? - ? * 3600)
		  AND f.id NOT IN (
			SELECT g.id FROM frames g
			WHERE g.venue_id = f.venue_id AND g.status = 'ok'
			ORDER BY g.received_at DESC, g.id DESC LIMIT 1
		  )`, ts(now), retainHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleFrame
	for rows.Next() {
		var sf StaleFrame
		if err := rows.Scan(&sf.ID, &sf.Path); err != nil {
			return nil, err
		}
		out = append(out, sf)
	}
	return out, rows.Err()
}

// DeleteFrames removes frame rows by id (files are deleted by the caller so
// a lost file never blocks metadata cleanup).
func (s *Store) DeleteFrames(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		end := min(start+chunk, len(ids))
		part := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM frames WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// PruneMeta applies the 90-day meta retention (§6.3): quarantined frame
// records, health events and expired sessions. Raw is never written, so
// there is nothing else to retain.
func (s *Store) PruneMeta(ctx context.Context, metaCutoff, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM frames WHERE status = 'quarantined' AND received_at < ?`, ts(metaCutoff)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM health_events WHERE at < ?`, ts(metaCutoff)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ts(now))
	return err
}

// --- health events --------------------------------------------------------

// LogHealth appends a headers-only health/quarantine event (§6.2). Never
// carries frame bytes (§7).
func (s *Store) LogHealth(ctx context.Context, kind string, venueID, deviceID int64, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO health_events (at, kind, venue_id, device_id, detail) VALUES (?, ?, ?, ?, ?)`,
		ts(time.Now()), kind, venueID, deviceID, detail)
	return err
}
