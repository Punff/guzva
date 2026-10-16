// Package mail sends the operator's outgoing email (business plan P1):
// invites, invoices, reminders, reports. Messages are queued in the store's
// outbox and sent by a sched job through SMTP submission (port 587 with
// STARTTLS — what Brevo's free tier and most small providers offer; GCP blocks
// port 25). Stdlib only (net/smtp), no new dependencies (AGENTS.md).
//
// Privacy: credentials never leave this package's config; once a message is
// sent its bodies are wiped from the outbox (invite and reset links must not
// sit at rest), and logs carry the id, template and recipient domain only.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"guzva/internal/store"
)

// Config is the SMTP submission account, from the environment.
type Config struct {
	Host string
	Port int    // default 587
	User string // often the account email; Brevo uses a login id
	Pass string
	From string // "guzva.ba <kontakt@guzva.ba>"
}

// Configured reports whether the sender can run.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// Message is one email ready to send.
type Message struct {
	To, Subject string
	Text, HTML  string // HTML optional
}

// ErrHeader rejects header injection (a CR/LF in a header value).
var ErrHeader = errors.New("mail: newline in header value")

func clean(v string) (string, error) {
	if strings.ContainsAny(v, "\r\n") {
		return "", ErrHeader
	}
	return v, nil
}

// build renders RFC 5322 bytes: multipart/alternative when HTML is present.
func build(from string, m Message, now time.Time) ([]byte, error) {
	to, err := clean(m.To)
	if err != nil {
		return nil, err
	}
	subj, err := clean(m.Subject)
	if err != nil {
		return nil, err
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := "guzva.ba"
	if at := strings.LastIndex(from, "@"); at >= 0 {
		domain = strings.Trim(from[at+1:], "> ")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\n",
		from, to, mime.QEncoding.Encode("utf-8", subj), now.Format(time.RFC1123Z), hex.EncodeToString(id[:]), domain)
	part := func(ctype, body string) {
		fmt.Fprintf(&b, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", ctype)
		qp := quotedprintable.NewWriter(&b)
		_, _ = qp.Write([]byte(body))
		_ = qp.Close()
		b.WriteString("\r\n")
	}
	if m.HTML == "" {
		part("text/plain", m.Text)
		return b.Bytes(), nil
	}
	boundary := "guzva-" + hex.EncodeToString(id[:6])
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	part("text/plain", m.Text)
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	part("text/html", m.HTML)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

// addrOnly extracts the bare address from "Name <a@b>" for the envelope.
func addrOnly(s string) string {
	if i, j := strings.LastIndex(s, "<"), strings.LastIndex(s, ">"); i >= 0 && j > i {
		return s[i+1 : j]
	}
	return strings.TrimSpace(s)
}

// Send delivers one message. STARTTLS is required unless the server is on
// loopback (tests, a local relay): credentials never cross the network in
// the clear.
func (c Config) Send(ctx context.Context, m Message) error {
	port := c.Port
	if port == 0 {
		port = 587
	}
	raw, err := build(c.From, m, time.Now())
	if err != nil {
		return err
	}
	d := net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(&tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if ip := net.ParseIP(c.Host); c.Host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("mail: server offers no STARTTLS; refusing to send credentials in the clear")
	}
	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			return err
		}
	}
	if err := cl.Mail(addrOnly(c.From)); err != nil {
		return err
	}
	if err := cl.Rcpt(addrOnly(m.To)); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// MaxAttempts after which a message is marked failed (≈ 1 day of backoff).
const MaxAttempts = 8

// backoff is the wait before attempt n+1: 1, 2, 4 … 128 minutes.
func backoff(n int) time.Duration { return time.Duration(1<<min(n, 7)) * time.Minute }

// Sender drains the outbox; Tick is the sched job.
type Sender struct {
	Cfg Config
	St  *store.Store
	Log *slog.Logger
	Now func() time.Time
}

// Tick sends what is due. Unconfigured: messages wait in the queue.
func (s *Sender) Tick(ctx context.Context) error {
	if !s.Cfg.Configured() {
		return nil
	}
	now := s.Now()
	due, err := s.St.DueMail(ctx, now, 20)
	if err != nil {
		return err
	}
	for _, m := range due {
		err := s.Cfg.Send(ctx, Message{To: m.To, Subject: m.Subject, Text: m.Text, HTML: m.HTML})
		dom := m.To[strings.LastIndex(m.To, "@")+1:]
		if err == nil {
			if err := s.St.MarkMailSent(ctx, m.ID, now); err != nil {
				return err
			}
			s.Log.Info("mail sent", "id", m.ID, "template", m.Template, "to_domain", dom)
			continue
		}
		attempts := m.Attempts + 1
		final := attempts >= MaxAttempts
		if err := s.St.MarkMailFailed(ctx, m.ID, attempts, now.Add(backoff(attempts)), err.Error(), final); err != nil {
			return err
		}
		s.Log.Warn("mail send failed", "id", m.ID, "template", m.Template, "to_domain", dom,
			"attempt", attempts, "final", final, "err", err.Error())
	}
	return nil
}
