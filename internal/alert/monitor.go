package alert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"guzva/internal/store"
)

// Telegram sends messages through a bot (sendMessage). Token and chat come
// from the environment only — never flags, never logs.
type Telegram struct {
	Token, Chat string
	Base        string // https://api.telegram.org; tests point it elsewhere
	Client      *http.Client
}

// Configured reports whether both the bot token and the chat id are set.
func (t *Telegram) Configured() bool { return t != nil && t.Token != "" && t.Chat != "" }

// Send posts one plain-text message.
func (t *Telegram) Send(ctx context.Context, text string) error {
	base := t.Base
	if base == "" {
		base = "https://api.telegram.org"
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	form := url.Values{"chat_id": {t.Chat}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/sendMessage",
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		// The URL holds the token; strip it from the error before it is logged.
		return fmt.Errorf("telegram: %s", strings.ReplaceAll(err.Error(), t.Token, "<token>"))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: status %d", resp.StatusCode)
	}
	return nil
}

// Monitor diffs the rules against the open-alert ledger once per Tick.
type Monitor struct {
	St       *store.Store
	Engine   *Engine
	TG       *Telegram
	Log      *slog.Logger
	Now      func() time.Time
	DiskFree func() (float64, error)
	Started  time.Time
}

// Tick takes a snapshot, evaluates the rules and sends what changed. With
// Telegram unconfigured it still keeps the ledger and logs each change, so
// the admin can see open alerts and nothing is lost when a bot is added.
func (m *Monitor) Tick(ctx context.Context) error {
	now := m.Now().UTC()
	snap, err := m.snapshot(ctx, now)
	if err != nil {
		return err
	}
	conds := m.Engine.Evaluate(snap)
	open, err := m.St.OpenAlerts(ctx)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	for _, c := range conds {
		current[c.Key] = true
		if _, isOpen := open[c.Key]; isOpen {
			continue
		}
		if err := m.notify(ctx, "⚠ "+c.Message); err != nil {
			m.Log.Warn("alert: send failed, will retry", "key", c.Key, "err", err.Error())
			continue // not recorded → raised again next tick
		}
		if err := m.St.RaiseAlert(ctx, c.Key, c.Message, now); err != nil {
			return err
		}
		m.Log.Info("alert raised", "key", c.Key)
	}
	warming := now.Sub(m.Started) < WarmUp
	for key, a := range open {
		if current[key] || (warming && HistoryBased(key)) {
			continue
		}
		if err := m.notify(ctx, "✓ Resolved: "+a.Message); err != nil {
			m.Log.Warn("alert: send failed, will retry", "key", key, "err", err.Error())
			continue
		}
		if err := m.St.ClearAlert(ctx, key, now); err != nil {
			return err
		}
		m.Log.Info("alert cleared", "key", key)
	}
	return nil
}

func (m *Monitor) notify(ctx context.Context, text string) error {
	if !m.TG.Configured() {
		return nil
	}
	return m.TG.Send(ctx, text)
}

func (m *Monitor) snapshot(ctx context.Context, now time.Time) (Snapshot, error) {
	snap := Snapshot{Now: now, DiskFree: -1}
	vs, err := m.St.ListVenues(ctx)
	if err != nil {
		return snap, err
	}
	names := map[int64]string{}
	quiet := map[int64]bool{} // paused / offboarded venues never alert
	for _, v := range vs {
		names[v.ID] = v.Name
		if v.Status == "paused" || v.Status == "offboarded" {
			quiet[v.ID] = true
			continue
		}
		h, err := m.St.Hours(ctx, v.ID)
		if err != nil {
			return snap, err
		}
		loc, err := time.LoadLocation(v.TZ)
		if err != nil {
			loc = time.UTC
		}
		snap.Venues = append(snap.Venues, VenueState{
			Slug: v.Slug, Name: v.Name, Open: h.ClassifyOpen(now.In(loc)), LastFrameAt: v.LastFrameAt,
		})
	}
	ds, err := m.St.ListDevices(ctx)
	if err != nil {
		return snap, err
	}
	for _, d := range ds {
		if d.Status != "active" || quiet[d.VenueID] {
			continue
		}
		snap.Devices = append(snap.Devices, DeviceState{
			Serial: d.Serial, VenueName: names[d.VenueID], Boot: d.Counters.Boot,
			Brownout: d.Counters.Brownout, RSSI: d.RSSI, LastSeen: d.LastSeenAt,
		})
	}
	if m.DiskFree != nil {
		if pct, err := m.DiskFree(); err == nil {
			snap.DiskFree = pct
		}
	}
	return snap, nil
}

// Ping calls the dead-man URL (an external heartbeat service such as
// healthchecks.io): when the pings stop — the VPS or this process is down —
// that service tells the operator. Nothing else on the box can.
func Ping(ctx context.Context, client *http.Client, u string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		// The ping URL is a per-check secret; log the cause, not the URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("dead-man ping: %w", ue.Err)
		}
		return fmt.Errorf("dead-man ping failed")
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("dead-man ping: status %d", resp.StatusCode)
	}
	return nil
}
