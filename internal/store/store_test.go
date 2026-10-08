package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"imaliguzve/internal/proto"
	"imaliguzve/internal/venue"
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
		Slug: slug, Name: "Venue " + slug, Style: "poster", FrameKey: "key-" + slug,
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
	_, err := s.CreateVenue(ctx, Venue{Slug: "jadro", Name: "dup", FrameKey: "x"})
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
	if got := d.DesiredCfg(); got != (proto.DesiredCfg{Ver: 1, IntervalS: 60, Quality: 18}) {
		t.Fatalf("desired cfg = %+v", got)
	}

	// Config bump logs to settings_log (§6.2).
	if err := s.SetDeviceCfg(ctx, did, 2, 30, 14, "admin"); err != nil {
		t.Fatalf("set cfg: %v", err)
	}
	d, _ = s.DeviceByTokenHash(ctx, "hash1")
	if d.CfgVer != 2 || d.CfgIntervalS != 30 || d.CfgQuality != 14 {
		t.Fatalf("cfg not applied: %+v", d)
	}
	var logN int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings_log WHERE device_id = ?`, did).Scan(&logN); err != nil {
		t.Fatal(err)
	}
	if logN != 1 {
		t.Fatalf("settings_log rows = %d, want 1", logN)
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
	// Wall clock, not a fixed instant: LogHealth stamps time.Now().
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

	if err := s.LogHealth(ctx, "quarantine:dark", v1, 1, "C1-1-aaaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneMeta(ctx, now.Add(-time.Hour), now); err != nil {
		// events are written with time.Now(); pruneMeta cutoff now-1h would
		// keep them only if written within the hour — force old event:
		t.Fatalf("prune meta: %v", err)
	}
	var evts int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_events`).Scan(&evts)
	if evts != 1 {
		t.Fatalf("recent health event pruned too early: %d", evts)
	}
	if err := s.PruneMeta(ctx, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM health_events`).Scan(&evts)
	if evts != 0 {
		t.Fatalf("old health event survived: %d", evts)
	}
}
