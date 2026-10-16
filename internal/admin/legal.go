package admin

import (
	"html/template"
	"net/http"

	"guzva/internal/qr"
	"guzva/internal/store"
)

// The legal pack (business plan P2): what a salon needs under the BiH data
// protection law before the camera runs — a notice at the entrance, a
// written decision on the processing, and the service terms. The notice is
// facts only. The decision and the terms are DRAFTS until a lawyer has read
// them, and they print that way.

type legalModel struct {
	Doc       string // notice | odluka | terms
	Venue     store.Venue
	PageURL   string
	PolicyURL string
	QR        template.HTML
	Hours     string
	Owner     bool // rendered from the owner panel (back link differs)
}

func (s *Server) legalModel(r *http.Request, v *store.Venue, doc string) legalModel {
	base := s.publicBase(r)
	m := legalModel{Doc: doc, Venue: *v, PageURL: base + "/" + v.Slug + "/", PolicyURL: base + "/privatnost/"}
	if code, err := qr.Encode(m.PolicyURL); err == nil {
		m.QR = template.HTML(code.SVG("#12202d")) // built from module coordinates, never user text
	}
	if h, err := s.st.Hours(r.Context(), v.ID); err == nil && h.Configured() {
		m.Hours = h.Spec()
	}
	return m
}

func (s *Server) legalDoc(w http.ResponseWriter, r *http.Request) {
	v, ok := s.loadVenue(w, r)
	if !ok {
		return
	}
	doc := r.PathValue("doc")
	if doc != "notice" && doc != "odluka" && doc != "terms" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "legal.html", s.legalModel(r, v, doc))
}

// ownerNotice lets the owner print the entrance notice themselves.
func (s *Server) ownerNotice(w http.ResponseWriter, r *http.Request, v *store.Venue) {
	m := s.legalModel(r, v, "notice")
	m.Owner = true
	s.render(w, "legal.html", m)
}
