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
	"guzva/internal/venue"
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
	var sched venue.Hours
	sched[time.Wednesday] = []venue.Range{{OpenMin: 8 * 60, CloseMin: 20 * 60}}
	st := buildOwnerStats(now.UTC(), loc, hours, days, frames, now.Add(-48*time.Hour), 2, sched, cat)

	want := []kpi{
		// Complete days only: today's 5 views / 3 visitors / 2 scans are
		// excluded; the 7 days before hold the 8-days-ago numbers.
		{Label: "Ljudi", Value: 0, Prev: 2, Down: true},
		{Label: "Pregledi", Value: 10, Prev: 4, Up: true},
		{Label: "QR skeniranja", Value: 0, Prev: 0},
	}
	for i, w := range want {
		if st.KPIs[i] != w {
			t.Errorf("kpi %d = %+v, want %+v", i, st.KPIs[i], w)
		}
	}
	if len(st.Week) != 7 || !st.Week[6].Today || st.Week[6].Value != "5" || st.Week[6].Label != "Sr" {
		t.Errorf("today group = %+v", st.Week[6])
	}
	if g := st.Week[3]; g.Value != "10" || g.Label != "Ne" || g.Today {
		t.Errorf("3 days ago group = %+v", g)
	}
	if g := st.Week[5]; !g.HasPrev || g.Prev.H == 0 {
		t.Errorf("8 days ago (yesterday's group, last week) missing: %+v", g)
	}
	if !st.Hours[17].Peak || st.Hours[17].Value != "15" || st.Peak != "Najviše gledaju između 17 i 18 h." {
		t.Errorf("peak = %q, hour17 = %+v", st.Peak, st.Hours[17])
	}
	if st.Uptime != 50 {
		t.Errorf("uptime = %d, want 50 (12 h of 24)", st.Uptime)
	}
	// Detail: round gridlines, today's opening hours shaded, a 28-point
	// trend with a 7-day average, a right-aligned uptime strip.
	if g := st.WeekGrid; len(g) != 3 || g[2].Label != "10" || g[1].Label != "5" {
		t.Errorf("week grid = %+v, want 0/5/10", g)
	}
	if len(st.Open) != 1 || st.Open[0].X != gutter+8*11 || st.Open[0].W != 12*11 {
		t.Errorf("open band = %+v, want 08–20", st.Open)
	}
	if n := len(strings.Fields(st.Trend.Line)); n != 28 || len(strings.Fields(st.Trend.Avg)) != 22 || st.Trend.Total != 19 {
		t.Errorf("trend: %d points, avg %d, total %d", n, len(strings.Fields(st.Trend.Avg)), st.Trend.Total)
	}
	// The trend's geometry must match the template's 600-wide viewBox: today
	// sits on the axis end (x 586), the oldest day on the gutter (x 26).
	if st.Trend.TodayX != 586 || !strings.HasPrefix(st.Trend.Line, "26,") {
		t.Errorf("trend spans %q … %d, want 26 … 586", strings.Fields(st.Trend.Line)[0], st.Trend.TodayX)
	}
	if len(st.Strip) != 144 || st.Strip[143].X != 286 || !st.Strip[143].On || st.Strip[0].On {
		t.Errorf("strip: %d cells, last %+v, first %+v", len(st.Strip), st.Strip[len(st.Strip)-1], st.Strip[0])
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
	if u := buildOwnerStats(now.UTC(), loc, nil, nil, recent, now.Add(-time.Hour), 0, sched, cat).Uptime; u != 100 {
		t.Errorf("new camera uptime = %d, want 100", u)
	}
	if u := buildOwnerStats(now.UTC(), loc, nil, nil, nil, now.Add(-5*time.Minute), 0, sched, cat).Uptime; u != -1 {
		t.Errorf("brand-new camera uptime = %d, want -1 (hidden)", u)
	}

	empty := buildOwnerStats(now.UTC(), loc, nil, nil, nil, now.Add(-48*time.Hour), 0, sched, cat)
	if empty.HasData || empty.KPIs[0].Up || empty.KPIs[0].Down || empty.Peak != "" {
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
	for _, want := range []string{"Pregledi", ">7<", "Tjedan prije: 0", "http://guzva.example/jadro/", "<svg", "Ispiši plakat", "Odjava"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dash lacks %q", want)
		}
	}
	for _, bad := range []string{"Sign out", "PLAN D1", "Savjeti", "Minute gledanja", "od prošlog tjedna"} {
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

func TestNiceMax(t *testing.T) {
	for in, want := range map[int64]int64{0: 4, 3: 4, 7: 10, 10: 10, 11: 20, 38: 50, 133: 200, 501: 1000} {
		if got := niceMax(in); got != want {
			t.Errorf("niceMax(%d) = %d, want %d", in, got, want)
		}
	}
}
