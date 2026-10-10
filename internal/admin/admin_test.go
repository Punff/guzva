package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"guzva/internal/auth"
	"guzva/internal/store"
)

type fixture struct {
	s   *Server
	st  *store.Store
	mux *http.ServeMux
}

func newFixture(t *testing.T, passHash string) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(st, passHash, []byte("test-secret"), false, log)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	return &fixture{s: s, st: st, mux: mux}
}

func (f *fixture) do(t *testing.T, method, path string, form url.Values, cookie *http.Cookie, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

// login performs a successful login and returns the session cookie.
func (f *fixture) login(t *testing.T) *http.Cookie {
	t.Helper()
	w := f.do(t, http.MethodPost, "/admin/login", url.Values{"password": {"sekret"}}, nil, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d (body %q)", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("no session cookie set")
	return nil
}

func TestLoginFlowAndFleetGuard(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))

	// Logged out: the board is never reachable.
	w := f.do(t, http.MethodGet, "/admin/", nil, nil, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("logged-out GET /admin/ = %d loc %q", w.Code, w.Header().Get("Location"))
	}
	// Same for an htmx caller: HX-Redirect + 401, never board HTML.
	w = f.do(t, http.MethodGet, "/admin/", nil, nil, true)
	if w.Code != http.StatusUnauthorized || w.Header().Get("HX-Redirect") != "/admin/login" {
		t.Fatalf("hx logged-out = %d hx-redirect %q", w.Code, w.Header().Get("HX-Redirect"))
	}

	// Wrong password: 401, message visible, no cookie.
	w = f.do(t, http.MethodPost, "/admin/login", url.Values{"password": {"nope"}}, nil, false)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Wrong password") {
		t.Fatalf("bad password = %d %q", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("cookie issued on failed login")
	}

	// Right password: 303 → /admin/, cookie present, board reachable.
	ck := f.login(t)
	w = f.do(t, http.MethodGet, "/admin/", nil, ck, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "guzva — fleet") {
		t.Fatalf("board = %d %q", w.Code, w.Body.String())
	}
	// Login page redirects away when already signed in.
	w = f.do(t, http.MethodGet, "/admin/login", nil, ck, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login-when-authed = %d", w.Code)
	}
}

func TestLoginThrottle(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	for i := 0; i < 5; i++ {
		w := f.do(t, http.MethodPost, "/admin/login", url.Values{"password": {"nope"}}, nil, false)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, w.Code)
		}
	}
	// Burst 5 spent → 429 regardless of password.
	w := f.do(t, http.MethodPost, "/admin/login", url.Values{"password": {"sekret"}}, nil, false)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt = %d, want 429", w.Code)
	}
}

func TestFailClosedWithoutHash(t *testing.T) {
	f := newFixture(t, "") // GUZVA_ADMIN_HASH unset
	w := f.do(t, http.MethodPost, "/admin/login", url.Values{"password": {"anything"}}, nil, false)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("unset hash = %d %q", w.Code, w.Body.String())
	}
}

func TestVenueDeviceLifecycle(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)

	// Create venue (htmx form → HX-Redirect reload).
	w := f.do(t, http.MethodPost, "/admin/venues", url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "tz": {"Europe/Sarajevo"},
		"style": {"poster"}, "hours": {"Mo-Fr 09:00-17:00"},
	}, ck, true)
	if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("create venue = %d hx-redirect %q body %q", w.Code, w.Header().Get("HX-Redirect"), w.Body.String())
	}
	v, err := f.st.VenueBySlug(t.Context(), "jadro")
	if err != nil {
		t.Fatalf("venue after create: %v", err)
	}
	h, err := f.st.Hours(t.Context(), v.ID)
	if err != nil || !h.Configured() {
		t.Fatalf("hours after create: %+v err %v", h, err)
	}

	// Bad slug → visible flash, no venue.
	w = f.do(t, http.MethodPost, "/admin/venues", url.Values{
		"slug": {"Jadro!"}, "name": {"X"}, "style": {"poster"},
	}, ck, true)
	if w.Header().Get("HX-Redirect") != "" || !strings.Contains(w.Body.String(), "Slug must") {
		t.Fatalf("bad slug: redirect %q body %q", w.Header().Get("HX-Redirect"), w.Body.String())
	}

	// Mint a device: token appears once in the fragment, store keeps sha256.
	w = f.do(t, http.MethodPost, "/admin/devices", url.Values{
		"venue_id": {"1"}, "serial": {"cam1"},
	}, ck, true)
	body := w.Body.String()
	if !strings.Contains(body, "gkv1_") || !strings.Contains(body, "SHOWN ONLY ONCE") {
		t.Fatalf("token fragment = %q", body)
	}
	token := extractToken(t, body)
	if _, err := f.st.DeviceByTokenHash(t.Context(), auth.HashToken(token)); err != nil {
		t.Fatalf("device by token hash: %v", err)
	}
	// The raw token itself must not be what is stored.
	if _, err := f.st.DeviceByTokenHash(t.Context(), token); err == nil {
		t.Fatal("raw token found in store — only sha256 may be persisted")
	}

	// Unknown venue → flash.
	w = f.do(t, http.MethodPost, "/admin/devices", url.Values{"venue_id": {"999"}}, ck, true)
	if !strings.Contains(w.Body.String(), "Unknown venue") {
		t.Fatalf("unknown venue = %q", w.Body.String())
	}

	// Revoke the device.
	devices, err := f.st.ListDevices(t.Context())
	if err != nil || len(devices) != 1 {
		t.Fatalf("list devices: %v n=%d", err, len(devices))
	}
	w = f.do(t, http.MethodPost, "/admin/devices/1/revoke", nil, ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("revoke: no redirect, body %q", w.Body.String())
	}
	dev, err := f.st.DeviceByTokenHash(t.Context(), auth.HashToken(token))
	if err != nil || dev.Status != "revoked" {
		t.Fatalf("status after revoke = %+v err %v", dev, err)
	}
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatalf("hashpw: %v", err)
	}
	return h
}

// extractToken pulls the gkv1_ token out of the once-only fragment.
func extractToken(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "gkv1_")
	if i < 0 {
		t.Fatal("no token in body")
	}
	end := i
	for end < len(body) && (body[end] == '_' ||
		(body[end] >= 'a' && body[end] <= 'z') ||
		(body[end] >= 'A' && body[end] <= 'Z') ||
		(body[end] >= '0' && body[end] <= '9') ||
		body[end] == '-' || body[end] == '+') {
		end++
	}
	return body[i:end]
}

// Owner panel: scoped login, own-venue edits, and the walls around them.
func TestOwnerPanel(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	ctx := t.Context()

	// Two venues; only jadro gets an owner password.
	for _, vs := range []struct{ slug, name string }{{"jadro", "Jadro"}, {"other", "Other"}} {
		w := f.do(t, http.MethodPost, "/admin/venues", url.Values{
			"slug": {vs.slug}, "name": {vs.name}, "tz": {"Europe/Sarajevo"},
			"style": {"poster"}, "hours": {"Mo-Fr 09:00-17:00"},
		}, ck, true)
		if w.Header().Get("HX-Redirect") == "" {
			t.Fatalf("create %s: %q", vs.slug, w.Body.String())
		}
	}
	jadro, _ := f.st.VenueBySlug(ctx, "jadro")
	if err := f.st.SetOwnerHash(ctx, jadro.ID, mustHash(t, "owner-sekret-1")); err != nil {
		t.Fatal(err)
	}

	ownerLogin := func(slug, pw string) *httptest.ResponseRecorder {
		return f.do(t, http.MethodPost, "/owner/login",
			url.Values{"slug": {slug}, "password": {pw}}, nil, false)
	}
	ownerCookie := func(w *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, c := range w.Result().Cookies() {
			if c.Name == auth.OwnerSessionCookieName {
				return c
			}
		}
		t.Fatal("no owner cookie")
		return nil
	}

	// Unknown venue, unset password, wrong password: one generic message.
	for _, tc := range [][2]string{{"nope", "x"}, {"other", "x"}, {"jadro", "wrong"}} {
		w := ownerLogin(tc[0], tc[1])
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Pogrešan objekt ili lozinka") {
			t.Fatalf("login %v = %d %q", tc, w.Code, w.Body.String())
		}
	}

	// Right password: 303 → /owner/, dash shows own venue only.
	w := ownerLogin("jadro", "owner-sekret-1")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("owner login = %d %q", w.Code, w.Body.String())
	}
	ock := ownerCookie(w)
	w = f.do(t, http.MethodGet, "/owner/", nil, ock, false)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Jadro") {
		t.Fatalf("dash = %d", w.Code)
	}
	if strings.Contains(body, "Other") {
		t.Fatalf("owner sees another venue: %q", body[:200])
	}
	if strings.Contains(body, "Revoke") || strings.Contains(body, "cfg_") {
		t.Fatal("owner dash leaks super-admin controls")
	}

	// Owner edits own venue details (incl. hours).
	w = f.do(t, http.MethodPost, "/owner/venue", url.Values{
		"name": {"Jadro Grad"}, "style": {"mono"}, "tz": {"Europe/Zagreb"},
		"hours": {"Sa-Su 10:00-14:00"},
	}, ock, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("owner update = %q", w.Body.String())
	}
	v, _ := f.st.VenueByID(ctx, jadro.ID)
	if v.Name != "Jadro Grad" || v.Style != "mono" || v.TZ != "Europe/Zagreb" {
		t.Fatalf("venue = %+v", v)
	}
	if h, _ := f.st.Hours(ctx, jadro.ID); h.Spec() != "Sa-Su 10:00-14:00" {
		t.Fatalf("hours spec = %q", h.Spec())
	}

	// Owner validation: same gate as super-admin (empty name, bad hours).
	w = f.do(t, http.MethodPost, "/owner/venue", url.Values{
		"name": {""}, "style": {"poster"},
	}, ock, true)
	if !strings.Contains(w.Body.String(), "Name is required") {
		t.Fatalf("empty name = %q", w.Body.String())
	}
	w = f.do(t, http.MethodPost, "/owner/venue", url.Values{
		"name": {"Jadro"}, "style": {"poster"}, "hours": {"nonsense"},
	}, ock, true)
	if !strings.Contains(w.Body.String(), "Bad hours") {
		t.Fatalf("bad hours = %q", w.Body.String())
	}

	// Walls: owner cookie grants nothing under /admin; admin cookie grants
	// nothing under /owner; logged-out owner routes bounce to owner login.
	w = f.do(t, http.MethodGet, "/admin/", nil, ock, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
		t.Fatalf("owner→admin = %d loc %q", w.Code, w.Header().Get("Location"))
	}
	w = f.do(t, http.MethodGet, "/owner/", nil, ck, false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/owner/login" {
		t.Fatalf("admin→owner = %d loc %q", w.Code, w.Header().Get("Location"))
	}
	w = f.do(t, http.MethodGet, "/owner/", nil, nil, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("logged-out owner dash = %d", w.Code)
	}

	// Logout clears the cookie.
	w = f.do(t, http.MethodPost, "/owner/logout", nil, ock, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("owner logout = %d", w.Code)
	}
}

// Super-admin edits: venue details, owner password, device config.
func TestSuperAdminEdits(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	ctx := t.Context()

	w := f.do(t, http.MethodPost, "/admin/venues", url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "tz": {"Europe/Sarajevo"},
		"style": {"poster"}, "hours": {""},
	}, ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("create: %q", w.Body.String())
	}
	v, _ := f.st.VenueBySlug(ctx, "jadro")

	// Venue page renders with prefilled hours (empty = no badge).
	w = f.do(t, http.MethodGet, "/admin/venues/1", nil, ck, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Jadro") {
		t.Fatalf("venue page = %d", w.Code)
	}
	// Unknown venue id → 404, not 500.
	if w := f.do(t, http.MethodGet, "/admin/venues/999", nil, ck, false); w.Code != http.StatusNotFound {
		t.Fatalf("missing venue = %d", w.Code)
	}

	// Update details.
	w = f.do(t, http.MethodPost, "/admin/venues/1", url.Values{
		"name": {"Jadro Grad"}, "style": {"mono"}, "tz": {"Europe/Zagreb"},
		"hours": {"Mo-Fr 09:00-17:00"},
	}, ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("update venue = %q", w.Body.String())
	}
	v, _ = f.st.VenueByID(ctx, v.ID)
	if v.Name != "Jadro Grad" || v.Style != "mono" {
		t.Fatalf("venue = %+v", v)
	}

	// Owner password: too short rejected, good one stored as hash only.
	w = f.do(t, http.MethodPost, "/admin/venues/1/ownerpw",
		url.Values{"password": {"short"}}, ck, true)
	if !strings.Contains(w.Body.String(), "at least 10") {
		t.Fatalf("short pw = %q", w.Body.String())
	}
	w = f.do(t, http.MethodPost, "/admin/venues/1/ownerpw",
		url.Values{"password": {"owner-sekret-1"}}, ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("set ownerpw = %q", w.Body.String())
	}
	v, _ = f.st.VenueByID(ctx, v.ID)
	if v.OwnerHash == "" || v.OwnerHash == "owner-sekret-1" {
		t.Fatal("owner password stored raw or not at all")
	}
	if !auth.CheckAdminPassword(v.OwnerHash, "owner-sekret-1") {
		t.Fatal("stored hash does not verify")
	}

	// Device page + config edit via the web (same gate as `guzva cfg`).
	w = f.do(t, http.MethodPost, "/admin/devices", url.Values{
		"venue_id": {"1"}, "serial": {"cam1"},
	}, ck, true)
	if !strings.Contains(w.Body.String(), "gkv1_") {
		t.Fatalf("mint: %q", w.Body.String())
	}
	w = f.do(t, http.MethodGet, "/admin/devices/1", nil, ck, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cfg_interval_s") {
		t.Fatalf("device page = %d", w.Code)
	}
	w = f.do(t, http.MethodPost, "/admin/devices/1/cfg", url.Values{
		"cfg_interval_s": {"45"}, "cfg_quality": {"12"},
		"cfg_framesize": {"5"}, "cfg_led_pwm": {"80"},
	}, ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("set cfg = %q", w.Body.String())
	}
	d, _ := f.st.DeviceBySerial(ctx, "cam1")
	if d.CfgVer != 2 || d.CfgIntervalS != 45 || d.CfgQuality != 12 ||
		d.CfgExtra["framesize"] != 5 || d.CfgExtra["led_pwm"] != 80 {
		t.Fatalf("cfg = %+v extra=%v", d, d.CfgExtra)
	}

	// Out-of-range and non-integer values are rejected, nothing saved.
	w = f.do(t, http.MethodPost, "/admin/devices/1/cfg", url.Values{
		"cfg_interval_s": {"45"}, "cfg_led_pwm": {"999"},
	}, ck, true)
	if !strings.Contains(w.Body.String(), "out of range") {
		t.Fatalf("range = %q", w.Body.String())
	}
	w = f.do(t, http.MethodPost, "/admin/devices/1/cfg", url.Values{
		"cfg_quality": {"high"},
	}, ck, true)
	if !strings.Contains(w.Body.String(), "must be an integer") {
		t.Fatalf("integer = %q", w.Body.String())
	}
	d, _ = f.st.DeviceBySerial(ctx, "cam1")
	if d.CfgVer != 2 {
		t.Fatalf("rejected edit bumped ver: %+v", d)
	}
	if w := f.do(t, http.MethodGet, "/admin/devices/999", nil, ck, false); w.Code != http.StatusNotFound {
		t.Fatalf("missing device = %d", w.Code)
	}
}
