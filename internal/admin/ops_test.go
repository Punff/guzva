package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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
