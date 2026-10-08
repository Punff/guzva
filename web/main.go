package main

// POC local console for one ESP32-CAM capture device.
// Stdlib only. One process:
//   - background poller: /jpg -> data/latest.jpg, /api/status + /api/settings
//     snapshots, freshness meta (stylised-identity ready: raw never served
//     to the public page, only the stylised render).
//   - public page  /      : venue still (stylised) + freshness via htmx.
//   - admin panel  /admin : device camera settings (proxied to ESP32) +
//     venue/site settings (name, style) stored in data/site.json.
//   - /health, /latest.jpg, /stylized.jpg, /frag/fresh helpers.
//
// Run: go run . --device http://192.168.1.8 --interval 60 --port 8000

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Venue struct {
	Name        string `json:"name"`
	Style       string `json:"style"` // raw | poster | mono
	IntervalSec int    `json:"interval_sec"`
}

func clampInterval(iv int) int {
	if iv < 5 {
		return 5
	}
	if iv > 3600 {
		return 3600
	}
	return iv
}

type Snapshot struct {
	JPG      []byte
	JPGAt    time.Time
	JPGSize  int
	JPGHash  string // fnv64a of last good frame: proves a *new* capture
	Same     int    // identical frames in a row (static scene, not stale feed)
	Captures int64  // device capture counter at last poll (-1 unknown)
	Status   []byte // raw /api/status body
	Settings []byte // raw /api/settings body
	Fails    int
	LastErr  string
	Venue    Venue
}

var (
	device      string
	intervalSec int
	interval    time.Duration
	port        int
	dataDir     string

	mu   sync.RWMutex
	snap Snapshot

	publicTmpl *template.Template
	adminTmpl  *template.Template
)

func venuePath() string { return filepath.Join(dataDir, "site.json") }

func loadVenue(fallbackInterval int) Venue {
	v := Venue{Name: "Jadro", Style: "poster", IntervalSec: fallbackInterval}
	b, err := os.ReadFile(venuePath())
	if err != nil {
		v.IntervalSec = clampInterval(v.IntervalSec)
		return v
	}
	_ = json.Unmarshal(b, &v)
	if v.Style != "raw" && v.Style != "poster" && v.Style != "mono" {
		v.Style = "poster"
	}
	if v.IntervalSec <= 0 {
		v.IntervalSec = fallbackInterval
	}
	v.IntervalSec = clampInterval(v.IntervalSec)
	return v
}

func saveVenue(v Venue) {
	b, _ := json.MarshalIndent(v, "", "  ")
	_ = os.WriteFile(venuePath(), b, 0644)
}

func get(url string, timeout time.Duration) ([]byte, error) {
	c := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "esp32cam-go/1.0")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// --- stylise (stdlib only): raw frame -> identity-safe render ---

func stylize(src []byte, style string) []byte {
	if style == "raw" {
		return src
	}
	img, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		return src
	}
	b := img.Bounds()
	// Downscale to max 480 wide (nearest): hides sensor noise, tiny output.
	const maxW = 480
	w, h := b.Dx(), b.Dy()
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	// Posterize to 4 levels per channel (+ grayscale for mono).
	const levels = 4
	for i, p := range dst.Pix {
		v := int(p) * levels / 256 * (255 / (levels - 1))
		if v > 255 {
			v = 255
		}
		dst.Pix[i] = uint8(v)
		_ = i
	}
	if style == "mono" {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, _ := dst.At(x, y).RGBA()
				lum := uint8((r*30 + g*59 + bl*11) / 100 >> 8)
				lum = lum / (256 / levels) * (255 / (levels - 1))
				dst.Set(x, y, color.Gray{Y: lum})
			}
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 70}); err != nil {
		return src
	}
	return out.Bytes()
}

// --- frame identity: fnv64a hash proves a *new* capture vs static scene ---

func hashBytes(b []byte) string {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	return fmt.Sprintf("%016x", h)
}

func parseCaptures(statusBody []byte) int64 {
	var m map[string]any
	if json.Unmarshal(statusBody, &m) != nil {
		return -1
	}
	if v, ok := m["captures"].(float64); ok {
		return int64(v)
	}
	return -1
}

func pollOnce() {
	ts := time.Now()
	jpg, err := get(device+"/jpg?t="+fmt.Sprint(ts.Unix()), 45*time.Second)
	jpgOK := err == nil && len(jpg) >= 512 && jpg[0] == 0xff && jpg[1] == 0xd8
	var jpgErr string
	if !jpgOK {
		if err == nil {
			err = fmt.Errorf("not a JPEG (%d bytes)", len(jpg))
		}
		jpgErr = err.Error()
	}
	status, err := get(device+"/api/status", 15*time.Second)
	statusOK := err == nil && json.Valid(status)
	settings, err := get(device+"/api/settings", 15*time.Second)
	if err == nil && json.Valid(settings) {
		mu.Lock()
		snap.Settings = settings
		_ = os.WriteFile(filepath.Join(dataDir, "settings.json"), settings, 0644)
		mu.Unlock()
	}

	mu.Lock()
	defer mu.Unlock()
	if jpgOK {
		h := hashBytes(jpg)
		if h == snap.JPGHash && snap.JPGHash != "" {
			snap.Same++
		} else {
			snap.JPGHash, snap.Same = h, 0
		}
		snap.JPG, snap.JPGAt, snap.JPGSize = jpg, ts, len(jpg)
		snap.Fails, snap.LastErr = 0, ""
		_ = os.WriteFile(filepath.Join(dataDir, "latest.jpg"), jpg, 0644)
	} else {
		snap.Fails++
		snap.LastErr = jpgErr
	}
	if statusOK {
		snap.Status = status
		snap.Captures = parseCaptures(status)
		_ = os.WriteFile(filepath.Join(dataDir, "device.json"), status, 0644)
	}
	meta, _ := json.Marshal(map[string]any{
		"ok": snap.JPGAt.Format(time.RFC3339), "size": snap.JPGSize,
		"hash": snap.JPGHash, "same_streak": snap.Same,
		"captures": snap.Captures,
		"failures": snap.Fails, "error": snap.LastErr,
	})
	_ = os.WriteFile(filepath.Join(dataDir, "status.json"), meta, 0644)
	log.Printf("poll jpg=%v status=%v fails=%d",
		jpgOK, statusOK, snap.Fails)
}

// --- HTTP ---

func fragFresh() string {
	mu.RLock()
	defer mu.RUnlock()
	if snap.JPGAt.IsZero() {
		return "No frame yet — poller is fetching from the device…"
	}
	ago := int(time.Since(snap.JPGAt).Seconds())
	if snap.Fails > 0 {
		return fmt.Sprintf("STALE — last good frame %ds ago (%d KB), failing %dx: %s",
			ago, snap.JPGSize/1024, snap.Fails, snap.LastErr)
	}
	base := fmt.Sprintf("Updated %ds ago • frame %s • %d KB • dev captures %d",
		ago, shortHash(snap.JPGHash), snap.JPGSize/1024, snap.Captures)
	if snap.Same > 0 {
		base += fmt.Sprintf(" • same scene ×%d (static room, feed is live)", snap.Same+1)
	}
	return base
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func handlePublic(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	v := snap.Venue
	mu.RUnlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = publicTmpl.Execute(w, map[string]any{"Venue": v, "Interval": v.IntervalSec})
}

func handleFragStill(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<img src="/stylized.jpg?t=%d" alt="venue still" style="width:100%%;background:#000;border-radius:12px;display:block">`,
		time.Now().Unix())
}

func handleFragFresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, template.HTMLEscapeString(fragFresh()))
}

func handleLatest(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	jpg := snap.JPG
	mu.RUnlock()
	if len(jpg) == 0 {
		http.Error(w, "no frame yet", 404)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(jpg)
}

func handleStylized(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	jpg := snap.JPG
	style := snap.Venue.Style
	mu.RUnlock()
	if len(jpg) == 0 {
		http.Error(w, "no frame yet", 404)
		return
	}
	out := stylize(jpg, style)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"device": device, "venue": snap.Venue,
		"last_jpg_at":   snap.JPGAt.Format(time.RFC3339),
		"last_jpg_size": snap.JPGSize, "last_jpg_hash": snap.JPGHash,
		"same_streak": snap.Same, "dev_captures": snap.Captures,
		"failures":   snap.Fails,
		"last_error": snap.LastErr, "has_status": len(snap.Status) > 0,
	})
}

// Proxy POST/GET with a body or query (actions) straight to the device.
func proxyToDevice(w http.ResponseWriter, r *http.Request) {
	u := device + r.URL.RequestURI()
	body, _ := io.ReadAll(r.Body)
	req, _ := http.NewRequest(r.Method, u, bytes.NewReader(body))
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("User-Agent", "esp32cam-go/1.0")
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		http.Error(w, "device unreachable: "+err.Error(), 502)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}

func venueFormHTML(v Venue, note string) string {
	sel := func(val, opt string) string {
		if val == opt {
			return "selected"
		}
		return ""
	}
	return fmt.Sprintf(`<div id="venueform">
<form hx-post="/admin/venue" hx-target="#venueform" hx-swap="outerHTML" hx-indicator="#vspinner">
<label>Name</label><input name="name" value="%s">
<label>Style (public image identity)</label>
<select name="style">
<option value="poster" %s>poster (colour, stylised)</option>
<option value="mono" %s>mono (grey, stylised)</option>
<option value="raw" %s>raw (debug only — shows faces)</option>
</select>
<label>Still interval (seconds, 5–3600)</label>
<input name="interval" type="number" min="5" max="3600" value="%d">
<p class="hint">Raw is debug-only: it bypasses the privacy-safe render. The poller and this page pick up a new interval immediately — no restart.</p>
<button>Save venue <span id="vspinner" class="htmx-indicator">…</span></button>
</form>
%s</div>`,
		template.HTMLEscapeString(v.Name),
		sel(v.Style, "poster"), sel(v.Style, "mono"), sel(v.Style, "raw"),
		v.IntervalSec, note)
}

// Venue settings save (htmx fragment): name + style + poll interval.
func handleVenueFrag(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	iv := snapVenueInterval()
	if s := r.FormValue("interval"); s != "" {
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
			iv = clampInterval(n)
		}
	}
	style := r.FormValue("style")
	if style != "raw" && style != "poster" && style != "mono" {
		style = "poster"
	}
	mu.Lock()
	snap.Venue.Name = r.FormValue("name")
	if snap.Venue.Name == "" {
		snap.Venue.Name = "Jadro"
	}
	snap.Venue.Style = style
	snap.Venue.IntervalSec = iv
	saveVenue(snap.Venue)
	v := snap.Venue
	mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, venueFormHTML(v,
		`<p class="ok">Saved `+time.Now().Format("15:04:05")+
			` — polling every `+fmt.Sprint(iv)+`s.</p>`))
}

func snapVenueInterval() int {
	mu.RLock()
	defer mu.RUnlock()
	return snap.Venue.IntervalSec
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	data := map[string]any{
		"Venue":  snap.Venue,
		"Device": device,
	}
	var cam map[string]any
	if json.Unmarshal(snap.Settings, &cam) == nil {
		data["Cam"] = cam
	}
	mu.RUnlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminTmpl.Execute(w, data)
}
func handleCameraFrag(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	form := url.Values{}
	for k, vs := range r.Form {
		if len(k) > 4 && k[:4] == "cam_" && len(vs) > 0 && vs[0] != "" {
			form.Set(k[4:], vs[0])
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(form) == 0 {
		fmt.Fprint(w, `<p class="hint">Nothing to send.</p>`)
		return
	}
	preq, _ := http.NewRequest(http.MethodPost, device+"/api/settings",
		bytes.NewBufferString(form.Encode()))
	preq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	preq.Header.Set("User-Agent", "esp32cam-go/1.0")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(preq)
	if err != nil {
		fmt.Fprintf(w, `<p class="hint">Device unreachable: %s</p>`,
			template.HTMLEscapeString(err.Error()))
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(w, `<p class="hint">Device refused (HTTP %d).</p>`, resp.StatusCode)
		return
	}
	fmt.Fprint(w, `<p class="ok">Applied &amp; saved on device at `+
		time.Now().Format("15:04:05")+`.</p>`)
}

func main() {
	flag.StringVar(&device, "device", "http://192.168.1.8", "ESP32-CAM base URL")
	flag.IntVar(&intervalSec, "interval", 60, "poll interval in seconds")
	flag.IntVar(&port, "port", 8000, "listen port")
	flag.StringVar(&dataDir, "data", "data", "data directory")
	flag.Parse()
	interval = time.Duration(intervalSec) * time.Second

	_ = os.MkdirAll(dataDir, 0755)
	mu.Lock()
	snap.Venue = loadVenue(intervalSec)
	snap.Captures = -1
	mu.Unlock()

	var err error
	publicTmpl, err = template.ParseFiles("templates/public.html")
	if err != nil {
		log.Fatal(err)
	}
	adminTmpl, err = template.ParseFiles("templates/admin.html")
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			pollOnce()
			// Interval is a live venue setting: no restart needed.
			iv := snapVenueInterval()
			if iv < 5 {
				iv = 5
			}
			time.Sleep(time.Duration(iv) * time.Second)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handlePublic)
	mux.HandleFunc("/frag/fresh", handleFragFresh)
	mux.HandleFunc("/frag/still", handleFragStill)
	mux.HandleFunc("/latest.jpg", handleLatest)
	mux.HandleFunc("/stylized.jpg", handleStylized)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/admin", handleAdmin)
	mux.HandleFunc("/admin/venue", handleVenueFrag)
	mux.HandleFunc("/admin/camera", handleCameraFrag)
	// Live device actions (capture/files/gpio/illumination/reboot…).
	mux.HandleFunc("/capture", proxyToDevice)
	mux.HandleFunc("/photo", proxyToDevice)
	mux.HandleFunc("/img", proxyToDevice)
	mux.HandleFunc("/download", proxyToDevice)
	mux.HandleFunc("/switch", proxyToDevice)
	mux.HandleFunc("/illumination", proxyToDevice)
	mux.HandleFunc("/data", proxyToDevice)
	mux.HandleFunc("/ping", proxyToDevice)
	mux.HandleFunc("/reboot", proxyToDevice)
	mux.HandleFunc("/api/", proxyToDevice)

	log.Printf("console http://localhost:%d device=%s every=%s", port, device, interval)
	log.Fatal(http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux))
}
