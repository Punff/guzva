// Package auth implements the device-credential half of PLAN D3 for Phase 1:
// static per-device bearer tokens, stored only as sha256, plus the per-IP and
// per-device token buckets that sit in front of ingest (§6.1).
//
// Owner/admin sessions (bcrypt + cookie + CSRF + scope middleware) arrive
// with Phase 2's owner P1 — deliberately not stubbed here, per the plan's
// "no interface until a second implementation exists" rule.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"guzva/internal/proto"
	"guzva/internal/store"

	"golang.org/x/time/rate"
)

// Errors surfaced to handlers; the wire answers 401 for both (§4).
var (
	ErrUnauthorized = errors.New("auth: missing or unknown token")
	ErrRevoked      = errors.New("auth: token revoked")
)

// GenerateToken mints a device token: gkv1_<43 url-safe chars>. It is shown
// to the installer exactly once; only its sha256 is ever persisted (§4).
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gkv1_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the sha256 hex of a bearer token — the storage form.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type ctxKey int

const deviceKey ctxKey = 1

// WithDevice carries the authenticated device on the request context.
func WithDevice(ctx context.Context, d *store.Device) context.Context {
	return context.WithValue(ctx, deviceKey, d)
}

// DeviceFrom returns the device put there by WithDevice.
func DeviceFrom(ctx context.Context) (*store.Device, bool) {
	d, ok := ctx.Value(deviceKey).(*store.Device)
	return d, ok
}

// Authenticate resolves the request's bearer token to an active device.
// Unknown and revoked tokens both fail; handlers answer 401 identically so
// the wire never leaks which tokens once existed (§4).
func Authenticate(ctx context.Context, st *store.Store, r *http.Request) (*store.Device, error) {
	h := r.Header.Get(proto.HeaderAuth)
	tok, ok := strings.CutPrefix(h, proto.BearerPrefix)
	if !ok {
		return nil, ErrUnauthorized
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, ErrUnauthorized
	}
	dev, err := st.DeviceByTokenHash(ctx, HashToken(tok))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if dev.Status != "active" {
		return nil, ErrRevoked
	}
	return dev, nil
}

// ClientIP extracts the caller identity from the peer address. v3.1 has no
// reverse proxy in front (TLS terminates in this binary, PLAN D4), so
// X-Forwarded-For would be client-supplied spoofing — it is deliberately
// ignored. Revisit only if a trusted proxy is ever added.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Limiter holds the two token-bucket floors from §6.1. Entries are swept so
// spoofed source IPs cannot grow the maps without bound.
type Limiter struct {
	mu sync.Mutex

	ipRate   rate.Limit
	ipBurst  int
	devRate  rate.Limit
	devBurst int

	ips   map[string]*bucket
	devs  map[int64]*bucket
	sweep time.Time
	now   func() time.Time
}

type bucket struct {
	l    *rate.Limiter
	seen time.Time
}

// NewLimiter builds limiters. The per-IP floor is deliberately generous
// (venues NAT many devices behind one address); it exists to stop floods,
// not to meter honest devices. The per-device bucket meters the tick rate.
func NewLimiter(ipRate float64, ipBurst int, devRate float64, devBurst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		ipRate: rate.Limit(ipRate), ipBurst: ipBurst,
		devRate: rate.Limit(devRate), devBurst: devBurst,
		ips:  make(map[string]*bucket),
		devs: make(map[int64]*bucket),
		now:  now,
	}
}

// DefaultLimiter: IP 2/s burst 30; device one per 5 s burst 4 (honest ticks
// are ≥30 s apart; the burst absorbs post-deploy retries).
func DefaultLimiter(now func() time.Time) *Limiter {
	return NewLimiter(2, 30, 0.2, 4, now)
}

// AllowIP reports whether this source may proceed.
func (l *Limiter) AllowIP(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked()
	b, ok := l.ips[ip]
	if !ok {
		b = &bucket{l: rate.NewLimiter(l.ipRate, l.ipBurst), seen: l.now()}
		l.ips[ip] = b
	}
	b.seen = l.now()
	return b.l.AllowN(b.seen, 1) // explicit clock: refills follow now(), not time.Now
}

// AllowDevice reports whether this device may proceed.
func (l *Limiter) AllowDevice(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.devs[id]
	if !ok {
		b = &bucket{l: rate.NewLimiter(l.devRate, l.devBurst), seen: l.now()}
		l.devs[id] = b
	}
	b.seen = l.now()
	return b.l.AllowN(b.seen, 1)
}

// sweepLocked drops entries idle for 10 minutes, at most once a minute and
// only once the maps are big enough to matter.
func (l *Limiter) sweepLocked() {
	now := l.now()
	if len(l.ips) < 1024 && len(l.devs) < 1024 {
		return
	}
	if now.Sub(l.sweep) < time.Minute {
		return
	}
	l.sweep = now
	cutoff := now.Add(-10 * time.Minute)
	for k, b := range l.ips {
		if b.seen.Before(cutoff) {
			delete(l.ips, k)
		}
	}
	for k, b := range l.devs {
		if b.seen.Before(cutoff) {
			delete(l.devs, k)
		}
	}
}
