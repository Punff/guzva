package admin

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func intakeForm1(f *fixture, stampAt time.Time) url.Values {
	v := withHours(url.Values{
		"salon_name": {"Brijačnica Đuro & Sin"}, "salon_type": {"brijac"}, "address": {"Ferhadija 12, Sarajevo"},
		"contact_name": {"Đuro Đurić"}, "phone": {"+387 61 111 222"}, "email": {"djuro@salon.ba"},
		"wifi": {"yes"}, "chairs": {"3"}, "consent": {"1"}, "t": {f.s.formStamp(stampAt)},
	}, map[string]string{"h0_open": "09:00", "h0_close": "17:00"})
	return v
}

// TestIntakeHappyPath: a valid application is stored as a new lead, the
// operator is notified, and the thank-you page is shown.
func TestIntakeHappyPath(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }
	notified := make(chan string, 1)
	f.s.Notify = func(_ context.Context, text string) error { notified <- text; return nil }

	if page := f.do(t, http.MethodGet, "/prijava/", nil, nil, false); page.Code != 200 || !strings.Contains(page.Body.String(), "Prijava salona") {
		t.Fatalf("form page = %d", page.Code)
	}
	w := f.do(t, http.MethodPost, "/prijava/", intakeForm1(f, now.Add(-time.Minute)), nil, false)
	if !strings.Contains(w.Body.String(), "Hvala na prijavi") {
		t.Fatalf("submit did not thank: %s", w.Body.String())
	}
	ls, _ := f.st.ListLeads(context.Background(), "")
	if len(ls) != 1 || ls[0].SalonName != "Brijačnica Đuro & Sin" || ls[0].Source != "public" || ls[0].HasWiFi != 1 || ls[0].Chairs != 3 || ls[0].HoursJSON == "" {
		t.Fatalf("lead = %+v", ls)
	}
	select {
	case msg := <-notified:
		if !strings.Contains(msg, "Brijačnica Đuro & Sin") {
			t.Errorf("notify = %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Error("operator not notified")
	}
}

// TestIntakeGuards: honeypot, a too-fast or forged stamp, missing fields,
// a bad email, no consent — each refused, nothing stored.
func TestIntakeGuards(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }
	cases := []struct {
		name string
		mod  func(v url.Values)
		want string
	}{
		{"honeypot", func(v url.Values) { v.Set("website", "http://spam") }, "nije poslana"},
		{"too fast", func(v url.Values) { v.Set("t", f.s.formStamp(now.Add(-time.Second))) }, "nije poslana"},
		{"forged stamp", func(v url.Values) { v.Set("t", "1760000000.deadbeef") }, "nije poslana"},
		{"missing", func(v url.Values) { v.Del("phone"); v.Del("salon_name") }, "Ispunite polja"},
		{"email", func(v url.Values) { v.Set("email", "nope") }, "Email adresa nije ispravna"},
		{"consent", func(v url.Values) { v.Del("consent") }, "suglasnost"},
		{"maps", func(v url.Values) { v.Set("maps_url", "https://evil.example/maps") }, "Google Karte"},
	}
	for _, c := range cases {
		v := intakeForm1(f, now.Add(-time.Minute))
		c.mod(v)
		body := f.do(t, http.MethodPost, "/prijava/", v, nil, false).Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: want %q in reply", c.name, c.want)
		}
	}
	if n, _ := f.st.CountLeads(context.Background(), ""); n != 0 {
		t.Fatalf("guards let %d leads through", n)
	}
}

// TestInviteToVenue: a personal link opens pre-filled, works once, and the
// resulting lead becomes a venue with its details copied.
func TestInviteToVenue(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	f.s.now = func() time.Time { return now }
	ctx := context.Background()

	w := f.do(t, http.MethodPost, "/admin/leads/invite", url.Values{"label": {"Salon Šiško"}, "phone": {"+387 62 333 444"}}, c, true)
	m := regexp.MustCompile(`/prijava/([A-Za-z0-9_-]+)`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no link in %q", w.Body.String())
	}
	token := m[1]
	page := f.do(t, http.MethodGet, "/prijava/"+token, nil, nil, false).Body.String()
	if !strings.Contains(page, `value="Salon Šiško"`) || !strings.Contains(page, `value="&#43;387 62 333 444"`) || !strings.Contains(page, `name="invite"`) {
		t.Fatal("invite page not pre-filled")
	}
	v := intakeForm1(f, now.Add(-time.Minute))
	v.Set("salon_name", "Salon Šiško")
	v.Set("invite", token)
	v.Set("maps_url", "https://maps.app.goo.gl/AbC123")
	if body := f.do(t, http.MethodPost, "/prijava/", v, nil, false).Body.String(); !strings.Contains(body, "Hvala") {
		t.Fatalf("invite submit failed: %s", body)
	}
	if body := f.do(t, http.MethodPost, "/prijava/", v, nil, false).Body.String(); !strings.Contains(body, "više ne vrijedi") {
		t.Error("a used invite link was accepted again")
	}
	ls, _ := f.st.ListLeads(ctx, "")
	if len(ls) != 1 || ls[0].Source != "invite" {
		t.Fatalf("leads = %+v", ls)
	}
	id := ls[0].ID

	leadPage := f.do(t, http.MethodGet, "/admin/leads/"+itoa64(id), nil, c, false).Body.String()
	if !strings.Contains(leadPage, `value="salon-sisko"`) {
		t.Errorf("slug not suggested: %s", regexp.MustCompile(`name="slug" value="[^"]*"`).FindString(leadPage))
	}
	if w := f.do(t, http.MethodPost, "/admin/leads/"+itoa64(id)+"/venue", url.Values{"slug": {"salon-sisko"}}, c, false); w.Code != http.StatusSeeOther {
		t.Fatalf("create venue = %d %s", w.Code, w.Body.String())
	}
	ven, err := f.st.VenueBySlug(ctx, "salon-sisko")
	if err != nil || ven.Name != "Salon Šiško" || ven.Location != "Ferhadija 12, Sarajevo" || ven.Phone != "+387 61 111 222" ||
		ven.MapsURL != "https://maps.app.goo.gl/AbC123" || ven.Listed {
		t.Fatalf("venue = %+v err %v", ven, err)
	}
	if h, _ := f.st.Hours(ctx, ven.ID); !h.Configured() {
		t.Error("hours not copied")
	}
	l, _ := f.st.LeadByID(ctx, id)
	if l.Status != "won" || l.VenueID != ven.ID {
		t.Fatalf("lead after = %+v", l)
	}
	if !strings.Contains(f.do(t, http.MethodGet, "/admin/leads?s=won", nil, c, false).Body.String(), "Salon Šiško") {
		t.Error("won filter does not list the lead")
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
