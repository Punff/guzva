package alert

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"guzva/internal/proto"
	"guzva/internal/store"
	"guzva/internal/venue"
)

var t0 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func keys(cs []Condition) string {
	var k []string
	for _, c := range cs {
		k = append(k, c.Key)
	}
	return strings.Join(k, ",")
}

func TestStaleOnlyWhenExpected(t *testing.T) {
	e := NewEngine()
	old := t0.Add(-20 * time.Minute)
	got := e.Evaluate(Snapshot{Now: t0, DiskFree: -1, Venues: []VenueState{
		{Slug: "open", Name: "Open", Open: venue.Open, LastFrameAt: old},
		{Slug: "nohours", Name: "NoHours", Open: venue.NoHours, LastFrameAt: old},
		{Slug: "closed", Name: "Closed", Open: venue.Closed, LastFrameAt: old},
		{Slug: "fresh", Name: "Fresh", Open: venue.Open, LastFrameAt: t0.Add(-time.Minute)},
		{Slug: "never", Name: "Never", Open: venue.Open},
	}})
	if k := keys(got); k != "stale:nohours,stale:open" {
		t.Fatalf("conditions = %s", k)
	}
}

func TestManyStale(t *testing.T) {
	var vs []VenueState
	for _, s := range []string{"a", "b", "c"} {
		vs = append(vs, VenueState{Slug: s, Name: s, Open: venue.Open, LastFrameAt: t0.Add(-time.Hour)})
	}
	if k := keys(NewEngine().Evaluate(Snapshot{Now: t0, DiskFree: -1, Venues: vs})); !strings.Contains(k, "many-stale") {
		t.Fatalf("conditions = %s", k)
	}
}

func TestRebootBrownoutWeakNeedHistory(t *testing.T) {
	e := NewEngine()
	dev := func(at time.Time, boot, brown uint32, rssi int) Snapshot {
		return Snapshot{Now: at, DiskFree: 50, Devices: []DeviceState{{Serial: "x", VenueName: "V", Boot: boot, Brownout: brown, RSSI: rssi, LastSeen: at}}}
	}
	// First sight: old counters are history, a weak reading just starts the clock.
	if k := keys(e.Evaluate(dev(t0, 40, 4, -80))); k != "" {
		t.Fatalf("first sight raised %s", k)
	}
	// 20 min later: two reboots + a brownout.
	if k := keys(e.Evaluate(dev(t0.Add(20*time.Minute), 42, 5, -80))); k != "brownout:x" {
		t.Fatalf("at +20m: %s", k)
	}
	// 40 min: third reboot within the hour → loop.
	if k := keys(e.Evaluate(dev(t0.Add(40*time.Minute), 43, 5, -80))); k != "brownout:x,reboot:x" {
		t.Fatalf("at +40m: %s", k)
	}
	// 61 min: weak for an hour. The boot window (now−1h = +1m) has slid past
	// the t0 sample, so only the boot seen between +20 and +40 is inside it:
	// no longer a loop.
	if k := keys(e.Evaluate(dev(t0.Add(61*time.Minute), 43, 5, -80))); k != "brownout:x,weak:x" {
		t.Fatalf("at +61m: %s", k)
	}
	// Signal recovers; 7 h later the brownout hold has expired and boots are quiet.
	e.Evaluate(dev(t0.Add(62*time.Minute), 43, 5, -60))
	if k := keys(e.Evaluate(dev(t0.Add(7*time.Hour), 43, 5, -60))); k != "" {
		t.Fatalf("at +7h: %s", k)
	}
}

func TestDiskLow(t *testing.T) {
	if k := keys(NewEngine().Evaluate(Snapshot{Now: t0, DiskFree: 9})); k != "disk" {
		t.Fatalf("conditions = %s", k)
	}
}

type fakeTG struct {
	mu   sync.Mutex
	msgs []string
	fail bool
}

func (f *fakeTG) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/botTOKEN/sendMessage") {
			t.Errorf("path %s", r.URL.Path)
		}
		if f.fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		b, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(b))
		if v.Get("chat_id") != "42" {
			t.Errorf("chat_id %q", v.Get("chat_id"))
		}
		f.msgs = append(f.msgs, v.Get("text"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMonitorLedger: one message per change, nothing re-sent across a
// restart, no false "resolved" from a history rule while warming up, and a
// failed send is retried on the next tick.
func TestMonitorLedger(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vid, _ := st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo"})
	did, err := st.CreateDevice(ctx, store.Device{VenueID: vid, Serial: "cam1"}, "hash1")
	if err != nil {
		t.Fatal(err)
	}
	now := t0
	beat := func(rssi int) {
		if err := st.UpdateDeviceBeat(ctx, did, store.Beat{RSSI: rssi, Counters: proto.Counters{Boot: 1}, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertFrame(ctx, store.Frame{VenueID: vid, CaptureID: "C1-1-aaaa", SHA256: "x", ReceivedAt: now.Add(-30 * time.Minute),
		Bytes: 1, Width: 320, Height: 240, Preset: "poster", PresetVer: 1, Path: "p", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeTG{}
	srv := f.server(t)
	mk := func(started time.Time) *Monitor {
		return &Monitor{St: st, Engine: NewEngine(), TG: &Telegram{Token: "TOKEN", Chat: "42", Base: srv.URL},
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }, Started: started}
	}
	m := mk(now)
	beat(-80)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.msgs) != 1 || !strings.HasPrefix(f.msgs[0], "⚠ Jadro: no picture") {
		t.Fatalf("first tick msgs = %q", f.msgs)
	}
	m.Tick(ctx) // same state: nothing new
	if len(f.msgs) != 1 {
		t.Fatalf("re-sent: %q", f.msgs)
	}

	// Weak signal for an hour → raised.
	for i := 0; i < 62; i++ {
		now = now.Add(time.Minute)
		beat(-80)
		m.Tick(ctx)
	}
	if !strings.Contains(strings.Join(f.msgs, "|"), "weak WiFi") {
		t.Fatalf("weak not raised: %q", f.msgs)
	}
	n := len(f.msgs)

	// Restart: fresh Engine memory. Weak must not be "resolved" while warming.
	m = mk(now)
	now = now.Add(time.Minute)
	beat(-80)
	m.Tick(ctx)
	if len(f.msgs) != n {
		t.Fatalf("restart produced messages: %q", f.msgs[n:])
	}

	// Telegram down: the recovery is retried, not lost.
	if _, err := st.InsertFrame(ctx, store.Frame{VenueID: vid, CaptureID: "C1-2-bbbb", SHA256: "y", ReceivedAt: now,
		Bytes: 1, Width: 320, Height: 240, Preset: "poster", PresetVer: 1, Path: "p2", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	m.Tick(ctx)
	f.fail = false
	m.Tick(ctx)
	last := f.msgs[len(f.msgs)-1]
	if !strings.HasPrefix(last, "✓ Resolved: Jadro: no picture") {
		t.Fatalf("recovery not sent after retry: %q", f.msgs[n:])
	}
}

func TestPingHidesURL(t *testing.T) {
	err := Ping(context.Background(), nil, "http://127.0.0.1:1/secret-uuid")
	if err == nil || strings.Contains(err.Error(), "secret-uuid") {
		t.Fatalf("err = %v", err)
	}
}
