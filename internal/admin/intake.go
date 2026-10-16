package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"guzva/internal/auth"
	"guzva/internal/i18n"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// Client intake (business plan P2): the public application form at
// /prijava/, personal pre-filled invite links /prijava/<token>, and the
// admin Leads inbox that turns an application into a venue.
//
// Spam without third parties: a honeypot field, a signed form timestamp
// (too fast = a bot, too old = a stale page) and a per-IP limit. The WiFi
// password is never asked for — that is collected on site.

const (
	inviteTTL     = 14 * 24 * time.Hour
	minFillTime   = 3 * time.Second
	maxFormAge    = 24 * time.Hour
	maxShortField = 160
	maxMessage    = 1000
)

var salonTypes = []string{"frizer", "brijac", "kozmetika", "drugo"}

// formStamp signs the time the form was rendered.
func (s *Server) formStamp(t time.Time) string {
	v := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, s.formKey)
	m.Write([]byte("intake|" + v))
	return v + "." + hex.EncodeToString(m.Sum(nil))[:24]
}

// stampOK verifies the signature and that the form took a human's time.
func (s *Server) stampOK(stamp string, now time.Time) bool {
	v, sig, ok := strings.Cut(stamp, ".")
	if !ok {
		return false
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return false
	}
	want := s.formStamp(time.Unix(sec, 0))
	if !hmac.Equal([]byte(want), []byte(v+"."+sig)) {
		return false
	}
	age := now.Sub(time.Unix(sec, 0))
	return age >= minFillTime && age <= maxFormAge
}

// intakeForm is the form's values (also the re-render after an error).
type intakeForm struct {
	SalonName, SalonType, Address, MapsURL string
	ContactName, Phone, Email              string
	Chairs                                 string
	WiFi                                   string // yes | no | unknown
	InstallWindow, HeardFrom, Message      string
	Consent                                bool
}

type intakeModel struct {
	Cat     *i18n.Catalog
	F       intakeForm
	Days    [7]venue.DayForm
	Stamp   string
	Invite  string // raw token when filling an invite link
	Invited bool
	Err     string
	Done    bool
	Types   []string
}

func (s *Server) intakeCat() *i18n.Catalog { return i18n.For(i18n.HR) }

func (s *Server) renderIntake(w http.ResponseWriter, m intakeModel) {
	m.Cat = s.intakeCat()
	m.Types = salonTypes
	if m.Stamp == "" {
		m.Stamp = s.formStamp(s.now())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	if err := s.tmpl.ExecuteTemplate(w, "intake.html", m); err != nil {
		s.log.Error("admin: render intake", "err", err.Error())
	}
}

func emptyDays(cat *i18n.Catalog) [7]venue.DayForm {
	return venue.EditModel(venue.Hours{}, cat.DayNames())
}

// intakePage shows the empty public form.
func (s *Server) intakePage(w http.ResponseWriter, r *http.Request) {
	s.renderIntake(w, intakeModel{Days: emptyDays(s.intakeCat()), F: intakeForm{WiFi: "unknown", SalonType: "frizer"}})
}

// invitePage shows the form pre-filled from a personal invite link.
func (s *Server) invitePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, ok := s.validInvite(r.Context(), token)
	cat := s.intakeCat()
	if !ok {
		s.renderIntake(w, intakeModel{Days: emptyDays(cat), Err: cat.Get("intake.err.invite"),
			F: intakeForm{WiFi: "unknown", SalonType: "frizer"}})
		return
	}
	f := intakeForm{WiFi: "unknown", SalonType: "frizer"}
	_ = json.Unmarshal([]byte(inv.PrefillJSON), &f)
	s.renderIntake(w, intakeModel{Days: emptyDays(cat), F: f, Invite: token, Invited: true})
}

func (s *Server) validInvite(ctx context.Context, token string) (*store.Invite, bool) {
	if len(token) < 20 || len(token) > 64 {
		return nil, false
	}
	inv, err := s.st.InviteByHash(ctx, auth.HashToken(token))
	if err != nil || !inv.UsedAt.IsZero() || s.now().After(inv.ExpiresAt) {
		return nil, false
	}
	return inv, true
}

func validEmail(e string) bool {
	at := strings.LastIndex(e, "@")
	return len(e) <= 254 && at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") &&
		!strings.ContainsAny(e, " \t\r\n<>,;\"'")
}

func validPhone(p string) bool {
	if p == "" || len(p) > maxVenuePhone {
		return false
	}
	for _, r := range p {
		if !(unicode.IsDigit(r) || strings.ContainsRune("+ -.()", r)) {
			return false
		}
	}
	return true
}

func oneLine(v string) string { return strings.Join(strings.Fields(v), " ") }

// intakeSubmit validates and stores an application.
func (s *Server) intakeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "400", http.StatusBadRequest)
		return
	}
	cat := s.intakeCat()
	now := s.now()
	f := intakeForm{
		SalonName: oneLine(r.FormValue("salon_name")), SalonType: r.FormValue("salon_type"),
		Address: oneLine(r.FormValue("address")), MapsURL: strings.TrimSpace(r.FormValue("maps_url")),
		ContactName: oneLine(r.FormValue("contact_name")), Phone: strings.TrimSpace(r.FormValue("phone")),
		Email: strings.TrimSpace(r.FormValue("email")), Chairs: strings.TrimSpace(r.FormValue("chairs")),
		WiFi: r.FormValue("wifi"), InstallWindow: oneLine(r.FormValue("install_window")),
		HeardFrom: oneLine(r.FormValue("heard_from")), Message: strings.TrimSpace(r.FormValue("message")),
		Consent: r.FormValue("consent") == "1",
	}
	token := r.FormValue("invite")
	days := emptyDays(cat)
	fail := func(msg string) {
		if h, herr := parseHours(r, cat); herr == "" {
			days = venue.EditModel(h, cat.DayNames())
		}
		s.renderIntake(w, intakeModel{F: f, Days: days, Err: msg, Invite: token, Invited: token != ""})
	}
	// Bots: honeypot filled, bad/too-fast stamp, or the IP limit. Same
	// polite message for all three — nothing tells a script which tripped.
	if r.FormValue("website") != "" || !s.stampOK(r.FormValue("t"), now) || !s.intakeLim.AllowIP(auth.ClientIP(r)) {
		s.log.Warn("intake: rejected as automated", "ip", auth.ClientIP(r))
		fail(cat.Get("intake.err.spam"))
		return
	}
	var missing []string
	for _, req := range []struct{ v, label string }{
		{f.SalonName, cat.Get("intake.name")}, {f.Address, cat.Get("intake.address")},
		{f.ContactName, cat.Get("intake.contact")}, {f.Phone, cat.Get("intake.phone")},
		{f.Email, cat.Get("intake.email")},
	} {
		if req.v == "" {
			missing = append(missing, strings.ToLower(req.label))
		}
	}
	if len(missing) > 0 {
		fail(cat.F("intake.err.required", strings.Join(missing, ", ")))
		return
	}
	for _, v := range []string{f.SalonName, f.Address, f.ContactName, f.InstallWindow, f.HeardFrom} {
		if len(v) > maxShortField {
			fail(cat.Get("intake.err.long"))
			return
		}
	}
	if len(f.Message) > maxMessage {
		fail(cat.Get("intake.err.long"))
		return
	}
	if !validPhone(f.Phone) {
		fail(cat.Get("intake.err.phone"))
		return
	}
	if !validEmail(f.Email) {
		fail(cat.Get("intake.err.email"))
		return
	}
	maps, ok := parseMapsURL(f.MapsURL)
	if !ok {
		fail(cat.Get("intake.err.maps"))
		return
	}
	if !f.Consent {
		fail(cat.Get("intake.err.consent"))
		return
	}
	h, herr := parseHours(r, cat)
	if herr != "" {
		fail(herr)
		return
	}
	l := store.Lead{SalonName: f.SalonName, SalonType: "drugo", Address: f.Address, MapsURL: maps,
		ContactName: f.ContactName, Phone: f.Phone, Email: f.Email, InstallWindow: f.InstallWindow,
		HeardFrom: f.HeardFrom, Message: f.Message}
	for _, t := range salonTypes {
		if f.SalonType == t {
			l.SalonType = t
		}
	}
	if n, err := strconv.Atoi(f.Chairs); err == nil && n > 0 && n <= 100 {
		l.Chairs = n
	}
	switch f.WiFi {
	case "yes":
		l.HasWiFi = 1
	case "no":
		l.HasWiFi = 2
	}
	if h.Configured() {
		if b, err := json.Marshal(h); err == nil {
			l.HoursJSON = string(b)
		}
	}
	if token != "" {
		inv, ok := s.validInvite(r.Context(), token)
		if !ok {
			fail(cat.Get("intake.err.invite"))
			return
		}
		if used, err := s.st.UseInvite(r.Context(), inv.ID, now); err != nil || !used {
			fail(cat.Get("intake.err.invite"))
			return
		}
		l.Source, l.InviteID = "invite", inv.ID
	}
	id, err := s.st.CreateLead(r.Context(), l, now)
	if err != nil {
		s.internal(w, "create lead", err)
		return
	}
	s.log.Info("intake: new application", "lead_id", id, "source", l.Source)
	if s.Notify != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_ = s.Notify(ctx, fmt.Sprintf("📝 New application: %s — %s (admin → Leads)", l.SalonName, l.Address))
		}()
	}
	s.renderIntake(w, intakeModel{Done: true})
}

// --- admin: leads inbox --------------------------------------------------------

var checklistItems = []struct{ Key, Label string }{
	{"rssi", "WiFi at the mounting point measured ≥ −65 dBm"},
	{"power", "Stable 5 V power at the mount (good adapter, short cable)"},
	{"framing", "Owner has seen and approved the framing"},
	{"notice", "Camera-notice sign put up at the entrance"},
	{"odluka", "Owner signed the decision on video surveillance"},
	{"network", "Camera on a network guests cannot join"},
}

type leadRow struct {
	store.Lead
	Age string
}

type leadsModel struct {
	Nav      navModel
	Filter   string
	Counts   map[string]int
	Rows     []leadRow
	Invites  []inviteRow
	Statuses []string
}

type inviteRow struct {
	store.Invite
	State string
}

func (s *Server) leadsPage(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("s")
	ls, err := s.st.ListLeads(r.Context(), filter)
	if err != nil {
		s.internal(w, "list leads", err)
		return
	}
	now := s.now().UTC()
	m := leadsModel{Nav: s.nav(r.Context()), Filter: filter, Counts: map[string]int{}, Statuses: store.LeadStatuses}
	for _, st := range append([]string{""}, store.LeadStatuses...) {
		if st == "" {
			continue
		}
		if c, err := s.st.CountLeads(r.Context(), st); err == nil {
			m.Counts[st] = c
		}
	}
	for _, l := range ls {
		m.Rows = append(m.Rows, leadRow{l, age(now, l.CreatedAt)})
	}
	if invs, err := s.st.ListInvites(r.Context(), 10); err == nil {
		for _, i := range invs {
			st := "open until " + i.ExpiresAt.Format("02.01.")
			switch {
			case !i.UsedAt.IsZero():
				st = "used " + i.UsedAt.Format("02.01.")
			case now.After(i.ExpiresAt):
				st = "expired"
			}
			m.Invites = append(m.Invites, inviteRow{i, st})
		}
	}
	s.render(w, "leads.html", m)
}

// publicBase is the site's own URL for links we hand out.
func (s *Server) publicBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// createInvite mints a personal link and shows it once.
func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	label := oneLine(r.FormValue("label"))
	if label == "" || len(label) > 80 {
		s.flash(w, "Give the link a label (who it is for), up to 80 characters.")
		return
	}
	pre := intakeForm{SalonName: label, WiFi: "unknown", SalonType: "frizer",
		ContactName: oneLine(r.FormValue("contact_name")), Phone: strings.TrimSpace(r.FormValue("phone")),
		Email: strings.TrimSpace(r.FormValue("email")), Address: oneLine(r.FormValue("address"))}
	b, _ := json.Marshal(pre)
	token, err := auth.GenerateToken()
	if err != nil {
		s.internal(w, "invite token", err)
		return
	}
	token = strings.TrimPrefix(token, "gkv1_")
	now := s.now()
	if _, err := s.st.CreateInvite(r.Context(), auth.HashToken(token), label, string(b), now, now.Add(inviteTTL)); err != nil {
		s.internal(w, "create invite", err)
		return
	}
	s.audit(r, "admin", "lead.invite", 0, "for "+label)
	link := s.publicBase(r) + "/prijava/" + url.PathEscape(token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tmpl.ExecuteTemplate(w, "invite-link", link)
}

type leadModel struct {
	Nav       navModel
	L         store.Lead
	Check     map[string]bool
	Items     []struct{ Key, Label string }
	Statuses  []string
	Slug      string
	HoursSpec string
	Saved     bool
	Venue     *store.Venue
}

func (s *Server) loadLead(w http.ResponseWriter, r *http.Request) (*store.Lead, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	l, err := s.st.LeadByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	return l, true
}

func (s *Server) leadPage(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadLead(w, r)
	if !ok {
		return
	}
	m := leadModel{Nav: s.nav(r.Context()), L: *l, Check: map[string]bool{}, Items: checklistItems,
		Statuses: store.LeadStatuses, Saved: r.URL.Query().Get("saved") != ""}
	_ = json.Unmarshal([]byte(l.ChecklistJSON), &m.Check)
	if l.HoursJSON != "" {
		var h venue.Hours
		if json.Unmarshal([]byte(l.HoursJSON), &h) == nil {
			m.HoursSpec = h.Spec()
		}
	}
	if l.VenueID > 0 {
		m.Venue, _ = s.st.VenueByID(r.Context(), l.VenueID)
	} else {
		m.Slug = s.suggestSlug(r.Context(), l.SalonName)
	}
	s.render(w, "lead.html", m)
}

func (s *Server) saveLead(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadLead(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	status := r.FormValue("status")
	valid := false
	for _, st := range store.LeadStatuses {
		valid = valid || st == status
	}
	if !valid || (status == "won" && l.VenueID == 0) {
		s.flash(w, "Pick a status. 'won' is set by creating the venue.")
		return
	}
	check := map[string]bool{}
	for _, it := range checklistItems {
		if r.FormValue("c_"+it.Key) == "1" {
			check[it.Key] = true
		}
	}
	b, _ := json.Marshal(check)
	notes := strings.TrimSpace(r.FormValue("notes"))
	if len(notes) > 4000 {
		notes = notes[:4000]
	}
	if err := s.st.UpdateLeadAdmin(r.Context(), l.ID, status, notes, string(b), s.now()); err != nil {
		s.internal(w, "save lead", err)
		return
	}
	s.audit(r, "admin", "lead.update", 0, fmt.Sprintf("lead %d → %s", l.ID, status))
	redirect(w, r, fmt.Sprintf("/admin/leads/%d?saved=1", l.ID))
}

// leadToVenue creates the venue from an application and marks it won.
func (s *Server) leadToVenue(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadLead(w, r)
	if !ok {
		return
	}
	if l.VenueID > 0 {
		s.flash(w, "This application already has a venue.")
		return
	}
	r.ParseForm()
	slug := strings.ToLower(strings.TrimSpace(r.FormValue("slug")))
	if !store.ValidSlug(slug) {
		s.flash(w, "Slug must be lowercase letters, digits or dashes (≤40), and not a reserved word.")
		return
	}
	ctx := r.Context()
	id, err := s.st.CreateVenue(ctx, store.Venue{Slug: slug, Name: l.SalonName, Style: "poster", TZ: store.DefaultTZ})
	if err == store.ErrSlugTaken {
		s.flash(w, "That slug is taken — pick another.")
		return
	}
	if err != nil {
		s.internal(w, "lead venue", err)
		return
	}
	phone := l.Phone
	if !validPhone(phone) {
		phone = ""
	}
	if err := s.st.UpdateVenue(ctx, id, l.SalonName, "poster", store.DefaultTZ, "", phone, l.Address); err != nil {
		s.internal(w, "lead venue info", err)
		return
	}
	if l.MapsURL != "" {
		_ = s.st.SetMapsURL(ctx, id, l.MapsURL)
	}
	if l.HoursJSON != "" {
		var h venue.Hours
		if json.Unmarshal([]byte(l.HoursJSON), &h) == nil {
			_ = s.st.SetHours(ctx, id, h)
		}
	}
	// Listed only once it is installed and sending; a fresh venue would sit
	// on the homepage with "no picture yet".
	_ = s.st.SetListed(ctx, id, false)
	_ = s.st.SetVenueStatus(ctx, id, "onboarding")
	if err := s.st.MarkLeadWon(ctx, l.ID, id, s.now()); err != nil {
		s.internal(w, "lead won", err)
		return
	}
	s.audit(r, "admin", "lead.venue", id, fmt.Sprintf("from lead %d, slug %s", l.ID, slug))
	redirect(w, r, fmt.Sprintf("/admin/venues/%d?created=1", id))
}

func (s *Server) deleteLead(w http.ResponseWriter, r *http.Request) {
	l, ok := s.loadLead(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	if r.FormValue("confirm") != "1" {
		s.flash(w, "Tick the confirmation to delete.")
		return
	}
	if err := s.st.DeleteLead(r.Context(), l.ID); err != nil {
		s.internal(w, "delete lead", err)
		return
	}
	s.audit(r, "admin", "lead.delete", 0, fmt.Sprintf("lead %d", l.ID))
	redirect(w, r, "/admin/leads")
}

// suggestSlug turns "Brijačnica Đuro & Sin" into "brijacnica-djuro-sin",
// unique and not reserved.
func (s *Server) suggestSlug(ctx context.Context, name string) string {
	repl := strings.NewReplacer("č", "c", "ć", "c", "š", "s", "ž", "z", "đ", "dj", "Č", "c", "Ć", "c", "Š", "s", "Ž", "z", "Đ", "dj")
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(repl.Replace(name)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if len(base) > 36 {
		base = strings.Trim(base[:36], "-")
	}
	if base == "" {
		base = "salon"
	}
	slug := base
	for i := 2; i < 100; i++ {
		if store.ValidSlug(slug) {
			if _, err := s.st.VenueBySlug(ctx, slug); err != nil {
				return slug
			}
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return slug
}
