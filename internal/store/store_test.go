package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"guzva/internal/proto"
	"guzva/internal/venue"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustVenue(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateVenue(context.Background(), Venue{
		Slug: slug, Name: "Venue " + slug, Style: "poster",
	})
	if err != nil {
		t.Fatalf("create venue: %v", err)
	}
	return id
}

func TestVenueSlugUnique(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustVenue(t, s, "jadro")
	_, err := s.CreateVenue(ctx, Venue{Slug: "jadro", Name: "dup"})
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
	v, err := s.VenueBySlug(ctx, "jadro")
	if err != nil || v.Name != "Venue jadro" {
		t.Fatalf("by slug: v=%+v err=%v", v, err)
	}
	if _, err := s.VenueBySlug(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeviceLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")

	did, err := s.CreateDevice(ctx, Device{
		VenueID: vid, Serial: "esp32-aa",
		CfgVer: 1, CfgIntervalS: 60, CfgQuality: 18,
	}, "hash1")
	if err != nil {
		t.Fatalf("create device: %v", err)
	}
	if _, err := s.CreateDevice(ctx, Device{VenueID: vid, Serial: "esp32-bb"}, "hash1"); !errors.Is(err, ErrSerialTaken) {
		// duplicate token_hash also violates UNIQUE — both map to a taken key
		t.Fatalf("duplicate token: err = %v", err)
	}
	if _, err := s.CreateDevice(ctx, Device{VenueID: vid, Serial: "esp32-aa"}, "hash2"); !errors.Is(err, ErrSerialTaken) {
		t.Fatalf("duplicate serial: err = %v", ErrSerialTaken)
	}

	d, err := s.DeviceByTokenHash(ctx, "hash1")
	if err != nil || d.ID != did || d.Status != "active" {
		t.Fatalf("by token: d=%+v err=%v", d, err)
	}
	if _, err := s.DeviceByTokenHash(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// Beat updates health counters + freshness of liveness.
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	err = s.UpdateDeviceBeat(ctx, did, Beat{
		FW: "1.0.0", RSSI: -70, FreeHeap: 123456,
		Counters:      proto.Counters{Boot: 3, PostFail: 2},
		CfgAppliedVer: 1, Reason: "", At: at,
	})
	if err != nil {
		t.Fatalf("beat: %v", err)
	}
	d, _ = s.DeviceByTokenHash(ctx, "hash1")
	if d.RSSI != -70 || d.FW != "1.0.0" || d.Counters.Boot != 3 || d.Counters.PostFail != 2 {
		t.Fatalf("beat not applied: %+v", d)
	}
	if !d.LastSeenAt.Equal(at) {
		t.Fatalf("last_seen = %v want %v", d.LastSeenAt, at)
	}
	if got := d.DesiredCfg(); got.Ver != 1 || got.IntervalS != 60 || got.Quality != 18 || got.Extra != nil {
		t.Fatalf("desired cfg = %+v", got)
	}

	// Config bump lands on the device row (audit = slog line at the caller),
	// cadence trio in its columns and sensor keys in cfg_json.
	if err := s.SetDeviceCfg(ctx, did, proto.DesiredCfg{
		Ver: 2, IntervalS: 30, Quality: 14,
		Extra: map[string]int{"framesize": 5, "led_pwm": 80, "brightness": 1},
	}); err != nil {
		t.Fatalf("set cfg: %v", err)
	}
	d, _ = s.DeviceByTokenHash(ctx, "hash1")
	if d.CfgVer != 2 || d.CfgIntervalS != 30 || d.CfgQuality != 14 {
		t.Fatalf("cfg not applied: %+v", d)
	}
	want := map[string]int{"framesize": 5, "led_pwm": 80, "brightness": 1}
	if len(d.CfgExtra) != len(want) {
		t.Fatalf("cfg extras = %v want %v", d.CfgExtra, want)
	}
	for k, v := range want {
		if d.CfgExtra[k] != v {
			t.Fatalf("cfg extra %s = %d want %d", k, d.CfgExtra[k], v)
		}
	}
	got := d.DesiredCfg()
	if got.Ver != 2 || got.Extra["framesize"] != 5 {
		t.Fatalf("desired cfg after bump = %+v", got)
	}

	// Clearing the extras (empty set) must empty cfg_json, not keep stale keys.
	if err := s.SetDeviceCfg(ctx, did, proto.DesiredCfg{Ver: 3, IntervalS: 30, Quality: 14}); err != nil {
		t.Fatalf("clear extras: %v", err)
	}
	d, _ = s.DeviceByTokenHash(ctx, "hash1")
	if len(d.CfgExtra) != 0 || d.CfgVer != 3 {
		t.Fatalf("extras not cleared: ver=%d extra=%v", d.CfgVer, d.CfgExtra)
	}

	// DeviceBySerial resolves the offline cfg tooling's lookup.
	bySerial, err := s.DeviceBySerial(ctx, "esp32-aa")
	if err != nil || bySerial.ID != did {
		t.Fatalf("by serial: d=%+v err=%v", bySerial, err)
	}
	if _, err := s.DeviceBySerial(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by serial miss: err = %v, want ErrNotFound", err)
	}
}

// TestMigrationV1ToV2 builds a v1-era database (no cfg_json column) and
// checks Open migrates it in place with existing devices intact.
func TestMigrationV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO venues (slug, name, style, tz, created_at) VALUES ('old', 'Old', 'poster', 'UTC', 0)`)
	if err != nil {
		t.Fatalf("seed venue: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO devices (venue_id, serial, token_hash, cfg_ver, cfg_interval_s, cfg_quality, created_at)
		VALUES (1, 'esp32-old', 'hashold', 7, 45, 12, 0)`)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer s.Close()

	var ver int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil || ver != 4 {
		t.Fatalf("user_version = %d err=%v, want 4", ver, err)
	}
	d, err := s.DeviceBySerial(context.Background(), "esp32-old")
	if err != nil {
		t.Fatalf("device after migration: %v", err)
	}
	if d.CfgVer != 7 || d.CfgIntervalS != 45 || d.CfgQuality != 12 || len(d.CfgExtra) != 0 {
		t.Fatalf("v1 device not intact: %+v", d)
	}
	// And the migrated row accepts extras.
	if err := s.SetDeviceCfg(context.Background(), d.ID, proto.DesiredCfg{
		Ver: 8, IntervalS: 45, Quality: 12, Extra: map[string]int{"framesize": 5},
	}); err != nil {
		t.Fatalf("set cfg after migration: %v", err)
	}
	d, _ = s.DeviceBySerial(context.Background(), "esp32-old")
	if d.CfgExtra["framesize"] != 5 {
		t.Fatalf("extras after migration: %v", d.CfgExtra)
	}
}

func TestInsertFrameIdempotentAndFreshness(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")
	at := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)

	f := Frame{VenueID: vid, CaptureID: "C1-1-aaaa", SHA256: "s1",
		ReceivedAt: at, Bytes: 100, Width: 640, Height: 480,
		Preset: "poster", PresetVer: 1, Path: "venues/1/stills/C1-1-aaaa.jpg", Status: "ok"}
	id, err := s.InsertFrame(ctx, f)
	if err != nil || id == 0 {
		t.Fatalf("insert: id=%d err=%v", id, err)
	}
	if _, err := s.InsertFrame(ctx, f); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("dup: err = %v, want ErrDuplicate", err)
	}
	v, _ := s.VenueByID(ctx, vid)
	if !v.LastFrameAt.Equal(at) {
		t.Fatalf("last_frame_at = %v, want %v", v.LastFrameAt, at)
	}

	latest, err := s.LatestFrame(ctx, vid)
	if err != nil || latest.CaptureID != "C1-1-aaaa" {
		t.Fatalf("latest: %+v err=%v", latest, err)
	}

	// A quarantined record must NOT advance freshness (§1 never publish a
	// failure — freshness tracks good renders only).
	later := at.Add(time.Hour)
	if _, err := s.InsertFrame(ctx, Frame{
		VenueID: vid, CaptureID: "C1-2-bbbb", SHA256: "s2", ReceivedAt: later,
		Status: "quarantined", Reason: "dark",
	}); err != nil {
		t.Fatalf("quarantined insert: %v", err)
	}
	v, _ = s.VenueByID(ctx, vid)
	if !v.LastFrameAt.Equal(at) {
		t.Fatalf("quarantine moved freshness: %v", v.LastFrameAt)
	}
	// Latest good frame is still the ok one.
	latest, _ = s.LatestFrame(ctx, vid)
	if latest.CaptureID != "C1-1-aaaa" {
		t.Fatalf("latest leaked quarantine: %+v", latest)
	}

	// Same capture_id is unique across statuses too.
	if _, err := s.InsertFrame(ctx, Frame{VenueID: vid, CaptureID: "C1-2-bbbb", Status: "ok"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("dup after quarantine: err = %v", err)
	}
}

func TestHoursRoundtrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")
	h, err := venue.ParseSpec("Mo-Fr 09:00-17:00;Sa 10:00-14:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHours(ctx, vid, h); err != nil {
		t.Fatalf("set hours: %v", err)
	}
	got, err := s.Hours(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Configured() || !got.OpenAt(time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("hours roundtrip wrong: %v", got)
	}
	// Replace with empty → unconfigured.
	if err := s.SetHours(ctx, vid, venue.Hours{}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Hours(ctx, vid)
	if got.Configured() {
		t.Fatal("hours should be cleared")
	}
}

func TestStaleFramesKeepsLastGoodPerVenue(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	v1 := mustVenue(t, s, "one")
	v2 := mustVenue(t, s, "two")

	insert := func(vid int64, cid string, at time.Time) {
		t.Helper()
		if _, err := s.InsertFrame(ctx, Frame{
			VenueID: vid, CaptureID: cid, SHA256: cid, ReceivedAt: at,
			Path: "p/" + cid, Status: "ok",
		}); err != nil {
			t.Fatalf("insert %s: %v", cid, err)
		}
	}
	// Venue 1: three frames — two expired, one recent (the newest).
	insert(v1, "C1-1-aaaa", now.Add(-72*time.Hour))
	insert(v1, "C1-2-bbbb", now.Add(-50*time.Hour))
	insert(v1, "C1-3-cccc", now.Add(-1*time.Hour))
	// Venue 2: only an ancient frame — it is last-good and must survive.
	insert(v2, "C9-1-dddd", now.Add(-1000*time.Hour))

	stale, err := s.StaleFrames(ctx, now, 48)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, sf := range stale {
		got[sf.ID] = sf.Path
	}
	if len(stale) != 2 {
		t.Fatalf("stale count = %d (%v), want 2", len(stale), got)
	}
	for _, sf := range stale {
		if sf.Path == "p/C1-3-cccc" || sf.Path == "p/C9-1-dddd" {
			t.Fatalf("last-good selected for prune: %s", sf.Path)
		}
	}

	// Delete and meta prune behave.
	var ids []int64
	for _, sf := range stale {
		ids = append(ids, sf.ID)
	}
	if err := s.DeleteFrames(ctx, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LatestFrame(ctx, v1); err != nil {
		t.Fatalf("latest after prune: %v", err)
	}

	// Meta retention: a fresh quarantined row survives a now-1h cutoff and
	// goes only when the cutoff passes its received_at.
	if _, err := s.InsertFrame(ctx, Frame{
		VenueID: v1, CaptureID: "C1-9-qqqq", SHA256: "q", ReceivedAt: now,
		Status: "quarantined", Reason: "dark",
	}); err != nil {
		t.Fatalf("insert quarantined: %v", err)
	}
	if err := s.PruneMeta(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatalf("prune meta: %v", err)
	}
	var q int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM frames WHERE status = 'quarantined'`).Scan(&q)
	if q != 1 {
		t.Fatalf("fresh quarantine pruned too early: %d", q)
	}
	if err := s.PruneMeta(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM frames WHERE status = 'quarantined'`).Scan(&q)
	if q != 0 {
		t.Fatalf("old quarantine survived: %d", q)
	}
}

// Migration v3 adds owner_hash; old databases gain the column with existing
// venues intact and no owner login until the super-admin sets one.
func TestMigrationToV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO venues (slug, name, style, tz, created_at)
		VALUES ('old', 'Old', 'poster', 'UTC', 0)`); err != nil {
		t.Fatalf("seed venue: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer s.Close()
	var ver int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil || ver != 4 {
		t.Fatalf("user_version = %d err=%v, want 4", ver, err)
	}
	v, err := s.VenueBySlug(context.Background(), "old")
	if err != nil {
		t.Fatalf("venue after migration: %v", err)
	}
	// Every migration in the chain must have run, not just the last one: a
	// missing fallthrough left case 3 unreachable and reported success.
	if v.Lang != "hr" {
		t.Fatalf("venue lang = %q after migrating from v1, want \"hr\"", v.Lang)
	}
	if v.Name != "Old" || v.OwnerHash != "" {
		t.Fatalf("v1 venue not intact: %+v", v)
	}
}

// UpdateVenue + SetOwnerHash + DevicesByVenue: the owner panel's data plane.
func TestVenueDetailsAndOwner(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")

	if err := s.UpdateVenue(ctx, vid, "Jadro Grad", "mono", "Europe/Zagreb"); err != nil {
		t.Fatalf("update venue: %v", err)
	}
	v, err := s.VenueByID(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "Jadro Grad" || v.Style != "mono" || v.TZ != "Europe/Zagreb" || v.Slug != "jadro" {
		t.Fatalf("venue = %+v (slug must be immutable)", v)
	}

	if err := s.SetOwnerHash(ctx, vid, "bcrypt-hash-here"); err != nil {
		t.Fatalf("set owner hash: %v", err)
	}
	v, _ = s.VenueByID(ctx, vid)
	if v.OwnerHash != "bcrypt-hash-here" {
		t.Fatalf("owner hash not stored: %+v", v)
	}

	did, err := s.CreateDevice(ctx, Device{
		VenueID: vid, Serial: "esp32-aa",
		CfgVer: 1, CfgIntervalS: 60, CfgQuality: 18,
	}, "hash1")
	if err != nil {
		t.Fatal(err)
	}
	other := mustVenue(t, s, "other")
	if _, err := s.CreateDevice(ctx, Device{VenueID: other, Serial: "esp32-bb"}, "hash2"); err != nil {
		t.Fatal(err)
	}
	devs, err := s.DevicesByVenue(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].ID != did {
		t.Fatalf("devices by venue = %+v", devs)
	}
}

// Reopening an already-current database must be a no-op, not an "unknown
// schema version" error. This is the regression test for a real outage: the
// v3 migration left user_version=3 with no matching `case`, so every service
// restart after the first boot failed to open the store.
func TestReopenCurrentDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	// A fresh database migrates 0→4 in one open; reopens stay at 4.
	// Every step of the chain must be terminal — a missing case is what
	// turned a deploy into an outage once.
	for i, want := range []int{4, 4, 4} {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		var ver int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
			t.Fatal(err)
		}
		if ver != want {
			t.Errorf("open #%d: user_version = %d, want %d", i+1, ver, want)
		}
		s.Close()
	}
}
