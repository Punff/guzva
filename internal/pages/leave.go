package pages

import (
	"io"
	"net/http"
	"strings"

	"guzva/internal/stats"
	"guzva/internal/store"
)

// LeavePath takes the public page's "this tab stopped watching" beacon.
const LeavePath = "/leave"

// Leave answers the beacon: body "<slug> <tab id>", memory only (no DB,
// D5), always 204 — the page cannot act on an answer while it unloads,
// and a bad body is simply ignored.
func Leave(rec *stats.Recorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 128))
		slug, tab, ok := strings.Cut(strings.TrimSpace(string(b)), " ")
		if ok && rec != nil && store.ValidSlug(slug) {
			rec.Leave(slug, tab)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
