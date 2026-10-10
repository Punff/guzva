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
			// The canonical form of a venue page is /<slug>/: the only
			// relative URL on it (latest.jpg) resolves against the
			// directory, and links shared to a phone usually arrive
			// without the slash. Redirect instead of serving the page
			// from a path whose relative links would 404.
			if !strings.HasSuffix(r.URL.Path, "/") {
				u := *r.URL
				u.Path = path.Clean(clean) + "/"
				http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
				return
			}
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
		// One policy for both files: revalidate.
		//
		// latest.jpg is NOT served immutable even though ?v= is unique per
		// frame, because the file behind that URL is mutable: it is
		// overwritten by the next frame. A client holding a stale ?v= (an
		// old index.html, a backgrounded tab) would fetch CURRENT bytes
		// under the OLD version's URL and pin the mismatch for the whole
		// immutable lifetime. Correctness beats saving one 16 KB re-fetch.
		//
		// index.html additionally inlines the CSS and JS, so a cached copy
		// is a visitor running an old build — that is the "it struggles to
		// pick it up after a deploy" symptom. no-cache still allows the
		// cheap conditional GET, so the common case is a 304.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, filepath.Base(full), fi.ModTime(), f)
	})
}
