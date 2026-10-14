package proto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Rollout compatibility (print v4 §3): firmware before Profile v1 still
// sends cfg_ver_applied and must parse; new firmware sends profile.
func TestMetaAcceptsLegacyAndProfile(t *testing.T) {
	var m Meta
	legacy := `{"capture_id":"C1-2-abcd","fw":"0.6","cfg_ver_applied":7,"counters":{"boot":3}}`
	if err := json.Unmarshal([]byte(legacy), &m); err != nil || m.CaptureID != "C1-2-abcd" || m.Counters.Boot != 3 {
		t.Fatalf("legacy meta: %+v err=%v", m, err)
	}
	if err := json.Unmarshal([]byte(`{"capture_id":"C1-3-abcd","profile":"p1"}`), &m); err != nil || m.Profile != "p1" {
		t.Fatalf("profile meta: %+v err=%v", m, err)
	}
}

// The response carries no device config: old firmware applies desired_cfg
// only if found, so leaving it out keeps them on their stored settings, and
// it must not contain a bare "ver" key the old parser would latch onto.
func TestResponseHasNoDeviceConfig(t *testing.T) {
	b, _ := json.Marshal(Response{NextS: IntervalS, ServerTime: time.Unix(0, 0).UTC()})
	for _, banned := range []string{"desired_cfg", `"ver"`, "interval_s"} {
		if strings.Contains(string(b), banned) {
			t.Errorf("response %s contains %s", b, banned)
		}
	}
	if !strings.Contains(string(b), `"next_s":60`) {
		t.Errorf("response %s lacks next_s", b)
	}
}

func TestValidCaptureID(t *testing.T) {
	for id, want := range map[string]bool{
		"C1-1-abcd": true, "C4294967295-12-00ff": true,
		"C1-1-ABCD": false, "1-1-abcd": false, "C1-1-abc": false, "": false,
	} {
		if ValidCaptureID(id) != want {
			t.Errorf("ValidCaptureID(%q) != %v", id, want)
		}
	}
}
