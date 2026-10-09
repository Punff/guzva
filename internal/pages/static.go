// Package pages: the static file handler behind the public side (§6.4).
// It serves exactly what Writer wrote — index.html and latest.jpg per venue
// — and nothing else: no directory listings, no traversal, no method other
// than GET/HEAD. The privacy proof ("the web root contains only
// index.html + latest.jpg per venue") lives in pages_test.go's disk scan.
package pages

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Static serves the public web root rooted at dir.
func Static(dir string) http.Handler {
	root := filepath.Clean(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "405 — method not allowed (GET only)", http.StatusMethodNotAllowed)
			return
		}
		// "/" is a lost visitor: there is no index or venue list (§6.4).
		clean := path.Clean(r.URL.Path)
		if clean == "/" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 — there is no page here.\n\n" +
				"Venue pages live at /<slug>/ — one URL per venue, never a list.\n"))
			return
		}
		full := filepath.Join(root, filepath.FromSlash(clean))
		if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
			// path.Clean already prevents "..", this is belt and braces.
			http.NotFound(w, r)
			return
		}
		if fi, err := os.Stat(full); err == nil && fi.IsDir() {
			// Serve a directory only as its index.html — never list it.
			full = filepath.Join(full, "index.html")
		}
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		// Matches the meta refresh: a stale-but-valid page beats a cache miss.
		w.Header().Set("Cache-Control", "public, max-age=30")
		http.ServeContent(w, r, filepath.Base(full), fi.ModTime(), f)
	})
}
