package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// healthz answers the dead-man watcher and uptime monitors (§6.6): unauth,
// no venue data, no secrets.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"ok":       true,
		"version":  s.Version,
		"uptime_s": int(time.Since(s.Started).Seconds()),
	}
	if s.DiskFreePct != nil {
		if pct, err := s.DiskFreePct(); err == nil {
			body["disk_free_pct"] = fmt.Sprintf("%.1f", pct)
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(body)
}

// metrics is the hand-rolled Prometheus endpoint (§2, §6.6). Content comes
// from the injected callback; Caddy never proxies this path (deploy/Caddyfile).
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if s.Metrics != nil {
		_, _ = fmt.Fprint(w, s.Metrics())
	}
}
