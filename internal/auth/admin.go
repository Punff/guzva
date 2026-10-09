// Package auth, admin half (PLAN §6.5): the single bcrypt credential from
// .env and the HMAC-signed session cookie that replaces a sessions table
// (§6.2: "admin on a signed cookie, no CSRF tokens"). SameSite=Strict +
// HttpOnly + a single admin is the accepted threat model; the limitation is
// documented: rotating the secret is the only server-side logout.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// SessionCookieName is the admin session cookie.
const SessionCookieName = "guzva_admin"

// SessionTTL bounds a session without a fresh login.
const SessionTTL = 12 * time.Hour

// sessionVersion prefixes cookie values so a future format change can
// invalidate old cookies at a glance.
const sessionVersion = "g1"

// CheckAdminPassword compares the bcrypt hash from .env with a candidate.
// An unset hash always fails — a console with no credential must not be
// loggable-in (fail closed).
func CheckAdminPassword(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// HashPassword mints the bcrypt hash stored in GUZVA_ADMIN_HASH
// (`guzva hashpw`).
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// RandSecret returns n cryptographically random bytes (cookie secret).
func RandSecret(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not recoverable — refuse to continue with
		// a predictable secret.
		panic("auth: entropy failure: " + err.Error())
	}
	return b
}

// Session signs and verifies the admin cookie: g1.<unix-expiry>.<hmac>.
// The expiry is inside the signed payload, so a cookie cannot be extended
// without the secret.
type Session struct {
	secret []byte
	now    func() time.Time
}

// NewSession builds a signer. secret comes from GUZVA_COOKIE_SECRET (or is
// generated per boot — sessions then reset on restart, logged as a warning).
func NewSession(secret []byte, now func() time.Time) *Session {
	if now == nil {
		now = time.Now
	}
	return &Session{secret: secret, now: now}
}

func (s *Session) sign(exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(sessionVersion + "." + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Value builds the cookie value for an expiry.
func (s *Session) Value(expires time.Time) string {
	exp := expires.Unix()
	return sessionVersion + "." + strconv.FormatInt(exp, 10) + "." + s.sign(exp)
}

// Verify reports whether the request carries a valid, unexpired session.
func (s *Session) Verify(r *http.Request) bool {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] != sessionVersion {
		return false
	}
	if !hmac.Equal([]byte(parts[2]), []byte(s.sign(parseExp(parts[1])))) {
		return false
	}
	exp := parseExp(parts[1])
	return exp != 0 && s.now().Unix() < exp
}

func parseExp(s string) int64 {
	exp, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return exp
}

// Issue sets the session cookie on the response.
func (s *Session) Issue(w http.ResponseWriter, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    s.Value(s.now().Add(ttl)),
		Path:     "/admin",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secure, // true only when the listener speaks TLS (bench is http)
		SameSite: http.SameSiteStrictMode,
	})
}

// Clear expires the cookie (client-side logout; §6.5 documents that
// rotating the secret is the only true server-side revoke).
func (s *Session) Clear(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// --- owner sessions --------------------------------------------------------
//
// The owner panel (one login per venue) rides a second, scoped cookie:
// o1.<venueID>.<exp>.<mac> on Path /owner. Same HMAC construction as the
// admin session, different name/version/path so the two credentials can
// never be confused: an owner cookie verifies nothing under /admin and
// vice versa. The scope comes from the signed payload — handlers must take
// the venue id from Verify, never from a form field or URL.
//
// An owner session ends client-side (Clear) or when the super-admin resets
// the venue's owner password — the old password then fails at next login,
// and there is nothing server-side to revoke because nothing server-side is
// stored (§6.2: no sessions table, same discipline as admin).

// OwnerSessionCookieName is the owner session cookie.
const OwnerSessionCookieName = "guzva_owner"

// ownerSessionVersion prefixes owner cookie values.
const ownerSessionVersion = "o1"

// OwnerSession signs and verifies the per-venue owner cookie.
type OwnerSession struct {
	secret []byte
	now    func() time.Time
}

// NewOwnerSession builds the owner-cookie signer (same secret source as
// admin — one secret, two disjoint cookie namespaces).
func NewOwnerSession(secret []byte, now func() time.Time) *OwnerSession {
	if now == nil {
		now = time.Now
	}
	return &OwnerSession{secret: secret, now: now}
}

func (s *OwnerSession) sign(venueID, exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(ownerSessionVersion + "." +
		strconv.FormatInt(venueID, 10) + "." + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// IssueOwner sets the owner cookie for a venue.
func (s *OwnerSession) IssueOwner(w http.ResponseWriter, venueID int64, ttl time.Duration, secure bool) {
	exp := s.now().Add(ttl).Unix()
	val := ownerSessionVersion + "." + strconv.FormatInt(venueID, 10) + "." +
		strconv.FormatInt(exp, 10) + "." + s.sign(venueID, exp)
	http.SetCookie(w, &http.Cookie{
		Name:     OwnerSessionCookieName,
		Value:    val,
		Path:     "/owner",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// VerifyOwner returns the venue id the request is logged in as. ok=false on
// missing, malformed, tampered or expired cookies — and on venueID <= 0, so
// a forged "venue 0" scope can never address "every venue".
func (s *OwnerSession) VerifyOwner(r *http.Request) (venueID int64, ok bool) {
	c, err := r.Cookie(OwnerSessionCookieName)
	if err != nil || c.Value == "" {
		return 0, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 4 || parts[0] != ownerSessionVersion {
		return 0, false
	}
	venueID, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil || venueID <= 0 {
		return 0, false
	}
	exp := parseExp(parts[2])
	if exp == 0 || s.now().Unix() >= exp {
		return 0, false
	}
	if !hmac.Equal([]byte(parts[3]), []byte(s.sign(venueID, exp))) {
		return 0, false
	}
	return venueID, true
}

// ClearOwner expires the owner cookie.
func (s *OwnerSession) ClearOwner(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     OwnerSessionCookieName,
		Value:    "",
		Path:     "/owner",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}
