package pages

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/webui"
)

// PrivacyPath is the one public page that is not a venue: the "O
// privatnosti" target the footer links to (PLAN §13). Its first segment is
// reserved from the slug namespace (store.ValidSlug), so no venue can
// shadow it or be shadowed by it.
const PrivacyPath = "/privatnost/"

// Privacy renders the privacy page once and serves it from memory — it
// changes only with a new binary, and a file would break the web root's
// exactly-two-files-per-venue rule (D5). Same headers, CSP and gzip path as
// a venue page; never counted as a visit.
func Privacy() (http.Handler, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("pages: parse templates: %w", err)
	}
	cat := i18n.For(i18n.HR) // the public default; venues are Croatian-first (D7)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "privacy.html", struct {
		Lang string
		Cat  *i18n.Catalog
		CSS  template.CSS
	}{string(cat.Lang), cat, template.CSS(webui.Public())}); err != nil {
		return nil, fmt.Errorf("pages: render privacy: %w", err)
	}
	body := buf.Bytes()
	built := time.Now()
	cache := &pageCache{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		securityHeaders(w)
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		cache.serve(w, r, "privatnost", built, body)
	}), nil
}
