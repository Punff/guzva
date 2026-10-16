# Business operations plan — intake, billing, fleet control

Status: **plan, not approved** (16 Oct 2026). Nothing here is built. Triggers:
the user set a price (20–30 KM/venue/month, PLAN D8's "first venue that must
pay"), and asked for (1) a form to send to clients that becomes a venue,
(2) a communication system for bills and camera logs, (3) any other additions.

Hard rules still apply (PLAN §1, AGENTS.md): one binary, one SQLite file, no new
Go dependencies (email via `net/smtp`, invoices as HTML — no PDF library), push-only
server, raw frames never stored, every mutation audited, `make test` the only gate.

---

## 1. What exists today (the starting point)

- Admin console: create/edit/delete venue and device, set owner password,
  per-venue picture style, fleet board with signal bars and last reject.
- Owner panel: details + hours + Maps link, three statistics, QR poster.
- Device health on every upload: fw, RSSI, free heap, boot/brownout/post/TLS
  counters, camera profile — stored on the device row; one journal line per
  upload (`make soak` reads it).
- Not built (PLAN §10/§11/§14 already describe them): lifecycle states
  (lead → active → paused → offboarded), owner invite link, alerts + dead-man,
  audit log, billing.

## 2. Workstreams

### A. Client intake form → venue

**A1. Public application form** — `guzva.ba/prijava/` (Croatian, same design,
linked from the homepage "Kontakt" section and sendable as a link).
Fields: salon name, type (frizer/brijačnica/kozmetika/other), address, Google
Maps link, contact person, phone, email, opening hours (the existing hours
editor), seats/chairs, "is there WiFi at the salon" (yes/no — **never** the
password over the web; that is collected on site), preferred install window,
how they heard of us, consent to the privacy policy + being contacted.
Anti-spam without third parties: honeypot field, per-IP rate limit (the existing
limiter), a minimum fill time. No CAPTCHA service.

**A2. Personal invite link** — for a salon you already talked to: admin creates
a link `guzva.ba/prijava/<token>` (signed, single-use, 14 days) that opens the
same form pre-filled and marked as "agreed".

**A3. Leads inbox (admin)** — list of applications with status
`new → contacted → survey booked → surveyed → won / lost`, notes, and the
install checklist (RSSI at the mount ≥ −65, power, framing sign-off, camera
notice delivered, decision on video surveillance signed).

**A4. "Create venue from lead"** — one button: fills the venue (name, suggested
slug, address, Maps link, phone, hours), creates the billing profile (C2) and
the owner account, mints the device token, and moves the lead to `won`. The
admin reviews before saving. Privacy: lost leads are deleted after 12 months;
the policy gains a short "contact form" section.

### B. Venue lifecycle + owner onboarding

- **States** on the venue: `onboarding → active → paused → offboarded`.
  Paused (holidays, unpaid) shows "Privremeno zatvoreno" publicly, hides from
  the homepage list, suppresses alerts, keeps data. Offboarded: device revoked,
  owner login disabled, files deleted per the retention rule, row archived.
- **Owner invite link** (PLAN §10, trigger now fired): the owner sets their own
  password from an emailed one-time link; you never know it. Same flow for
  "forgot password".
- **Legal pack per venue** (BiH ZZLP — the salon is the one fined 10–100k KM
  without it): printable **camera-notice sign** (A5, like the QR poster), a
  **template "Odluka o video nadzoru"** pre-filled with the venue's data, and
  the service **terms** (`/uvjeti/`) the salon accepts. Needs one legal read.

### C. Communication + billing

**C1. Outbound email** — an `outbox` table + a sched job that sends via SMTP
submission (port 587; GCP blocks port 25), retries with backoff, and logs every
message (to, subject, template, status — never the body of password links).
Templates in the catalog: invite, password reset, invoice, payment reminder,
camera offline, monthly report. Plain HTML + text, no tracking pixels.

**C2. Billing profile per venue** — legal name, address, JIB/IDB, PDV number
(if any), invoice email, price (KM/month), cycle (monthly / annual with 2
months free), start date, free pilot months, payment terms (e.g. 15 days).

**C3. Invoices** — monthly job creates **drafts** on the 1st; you review and
press "send" (no fully automatic billing at first). Sequential numbering per
year (`2026-0001`), venue-local dates, price incl./excl. PDV per your status.
Each invoice is an HTML page (printable to PDF from the browser, A4 CSS) at a
private signed URL, emailed with that link; the owner panel gets a "Računi"
tab. Payments are bank transfers: you mark paid (or import a bank CSV later).
Reminder emails at +3 and +10 days overdue; after N days you can pause the
venue with one click. CSV/monthly export for the accountant.

**C4. Owner monthly report** (suggested) — on the 1st: "U oktobru: 1.240
pregleda stranice, 86 skeniranja QR koda, najviše gužve petkom 16–18 h". This
is what justifies 25–30 KM, and it costs nothing.

### D. Camera logs + fleet control

**D1. Device events** (firmware + protocol, additive — old servers ignore it):
each upload's meta gains `reset_reason` (esp_reset_reason), `uptime_s`,
`wifi_reconnects`, and a small ring of the last error codes (camera init fail,
TLS fail, low heap…). The server keeps a `device_events` table (30-day
retention) and shows a timeline on the device page: reboots and why,
brownouts, signal over the day. Needs one firmware flash per board.

**D2. Alerts** — one push channel to you (PLAN §6.6): venue stale >15 min
during its opening hours, ≥3 venues stale at once, reboot loop (>3 boots/hour),
brownout counter rising, RSSI < −72 for an hour, disk < 15 %, TLS cert < 14
days, invoice overdue. One "recovered" message per incident; paused venues
suppressed. Plus a **dead-man**: an external free heartbeat service pinged by
the server, so you hear about it when the whole VPS is down.

**D3. Remote commands (push-only safe)** — the upload response may carry a
command the device executes: `reboot`, `reopen install window` (Q-10's 15-min
`/jpg` window, for remote focusing help). The server never connects to the
device. Later (Phase 3 of PLAN): OTA firmware updates the same way.

### E. Control centre (admin home)

One screen: active / paused / onboarding venues, monthly recurring revenue,
unpaid invoices and their age, open alerts, new leads. The existing fleet board
stays one click away.

### F. Other additions I recommend

1. **Domain + HTTPS first** — every link in emails (invites, invoices, the
   form) must be `guzva.ba`, not a bare IP with a browser warning.
2. **Audit log** (PLAN §11) — who changed what, when; required once money and
   client data are involved.
3. **Off-site backups** — invoices are legal records; the nightly backup must
   also leave the VPS (e.g. encrypted copy to cheap object storage).
4. **Terms of service + contract template** — price, notice period, who owns
   the camera, what happens on unpaid invoices.
5. **Install kit checklist** printed per venue (device id, serial, QR poster,
   camera notice, odluka) so an install takes one visit.

## 3. Data model (all additive migrations, reopen-tested as §1 requires)

`leads`, `invite_tokens`, venue `status` + `paused_reason`, `owner_email`,
`billing_profiles`, `invoices` + `invoice_lines` + `payments`, `outbox`,
`device_events`, `alerts`, `audit_log`. Personal data minimised; retention
rules per table (leads 12 months after lost; events 30 days; outbox 90 days;
invoices as long as the law requires — accountant to confirm, typically years).

## 4. Phases (each ends deployed and verified)

| Phase | Contents | Rough size |
|---|---|---|
| **P1 Foundation** | domain + HTTPS, outbound email + outbox, alert channel + dead-man, audit log | 2–3 days |
| **P2 Intake & onboarding** | application form + invite link, leads inbox, create-venue-from-lead, lifecycle states, owner invite/reset, legal pack | 3–4 days |
| **P3 Billing** | billing profiles, invoice drafts/sending/PDF-print, owner "Računi", payments + reminders, accountant export, monthly owner report | 4–5 days |
| **P4 Fleet control** | firmware event fields + flash, device timeline, alert rules, remote reboot / install window | 3–4 days + bench flash |
| Later | OTA updates, bank CSV import, card payments, `operator` role | on trigger |

Order: P1 is a prerequisite for everything that sends a link. P2 before P3
(a venue needs a billing profile before an invoice). P4 can run in parallel
once the domain exists.

## 5. Decisions needed from the user

1. **Email**: mailbox at the domain registrar or a small provider (Zoho/Brevo)
   with SMTP for the app — vs. running our own mail server on the VPS (not
   recommended: deliverability, SPF/DKIM/DMARC, GCP blocks port 25).
2. **Alert channel**: Telegram bot (recommended: free, instant, on your phone),
   ntfy, or email.
3. **Business form + PDV + fiscalisation** — what an invoice must contain and
   whether these sales need a fiscal device: an accountant's answer, before P3.
4. **Form**: public application form, invite-only links, or both (recommended:
   both).
5. **Billing**: monthly only, or also annual (2 months free); payment by bank
   transfer only to start.
6. **Who owns the camera** (rent included in the price vs. one-time install
   fee) — goes into the terms.
