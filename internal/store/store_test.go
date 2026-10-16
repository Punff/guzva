package store

import (
	"context"
	"database/sql"
	"errors"
	"guzva/internal/render"
	"path/filepath"
	"strings"
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

// The slug becomes a directory under the public web root, so the store
// itself must refuse a bad one no matter which writer slipped it through
// (maintenance W1/B5: the CLI could, before it grew the same validation).
// The admin console and `guzva provision` pre-validate for friendly
// errors; this is the enforcement they both rely on.
func TestCreateVenueEnforcesSlugRule(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for _, bad := range []string{"", "../etc", "Jadro", "with space", "-leading", strings.Repeat("x", 41), "privatnost"} {
		_, err := s.CreateVenue(ctx, Venue{Slug: bad, Name: "x", Style: "poster"})
		if !errors.Is(err, ErrBadSlug) {
			t.Errorf("CreateVenue(slug=%q): err = %v, want ErrBadSlug", bad, err)
		}
	}
	for _, ok := range []string{"jadro", "a1", strings.Repeat("x", 40)} {
		if _, err := s.CreateVenue(ctx, Venue{Slug: ok, Name: "x", Style: "poster"}); err != nil {
			t.Errorf("CreateVenue(slug=%q): %v", ok, err)
		}
	}
}

func TestDeviceLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")

	did, err := s.CreateDevice(ctx, Device{
		VenueID: vid, Serial: "esp32-aa",
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
		Counters: proto.Counters{Boot: 3, PostFail: 2},
		Profile:  "p1", At: at,
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
	if d.Profile != "p1" {
		t.Fatalf("profile = %q", d.Profile)
	}

	// DeviceBySerial resolves the CLI's lookup.
	bySerial, err := s.DeviceBySerial(ctx, "esp32-aa")
	if err != nil || bySerial.ID != did {
		t.Fatalf("by serial: d=%+v err=%v", bySerial, err)
	}
	if _, err := s.DeviceBySerial(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by serial miss: err = %v, want ErrNotFound", err)
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

// Migration v8 carries v3 masks (160×120 cells inside calib_json) over to
// masks_json at the 320×240 grid; thresholds/bias are dropped.
func TestMigrationV8BackfillsMasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:7] {
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO venues (slug, name, created_at, calib_json) VALUES
		('a', 'A', 0, '{"t":[50,100,150],"bias":4,"masks":[{"x0":10,"y0":20,"x1":30,"y1":40}]}'),
		('b', 'B', 0, 'not json')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Masks left the product in v11, but the v8 backfill must still have
	// run (the column is the record of what old venues had).
	var am, bm string
	_ = s.db.QueryRow(`SELECT masks_json FROM venues WHERE slug = 'a'`).Scan(&am)
	_ = s.db.QueryRow(`SELECT masks_json FROM venues WHERE slug = 'b'`).Scan(&bm)
	if am != MarshalMasks([]v3Mask{{X0: 20, Y0: 40, X1: 60, Y1: 80}}) || bm != "" {
		t.Errorf("masks_json a=%q b=%q, want one rect scaled ×2 and none", am, bm)
	}
}

// Look round-trips normalized; venue deletion cascades to devices, frames
// and visits; devices can be renamed, moved and deleted.
func TestLookAndCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")
	other := mustVenue(t, s, "other")
	if err := s.SetLook(ctx, vid, render.Look{Outline: "strong", Blur: "nonsense"}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.VenueByID(ctx, vid)
	if v.Look != (render.Look{Outline: "strong", Blur: "standard", Contrast: "normal"}) {
		t.Errorf("look = %+v", v.Look)
	}

	did, _ := s.CreateDevice(ctx, Device{VenueID: vid, Serial: "cam-a"}, "h-a")
	if _, err := s.CreateDevice(ctx, Device{VenueID: vid, Serial: "cam-b"}, "h-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateDevice(ctx, did, "cam-b", vid); !errors.Is(err, ErrSerialTaken) {
		t.Errorf("rename onto a taken serial: err = %v", err)
	}
	if err := s.UpdateDevice(ctx, did, "cam-renamed", other); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.DeviceByID(ctx, did); d.Serial != "cam-renamed" || d.VenueID != other {
		t.Errorf("device after move = %+v", d)
	}
	if err := s.DeleteDevice(ctx, did); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeviceByID(ctx, did); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted device still found: %v", err)
	}

	if _, err := s.InsertFrame(ctx, Frame{VenueID: vid, CaptureID: "C1-1-aaaa", SHA256: "x", ReceivedAt: time.Now(), Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddVisitHour(ctx, vid, time.Now(), 3, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVenue(ctx, vid); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"devices": 0, "frames": 0, "visits_hourly": 0} {
		var n int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE venue_id = ?`, vid).Scan(&n)
		if n != want {
			t.Errorf("%s rows left for the deleted venue: %d", table, n)
		}
	}
	if err := s.DeleteVenue(ctx, vid); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	if _, err := s.VenueByID(ctx, other); err != nil {
		t.Errorf("the other venue was touched: %v", err)
	}
}

// A v1-era database migrates through the whole chain in one open, with its
// venues and devices intact and every later column at its empty default.
func TestMigrationFromV1(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO devices (venue_id, serial, token_hash, cfg_ver, created_at)
		VALUES (1, 'esp32-old', 'hashold', 7, 0)`); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer s.Close()
	var ver int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil || ver != len(migrations) {
		t.Fatalf("user_version = %d err=%v, want %d", ver, err, len(migrations))
	}
	if d, err := s.DeviceBySerial(context.Background(), "esp32-old"); err != nil || d.Profile != "" {
		t.Fatalf("v1 device after migration: %+v err=%v", d, err)
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
	if v.Look != (render.Look{}).Normalize() {
		t.Fatalf("venue look = %+v after migrating from v1, want defaults", v.Look)
	}
	if v.Name != "Old" || v.OwnerHash != "" {
		t.Fatalf("v1 venue not intact: %+v", v)
	}
	// v7's location is additive: an old row gains the column empty, so no
	// pre-existing venue changes shape on the contact section.
	if v.Location != "" {
		t.Fatalf("venue location = %q after migrating from v1, want empty", v.Location)
	}
	if v.Description != "" || v.Phone != "" {
		t.Fatalf("v6 public info not empty on a v1 row: %+v", v)
	}
}

// UpdateVenue + SetOwnerHash + DevicesByVenue: the owner panel's data plane.
func TestVenueDetailsAndOwner(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid := mustVenue(t, s, "jadro")

	if err := s.UpdateVenue(ctx, vid, "Jadro Grad", "mono", "Europe/Zagreb", "", "", "Obala Kulina bana 3"); err != nil {
		t.Fatalf("update venue: %v", err)
	}
	v, err := s.VenueByID(ctx, vid)
	if err != nil {
		t.Fatal(err)
	}
	if v.Name != "Jadro Grad" || v.Style != "mono" || v.TZ != "Europe/Zagreb" || v.Slug != "jadro" {
		t.Fatalf("venue = %+v (slug must be immutable)", v)
	}
	if v.Location != "Obala Kulina bana 3" {
		t.Fatalf("venue location = %q, want it stored and read back", v.Location)
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
	// A fresh database migrates fully in one open; reopens stay there.
	want := len(migrations)
	for i := range 3 {
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

func TestSetMapsURL(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, err := s.CreateVenue(ctx, Venue{Slug: "jadro", Name: "Jadro", Style: "poster"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMapsURL(ctx, id, "https://maps.app.goo.gl/AbC123"); err != nil {
		t.Fatal(err)
	}
	v, err := s.VenueByID(ctx, id)
	if err != nil || v.MapsURL != "https://maps.app.goo.gl/AbC123" {
		t.Fatalf("MapsURL = %q, err %v", v.MapsURL, err)
	}
}

func TestPendingCommandDeliveredOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	vid, _ := s.CreateVenue(ctx, Venue{Slug: "jadro", Name: "Jadro", Style: "poster"})
	did, _ := s.CreateDevice(ctx, Device{VenueID: vid, Serial: "cam1"}, "h1")
	now := time.Now()
	if cmd, _ := s.TakePendingCommand(ctx, did, now); cmd != "" {
		t.Fatalf("empty queue gave %q", cmd)
	}
	s.SetPendingCommand(ctx, did, "reboot", now)
	if cmd, _ := s.TakePendingCommand(ctx, did, now); cmd != "reboot" {
		t.Fatalf("take = %q", cmd)
	}
	if cmd, _ := s.TakePendingCommand(ctx, did, now); cmd != "" {
		t.Fatalf("delivered twice: %q", cmd)
	}
	s.AddDeviceEvent(ctx, did, now, "boot", "reason=sw")
	if ev, _ := s.DeviceEvents(ctx, did, 5); len(ev) != 1 || ev[0].Kind != "boot" {
		t.Fatalf("events = %+v", ev)
	}
}
