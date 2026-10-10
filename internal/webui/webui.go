// Package webui holds the compiled stylesheets (DESIGN.md §4).
//
// Two entries, two consumers, one build step (`make css`):
//
//	public.css — scans internal/pages/templates only, INLINED into each
//	             venue's index.html, because D5 allows exactly two files in
//	             the web root. There is no third asset.
//	app.css    — scans internal/admin/templates only, served to both
//	             consoles at /admin/app.css.
//
// Both are compiled by the standalone Tailwind CLI from web/tokens.css.
// Never a CDN, never a runtime compiler, never a third-party request (§13).
package webui

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

//go:embed app.css
var appCSS []byte

//go:embed public.css
var publicCSS []byte

// App returns the compiled console stylesheet.
func App() []byte { return appCSS }

// Public returns the compiled public-page stylesheet, for inlining.
func Public() []byte { return publicCSS }

// appModTime is a fixed epoch: the bytes themselves are the version, and a
// real timestamp would imply freshness the embedded build cannot have.
var appModTime = time.Unix(0, 0)

// Handler serves the console stylesheet. It is embedded, so it only changes
// when a new binary ships; a content ETag plus a day of caching is honest.
func Handler() http.Handler {
	sum := sha256.Sum256(appCSS)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(appCSS)))
		http.ServeContent(w, r, "app.css", appModTime, bytes.NewReader(appCSS))
	})
}
