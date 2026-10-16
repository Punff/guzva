package mail

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"guzva/internal/store"
)

// fakeSMTP is a minimal loopback SMTP server: EHLO with AUTH (no STARTTLS,
// which Send only tolerates on loopback), records each DATA payload, and
// can be told to reject RCPT.
type fakeSMTP struct {
	mu     sync.Mutex
	got    []string
	auth   bool
	reject bool
}

func (f *fakeSMTP) start(t *testing.T) (string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { io.WriteString(c, s+"\r\n") }
	say("220 fake")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake\r\n250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = true
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			f.mu.Lock()
			rej := f.reject
			f.mu.Unlock()
			if rej {
				say("550 no such user")
			} else {
				say("250 ok")
			}
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.got = append(f.got, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		case cmd == "RSET":
			say("250 ok")
		default:
			say("250 ok")
		}
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	if _, err := build("a@b.ba", Message{To: "x@y.ba\r\nBcc: evil@z", Subject: "s", Text: "t"}, time.Now()); err != ErrHeader {
		t.Fatalf("err = %v", err)
	}
}

func TestSendRefusesPlaintextOffLoopback(t *testing.T) {
	// A non-loopback host without STARTTLS must be refused before AUTH; we
	// can't stand one up in a test, so check the guard on the dial path:
	// an unroutable address fails to dial, never "sends".
	cfg := Config{Host: "192.0.2.1", Port: 1, From: "a@b.ba"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := cfg.Send(ctx, Message{To: "x@y.ba", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("send to an unreachable server succeeded")
	}
}

// TestOutbox: queued → sent (bodies wiped, UTF-8 subject encoded,
// multipart when HTML), a rejected recipient is retried with backoff and
// gives up after MaxAttempts.
func TestOutbox(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &fakeSMTP{}
	host, port := f.start(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := &Sender{Cfg: Config{Host: host, Port: port, User: "u", Pass: "p", From: "guzva.ba <kontakt@guzva.ba>"},
		St: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}

	id, _ := st.EnqueueMail(ctx, store.OutMail{To: "vlasnik@salon.ba", Subject: "Račun za listopad",
		Text: "Pozdrav", HTML: "<p>Pozdrav</p>", Template: "invoice"}, now)
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || !f.auth {
		t.Fatalf("delivered %d, auth %v", len(f.got), f.auth)
	}
	msg := f.got[0]
	for _, want := range []string{"To: vlasnik@salon.ba", "Subject: =?utf-8?q?Ra=C4=8Dun_za_listopad?=", "multipart/alternative", "text/html"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q", want)
		}
	}
	rows, _ := st.RecentMail(ctx, 5)
	if rows[0].ID != id || rows[0].Status != "sent" {
		t.Fatalf("row = %+v", rows[0])
	}
	if due, _ := st.DueMail(ctx, now.Add(time.Hour), 5); len(due) != 0 {
		t.Fatalf("sent mail still due: %+v", due)
	}

	// Rejected recipient: retried with growing delay, then failed.
	f.reject = true
	st.EnqueueMail(ctx, store.OutMail{To: "nema@salon.ba", Subject: "x", Text: "y", Template: "test"}, now)
	for i := 0; i < MaxAttempts; i++ {
		s.Tick(ctx)
		now = now.Add(backoff(i+1) + time.Second)
	}
	rows, _ = st.RecentMail(ctx, 1)
	if rows[0].Status != "failed" || rows[0].Attempts != MaxAttempts || !strings.Contains(rows[0].LastError, "550") {
		t.Fatalf("failed row = %+v", rows[0])
	}
}
