package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Owner cookies are scoped, tamper-evident and disjoint from admin cookies.
func TestOwnerSession(t *testing.T) {
	s := NewOwnerSession([]byte("test-secret-0123456789abcdef"), nil)

	// Issue → verify round-trips the venue scope.
	w := httptest.NewRecorder()
	s.IssueOwner(w, 7, time.Hour, false)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != OwnerSessionCookieName {
		t.Fatalf("cookies = %v", cookies)
	}
	if cookies[0].Path != "/owner" || !cookies[0].HttpOnly {
		t.Errorf("cookie flags = %+v", cookies[0])
	}
	r := httptest.NewRequest("GET", "/owner/", nil)
	r.AddCookie(cookies[0])
	if vid, ok := s.VerifyOwner(r); !ok || vid != 7 {
		t.Fatalf("verify = %d,%v want 7,true", vid, ok)
	}

	// Tampered venue id fails (MAC covers the scope).
	bad := *cookies[0]
	bad.Value = "o1.8." + strings.Split(cookies[0].Value, ".")[2] + "." +
		strings.Split(cookies[0].Value, ".")[3]
	r2 := httptest.NewRequest("GET", "/owner/", nil)
	r2.AddCookie(&bad)
	if _, ok := s.VerifyOwner(r2); ok {
		t.Fatal("tampered scope verified")
	}

	// The admin cookie never verifies as owner and vice versa (disjoint
	// namespaces — one credential cannot cross panels).
	adm := NewSession([]byte("test-secret-0123456789abcdef"), nil)
	aw := httptest.NewRecorder()
	adm.Issue(aw, time.Hour, false)
	r3 := httptest.NewRequest("GET", "/owner/", nil)
	for _, c := range aw.Result().Cookies() {
		r3.AddCookie(c)
	}
	if _, ok := s.VerifyOwner(r3); ok {
		t.Fatal("admin cookie verified as owner")
	}
}
