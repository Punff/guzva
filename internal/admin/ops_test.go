package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"guzva/internal/store"
)

// TestMailTestAndAlertsPanel: the fleet page lists open alerts and the
// outbox; the test form queues one message (and says when SMTP is off),
// and refuses anything that is not one address (no header tricks).
func TestMailTestAndAlertsPanel(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	ctx := context.Background()
	if err := f.st.RaiseAlert(ctx, "stale:jadro", "Jadro: no picture for 20 min", time.Now()); err != nil {
		t.Fatal(err)
	}

	w := f.do(t, http.MethodPost, "/admin/mail/test", url.Values{"to": {"me@example.com"}}, c, true)
	if !strings.Contains(w.Body.String(), "SMTP is not configured") {
		t.Errorf("flash = %q", w.Body.String())
	}
	for _, bad := range []string{"", "nobody", "a@b.ba, c@d.ba", "a@b.ba\r\nBcc: x@y"} {
		w := f.do(t, http.MethodPost, "/admin/mail/test", url.Values{"to": {bad}}, c, true)
		if !strings.Contains(w.Body.String(), "Enter one email address") {
			t.Errorf("to=%q accepted: %q", bad, w.Body.String())
		}
	}
	rows, _ := f.st.RecentMail(ctx, 10)
	if len(rows) != 1 || rows[0].To != "me@example.com" || rows[0].Status != "queued" {
		t.Fatalf("outbox = %+v", rows)
	}

	page := f.do(t, http.MethodGet, "/admin/", nil, c, false).Body.String()
	for _, want := range []string{"Alerts &amp; email — 1 open", "Jadro: no picture for 20 min", "me@example.com", "queued"} {
		if !strings.Contains(page, want) {
			t.Errorf("fleet lacks %q", want)
		}
	}
}

// TestAuditLog: admin and owner edits are recorded with the changed field
// names (no values), a password change records only that it happened, and
// the fleet page lists the entries.
func TestAuditLog(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	ctx := context.Background()
	vid, err := f.st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo"})
	if err != nil {
		t.Fatal(err)
	}
	form := withHours(url.Values{"name": {"Jadro 2"}, "tz": {"Europe/Sarajevo"}, "style": {"poster"},
		"phone": {"+387 61 000 000"}, "listed": {"1"}}, nil)
	if w := f.do(t, http.MethodPost, fmt.Sprintf("/admin/venues/%d", vid), form, c, false); w.Code >= 400 {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	f.do(t, http.MethodPost, fmt.Sprintf("/admin/venues/%d/ownerpw", vid), url.Values{"password": {"vrlotajna123"}}, c, false)

	entries, _ := f.st.RecentAudit(ctx, vid, 10)
	var got []string
	for _, e := range entries {
		got = append(got, e.Actor+" "+e.Action+" "+e.Detail)
		if strings.Contains(e.Detail, "vrlotajna") || strings.Contains(e.Detail, "+387") {
			t.Errorf("audit leaks a value: %q", e.Detail)
		}
	}
	joined := strings.Join(got, " | ")
	for _, want := range []string{"admin venue.update changed: name, phone, hours", "admin venue.owner_password password set"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit lacks %q in %q", want, joined)
		}
	}
	page := f.do(t, http.MethodGet, "/admin/", nil, c, false).Body.String()
	if !strings.Contains(page, "venue.update") || !strings.Contains(page, "Jadro 2") {
		t.Error("fleet page does not list the audit entries")
	}
}
