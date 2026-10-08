// Command fakecam is the Phase-1 load/abuse harness from PLAN §3:
// fast / slow / flappy / dup / partial clients against POST /v1/ingest.
//
// The §6.1 gate is: 1 slow + 19 fast clients — fast p95 unaffected. Run:
//
//	go run ./tools/fakecam -addr http://127.0.0.1:8080 -token <tok> \
//	    -fast 19 -slow 1 -dur 20s -interval 1s
//
// Modes:
//
//	fast    normal device: good frame, unique capture_id per tick → 201
//	slow    stalls mid-body past the server's 30 s ReadTimeout — a pass
//	        is the server replying while we are still stalled (it stopped
//	        reading), not waiting for the whole body
//	flappy  alternates good and all-black frames → 201 / 422 churn
//	dup     sends every frame twice (same capture_id) → 201 then 409
//	partial health-only beats, no frame part → 201 (camera-degraded path)
//
// Exit status: 0 when every client saw only its expected outcomes, 1 otherwise.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type modeSpec struct {
	name  string
	count int
}

func main() {
	var (
		addr   = flag.String("addr", "http://127.0.0.1:8080", "base URL of the server")
		token  = flag.String("token", "", "device bearer token (ikv1_…)")
		tokens = flag.String("tokens", "", "comma-separated tokens, round-robined across clients (one per device; beats the per-device limiter)")
		// The per-device bucket admits 0.2 req/s with burst 4 (§6.1). A
		// client must tick slower than that or it earns 429s — default 6 s
		// leaves headroom; pair more -tokens with faster intervals.
		interval = flag.Duration("interval", 6*time.Second, "per-client tick interval (keep ≥ 5 s per token)")
		dur      = flag.Duration("dur", 10*time.Second, "how long to run")
		timeout  = flag.Duration("timeout", 40*time.Second, "per-request timeout (slow needs > 30 s)")
		fast     = flag.Int("fast", 0, "fast clients")
		slow     = flag.Int("slow", 0, "slow clients")
		flappy   = flag.Int("flappy", 0, "flappy clients")
		dup      = flag.Int("dup", 0, "dup clients")
		partial  = flag.Int("partial", 0, "partial (health-only) clients")
	)
	flag.Parse()
	toks := splitTokens(*tokens)
	if len(toks) == 0 && *token != "" {
		toks = []string{*token}
	}
	if len(toks) == 0 {
		fmt.Fprintln(os.Stderr, "fakecam: -token or -tokens is required (run `imaliguzve provision` and copy the printed token)")
		os.Exit(2)
	}
	base := strings.TrimRight(*addr, "/")
	if _, err := url.Parse(base); err != nil {
		fmt.Fprintf(os.Stderr, "fakecam: bad -addr: %v\n", err)
		os.Exit(2)
	}

	modes := []modeSpec{
		{"fast", *fast}, {"slow", *slow}, {"flappy", *flappy},
		{"dup", *dup}, {"partial", *partial},
	}
	total := 0
	for _, m := range modes {
		total += m.count
	}
	if total == 0 {
		modes = []modeSpec{{"fast", 1}} // default: one well-behaved device
		total = 1
	}

	if err := healthz(base); err != nil {
		fmt.Fprintf(os.Stderr, "fakecam: server not healthy: %v\n", err)
		os.Exit(1)
	}

	good := makeJPEG(320, 240, 1)
	black := makeJPEG(320, 240, -1) // -1 → uniform black (quarantine path)

	ctx, cancel := context.WithTimeout(context.Background(), *dur+*timeout+5*time.Second)
	defer cancel()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = map[string]map[string]int{} // mode → outcome → count
		fastMs  []float64
	)
	idx := 0
	for _, m := range modes {
		for c := 0; c < m.count; c++ {
			tok := toks[idx%len(toks)]
			ident := idx // globally unique → capture ids never collide across modes
			idx++
			wg.Add(1)
			go func(mode string, client int, tok string) {
				defer wg.Done()
				stats := map[string]int{}
				var lat []float64
				n := 0
				deadline := time.Now().Add(*dur)
				for time.Now().Before(deadline) {
					n++
					outcome, ms := onePost(ctx, base, tok, mode, client, n, good, black, *timeout)
					stats[outcome]++
					if ms >= 0 {
						lat = append(lat, ms)
					}
					select {
					case <-ctx.Done():
						deadline = time.Now()
					case <-time.After(*interval):
					}
				}
				mu.Lock()
				if results[mode] == nil {
					results[mode] = map[string]int{}
				}
				for k, v := range stats {
					results[mode][k] += v
				}
				if mode == "fast" {
					fastMs = append(fastMs, lat...)
				}
				mu.Unlock()
			}(m.name, ident, tok)
		}
	}
	wg.Wait()

	// Report.
	exit := 0
	for _, m := range modes {
		stats := results[m.name]
		if stats == nil {
			continue
		}
		var parts []string
		for _, k := range sortedKeys(stats) {
			parts = append(parts, fmt.Sprintf("%s=%d", k, stats[k]))
		}
		fmt.Printf("%-8s clients=%d  %s\n", m.name, m.count, strings.Join(parts, "  "))
		if bad := unexpected(m.name, stats); bad > 0 {
			fmt.Fprintf(os.Stderr, "  ^ %d unexpected outcomes in mode %q\n", bad, m.name)
			exit = 1
		}
	}
	if len(fastMs) > 0 {
		sort.Float64s(fastMs)
		fmt.Printf("fast latency: n=%d p50=%.0fms p95=%.0fms p99=%.0fms max=%.0fms\n",
			len(fastMs), pct(fastMs, 50), pct(fastMs, 95), pct(fastMs, 99), fastMs[len(fastMs)-1])
	}
	os.Exit(exit)
}

// onePost performs a single ingest for the given mode and returns the
// outcome label (a status code, or an error class) and latency in ms
// (-1 when no response arrived).
func onePost(ctx context.Context, base, token, mode string, client, n int,
	good, black []byte, timeout time.Duration) (string, float64) {

	cid := captureID(client, n)
	var frame []byte
	healthOnly := false
	double := false
	switch mode {
	case "flappy":
		if n%2 == 0 {
			frame = black
		} else {
			frame = good
		}
	case "dup":
		frame = good
		double = true
	case "partial":
		healthOnly = true
	case "slow":
		frame = good
	default: // fast
		frame = good
	}

	if mode == "slow" {
		return slowPost(ctx, base, token, cid, frame, timeout)
	}

	if healthOnly {
		frame = nil
	}
	body, ctype := multipartBody(metaJSON(cid), frame)

	post := func() (string, float64) {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(rctx, http.MethodPost, base+"/v1/ingest", bytes.NewReader(body))
		if err != nil {
			return "build-error", -1
		}
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Authorization", "Bearer "+token)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return classifyNetErr(err), -1
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return fmt.Sprintf("%d", resp.StatusCode), float64(time.Since(start).Milliseconds())
	}

	out, ms := post()
	if double && out == "201" {
		// Immediate retry with the identical capture_id → 409 (§4).
		out2, ms2 := post()
		if out2 != "409" {
			return "dup-expected-409-got-" + out2, ms2
		}
		return "201+409", ms
	}
	return out, ms
}

// slowPost stalls mid-body for longer than the server's ReadTimeout. A pass
// is the server answering or dropping us WHILE we are still stalled (≈30 s,
// before the writer finished) — proof it stopped reading instead of hanging
// on a slow client (§6.1).
func slowPost(ctx context.Context, base, token, cid string, frame []byte, timeout time.Duration) (string, float64) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	ctype := slowBody(pw, metaJSON(cid), frame, done)
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, base+"/v1/ingest", pr)
	if err != nil {
		return "build-error", -1
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return classifyNetErr(err), -1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	select {
	case <-done:
		// Our whole body was sent before the reply — the server waited for
		// a slow client instead of cutting it off.
		return "server-stalled-" + fmt.Sprintf("%d", resp.StatusCode), float64(time.Since(start).Milliseconds())
	default:
		return "killed-by-server", float64(time.Since(start).Milliseconds())
	}
}

// --- helpers ---------------------------------------------------------------

func splitTokens(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// runBoot makes capture ids unique across fakecam runs (a real device's boot
// counter only grows — NVS). Per-client so parallel clients never collide.
var runBoot = rand.Intn(900_000_000)

func captureID(client, n int) string {
	// C<boot>-<ctr>-<rand4> (§4): boot binds to the fake device+run, ctr to
	// the tick, rand4 to this capture.
	return fmt.Sprintf("C%d-%d-%04x", runBoot+client, n, rand.Intn(0x10000))
}

func metaJSON(cid string) string {
	m := map[string]any{
		"capture_id":      cid,
		"fw":              "fakecam-0",
		"rssi":            -55,
		"free_heap":       180000,
		"cfg_ver_applied": 1,
		"counters":        map[string]any{"boot": 1},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func multipartBody(meta string, frame []byte) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("meta", meta)
	if frame != nil {
		fw, _ := w.CreateFormFile("frame", "frame.jpg")
		_, _ = fw.Write(frame)
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// slowBody streams the request into pw: headers + meta immediately, the
// frame split in half with a stall well past the server's 30 s ReadTimeout
// between the halves. done is closed once the writer has finished.
func slowBody(pw *io.PipeWriter, meta string, frame []byte, done chan<- struct{}) string {
	w := multipart.NewWriter(pw)
	ctype := w.FormDataContentType() // boundary fixed at construction
	go func() {
		defer close(done)
		_ = w.WriteField("meta", meta)
		if fw, err := w.CreateFormFile("frame", "frame.jpg"); err == nil {
			mid := len(frame) / 2
			_, _ = fw.Write(frame[:mid])
			time.Sleep(45 * time.Second)
			_, _ = fw.Write(frame[mid:])
		}
		_ = w.Close()
		_ = pw.Close()
	}()
	return ctype
}

func healthz(base string) error {
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz → %d", resp.StatusCode)
	}
	return nil
}

func classifyNetErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "EOF"), strings.Contains(s, "connection reset"),
		strings.Contains(s, "broken pipe"), strings.Contains(s, "unexpected EOF"):
		return "killed-by-server"
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "Client.Timeout"):
		return "timeout"
	default:
		return "net-error"
	}
}

// unexpected counts outcomes a mode must never see.
func unexpected(mode string, stats map[string]int) int {
	allowed := map[string]map[string]bool{
		"fast":    {"201": true},
		"flappy":  {"201": true, "422": true},
		"dup":     {"201+409": true},
		"partial": {"201": true},
		"slow":    {"killed-by-server": true},
	}[mode]
	bad := 0
	for k, v := range stats {
		if k == "timeout" || k == "net-error" || strings.HasPrefix(k, "dup-expected") {
			bad += v
			continue
		}
		if !allowed[k] {
			bad += v
		}
	}
	return bad
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p / 100)
	return sorted[i]
}

// makeJPEG renders a stable synthetic frame; seed -1 produces uniform black
// (the §7 quarantine case).
func makeJPEG(w, h int, seed int64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if seed < 0 {
				img.Set(x, y, color.RGBA{A: 255})
				continue
			}
			n := rnd.Intn(24) - 12
			img.Set(x, y, color.RGBA{
				R: uint8(clamp(60 + x*120/w + n)),
				G: uint8(clamp(80 + y*100/h + n)),
				B: uint8(clamp(120 + (x+y)*40/(w+h) + n)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
