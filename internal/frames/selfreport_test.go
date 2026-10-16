package frames

import (
	"context"
	"strings"
	"testing"

	"guzva/internal/proto"
)

// TestSelfReportTimelineAndCommand (business plan P4): the P4 meta fields
// are stored; a reboot, a brownout, a firmware change and a new error each
// land on the timeline once; a queued command rides exactly one reply; and a
// beat from older firmware (no P4 fields) leaves the self-report alone.
func TestSelfReportTimelineAndCommand(t *testing.T) {
	f := newFixture(t, 20)
	ctx := context.Background()
	beat := func(id string, boot, brown uint32, fw, reset, lastErr string, uptime uint32) map[string]any {
		t.Helper()
		w := f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{
			CaptureID: id, FW: fw, RSSI: -60, FreeHeap: 100000, Profile: "p1",
			Counters: proto.Counters{Boot: boot, Brownout: brown}, ResetReason: reset, UptimeS: uptime,
			WiFiReconnects: 2, LastErr: lastErr,
		}), nil)
		if w.Code != proto.StatusStored {
			t.Fatalf("%s: %d %s", id, w.Code, w.Body.String())
		}
		return decodeResponse(t, w)
	}
	beat("C5-1-aaaa", 5, 0, "v1", "power_on", "", 30)
	beat("C5-2-aaaa", 5, 0, "v1", "power_on", "", 90)
	d := f.device(t)
	if d.ResetReason != "power_on" || d.UptimeS != 90 || d.WiFiReconnects != 2 {
		t.Fatalf("self-report not stored: %+v", d)
	}

	// Reboot after a brownout, on new firmware, with an error since boot.
	beat("C6-1-aaaa", 6, 1, "v2", "brownout", "tls:-0x7280", 12)
	ev, _ := f.st.DeviceEvents(ctx, d.ID, 10)
	var kinds []string
	for _, e := range ev {
		kinds = append(kinds, e.Kind+":"+e.Detail)
	}
	joined := strings.Join(kinds, " | ")
	for _, want := range []string{"boot:boot 6, reason brownout", "brownout:brownout #1", "firmware:v1 → v2", "error:tls:-0x7280", "boot:first contact"} {
		if !strings.Contains(joined, want) {
			t.Errorf("timeline lacks %q in %s", want, joined)
		}
	}

	// Command: queued → carried by exactly one reply → logged.
	f.st.SetPendingCommand(ctx, d.ID, proto.CmdReboot, testNow)
	if r := beat("C6-2-aaaa", 6, 1, "v2", "brownout", "tls:-0x7280", 70); r["cmd"] != "reboot" {
		t.Fatalf("reply cmd = %v", r["cmd"])
	}
	if r := beat("C6-3-aaaa", 6, 1, "v2", "brownout", "tls:-0x7280", 130); r["cmd"] != nil {
		t.Fatalf("command delivered twice: %v", r["cmd"])
	}
	if ev, _ := f.st.DeviceEvents(ctx, d.ID, 1); len(ev) != 1 || ev[0].Detail != "sent: reboot" {
		t.Fatalf("cmd event = %+v", ev)
	}

	// Older firmware (no P4 fields) must not blank the self-report.
	w := f.post(t, "Bearer "+testToken, metaJSON(t, proto.Meta{CaptureID: "C6-4-aaaa", FW: "v2", RSSI: -60,
		Counters: proto.Counters{Boot: 6, Brownout: 1}}), nil)
	if w.Code != proto.StatusStored {
		t.Fatal(w.Code)
	}
	if d := f.device(t); d.ResetReason != "brownout" || d.LastErr != "tls:-0x7280" {
		t.Errorf("old-firmware beat blanked the self-report: %+v", d)
	}
}
