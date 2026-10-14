package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"guzva/internal/auth"
	"guzva/internal/i18n"
	"guzva/internal/store"
)

func TestBuildOwnerStats(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Sarajevo")
	now := time.Date(2026, 10, 14, 18, 30, 0, 0, loc) // a Wednesday
	at := func(daysAgo, hour int) time.Time {
		return time.Date(2026, 10, 14-daysAgo, hour, 0, 0, 0, loc).UTC()
	}
	hours := []store.VisitHour{
		{Hour: at(0, 17), Views: 5, Polls: 4, QR: 2},
		{Hour: at(3, 17), Views: 10},
		{Hour: at(8, 9), Views: 4}, // last week
	}
	days := []store.VisitDay{{Day: "2026-10-14", Visitors: 3}, {Day: "2026-10-06", Visitors: 2}}
	var frames []time.Time
	for t0 := now.Add(-12 * time.Hour); t0.Before(now); t0 = t0.Add(10 * time.Minute) {
		frames = append(frames, t0)
	}
	cat := i18n.For(i18n.HR)
	st := buildOwnerStats(now.UTC(), loc, hours, days, frames, now.Add(-48*time.Hour), 2, cat)

	views := st.KPIs[0]
	if views.Value != 15 || !views.Up || views.Trend != "+275 % od prošlog tjedna" {
		t.Errorf("views kpi = %+v", views)
	}
	if v := st.KPIs[1]; v.Value != 3 || v.Trend != "+50 % od prošlog tjedna" {
		t.Errorf("visitors kpi = %+v", v)
	}
	if m := st.KPIs[2]; m.Value != 2 || m.Trend != "Novo ovaj tjedan" {
		t.Errorf("minutes kpi = %+v (4 polls × 30 s)", m)
	}
	if q := st.KPIs[3]; q.Value != 2 {
		t.Errorf("qr kpi = %+v", q)
	}
	if len(st.Days) != 14 || !st.Days[13].Hi || st.Days[13].H == 0 || st.Days[0].Hi {
		t.Errorf("day bars: n=%d last=%+v", len(st.Days), st.Days[13])
	}
	if !st.Hours[17].Hi || st.Peak != "Najviše gledaju između 17 i 18 h." {
		t.Errorf("peak = %q, hour17 = %+v", st.Peak, st.Hours[17])
	}
	if st.Uptime != 50 {
		t.Errorf("uptime = %d, want 50 (12 h of 24)", st.Uptime)
	}
	joined := strings.Join(st.Insights, "|")
	if !strings.Contains(joined, "2 gostiju je ovaj tjedan skeniralo QR kod.") ||
		!strings.Contains(joined, "Najviše pregleda ovaj tjedan: Nedjelja (10).") {
		t.Errorf("insights = %q", joined)
	}
	if st.Live != 2 || !st.HasData {
		t.Errorf("live/hasData = %d/%v", st.Live, st.HasData)
	}

	// A camera installed an hour ago with a frame every 10 min is 100 %
	// online, not 4 %; one installed 5 min ago is too new to rate.
	recent := []time.Time{now.Add(-55 * time.Minute)}
	for i := 1; i < 6; i++ {
		recent = append(recent, recent[0].Add(time.Duration(i)*10*time.Minute))
	}
	if u := buildOwnerStats(now.UTC(), loc, nil, nil, recent, now.Add(-time.Hour), 0, cat).Uptime; u != 100 {
		t.Errorf("new camera uptime = %d, want 100", u)
	}
	if u := buildOwnerStats(now.UTC(), loc, nil, nil, nil, now.Add(-5*time.Minute), 0, cat).Uptime; u != -1 {
		t.Errorf("brand-new camera uptime = %d, want -1 (hidden)", u)
	}

	empty := buildOwnerStats(now.UTC(), loc, nil, nil, nil, now.Add(-48*time.Hour), 0, cat)
	if empty.HasData || empty.KPIs[0].Trend != "" || !strings.Contains(empty.Insights[0], "Još nema posjeta") {
		t.Errorf("empty = %+v", empty)
	}
}

// The dashboard renders stats, the share link + QR, and the poster route is
// owner-only.
func TestOwnerDashShareAndPoster(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	ck := f.login(t)
	f.do(t, http.MethodPost, "/admin/venues", withHours(url.Values{
		"slug": {"jadro"}, "name": {"Jadro"}, "style": {"poster"},
	}, nil), ck, true)
	v, _ := f.st.VenueBySlug(t.Context(), "jadro")
	if err := f.st.SetOwnerHash(t.Context(), v.ID, mustHash(t, "owner-sekret-1")); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddVisitHour(t.Context(), v.ID, time.Now().Truncate(time.Hour), 7, 2, 1); err != nil {
		t.Fatal(err)
	}
	if w := f.do(t, http.MethodGet, "/owner/poster", nil, nil, false); w.Code != http.StatusSeeOther {
		t.Fatalf("poster without login = %d", w.Code)
	}
	w := f.do(t, http.MethodPost, "/owner/login", url.Values{"slug": {"jadro"}, "password": {"owner-sekret-1"}}, nil, false)
	var ock *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.OwnerSessionCookieName {
			ock = c
		}
	}
	req := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "guzva.example"
		r.AddCookie(ock)
		rec := httptest.NewRecorder()
		f.mux.ServeHTTP(rec, r)
		return rec
	}
	dash := req("/owner/").Body.String()
	for _, want := range []string{"Pregledi", ">7<", "http://guzva.example/jadro/", "<svg", "Ispiši plakat", "Odjava"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dash lacks %q", want)
		}
	}
	for _, bad := range []string{"Sign out", "PLAN D1"} {
		if strings.Contains(dash, bad) {
			t.Errorf("dash contains %q", bad)
		}
	}
	// No raw catalog key reached the page (owner.js is a file name).
	for _, m := range regexp.MustCompile(`(owner|poster)\.[a-z_.]+`).FindAllString(dash, -1) {
		if m != "owner.js" {
			t.Errorf("raw catalog key on the dashboard: %q", m)
		}
	}
	poster := req("/owner/poster").Body.String()
	if !strings.Contains(poster, "Skeniraj i pogledaj uživo") || !strings.Contains(poster, "<svg") || !strings.Contains(poster, "Jadro") {
		t.Errorf("poster = %.300s", poster)
	}
}
