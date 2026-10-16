package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"guzva/internal/store"
)

func TestOffboardNeedsSlugAndLocksEverything(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	ctx := context.Background()
	vid, _ := f.st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo"})
	did, _ := f.st.CreateDevice(ctx, store.Device{VenueID: vid, Serial: "cam1"}, "h1")
	f.st.SetOwnerHash(ctx, vid, mustHash(t, "vlasnik12345"))
	path := fmt.Sprintf("/admin/venues/%d/status", vid)

	if w := f.do(t, http.MethodPost, path, url.Values{"status": {"offboarded"}}, c, true); !strings.Contains(w.Body.String(), "type the slug") {
		t.Fatal("offboard without the slug was not refused")
	}
	f.do(t, http.MethodPost, path, url.Values{"status": {"offboarded"}, "confirm": {"jadro"}}, c, false)
	v, _ := f.st.VenueByID(ctx, vid)
	d, _ := f.st.DeviceByID(ctx, did)
	if v.Status != "offboarded" || v.OwnerHash != "" || v.Listed || d.Status != "revoked" {
		t.Fatalf("after offboard: venue %+v device %s", v, d.Status)
	}
	f.do(t, http.MethodPost, path, url.Values{"status": {"paused"}}, c, false)
	if v, _ := f.st.VenueByID(ctx, vid); v.Status != "paused" {
		t.Fatalf("status = %s", v.Status)
	}
	if entries, _ := f.st.RecentAudit(ctx, vid, 5); len(entries) < 2 || !strings.Contains(entries[1].Detail, "→ offboarded") {
		t.Errorf("audit = %+v", entries)
	}
}

// TestOwnerInviteSetLoginAndReset: the invite link sets a password that
// works, the link is single use, and the reset form reveals nothing.
func TestOwnerInviteSetLoginAndReset(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	ctx := context.Background()
	vid, _ := f.st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo"})

	w := f.do(t, http.MethodPost, fmt.Sprintf("/admin/venues/%d/owner", vid), url.Values{"email": {"vlasnik@jadro.ba"}}, c, true)
	m := regexp.MustCompile(`/owner/set/([A-Za-z0-9_-]+)`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no link: %s", w.Body.String())
	}
	token := m[1]
	mail, _ := f.st.DueMail(ctx, f.s.now().Add(1e9), 5)
	if len(mail) != 1 || mail[0].To != "vlasnik@jadro.ba" || !strings.Contains(mail[0].Text, "/owner/set/"+token) || !strings.Contains(mail[0].Text, "jadro") {
		t.Fatalf("invite mail = %+v", mail)
	}

	if page := f.do(t, http.MethodGet, "/owner/set/"+token, nil, nil, false).Body.String(); !strings.Contains(page, "Lozinka za vlasnički panel salona Jadro") {
		t.Fatal("set page does not name the venue")
	}
	if body := f.do(t, http.MethodPost, "/owner/set/"+token, url.Values{"password": {"kratka"}, "password2": {"kratka"}}, nil, false).Body.String(); !strings.Contains(body, "najmanje 10") {
		t.Error("short password accepted")
	}
	if w := f.do(t, http.MethodPost, "/owner/set/"+token, url.Values{"password": {"nova-lozinka-1"}, "password2": {"nova-lozinka-1"}}, nil, false); w.Code != http.StatusSeeOther {
		t.Fatalf("set = %d", w.Code)
	}
	if w := f.do(t, http.MethodPost, "/owner/login", url.Values{"slug": {"jadro"}, "password": {"nova-lozinka-1"}}, nil, false); w.Code != http.StatusSeeOther {
		t.Fatalf("login with the new password = %d", w.Code)
	}
	if page := f.do(t, http.MethodGet, "/owner/set/"+token, nil, nil, false).Body.String(); !strings.Contains(page, "više ne vrijedi") {
		t.Error("a used link still works")
	}

	// Reset: wrong email and right email answer the same; only the right
	// one queues a mail.
	wrong := f.do(t, http.MethodPost, "/owner/reset", url.Values{"slug": {"jadro"}, "email": {"netko@drugi.ba"}}, nil, false).Body.String()
	right := f.do(t, http.MethodPost, "/owner/reset", url.Values{"slug": {"jadro"}, "email": {"VLASNIK@jadro.ba"}}, nil, false).Body.String()
	if !strings.Contains(wrong, "Ako podaci odgovaraju") || !strings.Contains(right, "Ako podaci odgovaraju") {
		t.Fatal("reset answers differ or are missing")
	}
	rows, _ := f.st.RecentMail(ctx, 10)
	resets := 0
	for _, r := range rows {
		if r.Template == "owner-reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("reset mails = %d, want 1", resets)
	}
}

// TestLegalPack: the three documents render with the venue's details; only
// the drafts carry the NACRT mark; the owner can print the notice.
func TestLegalPack(t *testing.T) {
	f := newFixture(t, mustHash(t, "sekret"))
	c := f.login(t)
	ctx := context.Background()
	vid, _ := f.st.CreateVenue(ctx, store.Venue{Slug: "jadro", Name: "Jadro", Style: "poster", TZ: "Europe/Sarajevo"})
	f.st.UpdateVenue(ctx, vid, "Jadro", "poster", "Europe/Sarajevo", "", "", "Ferhadija 12, Sarajevo")
	get := func(doc string) string {
		return f.do(t, http.MethodGet, fmt.Sprintf("/admin/venues/%d/legal/%s", vid, doc), nil, c, false).Body.String()
	}
	notice, odluka, terms := get("notice"), get("odluka"), get("terms")
	if !strings.Contains(notice, "Video nadzor") || !strings.Contains(notice, "/jadro/") || !strings.Contains(notice, "/privatnost/") ||
		!strings.Contains(notice, "<svg") || strings.Contains(notice, "NACRT") {
		t.Error("notice wrong")
	}
	for name, doc := range map[string]string{"odluka": odluka, "terms": terms} {
		if !strings.Contains(doc, "NACRT") || !strings.Contains(doc, "Jadro") || !strings.Contains(doc, "Ferhadija 12") {
			t.Errorf("%s lacks the draft mark or the venue details", name)
		}
	}
	if w := f.do(t, http.MethodGet, fmt.Sprintf("/admin/venues/%d/legal/other", vid), nil, c, false); w.Code != http.StatusNotFound {
		t.Errorf("unknown doc = %d", w.Code)
	}
	f.st.SetOwnerHash(ctx, vid, mustHash(t, "vlasnik12345"))
	w := f.do(t, http.MethodPost, "/owner/login", url.Values{"slug": {"jadro"}, "password": {"vlasnik12345"}}, nil, false)
	var oc *http.Cookie
	for _, ck := range w.Result().Cookies() {
		oc = ck
	}
	if body := f.do(t, http.MethodGet, "/owner/notice", nil, oc, false).Body.String(); !strings.Contains(body, "Video nadzor") {
		t.Error("owner cannot print the notice")
	}
}
