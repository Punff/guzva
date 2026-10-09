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
