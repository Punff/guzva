// Package pages: the static file handler behind the public side (§6.4).
// It serves exactly what Writer wrote — index.html + latest.png per venue
// — and nothing else: no directory listings, no traversal, no method other
// than GET/HEAD. The privacy proof ("the web root contains only
// index.html + one still per venue") lives in pages_test.go's disk scan.
package pages

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Static serves the public web root rooted at dir.
func Static(dir string) http.Handler {
	root := filepath.Clean(dir)
	cache := &pageCache{}
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
			// relative URL on it (the still) resolves against the
			// directory, and links shared to a phone usually arrive
			// without the slash. Redirect instead of serving the page from a
			// path whose relative links would 404.
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

		// One cache policy for both files: revalidate.
		//
		// The still is NOT served immutable even though ?v= is unique per
		// frame, because the file behind that URL is mutable: the next frame
		// overwrites it. A client holding a stale ?v= (an old index.html, a
		// backgrounded tab) would fetch CURRENT bytes under the OLD
		// version's URL and pin the mismatch for the whole immutable
		// lifetime. Correctness beats saving one small re-fetch.
		//
		// index.html additionally inlines the CSS and JS, so a cached copy
		// is a visitor running an old build — the "it struggles to pick it
		// up after a deploy" symptom. no-cache still allows the conditional
		// GET, so the common case is a 304.
		w.Header().Set("Cache-Control", "no-cache")
		securityHeaders(w)
		// ⚑7: a venue's interior should not surface in a search index.
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")

		if filepath.Base(full) == "index.html" {
			body, err := io.ReadAll(f)
			if err != nil {
				http.Error(w, "500 — internal error", http.StatusInternalServerError)
				return
			}
			cache.serve(w, r, filepath.Dir(full), fi.ModTime(), body)
			return
		}

		// Stills are already compressed (PNG or JPEG); gzip would only cost
		// CPU. The type follows the file: during the v1→v2 transition a venue
		// may still publish latest.jpg until its first v2 frame, and serving
		// JPEG bytes as image/png would break the picture (nosniff is set).
		switch strings.ToLower(filepath.Ext(full)) {
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		case ".jpg", ".jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
		default:
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, filepath.Base(full), fi.ModTime(), f)
	})
}

// securityHeaders are the unconditional ones (DESIGN.md §7.4). All free, and
// none of them depends on the page being correct.
func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

// pageCache holds one venue page compressed, plus its CSP string, keyed by
// path+mtime. The web root must stay exactly two files, so there are no .gz
// siblings to precompute; compressing on the fly and remembering the result is
// the same trade with none of the extra files (§13).
type pageCache struct {
	mu    sync.Mutex
	key   string
	mtime time.Time
	plain []byte
	gz    []byte
	csp   string
}

// pageBudget bounds what stays resident per venue page. A page is ~20 KB;
// anything vastly larger is a bug or an attack and is not cached.
const pageBudget = 256 << 10

func (c *pageCache) get(key string, mtime time.Time, body []byte) (gz []byte, csp string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == key && c.mtime.Equal(mtime) && len(c.plain) == len(body) {
		return c.gz, c.csp
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(body)
	_ = zw.Close()
	csp = buildCSP(body)
	if len(body) <= pageBudget && buf.Len() <= pageBudget {
		// Single-slot on purpose: one operator, a handful of venues, and a
		// map keyed by slug would grow without bound.
		c.key, c.mtime, c.plain, c.gz, c.csp = key, mtime, body, buf.Bytes(), csp
		return c.gz, c.csp
	}
	c.key, c.mtime, c.plain, c.gz, c.csp = "", time.Time{}, nil, nil, ""
	return buf.Bytes(), csp
}

func (c *pageCache) serve(w http.ResponseWriter, r *http.Request, dir string, mtime time.Time, body []byte) {
	gz, csp := c.get(filepath.Join(dir, "index.html"), mtime, body)
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !acceptsGzip(r) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(gz)
	}
}

func acceptsGzip(r *http.Request) bool {
	for _, v := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(v, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

// buildCSP hashes the two inline blocks, so style-src and script-src need no
// 'unsafe-inline'. The page has exactly one <style> (the design system) and
// one <script> (the swap loop), and both are byte-identical for every venue —
// per-venue data lives in the markup, never in the script.
func buildCSP(page []byte) string {
	var sb strings.Builder
	sb.WriteString("default-src 'none'; img-src 'self' blob: data:; connect-src 'self'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	// 'unsafe-hashes' is required for hashes to apply to style ATTRIBUTES;
	// without it the browser rejects the page's one inline style (the frame
	// box's aspect-ratio) and logs a violation. The value still has to match,
	// so this is not a blanket allow — it is the hash list that decides.
	sb.WriteString("; style-src 'unsafe-hashes'")
	for _, h := range append(inlineHashes(page, "style"), styleAttrHashes(page)...) {
		sb.WriteString(" 'sha256-" + h + "'")
	}
	sb.WriteString("; script-src 'unsafe-hashes'")
	for _, h := range inlineHashes(page, "script") {
		sb.WriteString(" 'sha256-" + h + "'")
	}
	return sb.String()
}

// styleAttrHashes returns the base64 sha256 of every style="" value, which
// CSP needs alongside 'unsafe-hashes'. The page has exactly one (the frame
// box's aspect-ratio) and it is derived from the render's dimensions.
func styleAttrHashes(page []byte) []string {
	var out []string
	for _, m := range styleAttrRe.FindAllSubmatch(page, -1) {
		sum := sha256.Sum256(m[1])
		out = append(out, base64.StdEncoding.EncodeToString(sum[:]))
	}
	return out
}

var styleAttrRe = regexp.MustCompile(`(?s)style="([^"]*)"`)

// inlineHashes returns the base64 sha256 of every <tag>…</tag> body in page.
func inlineHashes(page []byte, tag string) []string {
	var out []string
	s := string(page)
	open, close := "<"+tag+">", "</"+tag+">"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return out
		}
		j := strings.Index(s[i:], close)
		if j < 0 {
			return out
		}
		sum := sha256.Sum256([]byte(s[i+len(open) : i+j]))
		out = append(out, base64.StdEncoding.EncodeToString(sum[:]))
		s = s[i+j+len(close):]
	}
}
