package admin

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"guzva/internal/i18n"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// The console pages (admin redesign, 16 Oct 2026): Overview, Venues, New
// venue, Cameras, Alerts & email, Activity — one shell (parts.html
// "admin-open"/"admin-close") with the side rail and its counts.

// navModel feeds the rail: counts beside the pages where something waits.
type navModel struct {
	Alerts  int
	Leads   int
	Version string
}

// Version is the build stamp shown under the rail; set by main.
var Version = ""

func (s *Server) nav(ctx context.Context) navModel {
	n := navModel{Version: Version}
	if open, err := s.st.OpenAlerts(ctx); err == nil {
		n.Alerts = len(open)
	}
	if c, err := s.st.CountLeads(ctx, "new"); err == nil {
		n.Leads = c
	}
	return n
}

// board is the venue + camera state every console page builds from.
type board struct {
	Venues  []venueView
	Free    []store.Venue // venues without a camera
	Devices []store.Device
	Names   map[int64]string
}

func (s *Server) board(ctx context.Context) (*board, error) {
	venues, err := s.st.ListVenues(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	views, err := s.st.VisitViewsSince(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	b := &board{Devices: devices, Names: map[int64]string{}}
	byVenue := map[int64][]deviceView{}
	for _, d := range devices {
		byVenue[d.VenueID] = append(byVenue[d.VenueID], newDeviceView(d, now))
	}
	// Needs attention first (design-pass2 §6): no signal beats late, late
	// beats fresh; a venue with no frame yet is last (usually a setup in
	// progress). Stable sort, so nothing jumps around while you look.
	attention := map[venue.Freshness]int{venue.Stale: 0, venue.Late: 1, venue.Fresh: 2, venue.Unknown: 3}
	for _, v := range venues {
		b.Names[v.ID] = v.Name
		if len(byVenue[v.ID]) == 0 {
			b.Free = append(b.Free, v)
		}
		fr := venue.Classify(v.LastFrameAt, now)
		vv := venueView{ID: v.ID, Slug: v.Slug, Name: v.Name, LastAge: age(now, v.LastFrameAt),
			Devices: byVenue[v.ID], Views7: views[v.ID], attention: attention[fr]}
		if s.Stats != nil {
			vv.Live = s.Stats.Live(v.Slug)
		}
		switch fr {
		case venue.Fresh:
			vv.Fresh = chip{"fresh", "Fresh"}
		case venue.Late:
			vv.Fresh = chip{"late", "Late"}
		case venue.Stale:
			vv.Fresh = chip{"stale", "No signal"}
		default:
			vv.Fresh = chip{"unknown", "No frame yet"}
		}
		b.Venues = append(b.Venues, vv)
	}
	slices.SortStableFunc(b.Venues, func(x, y venueView) int { return x.attention - y.attention })
	return b, nil
}

// attentionItem is one line of the Overview's "Needs you now".
type attentionItem struct {
	Cls, Text, Href string
}

type overviewModel struct {
	Nav       navModel
	Items     []attentionItem
	Venues    []venueView
	Cameras   int
	Views7    int64
	ShowEmpty bool
}

func (s *Server) render(w http.ResponseWriter, name string, model any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, name, model); err != nil {
		s.log.Error("admin: render", "page", name, "err", err.Error())
	}
}

// overview is the console home: what needs you, then the venue board.
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	b, err := s.board(r.Context())
	if err != nil {
		s.internal(w, "board", err)
		return
	}
	m := overviewModel{Nav: s.nav(r.Context()), Venues: b.Venues, Cameras: len(b.Devices)}
	if open, err := s.st.OpenAlerts(r.Context()); err == nil {
		var as []store.Alert
		for _, a := range open {
			as = append(as, a)
		}
		sort.Slice(as, func(i, j int) bool { return as[i].RaisedAt.Before(as[j].RaisedAt) })
		for _, a := range as {
			m.Items = append(m.Items, attentionItem{"stale", a.Message, "/admin/alerts"})
		}
	}
	if m.Nav.Leads > 0 {
		m.Items = append(m.Items, attentionItem{"late", plural(m.Nav.Leads, "new application", "new applications") + " to answer", "/admin/leads"})
	}
	for _, v := range b.Venues {
		m.Views7 += v.Views7
		if len(v.Devices) == 0 {
			m.Items = append(m.Items, attentionItem{"unknown", v.Name + " has no camera yet", "/admin/cameras"})
		}
	}
	m.ShowEmpty = len(m.Items) == 0
	s.render(w, "fleet.html", m)
}

type venuesModel struct {
	Nav     navModel
	Venues  []venueView
	Created bool
	Deleted string
}

func (s *Server) venuesList(w http.ResponseWriter, r *http.Request) {
	b, err := s.board(r.Context())
	if err != nil {
		s.internal(w, "board", err)
		return
	}
	s.render(w, "venues.html", venuesModel{Nav: s.nav(r.Context()), Venues: b.Venues,
		Deleted: r.URL.Query().Get("deleted")})
}

type venueNewModel struct {
	Nav  navModel
	Days [7]venue.DayForm
}

func (s *Server) venueNew(w http.ResponseWriter, r *http.Request) {
	defHours, _ := venue.ParseSpec("Mo-Fr 09:00-17:00")
	s.render(w, "venue_new.html", venueNewModel{Nav: s.nav(r.Context()),
		Days: venue.EditModel(defHours, i18n.For(i18n.EN).DayNames())})
}

type camerasModel struct {
	Nav    navModel
	Rows   []cameraRow
	Free   []store.Venue
	NoCams bool
}

type cameraRow struct {
	deviceView
	VenueID int64
	Venue   string
}

func (s *Server) cameras(w http.ResponseWriter, r *http.Request) {
	b, err := s.board(r.Context())
	if err != nil {
		s.internal(w, "board", err)
		return
	}
	now := s.now().UTC()
	m := camerasModel{Nav: s.nav(r.Context()), Free: b.Free, NoCams: len(b.Devices) == 0}
	for _, d := range b.Devices {
		m.Rows = append(m.Rows, cameraRow{newDeviceView(d, now), d.VenueID, b.Names[d.VenueID]})
	}
	s.render(w, "cameras.html", m)
}

type alertsModel struct {
	Nav    navModel
	Alerts []store.Alert
	Mail   []store.OutMail
	MailOn bool
}

func (s *Server) alertsPage(w http.ResponseWriter, r *http.Request) {
	m := alertsModel{Nav: s.nav(r.Context()), MailOn: s.MailOn}
	if open, err := s.st.OpenAlerts(r.Context()); err == nil {
		for _, a := range open {
			m.Alerts = append(m.Alerts, a)
		}
		sort.Slice(m.Alerts, func(i, j int) bool { return m.Alerts[i].RaisedAt.Before(m.Alerts[j].RaisedAt) })
	}
	if mail, err := s.st.RecentMail(r.Context(), 30); err == nil {
		m.Mail = mail
	}
	s.render(w, "alerts.html", m)
}

type activityModel struct {
	Nav   navModel
	Audit []auditView
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	b, err := s.board(r.Context())
	if err != nil {
		s.internal(w, "board", err)
		return
	}
	m := activityModel{Nav: s.nav(r.Context())}
	if entries, err := s.st.RecentAudit(r.Context(), 0, 100); err == nil {
		for _, e := range entries {
			m.Audit = append(m.Audit, auditView{e, b.Names[e.VenueID]})
		}
	}
	s.render(w, "activity.html", m)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
