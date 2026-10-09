package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"guzva/internal/proto"
	"guzva/internal/store"
)

func TestGenerateAndHashToken(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "gkv1_") || len(tok) < 40 {
		t.Fatalf("token shape: %q", tok)
	}
	tok2, _ := GenerateToken()
	if tok == tok2 {
		t.Fatal("tokens must be unique")
	}
	h := HashToken(tok)
	if len(h) != 64 || h != HashToken(tok) {
		t.Fatalf("hash = %q", h)
	}
	if h == HashToken(tok2) {
		t.Fatal("hash collision on distinct tokens")
	}
}

func seedDevice(t *testing.T, status string) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	vid, err := st.CreateVenue(ctx, store.Venue{Slug: "v", Name: "V"})
	if err != nil {
		t.Fatal(err)
	}
	const hash = "aabbcc"
	if _, err := st.CreateDevice(ctx, store.Device{
		VenueID: vid, Serial: "s1", Status: status,
		CfgVer: 1, CfgIntervalS: 60, CfgQuality: 18,
	}, hash); err != nil {
		t.Fatal(err)
	}
	return st, hash
}

func TestAuthenticate(t *testing.T) {
	st, _ := seedDevice(t, "active")
	ctx := context.Background()

	// Resolve the seeded hash so we know the raw token.
	dev, err := st.DeviceByTokenHash(ctx, HashToken("secret-token"))
	if err == nil {
		t.Fatal("unexpected device")
	}
	// Seed a device whose token hash matches "secret-token".
	vid, _ := st.VenueByID(ctx, 1)
	if vid == nil {
		t.Fatal("venue missing")
	}
	if _, err := st.CreateDevice(ctx, store.Device{
		VenueID: vid.ID, Serial: "s2",
		CfgVer: 1, CfgIntervalS: 60, CfgQuality: 18,
	}, HashToken("secret-token")); err != nil {
		t.Fatal(err)
	}

	req := func(auth string) *http.Request {
		r := httptest.NewRequest("POST", proto.PathIngest, nil)
		if auth != "" {
			r.Header.Set(proto.HeaderAuth, auth)
		}
		return r
	}

	d, err := Authenticate(ctx, st, req("Bearer secret-token"))
	if err != nil || d.Serial != "s2" {
		t.Fatalf("valid token: d=%+v err=%v", d, err)
	}
	if _, err := Authenticate(ctx, st, req("")); err != ErrUnauthorized {
		t.Fatalf("missing: err = %v", err)
	}
	if _, err := Authenticate(ctx, st, req("Basic abc")); err != ErrUnauthorized {
		t.Fatalf("wrong scheme: err = %v", err)
	}
	if _, err := Authenticate(ctx, st, req("Bearer nope")); err != ErrUnauthorized {
		t.Fatalf("unknown: err = %v", err)
	}

	// Revoked tokens answer 401 too (§4) — seed a revoked device.
	if _, err := st.CreateDevice(ctx, store.Device{
		VenueID: vid.ID, Serial: "s3", Status: "revoked",
		CfgVer: 1, CfgIntervalS: 60, CfgQuality: 18,
	}, HashToken("revoked-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := Authenticate(ctx, st, req("Bearer revoked-token")); err != ErrRevoked {
		t.Fatalf("revoked: err = %v", err)
	}
	_ = dev
}

func TestLimiterBurstAndRefill(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(1, 2, 0.5, 3, func() time.Time { return now })

	// IP: burst 2, third denied.
	if !l.AllowIP("1.2.3.4") || !l.AllowIP("1.2.3.4") {
		t.Fatal("burst should allow 2")
	}
	if l.AllowIP("1.2.3.4") {
		t.Fatal("third call should be denied")
	}
	// Different IP unaffected.
	if !l.AllowIP("5.6.7.8") {
		t.Fatal("other ip should pass")
	}
	// Device bucket independent from IP.
	if !l.AllowDevice(1) || !l.AllowDevice(1) || !l.AllowDevice(1) {
		t.Fatal("device burst 3")
	}
	if l.AllowDevice(1) {
		t.Fatal("device 4th denied")
	}
	// Refill: ip rate 1/s → after 1 s one token back.
	now = now.Add(time.Second)
	if !l.AllowIP("1.2.3.4") {
		t.Fatal("should refill after 1s")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.1.2.3:5555"
	if got := ClientIP(r); got != "10.1.2.3" {
		t.Errorf("remote = %q", got)
	}
	// No proxy in front (v3.1): a client-supplied XFF must be ignored —
	// otherwise the per-IP floor is trivially spoofed.
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := ClientIP(r); got != "10.1.2.3" {
		t.Errorf("xff spoof accepted: %q", got)
	}
}
