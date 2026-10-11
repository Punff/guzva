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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"guzva/internal/proto"
	"guzva/internal/render"
	"guzva/internal/venue"

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
  tz            TEXT NOT NULL DEFAULT 'Europe/Sarajevo',
  style         TEXT NOT NULL DEFAULT 'poster',
  hours_json    TEXT NOT NULL DEFAULT '',
  ref_hash      TEXT NOT NULL DEFAULT '',
  last_frame_at INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);

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

`

// schemaV2 adds the operator-set sensor keys as one JSON object per device
// (the cadence trio stays in typed columns — they drive next_s and the
// admin board). Empty string = no extras set.
const schemaV2 = `
ALTER TABLE devices ADD COLUMN cfg_json TEXT NOT NULL DEFAULT '';
`

// schemaV3 adds the owner-panel credential: one bcrypt hash per venue,
// empty = no owner login yet (super-admin sets it). The password itself is
// never stored — verification is bcrypt against this hash.
const schemaV3 = `
ALTER TABLE venues ADD COLUMN owner_hash TEXT NOT NULL DEFAULT '';
`

// schemaV4 adds the per-venue page language (DESIGN.md ⚑9). Croatian is the
// product default; English is for tourist venues, chosen per venue rather than
// by a toggle, so no second file and no D5 change.
const schemaV4 = `
ALTER TABLE venues ADD COLUMN lang TEXT NOT NULL DEFAULT 'hr';
`

// schemaV5 adds the render calibration (DESIGN.md ⚑5): thresholds, bias,
// invert, masks and preset as one JSON document. Nullable in spirit —
// DEFAULT ” means "defaults, no masks" — so old venues render exactly as
// before without any backfill.
const schemaV5 = `
ALTER TABLE venues ADD COLUMN calib_json TEXT NOT NULL DEFAULT '';
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
		ver = 1 // fresh database also needs v2
		fallthrough
	case 1:
		if _, err := s.db.ExecContext(ctx, schemaV2); err != nil {
			return fmt.Errorf("store: migration v2: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
			return fmt.Errorf("store: set user_version: %w", err)
		}
		ver = 2 // ... and v3 as well
		fallthrough
	case 2:
		if _, err := s.db.ExecContext(ctx, schemaV3); err != nil {
			return fmt.Errorf("store: migration v3: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 3"); err != nil {
			return fmt.Errorf("store: set user_version: %w", err)
		}
		ver = 3 // ... and v4 as well; every step must reach the terminal
		fallthrough
	case 3:
		if _, err := s.db.ExecContext(ctx, schemaV4); err != nil {
			return fmt.Errorf("store: migration v4: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 4"); err != nil {
			return fmt.Errorf("store: set user_version: %w", err)
		}
		ver = 4 // ... and v5 as well; every step must reach the terminal
		fallthrough
	case 4:
		if _, err := s.db.ExecContext(ctx, schemaV5); err != nil {
			return fmt.Errorf("store: migration v5: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
			return fmt.Errorf("store: set user_version: %w", err)
		}
		ver = 5
	case 5:
		// current — nothing to do.
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
// DefaultTZ is used when a venue is created without one.
const DefaultTZ = "Europe/Sarajevo"

// slugRe is the public-slug rule, one source for every writer: the admin
// console flashes it, the CLI prints it, and CreateVenue enforces it — an
// operator typo must never become a path outside the web root (maintenance
// W1/B5: pages publish under data/public/<slug>).
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ErrBadSlug is returned by CreateVenue for a slug the rule above rejects.
var ErrBadSlug = errors.New("store: bad slug")

// ValidSlug reports whether s is a legal public slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// CreateVenue inserts a venue and returns its id. Hours are set separately
// via SetHours (hours live as JSON on the venue row, §6.2).
func (s *Store) CreateVenue(ctx context.Context, v Venue) (int64, error) {
	if !ValidSlug(v.Slug) {
		return 0, ErrBadSlug
	}
	tz := v.TZ
	if tz == "" {
		tz = DefaultTZ
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO venues (slug, name, tz, style, last_frame_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.Slug, v.Name, tz, v.Style, ts(v.LastFrameAt), ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrSlugTaken
		}
		return 0, fmt.Errorf("store: create venue: %w", err)
	}
	return res.LastInsertId()
}

const venueCols = `id, slug, name, tz, style, owner_hash, lang, calib_json, last_frame_at, created_at`

func scanVenue(row interface{ Scan(...any) error }) (*Venue, error) {
	var v Venue
	var last, created int64
	err := row.Scan(&v.ID, &v.Slug, &v.Name, &v.TZ, &v.Style, &v.OwnerHash, &v.Lang, &v.CalibJSON, &last, &created)
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

// ListVenues returns every venue ordered by slug — the admin fleet board
// and the scheduler's page refresh both walk this list.
func (s *Store) ListVenues(ctx context.Context) ([]Venue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+venueCols+` FROM venues ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Venue
	for rows.Next() {
		v, err := scanVenue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// SetLastFrameAt advances canonical freshness for the venue. Only called for
// stored (ok) frames: quarantined frames and health beats never make a venue
// look fresh (§1 never publish a failure).
func (s *Store) SetLastFrameAt(ctx context.Context, venueID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET last_frame_at = ? WHERE id = ?`, ts(at), venueID)
	return err
}

// --- hours (JSON on the venue row, §6.2) ----------------------------------

// SetHours replaces the venue's weekly schedule as one JSON document on the
// venue row — v3.1 dropped the hours table (three tables total).
func (s *Store) SetHours(ctx context.Context, venueID int64, h venue.Hours) error {
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("store: marshal hours: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE venues SET hours_json = ? WHERE id = ?`, string(b), venueID)
	return err
}

// Hours loads the venue's schedule (pure value; classification happens in
// the venue package). Unconfigured hours parse to the zero value.
func (s *Store) Hours(ctx context.Context, venueID int64) (venue.Hours, error) {
	var h venue.Hours
	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT hours_json FROM venues WHERE id = ?`, venueID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, fmt.Errorf("store: load hours: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return h, nil
	}
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return venue.Hours{}, fmt.Errorf("store: parse hours_json: %w", err)
	}
	return h, nil
}

// --- devices --------------------------------------------------------------

// Venue is the site-plane entity. OwnerHash is the bcrypt hash of the
// venue owner's panel password ("" = no owner login; super-admin sets it).
// It is selected so the owner login can verify — it is never rendered.
type Venue struct {
	ID          int64
	Slug        string
	Name        string
	TZ          string // IANA name; public pages render venue-local time (§6.4)
	Style       string // preset name applied to new frames (site-plane)
	OwnerHash   string
	Lang        string // page language, "hr" (default) or "en" (⚑9)
	CalibJSON   string // render calib document, "" = defaults (⚑5)
	LastFrameAt time.Time
	CreatedAt   time.Time
}

// UpdateVenue replaces the editable site-plane details (owner panel +
// super-admin). Slug is identity and never changes; hours go through
// SetHours so the two writers cannot disagree on the format.
func (s *Store) UpdateVenue(ctx context.Context, id int64, name, style, tz string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET name = ?, style = ?, tz = ? WHERE id = ?`,
		name, style, tz, id)
	return err
}

// SetOwnerHash stores the bcrypt hash of the owner's panel password
// (super-admin only; the password itself never reaches the store).
// SetCalib stores the venue's render calibration document (DESIGN.md ⚑5).
// The document must already be validated — SetCalib stores exactly what it is
// given, so callers validate first (the admin form builds it from fields;
// ParseCalib remains the read-path backstop for hand-edited rows).
func (s *Store) SetCalib(ctx context.Context, id int64, calibJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET calib_json = ? WHERE id = ?`, calibJSON, id)
	return err
}

func (s *Store) SetOwnerHash(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET owner_hash = ? WHERE id = ?`, hash, id)
	return err
}

// DevicesByVenue lists one venue's devices for the owner health view (and
// the super-admin device page) — same row scan as ListDevices, scoped.
func (s *Store) DevicesByVenue(ctx context.Context, venueID int64) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE venue_id = ? ORDER BY serial`, venueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

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
	CfgExtra      map[string]int // operator-set sensor keys (cfg_json), may be nil
	CfgAppliedVer int
	LastSeenAt    time.Time
	LastReason    string
	CreatedAt     time.Time
}

// DesiredCfg is what the server tells the device to run (piggybacked on
// every ingest response): cadence trio from the columns, sensor keys from
// cfg_json. Extra is shared with the struct — callers must not mutate it.
func (d *Device) DesiredCfg() proto.DesiredCfg {
	return proto.DesiredCfg{
		Ver: d.CfgVer, IntervalS: d.CfgIntervalS, Quality: d.CfgQuality,
		Extra: d.CfgExtra,
	}
}

const deviceCols = `id, venue_id, serial, status, fw, rssi, free_heap,
	boot, brownout, join_fail, post_fail, tls_fail, capture_fail,
	cfg_ver, cfg_interval_s, cfg_quality, cfg_json, cfg_applied_ver, last_seen_at, last_reason, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var seen, created int64
	var cfgJSON string
	err := row.Scan(&d.ID, &d.VenueID, &d.Serial, &d.Status, &d.FW, &d.RSSI, &d.FreeHeap,
		&d.Counters.Boot, &d.Counters.Brownout, &d.Counters.JoinFail, &d.Counters.PostFail,
		&d.Counters.TLSFail, &d.Counters.CaptureFail,
		&d.CfgVer, &d.CfgIntervalS, &d.CfgQuality, &cfgJSON, &d.CfgAppliedVer,
		&seen, &d.LastReason, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan device: %w", err)
	}
	if cfgJSON != "" {
		if err := json.Unmarshal([]byte(cfgJSON), &d.CfgExtra); err != nil {
			return nil, fmt.Errorf("store: parse cfg_json: %w", err)
		}
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
	cfgJSON, err := marshalCfgExtra(d.CfgExtra)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO devices (venue_id, serial, token_hash, status, cfg_ver, cfg_interval_s, cfg_quality, cfg_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.VenueID, d.Serial, tokenHash, status, d.CfgVer, d.CfgIntervalS, d.CfgQuality, cfgJSON, ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrSerialTaken
		}
		return 0, fmt.Errorf("store: create device: %w", err)
	}
	return res.LastInsertId()
}

// DeviceBySerial resolves a device by its serial (admin label, §4). Used by
// the offline `guzva cfg` tooling; ingest never sees a serial.
func (s *Store) DeviceBySerial(ctx context.Context, serial string) (*Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE serial = ?`, serial))
}

// DeviceByID resolves a device by row id (admin console URLs; the id is
// meaningless off-box so it carries no authority — every admin route
// re-checks the session).
func (s *Store) DeviceByID(ctx context.Context, id int64) (*Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
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

// SetDeviceCfg applies a full desired-config change (device plane, §1): the
// cadence trio updates its columns and every other validated key lands in
// cfg_json. cfg.Ver must already be the NEW version (the caller bumps it) —
// ver is the read-back contract with the device. The audit trail is a slog
// line at the caller — v3.1 dropped settings_log (§6.2 "changes are slog
// audit lines").
func (s *Store) SetDeviceCfg(ctx context.Context, deviceID int64, cfg proto.DesiredCfg) error {
	cfgJSON, err := marshalCfgExtra(cfg.Extra)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE devices SET cfg_ver = ?, cfg_interval_s = ?, cfg_quality = ?, cfg_json = ? WHERE id = ?`,
		cfg.Ver, cfg.IntervalS, cfg.Quality, cfgJSON, deviceID)
	return err
}

// marshalCfgExtra renders the sensor-key map for cfg_json. The cadence trio
// lives in typed columns; if a caller puts one in Extra anyway it is dropped
// here so the column cannot drift from the columns.
func marshalCfgExtra(extra map[string]int) (string, error) {
	if len(extra) == 0 {
		return "", nil
	}
	m := make(map[string]int, len(extra))
	for k, v := range extra {
		if k == "ver" || k == "interval_s" || k == "quality" {
			continue
		}
		m[k] = v
	}
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("store: marshal cfg_json: %w", err)
	}
	return string(b), nil
}

// SetDeviceStatus flips a device between active and revoked (admin revoke).
// Revocation never deletes the device: identity survives auth failures (§1).
func (s *Store) SetDeviceStatus(ctx context.Context, deviceID int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET status = ? WHERE id = ?`, status, deviceID)
	return err
}

// ListDevices returns every device for the admin fleet board (§6.4),
// ordered by venue then serial so the board groups itself.
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+deviceCols+` FROM devices ORDER BY venue_id, serial`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
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
// records. Raw is never written, so there is nothing else to retain (v3.1:
// no health_events, no sessions).
func (s *Store) PruneMeta(ctx context.Context, metaCutoff time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM frames WHERE status = 'quarantined' AND received_at < ?`, ts(metaCutoff))
	return err
}

// ParseCalib decodes a venue's calib_json into a render Calib. The parsing
// rules (empty/malformed → defaults) live in render so the pipeline and the
// store cannot disagree about what a document means.
func ParseCalib(raw string) render.Calib {
	return render.ParseCalib(raw)
}
