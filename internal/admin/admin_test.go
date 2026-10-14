package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"guzva/internal/auth"
	"guzva/internal/render"
	"guzva/internal/store"
)

func itoa(n int) string { return string(rune('0' + n)) }

// hoursWeek posts a full valid week in the editor's structured fields:
// weekdays 09:00–17:00, weekend closed. Overrides replace individual keys.
func hoursWeek(over map[string]string) url.Values {
	v := url.Values{}
	for d := 0; d < 5; d++ {
		v.Set("h"+itoa(d)+"_open", "09:00")
		v.Set("h"+itoa(d)+"_close", "17:00")
	}
	v.Set("h5_closed", "1")
	v.Set("h6_closed", "1")
	for k, val := range over {
		v.Set(k, val)
	}
	return v
}

// withHours merges base form values with a week of hours. Overrides
// replace keys; drop removes default keys the override supersedes (e.g. the
// default h5_closed when Saturday gets times) — Set alone cannot unsend a key.
func withHours(base url.Values, over map[string]string, drop ...string) url.Values {
	for k, vals := range hoursWeek(over) {
		for _, val := range vals {
			base.Set(k, val)
		}
	}
	for _, k := range drop {
		base.Del(k)
	}
	return base
}

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

// The owner panel shares the throttle, and its 429 page must render the
// catalog sentence — the throttle key has no template fetch, so
// TestFetchedKeysExist cannot see it. Asserted here end-to-end instead: a
// removed or renamed key would print itself raw to a locked-out owner.
func TestOwnerLoginThrottleCopy(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	for i := 0; i < 5; i++ {
		f.do(t, http.MethodPost, "/owner/login",
			url.Values{"slug": {"jadro"}, "password": {"nope"}}, nil, false)
	}
	w := f.do(t, http.MethodPost, "/owner/login",
		url.Values{"slug": {"jadro"}, "password": {"nope"}}, nil, false)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt = %d, want 429", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Previše pokušaja") {
		t.Fatalf("throttled owner login missing catalog sentence: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "owner.login.err") {
		t.Fatalf("throttled owner login rendered a raw i18n key: %q", w.Body.String())
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
	w := f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "tz": {"Europe/Sarajevo"},
		"style": {"poster"},
	}, nil), ck, true)
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
		w := f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
			"slug": {vs.slug}, "name": {vs.name}, "tz": {"Europe/Sarajevo"},
			"style": {"poster"},
		}, nil), ck, true)
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

	// Unknown venue, unset password, wrong password: one generic message,
	// from the catalog (owner.login.err.badcreds). Asserted two ways: the
	// sentence is the catalog's, and no raw key ever reaches the page.
	for _, tc := range [][2]string{{"nope", "x"}, {"other", "x"}, {"jadro", "wrong"}} {
		w := ownerLogin(tc[0], tc[1])
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Prijava nije uspjela") {
			t.Fatalf("login %v = %d %q", tc, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "owner.login.err") {
			t.Fatalf("login %v rendered a raw i18n key: %q", tc, w.Body.String())
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
	weekend := map[string]string{}
	for d := 0; d < 5; d++ {
		weekend["h"+itoa(d)+"_closed"] = "1"
	}
	weekend["h5_open"], weekend["h5_close"] = "10:00", "14:00"
	weekend["h6_open"], weekend["h6_close"] = "10:00", "14:00"
	weekendDrop := []string{"h5_closed", "h6_closed"}
	for d := 0; d < 5; d++ {
		weekendDrop = append(weekendDrop, "h"+itoa(d)+"_open", "h"+itoa(d)+"_close")
	}
	w = f.do(t, http.MethodPost, "/owner/venue", withHours(url.Values{
		"name": {"Jadro Grad"}, "style": {"mono"}, "tz": {"Europe/Zagreb"},
	}, weekend, weekendDrop...), ock, true)
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
	w = f.do(t, http.MethodPost, "/owner/venue", withHours(url.Values{
		"name": {"Jadro"}, "style": {"poster"},
	}, map[string]string{"h0_open": "17:00", "h0_close": "09:00"}), ock, true)
	if !strings.Contains(w.Body.String(), "Ponedjeljak") {
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

	// All-closed is the explicit form of the old empty-hours create: no
	// ranges configured, so no open/closed badge at all.
	closed := map[string]string{}
	closedDrop := []string{}
	for d := 0; d < 7; d++ {
		closed["h"+itoa(d)+"_closed"] = "1"
	}
	for d := 0; d < 5; d++ {
		closedDrop = append(closedDrop, "h"+itoa(d)+"_open", "h"+itoa(d)+"_close")
	}
	w := f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "tz": {"Europe/Sarajevo"},
		"style": {"poster"},
	}, closed, closedDrop...), ck, true)
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
	w = f.do(t, http.MethodPost, "/admin/venues/1", withHours(url.Values{
		"name": {"Jadro Grad"}, "style": {"mono"}, "tz": {"Europe/Zagreb"},
	}, nil), ck, true)
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

	// Device page: read-only health (the camera profile is frozen in
	// firmware, print v4) — no config form, no config route.
	w = f.do(t, http.MethodPost, "/admin/devices", url.Values{
		"venue_id": {"1"}, "serial": {"cam1"},
	}, ck, true)
	if !strings.Contains(w.Body.String(), "gkv1_") {
		t.Fatalf("mint: %q", w.Body.String())
	}
	w = f.do(t, http.MethodGet, "/admin/devices/1", nil, ck, false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cam1") || strings.Contains(w.Body.String(), "cfg_") {
		t.Fatalf("device page = %d", w.Code)
	}
	if w := f.do(t, http.MethodPost, "/admin/devices/1/cfg", url.Values{}, ck, true); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("config route still answers: %d", w.Code)
	}
	if w := f.do(t, http.MethodGet, "/admin/devices/999", nil, ck, false); w.Code != http.StatusNotFound {
		t.Fatalf("missing device = %d", w.Code)
	}
}

// A presentation bug a browser found: a never-heard board rendered as a
// perfect "0 dBm" signal.
func TestNeverSeenDeviceHasNoSignalReading(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "style": {"poster"},
	}, nil), ck, true)
	f.do(t, http.MethodPost, "/admin/devices", url.Values{
		"venue_id": {"1"}, "serial": {"never-seen"},
	}, ck, true)

	w := f.do(t, http.MethodGet, "/admin/", nil, ck, false)
	body := w.Body.String()
	if strings.Contains(body, "0 dBm") {
		t.Error("fleet board printed 0 dBm for a board that never checked in")
	}
	if !strings.Contains(body, "never-seen") || !strings.Contains(body, "—") {
		t.Errorf("fleet board missing the never-seen row:\n%s", body)
	}
}

// A duplicate class attribute is silently dropped by the HTML parser, so the
// first one wins and the second is invisible — the symptom is an element that
// just does not move, with nothing in the logs. None may exist.
func TestNoDuplicateClassAttributes(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "style": {"poster"},
	}, nil), ck, true)
	f.do(t, http.MethodPost, "/admin/devices", url.Values{
		"venue_id": {"1"}, "serial": {"cam1"},
	}, ck, true)

	seen := regexp.MustCompile(`class="[^"]*"\s[^>]*class="`)
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/admin/", f.do(t, http.MethodGet, "/admin/", nil, ck, false).Body.String()},
		{"/admin/venues/1", f.do(t, http.MethodGet, "/admin/venues/1", nil, ck, false).Body.String()},
		{"/admin/devices/1", f.do(t, http.MethodGet, "/admin/devices/1", nil, ck, false).Body.String()},
	} {
		if m := seen.FindString(tc.body); m != "" {
			t.Errorf("%s has a duplicate class attribute: %q", tc.path, m[:min(len(m), 120)])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// The kit gallery is dev-only: without the flag the route 404s even for a
// logged-in admin, because a gallery of all states is a confusing surface to
// leave on a production box.
func TestKitGated(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	w := f.do(t, http.MethodGet, "/admin/_kit", nil, ck, false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("kit without flag = %d, want 404", w.Code)
	}
	f.s.Kit = true
	w = f.do(t, http.MethodGet, "/admin/_kit", nil, ck, false)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("kit with flag = %d", w.Code)
	}
	// Every component renders: chips in all four states, cards, buttons,
	// inputs, table, textures. A missing partial fails here, not on the box.
	for _, want := range []string{
		"Svježa slika", "Slika kasni", "Nema nove slike", "Čekamo prvu sliku",
		"card--raised", "card--inset", "btn--primary", "btn--danger",
		"tbl", "tx-hatch", "tx-dither", "tx-shutter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("kit page missing %q", want)
		}
	}
}

// Masks form: valid rows store, an empty row is no mask, bad geometry
// flashes in words, clearing every row removes the masks.
func TestSetMasks(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "style": {"poster"},
	}, nil), ck, true)
	set := func(v url.Values) *httptest.ResponseRecorder {
		return f.do(t, http.MethodPost, "/admin/venues/1/masks", v, ck, true)
	}
	masks := func() []render.Rect {
		v, err := f.st.VenueByID(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		return v.Masks
	}

	w := set(url.Values{"m1x0": {"10"}, "m1y0": {"20"}, "m1x1": {"300"}, "m1y1": {"240"}})
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("valid masks: no redirect, body %q", w.Body.String())
	}
	if m := masks(); len(m) != 1 || m[0] != (render.Rect{X0: 10, Y0: 20, X1: 300, Y1: 240}) {
		t.Fatalf("stored masks = %+v", m)
	}
	for _, bad := range []url.Values{
		{"m0x0": {"30"}, "m0y0": {"20"}, "m0x1": {"10"}, "m0y1": {"40"}}, // inverted
		{"m0x0": {"30"}, "m0y0": {"20"}},                                 // partial
		{"m0x0": {"0"}, "m0y0": {"0"}, "m0x1": {"321"}, "m0y1": {"40"}},  // off grid
	} {
		if w := set(bad); !strings.Contains(w.Body.String(), "mask 1") {
			t.Errorf("bad mask %v accepted: %q", bad, w.Body.String())
		}
	}
	if len(masks()) != 1 {
		t.Fatal("a rejected form changed the stored masks")
	}
	set(url.Values{})
	if m := masks(); m != nil {
		t.Fatalf("cleared masks = %+v", m)
	}
	w = f.do(t, http.MethodGet, "/admin/venues/1", nil, ck, false)
	if !strings.Contains(w.Body.String(), "Privacy masks") {
		t.Fatal("venue page missing masks section")
	}
}

// W2/H8: the consoles inherit the C1 security contract through
// secureHeaders — before W2 the public pages carried CSP/nosniff/DENY and
// the admin + owner surfaces carried none at all.
func TestConsoleSecurityHeaders(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	for _, path := range []string{"/admin/login", "/owner/login"} {
		w := f.do(t, http.MethodGet, path, nil, nil, false)
		csp := w.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Errorf("%s: no CSP", path)
			continue
		}
		for _, banned := range []string{"unsafe-inline", "unsafe-eval"} {
			if strings.Contains(csp, banned) {
				t.Errorf("%s: CSP allows %s: %q", path, banned, csp)
			}
		}
		for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options",
			"Referrer-Policy", "Cross-Origin-Resource-Policy", "Permissions-Policy"} {
			if w.Header().Get(h) == "" {
				t.Errorf("%s: missing %s", path, h)
			}
		}
	}
}

// W2/H8: /admin/static/ must not render a directory listing — the public
// handler forbids them, and the consoles must not be the exception.
func TestAdminStaticNoDirectoryListing(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	if w := f.do(t, http.MethodGet, "/admin/static/", nil, nil, false); w.Code != http.StatusNotFound {
		t.Fatalf("listing attempt = %d, want 404", w.Code)
	}
	if w := f.do(t, http.MethodGet, "/admin/static/htmx.min.js", nil, nil, false); w.Code != http.StatusOK {
		t.Fatalf("htmx.min.js = %d, want 200", w.Code)
	}
}

// W2/B2: the owner dash's one-sentence summary must come from the catalog.
// Before W2 the keys owner.fresh/late/stale did not exist and the live panel
// rendered the raw key text ("owner.fresh") where the sentence belongs.
func TestOwnerDashSummaryFromCatalog(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	ctx := t.Context()
	w := f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "tz": {"Europe/Sarajevo"}, "style": {"poster"},
	}, nil), ck, true)
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatalf("create venue: %q", w.Body.String())
	}
	jadro, _ := f.st.VenueBySlug(ctx, "jadro")
	if err := f.st.SetOwnerHash(ctx, jadro.ID, mustHash(t, "owner-sekret-1")); err != nil {
		t.Fatal(err)
	}
	// A frame two minutes old → the fresh branch of the summary (venue.Classify
	// turns Late at 3 min).
	if _, err := f.st.InsertFrame(ctx, store.Frame{
		VenueID: jadro.ID, CaptureID: "C1-1-aaaa", ReceivedAt: time.Now().Add(-2 * time.Minute),
		Preset: "poster", PresetVer: 2, Width: 160, Height: 120, Status: "ok",
	}); err != nil {
		t.Fatal(err)
	}
	ow := f.do(t, http.MethodPost, "/owner/login",
		url.Values{"slug": {"jadro"}, "password": {"owner-sekret-1"}}, nil, false)
	var ock *http.Cookie
	for _, c := range ow.Result().Cookies() {
		if c.Name == auth.OwnerSessionCookieName {
			ock = c
		}
	}
	if ock == nil {
		t.Fatalf("owner login = %d %q", ow.Code, ow.Body.String())
	}
	body := f.do(t, http.MethodGet, "/owner/", nil, ock, false).Body.String()
	for _, key := range []string{"owner.fresh", "owner.late", "owner.stale"} {
		if strings.Contains(body, key) {
			t.Fatalf("dash renders raw i18n key %q: %s", key, body[:400])
		}
	}
	if !strings.Contains(body, "Slika je svježa") {
		t.Fatalf("dash missing the fresh summary sentence: %s", body[:400])
	}
}

// W2/B4: the no-JS contract (DESIGN §8) — every console action is a plain
// form POST + redirect; htmx only enhances. Before W2 the create, mint,
// revoke and details forms carried hx-post with no method/action and did
// nothing at all without JS.
func TestFormsWorkWithoutJS(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	body := f.do(t, http.MethodGet, "/admin/", nil, ck, false).Body.String()
	if !strings.Contains(body, `method="post" action="/admin/venues"`) {
		t.Error("fleet page missing no-JS fragment for the create form")
	}
	// (The mint form only renders once a venue exists — checked below.)
	// A real no-JS POST (no HX-Request header) must create the venue.
	w := f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"nojs"}, "name": {"NoJS"}, "tz": {"Europe/Sarajevo"}, "style": {"poster"},
	}, nil), ck, false)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("no-JS create venue = %d, want 303: %q", w.Code, w.Body.String())
	}
	if _, err := f.st.VenueBySlug(t.Context(), "nojs"); err != nil {
		t.Fatalf("venue not created: %v", err)
	}
	// With a venue existing, the mint form ships too.
	body = f.do(t, http.MethodGet, "/admin/", nil, ck, false).Body.String()
	if !strings.Contains(body, `method="post" action="/admin/devices"`) {
		t.Error("fleet page missing no-JS fragment for the mint form")
	}
}
