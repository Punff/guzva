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

// schemaV6 adds the site-plane venue info (design-pass2 §5): an optional
// public description (≤200 chars, plain text) and phone number for the
// QR-scanning guest. Empty defaults = nothing renders on the page, so no
// existing venue changes shape.
const schemaV6 = `
ALTER TABLE venues ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE venues ADD COLUMN phone TEXT NOT NULL DEFAULT '';
`

// schemaV7 adds the public location line (design-pass2 §5 contact block):
// where the venue physically is, for the guest walking up to it. One free-text
// line, deliberately NOT lat/lon: a coordinate pair on a public page invites
// map-embed third parties and turns a contact field into a precise location
// beacon, which §1's privacy discipline and the no-third-party rule argue
// against. Empty default = nothing renders, so no existing venue changes shape.
const schemaV7 = `
ALTER TABLE venues ADD COLUMN location TEXT NOT NULL DEFAULT '';
`

// schemaV8 replaces the render calibration with masks only (print v4:
// thresholds, bias, invert and preset are gone; the style column already
// holds the preset). Masks are in the 320×240 grid; calib_json is left in
// place, unused, so a restore of an older backup still reads.
const schemaV8 = `
ALTER TABLE venues ADD COLUMN masks_json TEXT NOT NULL DEFAULT '';
`

// schemaV9 retires the remote device config (print v4: the camera profile
// is frozen in firmware). The cfg_* columns stay, unused, so a restore of an
// older backup still opens; profile records what the device reports.
const schemaV9 = `
ALTER TABLE devices ADD COLUMN profile TEXT NOT NULL DEFAULT '';
`

// schemaV10 adds the owner panel's visit counts (internal/stats). Counts
// only: no IPs, no visitor ids — those never leave memory.
const schemaV10 = `
CREATE TABLE visits_hourly (
  venue_id INTEGER NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
  hour     INTEGER NOT NULL,          -- unix seconds, start of the UTC hour
  views    INTEGER NOT NULL DEFAULT 0,
  polls    INTEGER NOT NULL DEFAULT 0,
  qr       INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (venue_id, hour)
);
CREATE TABLE visits_daily (
  venue_id INTEGER NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
  day      TEXT NOT NULL,             -- UTC date, 2006-01-02
  visitors INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (venue_id, day)
);
`

// schemaV11 adds the per-venue picture look (render.Look presets as JSON)
// and retires privacy masks from the product (masks_json stays, unread, so
// old backups still open).
const schemaV11 = `
ALTER TABLE venues ADD COLUMN look_json TEXT NOT NULL DEFAULT '';
`

// schemaV12 adds the venue's optional Google Maps link (owner-pasted, the
// O salonu card's "Otvori u Google Kartama" button). Additive.
const schemaV12 = `
ALTER TABLE venues ADD COLUMN maps_url TEXT NOT NULL DEFAULT '';
`

// schemaV13 adds the homepage listing switch (D12: venues are browsable
// from guzva.ba/). Existing venues default to listed; the admin unlists
// bench/test venues.
const schemaV13 = `
ALTER TABLE venues ADD COLUMN listed INTEGER NOT NULL DEFAULT 1;
`

// migrations[i] takes the schema from version i to i+1. Append only.
var migrations = []struct {
	sql  string
	post func(ctx context.Context, tx *sql.Tx) error
}{
	{sql: schemaV1}, {sql: schemaV2}, {sql: schemaV3}, {sql: schemaV4},
	{sql: schemaV5}, {sql: schemaV6}, {sql: schemaV7},
	{sql: schemaV8, post: backfillMasks},
	{sql: schemaV9},
	{sql: schemaV10},
	{sql: schemaV11},
	{sql: schemaV12},
	{sql: schemaV13},
}

func (s *Store) migrate(ctx context.Context) error {
	var ver int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("store: read user_version: %w", err)
	}
	if ver > len(migrations) {
		return fmt.Errorf("store: unknown schema version %d (binary older than db?)", ver)
	}
	for ; ver < len(migrations); ver++ {
		m := migrations[ver]
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration v%d: %w", ver+1, err)
		}
		if m.post != nil {
			if err := m.post(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("store: migration v%d: %w", ver+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", ver+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: set user_version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// backfillMasks carries v3 masks (160×120 cells, inside calib_json) over to
// masks_json at the 320×240 grid. A calib document that does not parse
// carried no effective masks before either, so it is skipped.
func backfillMasks(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, calib_json FROM venues WHERE calib_json != ''`)
	if err != nil {
		return err
	}
	type upd struct {
		id  int64
		doc string
	}
	var ups []upd
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var doc struct {
			Masks []render.Rect `json:"masks"`
		}
		if json.Unmarshal([]byte(raw), &doc) != nil || len(doc.Masks) == 0 {
			continue
		}
		for i := range doc.Masks {
			m := &doc.Masks[i]
			m.X0, m.Y0, m.X1, m.Y1 = m.X0*2, m.Y0*2, m.X1*2, m.Y1*2
		}
		ups = append(ups, upd{id, MarshalMasks(doc.Masks)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range ups {
		if _, err := tx.ExecContext(ctx, `UPDATE venues SET masks_json = ? WHERE id = ?`, u.doc, u.id); err != nil {
			return err
		}
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

// reservedSlugs are first path segments the server answers itself; a venue
// with one of these slugs would be unreachable (pages.PrivacyPath).
var reservedSlugs = map[string]bool{"privatnost": true}

// ValidSlug reports whether s is a legal public slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) && !reservedSlugs[s] }

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

const venueCols = `id, slug, name, tz, style, description, phone, location, maps_url, listed, owner_hash, lang, look_json, last_frame_at, created_at`

func scanVenue(row interface{ Scan(...any) error }) (*Venue, error) {
	var v Venue
	var last, created int64
	var look string
	err := row.Scan(&v.ID, &v.Slug, &v.Name, &v.TZ, &v.Style, &v.Description, &v.Phone,
		&v.Location, &v.MapsURL, &v.Listed, &v.OwnerHash, &v.Lang, &look, &last, &created)
	if look != "" {
		_ = json.Unmarshal([]byte(look), &v.Look) // malformed → zero Look = defaults
	}
	v.Look = v.Look.Normalize()
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
	Description string // optional public one-liner (design-pass2 §5)
	Phone       string // optional public contact, digits/+/spaces
	Location    string // optional public "where we are" line, free text
	MapsURL     string // optional Google Maps link (validated by admin)
	Listed      bool   // shown in the homepage list (D12); admin-only switch
	OwnerHash   string
	Lang        string      // page language, "hr" (default) or "en" (⚑9)
	Look        render.Look // picture tuning presets (admin style panel)
	LastFrameAt time.Time
	CreatedAt   time.Time
}

// UpdateVenue replaces the editable site-plane details (owner panel +
// super-admin). Slug is identity and never changes; hours go through
// SetHours so the two writers cannot disagree on the format.
func (s *Store) UpdateVenue(ctx context.Context, id int64, name, style, tz, description, phone, location string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE venues SET name = ?, style = ?, tz = ?, description = ?, phone = ?, location = ? WHERE id = ?`,
		name, style, tz, description, phone, location, id)
	return err
}

// SetMapsURL stores the venue's Google Maps link ("" clears it). The admin
// layer validates it; the store only persists.
func (s *Store) SetMapsURL(ctx context.Context, id int64, url string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE venues SET maps_url = ? WHERE id = ?`, url, id)
	return err
}

// SetListed shows or hides the venue in the homepage list (D12). The venue
// page itself stays reachable by its link either way.
func (s *Store) SetListed(ctx context.Context, id int64, listed bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE venues SET listed = ? WHERE id = ?`, listed, id)
	return err
}

// SetLook stores the venue's picture tuning (normalized: only known
// preset values reach the column).
func (s *Store) SetLook(ctx context.Context, id int64, l render.Look) error {
	b, err := json.Marshal(l.Normalize())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE venues SET look_json = ? WHERE id = ?`, string(b), id)
	return err
}

// SetOwnerHash stores the bcrypt hash of the owner's panel password
// (super-admin only; the password itself never reaches the store).
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
	ID         int64
	VenueID    int64
	Serial     string
	Status     string // active | revoked
	FW         string
	RSSI       int
	FreeHeap   int
	Counters   proto.Counters
	Profile    string // camera profile the firmware was built with (print v4)
	LastSeenAt time.Time
	LastReason string
	CreatedAt  time.Time
}

const deviceCols = `id, venue_id, serial, status, fw, rssi, free_heap,
	boot, brownout, join_fail, post_fail, tls_fail, capture_fail,
	profile, last_seen_at, last_reason, created_at`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var seen, created int64
	err := row.Scan(&d.ID, &d.VenueID, &d.Serial, &d.Status, &d.FW, &d.RSSI, &d.FreeHeap,
		&d.Counters.Boot, &d.Counters.Brownout, &d.Counters.JoinFail, &d.Counters.PostFail,
		&d.Counters.TLSFail, &d.Counters.CaptureFail,
		&d.Profile, &seen, &d.LastReason, &created)
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
		INSERT INTO devices (venue_id, serial, token_hash, status, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		d.VenueID, d.Serial, tokenHash, status, ts(time.Now()))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrSerialTaken
		}
		return 0, fmt.Errorf("store: create device: %w", err)
	}
	return res.LastInsertId()
}

// DeviceBySerial resolves a device by its serial (admin label, §4). Used by
// the offline CLI; ingest never sees a serial.
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
	FW       string
	RSSI     int
	FreeHeap int
	Counters proto.Counters
	Profile  string
	Reason   string
	At       time.Time
}

// UpdateDeviceBeat records the health headers of one ingest.
func (s *Store) UpdateDeviceBeat(ctx context.Context, deviceID int64, b Beat) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE devices SET fw = ?, rssi = ?, free_heap = ?,
			boot = ?, brownout = ?, join_fail = ?, post_fail = ?, tls_fail = ?, capture_fail = ?,
			profile = ?, last_seen_at = ?, last_reason = ?
		WHERE id = ?`,
		b.FW, b.RSSI, b.FreeHeap,
		b.Counters.Boot, b.Counters.Brownout, b.Counters.JoinFail,
		b.Counters.PostFail, b.Counters.TLSFail, b.Counters.CaptureFail,
		b.Profile, ts(b.At), b.Reason, deviceID)
	return err
}

// LastReject returns the venue's most recent quarantined frame (reason and
// when), or ErrNotFound. The admin device page shows it so "why is the
// print stale?" is answered without the journal.
func (s *Store) LastReject(ctx context.Context, venueID int64) (string, time.Time, error) {
	var reason string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT reason, received_at FROM frames
		WHERE venue_id = ? AND status = 'quarantined' ORDER BY received_at DESC, id DESC LIMIT 1`,
		venueID).Scan(&reason, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	return reason, fromTS(at), err
}

// DeleteVenue removes a venue row; its devices, frames and visit counts go
// with it (ON DELETE CASCADE, foreign_keys on). Files — renders and the
// public page — are the caller's to remove. ErrNotFound if it did not exist.
func (s *Store) DeleteVenue(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM venues WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDevice removes a device and its token for good (revoke keeps the
// identity; delete forgets it). Frames belong to the venue and stay.
func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateDevice renames a device and/or moves it to another venue (the
// token is unchanged, so a flashed board keeps working after a move).
func (s *Store) UpdateDevice(ctx context.Context, id int64, serial string, venueID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE devices SET serial = ?, venue_id = ? WHERE id = ?`, serial, venueID, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrSerialTaken
	}
	return err
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

// MarshalMasks writes the masks_json format ("" for none). Masks left the
// product in v11; this remains for the v8 backfill migration.
func MarshalMasks(m []render.Rect) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// --- visits (owner statistics) ----------------------------------------------

// VisitHour is one venue-hour of public-page counts.
type VisitHour struct {
	Hour  time.Time // start of the UTC hour
	Views int64
	Polls int64 // still refreshes of an open tab, ~30 s of watching each
	QR    int64
}

// VisitDay is one venue's distinct visitors on a UTC day.
type VisitDay struct {
	Day      string // 2006-01-02
	Visitors int64
}

// AddVisitHour adds counts to a venue-hour (deltas from the recorder).
func (s *Store) AddVisitHour(ctx context.Context, venueID int64, hour time.Time, views, polls, qr int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO visits_hourly (venue_id, hour, views, polls, qr) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (venue_id, hour) DO UPDATE SET
			views = views + excluded.views, polls = polls + excluded.polls, qr = qr + excluded.qr`,
		venueID, ts(hour), views, polls, qr)
	return err
}

// SetVisitDay records a day's distinct visitors, keeping the larger of the
// stored and given counts (a restart empties the in-memory set).
func (s *Store) SetVisitDay(ctx context.Context, venueID int64, day string, visitors int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO visits_daily (venue_id, day, visitors) VALUES (?, ?, ?)
		ON CONFLICT (venue_id, day) DO UPDATE SET visitors = MAX(visitors, excluded.visitors)`,
		venueID, day, visitors)
	return err
}

// VisitHours returns a venue's hourly counts at or after since, oldest first.
func (s *Store) VisitHours(ctx context.Context, venueID int64, since time.Time) ([]VisitHour, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hour, views, polls, qr FROM visits_hourly
		WHERE venue_id = ? AND hour >= ? ORDER BY hour`, venueID, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VisitHour
	for rows.Next() {
		var h VisitHour
		var at int64
		if err := rows.Scan(&at, &h.Views, &h.Polls, &h.QR); err != nil {
			return nil, err
		}
		h.Hour = fromTS(at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// VisitDays returns a venue's daily visitors on or after sinceDay.
func (s *Store) VisitDays(ctx context.Context, venueID int64, sinceDay string) ([]VisitDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT day, visitors FROM visits_daily
		WHERE venue_id = ? AND day >= ? ORDER BY day`, venueID, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VisitDay
	for rows.Next() {
		var d VisitDay
		if err := rows.Scan(&d.Day, &d.Visitors); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// VisitViewsSince sums each venue's page views at or after since (the
// fleet board's "views / 7 d" column).
func (s *Store) VisitViewsSince(ctx context.Context, since time.Time) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT venue_id, SUM(views) FROM visits_hourly
		WHERE hour >= ? GROUP BY venue_id`, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PruneVisits drops visit counts older than cutoff (a year is plenty for
// "how is my page doing" and keeps the table bounded).
func (s *Store) PruneVisits(ctx context.Context, cutoff time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM visits_hourly WHERE hour < ?`, ts(cutoff)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM visits_daily WHERE day < ?`, cutoff.UTC().Format("2006-01-02"))
	return err
}

// FrameTimes returns when a venue's stored (status ok) frames arrived at
// or after since, oldest first — the camera-uptime input on the owner panel.
func (s *Store) FrameTimes(ctx context.Context, venueID int64, since time.Time) ([]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT received_at FROM frames
		WHERE venue_id = ? AND status = 'ok' AND received_at >= ? ORDER BY received_at`, venueID, ts(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, fromTS(at))
	}
	return out, rows.Err()
}
