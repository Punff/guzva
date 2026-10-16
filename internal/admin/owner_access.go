package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/i18n"
	"guzva/internal/store"
)

// Venue lifecycle + owner self-service (business plan P2): the admin moves a
// venue through onboarding → active → paused → offboarded, and owners set
// (and reset) their own password from a one-time link, so the operator never
// has to know or send a password.

const (
	ownerInviteTTL = 72 * time.Hour
	ownerResetTTL  = 2 * time.Hour
)

// setVenueStatus is the admin lifecycle control.
func (s *Server) setVenueStatus(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	status := r.FormValue("status")
	valid := false
	for _, st := range store.VenueStatuses {
		valid = valid || st == status
	}
	if !valid {
		s.flash(w, "Pick a status.")
		return
	}
	ctx := r.Context()
	if status == "offboarded" {
		if r.FormValue("confirm") != v.Slug {
			s.flash(w, "To offboard, type the slug ("+v.Slug+") to confirm. It revokes the camera, disables the owner login and takes the public page down.")
			return
		}
		if err := s.st.RevokeVenueDevices(ctx, v.ID); err != nil {
			s.internal(w, "offboard revoke", err)
			return
		}
		if err := s.st.SetOwnerHash(ctx, v.ID, ""); err != nil {
			s.internal(w, "offboard owner", err)
			return
		}
		_ = s.st.SetListed(ctx, v.ID, false)
	}
	if err := s.st.SetVenueStatus(ctx, v.ID, status); err != nil {
		s.internal(w, "set status", err)
		return
	}
	s.log.Info("admin: venue status", "venue_id", v.ID, "slug", v.Slug, "status", status, "actor", "admin")
	s.audit(r, "admin", "venue.status", v.ID, v.Status+" → "+status)
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?saved=1", v.ID))
}

// newOwnerLink mints a one-time owner link and returns its URL.
func (s *Server) newOwnerLink(r *http.Request, v *store.Venue, purpose string, ttl time.Duration) (string, error) {
	token, err := auth.GenerateToken()
	if err != nil {
		return "", err
	}
	token = strings.TrimPrefix(token, "gkv1_")
	now := s.now()
	if err := s.st.CreateOwnerToken(r.Context(), auth.HashToken(token), v.ID, purpose, now, now.Add(ttl)); err != nil {
		return "", err
	}
	return s.publicBase(r) + "/owner/set/" + url.PathEscape(token), nil
}

// ownerInvite saves the owner's email and sends (and shows once) a link to
// set the password.
func (s *Server) ownerInvite(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	if v.Status == "offboarded" {
		s.flash(w, "This venue is offboarded.")
		return
	}
	r.ParseForm()
	email := strings.TrimSpace(r.FormValue("email"))
	if email != "" && !validEmail(email) {
		s.flash(w, "That email address doesn't look right.")
		return
	}
	if err := s.st.SetOwnerEmail(r.Context(), v.ID, email); err != nil {
		s.internal(w, "owner email", err)
		return
	}
	link, err := s.newOwnerLink(r, v, "invite", ownerInviteTTL)
	if err != nil {
		s.internal(w, "owner invite", err)
		return
	}
	sent := "Not emailed (no owner email) — send the link yourself."
	if email != "" {
		cat := i18n.For(i18n.Normalize(v.Lang))
		if _, err := s.st.EnqueueMail(r.Context(), store.OutMail{To: email, Template: "owner-invite",
			Subject: cat.F("mail.invite.subject", v.Name),
			Text:    cat.F("mail.invite.body", v.Name, link, v.Slug)}, s.now()); err != nil {
			s.internal(w, "enqueue invite", err)
			return
		}
		sent = "Emailed to " + email + "."
		if !s.MailOn {
			sent = "Queued for " + email + ", but SMTP is not configured — send the link yourself for now."
		}
	}
	s.log.Info("admin: owner invite", "venue_id", v.ID, "actor", "admin")
	s.audit(r, "admin", "venue.owner_invite", v.ID, "invite link created (72 h)")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "owner-link", map[string]string{"Link": link, "Sent": sent})
}

type ownerSetModel struct {
	Cat   *i18n.Catalog
	Venue string
	Token string
	Err   string
	Bad   bool
}

func (s *Server) ownerTokenOK(r *http.Request, token string) (*store.OwnerToken, *store.Venue, bool) {
	if len(token) < 20 || len(token) > 64 {
		return nil, nil, false
	}
	t, err := s.st.OwnerTokenByHash(r.Context(), auth.HashToken(token))
	if err != nil || !t.UsedAt.IsZero() || s.now().After(t.ExpiresAt) {
		return nil, nil, false
	}
	v, err := s.st.VenueByID(r.Context(), t.VenueID)
	if err != nil || v.Status == "offboarded" {
		return nil, nil, false
	}
	return t, v, true
}

func (s *Server) ownerSetPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	_, v, ok := s.ownerTokenOK(r, token)
	m := ownerSetModel{Cat: i18n.For(i18n.HR), Token: token, Bad: !ok}
	if ok {
		m.Cat, m.Venue = i18n.For(i18n.Normalize(v.Lang)), v.Name
	}
	s.render(w, "owner_set.html", m)
}

func (s *Server) ownerSetSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	t, v, ok := s.ownerTokenOK(r, token)
	if !ok {
		s.render(w, "owner_set.html", ownerSetModel{Cat: i18n.For(i18n.HR), Bad: true})
		return
	}
	cat := i18n.For(i18n.Normalize(v.Lang))
	r.ParseForm()
	pw, pw2 := r.FormValue("password"), r.FormValue("password2")
	m := ownerSetModel{Cat: cat, Venue: v.Name, Token: token}
	switch {
	case len(pw) < minOwnerPassword:
		m.Err = cat.Get("owner.set.err.short")
	case pw != pw2:
		m.Err = cat.Get("owner.set.err.match")
	}
	if m.Err != "" {
		s.render(w, "owner_set.html", m)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.internal(w, "hash owner pw", err)
		return
	}
	if used, err := s.st.UseOwnerToken(r.Context(), t, s.now()); err != nil || !used {
		s.render(w, "owner_set.html", ownerSetModel{Cat: cat, Bad: true})
		return
	}
	if err := s.st.SetOwnerHash(r.Context(), v.ID, hash); err != nil {
		s.internal(w, "set owner hash", err)
		return
	}
	s.log.Info("owner: password set via link", "venue_id", v.ID, "purpose", t.Purpose)
	s.audit(r, "owner", "owner.password_set", v.ID, "via "+t.Purpose+" link (value not recorded)")
	http.Redirect(w, r, "/owner/login?set=1", http.StatusSeeOther)
}

type ownerResetModel struct {
	Cat  *i18n.Catalog
	Sent bool
}

func (s *Server) ownerResetPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "owner_reset.html", ownerResetModel{Cat: i18n.For(i18n.HR)})
}

// ownerResetSubmit always answers the same, whether or not slug and email
// matched — the page must not reveal which venues or emails exist.
func (s *Server) ownerResetSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	done := func() { s.render(w, "owner_reset.html", ownerResetModel{Cat: i18n.For(i18n.HR), Sent: true}) }
	if !s.loginLim.AllowIP(auth.ClientIP(r)) {
		done()
		return
	}
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	email := strings.TrimSpace(r.FormValue("email"))
	v, err := s.st.VenueBySlug(r.Context(), slug)
	if err != nil || v.OwnerEmail == "" || !strings.EqualFold(v.OwnerEmail, email) || v.Status == "offboarded" {
		done()
		return
	}
	link, err := s.newOwnerLink(r, v, "reset", ownerResetTTL)
	if err != nil {
		s.internal(w, "owner reset link", err)
		return
	}
	cat := i18n.For(i18n.Normalize(v.Lang))
	if _, err := s.st.EnqueueMail(r.Context(), store.OutMail{To: v.OwnerEmail, Template: "owner-reset",
		Subject: cat.Get("mail.reset.subject"), Text: cat.F("mail.reset.body", v.Name, link)}, s.now()); err != nil {
		s.internal(w, "enqueue reset", err)
		return
	}
	s.log.Info("owner: reset link sent", "venue_id", v.ID)
	s.audit(r, "owner", "owner.reset_requested", v.ID, "")
	done()
}
