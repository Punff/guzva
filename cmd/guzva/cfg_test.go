package main

import "testing"

func TestParseCfgSet(t *testing.T) {
	got, err := parseCfgSet("framesize=5, led_pwm=80, brightness=-1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 3 || got["framesize"] != 5 || got["led_pwm"] != 80 || got["brightness"] != -1 {
		t.Fatalf("got = %v", got)
	}
	if m, err := parseCfgSet(""); err != nil || m != nil {
		t.Fatalf("empty: m=%v err=%v", m, err)
	}
	for _, bad := range []string{"framesize", "framesize=x", "ver=3", "led_pwm=300", "typo=1"} {
		if _, err := parseCfgSet(bad); err == nil {
			t.Errorf("parseCfgSet(%q) must fail", bad)
		}
	}
}
