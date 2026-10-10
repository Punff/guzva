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

/*
 * app.css is the compiled design-system stylesheet: `make css` runs the
 * Tailwind CLI over web/input.css and the Go templates, and the bytes are
 * embedded here. The consoles serve it at /admin/static/app.css; the public
 * page inlines it (D5 forbids a third file in the web root). Never from a
 * CDN, never a runtime compiler.
 */

//go:embed app.css
var appCSS []byte

// CSS returns the compiled stylesheet bytes.
func CSS() []byte { return appCSS }

var cssModTime = time.Unix(0, 0) // Last-Modified is a fixed epoch; the bytes themselves change it.

// Handler serves app.css. The bytes only change on a rebuild, so a day of
// browser caching plus a strong ETag from the content is honest.
func Handler() http.Handler {
	sum := sha256.Sum256(appCSS)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(appCSS)))
		http.ServeContent(w, r, "app.css", cssModTime, bytes.NewReader(appCSS))
	})
}
