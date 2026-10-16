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

var appHash = func() string {
	sum := sha256.Sum256(appCSS)
	return hex.EncodeToString(sum[:])[:8]
}()

// AppPath is the console stylesheet's URL, hashed by content
// (DESIGN.md §4: app.[hash].css). A new build is therefore a NEW URL, so a
// browser holding yesterday's stylesheet cannot possibly serve it for the new
// templates — which is exactly the bug this replaces: the sheet was served at
// a fixed /admin/app.css under a long max-age, so browsers kept an old one
// while the templates moved on, and every new component class silently did
// nothing.
func AppPath() string { return "/admin/app." + appHash + ".css" }

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
		// Safe to cache forever: the filename carries the hash, so this URL's
		// bytes can never change.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(appCSS)))
		http.ServeContent(w, r, "app.css", appModTime, bytes.NewReader(appCSS))
	})
}
