# guzva.ba — Dev / Production Plan v3.2 (product + platform)

Supersedes v3.1. Same architecture (one static Go binary on one VM, ESP32-CAMs
push outbound, no inbound ports at venues). v3.2 is the shift from **protocol
spike** to **product**: it adds the surfaces (public page, admin console, vendor
panel), the design system and localisation, vendor lifecycle, overviews/logs,
automation, and the Bosnia deployment profile (locale, law, hosting reality).
§14 keeps every deferral with a measurable trigger.

Tone note: v3.1 was written while the device↔VPS link was being proven, so its
§§5–8 are dense with bench forensics (kept — they are the contract) and it
mentions the product only in passing. v3.2 keeps that foundation and builds the
missing product plan on top. The current state is *messy but sound*: the hard
part (device → TLS → ingest → render → static page, idempotent, private) works
end to end; what is missing is polish, management and automation.

Status (09 Oct 2026 — bench cutover done, product plan added):
- **Go server live on the VPS, serving the real board.** `guzva.service` on
  `:443` with the bench cert `/etc/guzva/tls.*` (copied from the rig so the
  firmware's embedded CA validates — no re-flash). Python rig `camtest` stopped
  + disabled, kept as reference. `make deploy` is the only deploy path.
- **Device plane complete:** all 22 `desired_cfg` keys (ranges pinned to
  `docs/proto-v1.md` by a test), `cfg_json` for the 19 sensor keys (migration
  v2), `guzva cfg`, `guzva provision -token`. Board applies server-driven config
  end-to-end (verified `led_pwm=0` pushed live from the VPS with no reflash).
- **Surfaces built, not yet polished:** static public page (D5), super-admin
  console, per-venue owner panel (D3, scoped cookie, IDOR-tested). All use
  hand-written CSS today; v3.2 replaces that with Tailwind (§9).
- **Ops workflow shipped:** `Makefile` (`test`/`build`/`deploy`/`rollback`/
  `check`/`logs`/`backup`), releases layout `/srv/guzva/bin/releases/<tag>` +
  `current`, `deploy/provision.sh` (idempotent, proven on the live box),
  `guzva-backup.{service,timer}` (nightly snapshot, keep 14), `docs/runbook.md`
  + `docs/ops-log.md`.
- **Two hard bugs found and fixed** (both pinned by regression tests):
  a firmware HTTP-parser bug that discarded valid `201`s (needle tested only at
  the buffer tail; bytes past the needle dropped), and a store migration whose
  missing `case 3:` would have failed **every restart after the v3 upgrade**.
  Lesson recorded in §1: dogfood every change against real migrated state.
- **Known gaps → §14:** 48 h soak numbers; render-ms log line; autocert +
  domain; ingest host in NVS; reference-frame hash; the 2×422 reasons.
- **Bench link is marginal** (RSSI −70…−76): 12–20 s connects, ~30 min stale
  gaps, brownout counter at 4. Root cause is RF + supply, not software; the
  install gate (RSSI ≥ −65) is non-negotiable at a real venue (§13).

---

ssh info: root@guzva

---

## 0. Decisions log

| ID | Decision | Consequence |
|----|----------|-------------|
| D1 | Stylise **server-side** | Raw JPEG crosses the wire device→VM (TLS only), lives in RAM for one `Process` call, never written. Styles changeable without a firmware release. |
| D2 | Stills history is **not** a product, but may become one | 48 h ring per venue + last-good. Sidecars keep `preset@version`, hashes, `received_at`, so longer retention can be added later. |
| D3 | Owner auth = per-venue password + scoped cookie | **Built 09 Oct.** One bcrypt hash on the venue row, `o1.<venueID>.<exp>.<mac>` cookie, Path `/owner`, no sessions table. Owner edits venue details + read-only device health; tokens/config/revoke stay super-admin. |
| D4 | Host on **GCP Always Free e2-micro** (US regions only) | €0 recurring, but ~150–200 ms Bosnia↔US. Accepted by decision 09 Oct; §13 makes the latency mitigations and the EU-VPS trigger explicit. |
| D5 | Public side = **pre-rendered static files** | Public requests never touch SQLite, templates, or the render pipeline. Web root stays exactly `index.html` + `latest.jpg` per venue — so any CSS must be **inlined** into the page (§9). |
| D6 | UI built with **TailwindCSS**, compiled to embedded CSS | One Tailwind CLI build scans Go templates + emits a purged stylesheet; `go:embed` it. No CDN, no runtime compiler. Public page inlines the purged CSS (D5); admin/owner serve it as an embedded static asset. |
| D7 | **Croatian (`hr`) is the product locale**, English fallback | Public and vendor surfaces are Croatian (Latin script); admin/internal may stay English. One message catalog (`internal/i18n`); formats are `dd.mm.yyyy`, 24 h, `Europe/Sarajevo`. bs/sr are near-identical and can be added as extra catalogs. |
| D8 | Vendor lifecycle in scope; **billing/invoicing deferred** | Onboarding checklist, invite/activate, per-venue settings, health/overview, offboarding are product features (§10). Billing (BAM, PDV 17 %) is a §14 trigger with the shape recorded. |
| D9 | **No biometrics, ever** | No face/plate/people detection; it is a privacy commitment and a Bosnian/EU legal simplification (§13). Recorded here so no future feature quietly adds it. |

---

## 1. Contracts (non-negotiable)

- **Freshness:** server `received_at` is canonical; device clock is advisory only.
- **Idempotency:** `UNIQUE(venue_id, capture_id)` + sha256 guard; a retry returns 409 with the same body as a 201.
- **Settings planes:** device-plane (interval/quality/sensors; confirmed by read-back; mismatch = visible amber state) vs site-plane (name/style/hours/timezone; cloud-only; zero device traffic).
- **Never publish a failure:** keep last-good frame, show STALE. Garbage never replaces a good still.
- **Degradation:** network/auth/server errors never erase identity. Only installer factory-reset erases.
- **Dogfood real state:** every change is exercised against a migrated database and a real device/host before it is called done. Two of the worst bugs (a discarded `201`, a restart-proof migration gap) were invisible to tests that used fresh state.
- **Privacy:**
  - raw JPEG is on the wire device→VM inside TLS, exists in server RAM inside one `Process` call, is zeroed on return;
  - at rest, on any public route, in logs, and in quarantine: stylised renders only (quarantine = headers-only records);
  - request bodies are never logged;
  - the web root contains only `index.html` + `latest.jpg` per venue; the 48 h ring lives outside it;
  - proven by tests and a disk scan in CI, not by policy.
- **No biometric processing** (D9), and no third-party TLS-terminating proxy in front of ingest (a CDN would see raw frames). Public pages may be proxied.
- **Isolation:** push-only. The server never dials devices (no SSRF surface).
- **Accessibility & honesty (new in v3.2):** every user-facing state is reachable and named (fresh/stale/no-frame/error); colour is never the only signal; the UI never claims a frame is live when it is not.

---

## 2. Architecture

```
[venue LAN]
  ESP32-CAM ──HTTPS POST ingest.guzva.ba/v1/ingest──┐   (outbound only)
                                                         v
                      [GCP e2-micro, Always Free, Ubuntu 24.04, dedicated]
                      Go binary `guzva` (autocert TLS on :80/:443)
                         ├─ ingest   (/v1/ingest, /healthz)
                         ├─ public   (data/public/{slug}/index.html + latest.jpg)
                         ├─ admin    (/admin/…, signed-cookie session)
                         ├─ owner    (/owner/…, scoped cookie)
                         ├─ sched    (30 s tick: state flips, missed-poll, pruner, alerts)
                         ├─ SQLite (WAL) ── nightly snapshot ──> Cloud Storage bucket
                         └─ disk: data/public/ · data/stills/ (48 h ring) · data/ref/ · data/certs/
```

Stack: Go stdlib `net/http` (1.22 ServeMux patterns), `html/template` + `embed`,
`time/tzdata` embedded, `log/slog` → journald, `modernc.org/sqlite` (CGO-free),
`golang.org/x/crypto` (bcrypt + autocert), `golang.org/x/time/rate`.
**New in v3.2:** Tailwind CLI in the build (D6), `internal/i18n` (D7), embedded
static assets for the consoles. Config = env + `flag`; secrets in
`/etc/guzva/guzva.env` (mode 600, systemd `EnvironmentFile`). No framework, no
Docker, no ORM, no reverse proxy.

---

## 3. Repo layout (dependencies flow downward only)

```
cmd/guzva/            main: serve / provision / cfg / hashpw
internal/proto/       wire types + constants; mirrors docs/proto-v1.md
internal/store/       SQLite, embedded migrations; the ONLY package with SQL
internal/render/      Process(FrameIn) → Outputs; Preset registry
internal/frames/      ingest logic, save, prune
internal/venue/       hours, timezone, open/closed, freshness — pure functions
internal/pages/       pre-render public index.html + latest.jpg (atomic)
internal/admin/       admin + owner handlers, templates, embedded static
internal/auth/        device tokens, admin/owner sessions, rate limits
internal/i18n/        message catalogs (hr default, en) + locale formats  [new]
internal/sched/       30 s tick, pruner, missed-poll, alerts
web/                  tailwind input.css + generated app.css (embedded)     [new]
firmware/             PlatformIO project
tools/fakecam/        one small Go client: fast / slow / flappy / dup
deploy/               Makefile, units, backup + provision.sh
docs/                 proto-v1.md, runbook.md, ops-log.md, ux.md (new)
```

Interface rule: an interface exists only when a second implementation exists
today. Currently `Clock`, `Preset`, and (new) the `i18n.Catalog` lookup.
Layout on the box: `/srv/guzva/{bin/releases/<tag>, bin/current → symlink, data/, .env}`,
owned by a dedicated `guzva` user.

---

## 4. Protocol v1 (`docs/proto-v1.md` is the single source of truth)

`POST /v1/ingest` — multipart: `meta` (JSON) + optional `frame` (JPEG).
- `meta`: `capture_id` (`C<boot>-<ctr>-<rand4>`, monotonic from NVS, bound to
  identity), `fw`, `rssi`, `free_heap`, `cfg_ver_applied`, counters
  (`boot, brownout, join_fail, post_fail, tls_fail, capture_fail`), `reason`.
- No `frame` = health-only beat (camera-degraded mode).
- Auth: `Authorization: Bearer <device token>`; server stores only sha256.
- Responses: `201` stored · `409` duplicate (same body) · `422` rejected +
  reason (quarantined) · `401` revoked/unknown · `413` too big · `429` +
  `Retry-After` · `507` disk degraded.
- Response body: `{ desired_cfg: {ver, interval_s, quality, …}, next_s, server_time }`
  (full device-plane key set — `docs/proto-v1.md`, proven live).

Hostnames: ingest on its own name (`ingest.guzva.ba`) from day 1, stored in
device NVS (not compiled in) so a later CDN for the public name needs no
reflash. Both names point at the same VM; autocert serves both.

Versioning: path `/v1`; additive optional fields only within a major; `X-Proto`
header; dual-serve a major for 6 months when `/v2` appears.

Later (Phase 3): `POST /v1/claim`. Until then the installer provisions the token
over the LAN/USB provisioning path.

---

## 5. Firmware (M1) — dumb sensor, smart about survival

Does: capture on a timer, POST outbound, apply remote config with read-back,
survive brownout / WiFi loss / camera death without humans.

Never: serve public traffic, MJPEG, ML/stylise, face handling, open inbound
ports. Prod builds delete: port-81 stream, motion, timelapse-as-product,
RGB/gray endpoints, LAN OTA form. Kept (caged): `/api/status`, `/jpg`
rate-limited, provisioning, mDNS `guzva-<serial>.local`.

**No frame queue.** A failed upload drops the frame, counts `post_fail`; the next
tick captures a fresh one. The server shows an honest STALE gap. (SD ring +
backfill return only if the §14 trigger fires.)

Mechanisms: HTTPS via `WiFiClientSecure` + embedded CA bundle (never
`setInsecure` in prod, grep-gated); capture never waits for NTP; chunked 4–8 KB
upload with immediate `fb_return`; single in-flight request; 12 s
connect/handshake + 25 s POST timeouts (measured); VGA q14–18 cap; device bearer
token, ingest hostname and capture counter in NVS (`prov` ns); desired-cfg
version with applied read-back in every POST; camera probe fail → exponential
backoff → health-only degraded mode + LED pattern after 5; brownout detector on,
XCLK 16 MHz fallback, 470–1000 µF cap in the install spec; task + upload
watchdogs with an hourly-max reboot; partition table `Default 4MB with spiffs`.

On `401` (revoked): keep identity, slow health-only beats (60→600 s backoff),
amber LED until re-provisioned.

**Measured TLS reality (keep until autocert):** Go's `NewSessionTicket` stalls
this core's mbedtls — `serve` pins TLS 1.2 and disables session tickets, which
took connect from a 12 s timeout to ≈2–3 s. Cipher choice is not load-bearing.

Build order: (1)✓ skeleton → (2)✓ NVS + factory reset → (3)✓ WiFi manager →
(4)✓ HTTPS ingest + TLS spike → (5)✓ degraded mode + counters → (6)✓ desired-cfg
read-back → (7)✓ LAN cage / ✗ reference-frame hash → (8) ingest host from NVS
(§14) → (9, Phase 3) pull OTA + rollback.

Acceptance: ≥95% POST success at ≥ −65 dBm over 48 h; heap-leak bounded; camera
death and WiFi loss recover without a power cycle.

---

## 6. Server

### 6.1 Ingest flow
`auth → rate-limit (per-IP + per-device) → MaxBytesReader → parse meta →
idempotency check → render.Process → write ring file + frames row → atomically
replace latest.jpg → update venue.last_frame_at → rewrite index.html if state
changed → respond with desired_cfg`.

Bounded work: one goroutine per connection; a global semaphore caps concurrent
`Process` calls; `ReadHeaderTimeout` / `ReadTimeout` stop slow clients. Gate:
fakecam with 1 slow + 19 fast — fast p95 unaffected.

### 6.2 Tables (pilot set: three)
- `venues`: id, slug, name, tz, style, `hours_json`, `owner_hash`, ref_hash,
  last_frame_at.
- `devices`: id, venue_id, token_sha256, fw, last_seen_at, last_rssi, cfg
  desired/applied versions, latest counters.
- `frames`: venue_id, capture_id, hash, received_at, preset@version, dims,
  quarantine flag; `UNIQUE(venue_id, capture_id)`.

No sessions table (both consoles use signed cookies), no `settings_log` (audit
is slog + the admin activity view, §11), no `health_events`. Added only with
their features: viewer_hits, intents, OTA releases/state, claim codes. SQLite:
WAL, single writer, `busy_timeout`, embedded migrations.

### 6.3 Retention (D2)
48 h ring per venue in `data/stills/{venue_id}/` + last-good always kept. Hourly
pruner: one SQL select + file deletes. Raw never written.

### 6.4 Public page (static, D5)
`data/public/{slug}/index.html` + `latest.jpg`, served by `http.FileServer` —
no DB, no per-request render. The page shows the venue name, the still
(`latest.jpg?v=<received_at>`), a Fresh/STALE badge, open/closed, today's hours
in venue-local time, and an absolute clock + day label (never a relative age,
which a static file can't keep correct). `<meta refresh 30>` is the no-JS
fallback; with JS the page re-fetches itself and swaps only what changed. Purged
Tailwind CSS is **inlined** (D5 forbids a third file). `index.html` is rewritten
only when state changes; writes are temp-file + rename; after a restart `sched`
re-derives state within one tick. Detail in §9.

### 6.5 Auth
- **Device:** bearer token, sha256 at rest, revocable.
- **Admin:** single bcrypt credential from `.env`; throttled login; HMAC cookie
  (`HttpOnly`, `Secure`, `SameSite=Strict`). Accepted limitation: logout is
  client-side; rotating the secret is the server-side kill.
- **Owner:** per-venue bcrypt password (hash on the venue row; empty = no login)
  → scoped cookie `guzva_owner` (`o1.<venueID>.<exp>.<mac>`, Path `/owner`),
  verified by `requireOwner` which injects the venue — never a form/URL param.
  Shared throttle; one generic failure message (no venue enumeration); no
  sessions table. IDOR test lives in `internal/admin/admin_test.go`.

### 6.6 Observability and alerting
`/healthz` (+ TLS expiry) and the fleet board; **no `/metrics`** until a trigger.
Missed-poll = one SQL query on `last_frame_at`. STALE badge after >3 min.
Push alert (one channel: ntfy/Telegram): open-hours stale >15 min, ≥3 venues
stale at once, or dead-man fired. Dead-man: an external heartbeat pinged by
`sched`, which also watches `/healthz`. Detail in §11.

---

## 7. Image pipeline (`internal/render`) — server-side (D1)

`Process(ctx, FrameIn) → Outputs`, `Clock` injected.

Stages: decode (4 MiB cap) → validate (magic/size/dims/truncation/dark/dead
sensor) → normalize (bilinear to 640 w; EXIF dropped by construction and
asserted) → `Preset.Render` (pure, deterministic, versioned) → encode public
640 w q68 → store → raw zeroed on return.

Presets: `poster` (4-level), `mono`. **No raw preset.** A new style = one file +
one registry line + one golden test. Style changes apply to new frames only
(documented; also a privacy feature). A 320 w thumb is added when a page needs
it.

Quarantine: JSONL, headers-only records, never frame bytes. Never publish a
failure.

Performance: ~30–50 ms/frame with the stdlib; **measure on the e2-micro in
Phase 1**. libvips only if p95 render >200 ms sustained, behind the same
signature.

Privacy proofs as tests: no-raw-persist (disk scan), no-raw-route,
web-root-contains-only-allowed-files, EXIF-stripped, input-zeroed,
debug-tag-absent-in-prod, request-body-never-logged. Plus golden renders, corrupt
corpus, decode fuzz, determinism ×100, race hammer.

---

## 8. VM ops (GCP Always Free e2-micro, dedicated)

**Free-tier checklist** (public sources, 09 Oct 2026 — re-check on billing):
- machine type e2-micro, region `us-west1` / `us-central1` / `us-east1`;
- **standard** persistent disk ≤30 GB; only **one** e2-micro VM per account;
- one in-use external IPv4 covered; ephemeral IP changes if the VM stops
  (update DNS);
- egress ≈1 GB/month free (NA origin), then ≈$0.12/GB — **watched**, because
  Bosnia↔US public views are the likely first charge (D4/§13);
- no load balancer, Cloud NAT, snapshot schedules, extra static IPs;
- a **billing budget alert at ~$1**.

**Network/firewall:** VPC allows tcp 80/443 only; SSH from your IP. UFW inside
mirrors it. Nothing opened at venues.

**TLS:** `autocert` in the binary (HTTP-01 on :80 + redirect, certs cached in
`data/certs/`). Unit gets `AmbientCapabilities=CAP_NET_BIND_SERVICE`. *ESP32
constraint:* tickets stay disabled + TLS 1.2 pinned (§5); autocert must not
reintroduce tickets without re-testing the board.

**Resource guards:** `MemoryMax=700M`, `Restart=always`, `RestartSec=2`, 1 GB
swap. 964 MB RAM total on the box — render concurrency is capped.

**Deploy (`make deploy`):** static build → `scp` release → flip `current` →
restart → healthcheck (version match + page + admin). Last 3 releases kept.
`deploy/provision.sh` is idempotent and provisions user, dirs, units, timers,
swap, firewall. No Docker.

**Backups:** nightly `sqlite3 .backup` → gzip with `data/ref/`, keep 14 under
`/var/backups/guzva/` (Cloud upload lands with the domain work). RPO ≈ 24 h is
fine: the DB holds config only. One restore drill logged **before the pilot**,
then quarterly.

**Disk:** 10 % reserve; <15 % free → DEGRADED + `507`; <5 % → read-only + alert.

**Security:** SSH keys only, unattended-upgrades, throttled logins, web root =
two files per venue.

**Disaster note:** devices keep capturing through a VM outage; the first tick
after restore succeeds. Restore order: new VM (`provision.sh`) → DNS/IP → latest
nightly DB + refs → secrets → verify → fleet recovers. The binary and scripts run
on any few-euro VPS if the free tier changes.

**Docs mandatory:** `runbook.md` (leads with power) and `ops-log.md`.

---

## 9. Product surfaces & UX  *(new in v3.2)*

Three surfaces, one design system. Full spec in `docs/ux.md`.

### 9.1 Design system (D6)
- **TailwindCSS compiled to embedded CSS.** `web/input.css` + a Tailwind CLI
  build over the Go templates → `web/app.css` (purged). Admin/owner serve it at
  `/admin/static/app.css`; the public page **inlines** it (D5: two-file root).
  No CDN, no runtime compiler.
- Tokens: colour (incl. dark), spacing, radius, type scale, elevation, motion —
  as CSS variables the Tailwind theme maps onto, so both consoles and the public
  page share one look.
- Components we actually reuse: badge, card/frame, button, form row, table,
  toggle, empty state, toast/flash, skeleton. Built as Go template partials, not
  a JS component library.
- htmx for interactivity (already embedded); **no SPA**, no build-time JS
  framework. Progressive enhancement: every console action works without JS as a
  plain form POST.

### 9.2 Public page (the product's face)
Current state works and is honest; v3.2 raises it to "polished":
- in-place still swap (no full-document refresh flash), reserved frame box (no
  layout shift), skeleton while the JPEG loads;
- freshness as the headline (`21:48 · danas · CET`), STALE only when true and
  visibly so (it is the honesty contract made visible);
- open/closed, today's hours, venue name, and — new — an optional venue
  **description/contact** (site-plane, owner-editable) for QR-from-table use;
- empty state ("waiting for the first frame"), offline state (JS retry), and a
  one-line privacy footer in Croatian;
- **QR poster** (A5/A4) generated per venue for the wall/table, so a guest can
  reach the still — printable, with the venue name and page URL;
- a11y: WCAG AA, keyboard reachable, `aria-live` for new frames,
  `prefers-reduced-motion`, alt text that names the venue;
- metadata: description, Open Graph, theme-color, inline SVG favicon.

### 9.3 Super-admin console (`/admin`)
- **Fleet overview** (§11): every venue's freshness, last frame, device health,
  config read-back (amber), and open alerts at a glance.
- Venue page: details, hours, owner account, public-page preview link, QR
  poster, reference-frame sign-off record.
- Device page: full `desired_cfg` form **grouped** (cadence vs image vs
  hardware) with ranges and inline validation — not a 21-row table; token
  mint (once) / revoke; counters explained in plain language, not `b181·bo4·j0`.
- Vendor lifecycle actions (§10).
- Activity/audit view (§11), and a "what changed" feed per venue.
- Everything above also exists as a CLI verb (`guzva cfg`, `provision`,
  future `venue`), because SSH must never be the only way.

### 9.4 Vendor (owner) console (`/owner`)
- Croatian-first. Shows: public-page preview, venue details edit (name, style,
  tz, hours, description/contact), read-only device health, and a plain-language
  "what to do if the picture is old" (the runbook, humanised).
- Never sees tokens, camera settings, other venues, or billing. Enforced by the
  scoped cookie, not by hiding buttons.
- Mobile-first: this is used from a phone behind a bar.

### 9.5 State & copy rules
- Every state has a name and a sentence: fresh / stale / no-frame / degraded /
  revoked / quota. No spinner without a reason, no error without a next step.
- Croatian copy reviewed as a set (one catalog), not written ad hoc per handler.
- Dates `dd.mm.yyyy.`, 24 h clock, `Europe/Sarajevo`.

---

## 10. Vendor & user management  *(new in v3.2)*

Lifecycle (billing deferred, D8): **lead → surveyed → installed → active →
paused → offboarded.**

- **Survey & install gate** (must pass before provision): RSSI ≥ −65 at the
  mounting point, stable power (470–1000 µF cap), sightline, owner sign-off on
  the framing. Recorded on the venue (install checklist + reference-frame hash).
- **Onboarding**: create venue → mint device → flash/provision → first `201` →
  record reference-frame hash → create owner account → hand over credentials
  out-of-band → QR poster.
- **Invite/activate (owner)**: admin sets the owner password now; v3.2 adds a
  one-time **invite link** (short-lived signed token) so the owner sets their
  own password — avoids the admin knowing it (D3 stays the credential model;
  this only changes who types it).
- **Per-venue settings**: split cleanly by plane — owner edits site-plane;
  admin edits device-plane; the UI says which is which.
- **Paused**: stop expecting frames (holiday/seasonal) without revoking; the
  freshness dashboard shows "paused", not "stale", and alerts suppress.
- **Offboarding**: revoke device(s), disable owner login, decide data
  retain/delete, archive the venue row. Documented so it is one click, not a
  manual sweep.
- **Roles**: `super-admin` (you), `owner` (one per venue), and the door left
  open for `operator` (a trusted helper with admin-minus-secrets) — but only
  when a second admin exists (trigger in §14).
- **Audit**: every mutation logs actor (admin/owner), venue, field change (§11).

---

## 11. Overviews, logs & audit  *(new in v3.2)*

No `/metrics`, no health-history table (kept from v3.1) — but real *visibility*:

- **Fleet overview** (admin home): one row per venue — freshness badge, last
  frame age, device last-seen + RSSI + config read-back, active alerts. Sort by
  "needs attention". This is the "is everything OK?" screen.
- **Venue timeline**: recent frames (ring), state flips (fresh↔stale↔closed),
  config changes, install sign-off — derived from the `frames` ring + slog, not
  a new event table.
- **Activity/audit view**: the slog audit stream surfaced readably (who changed
  what, when). File remains the source of truth; the view is a bounded tail.
- **Logs**: structured slog → journald; the per-ingest line (status/serial/
  capture_id/rssi/heap/counters/tls) is the operational record. The runbook
  documents the exact `journalctl` queries for the common questions.
- **Alerting** (§6.6): one push channel; alert on open-hours stale >15 min, ≥3
  venues stale, dead-man; **suppress for paused venues**; one "recovered" notice.
- **Soak/exit data**: a `render_ms` field joins the ingest line so the exit gate
  is measurable without resurrecting `/metrics` (§14).

---

## 12. Automation & CI/CD  *(new in v3.2)*

Principle: if it is done twice by hand, it becomes a target or a script.

- **CI (GitHub Actions)**: `gofmt`/`go vet`/`go test -race`; Tailwind build is
  part of `make build` (CI fails if `web/app.css` is stale); privacy tests
  (disk-scan, web-root allow-list); firmware `pio run` compile; `go build` for
  linux/amd64. PR gate.
- **CD**: `make deploy` from a workstation today; a tag-triggered CI deploy is
  the trigger in §14 (needs SSH deploy key handling — deliberately not rushed).
- **Scheduled jobs** (`sched` + systemd timers): page state refresh (30 s),
  pruner (hourly), nightly backup (03:17), dead-man heartbeat, alert checks.
  All idempotent, all logged.
- **Provisioning automation**: `deploy/provision.sh` (VPS, idempotent), a
  documented ESP32 flash+provision recipe, and (Phase 2) a single
  `make onboard venue=<slug>` that chains provision + cfg defaults + QR poster.
- **Cert renewal**: autocert handles it; a TLS-expiry check feeds `/healthz` and
  the alert channel.
- **Backups**: timer runs it; `make backup` for on-demand; restore drill is a
  scripted, logged procedure, not a paragraph.

---

## 13. Bosnia deployment profile  *(new in v3.2)*

**Locale (D7).** Croatian (`hr`, Latin script) is the product language: public
page, vendor panel, printed/QR materials, and email/alert copy. Admin/internal
may stay English. Formats: `dd.mm.yyyy.`, 24 h clock, `Europe/Sarajevo`
(CET/CEST). bs/sr catalogs are near-identical and can be added without code
changes (one catalog file each). Firmware logs stay English (developer-facing).

**Hosting & latency (D4).** The box is in a **US free-tier region**, so
Bosnia↔VM is ~150–200 ms. Mitigations, in order:
- keep-alive reuse of the TLS connection across ticks (already in firmware) — the
  steady-state cost is roughly one RTT per POST, not a full handshake;
- a realistic `interval_s` (45–90 s) rather than 30 s, so RTT is amortised;
- `gzip`/Brotli for text responses; `Cache-Control` on the static page and still;
- fresh-connect budget stays generous (12 s) because a US RTT plus SYN
  retransmits on a weak link is exactly what we measured on the bench.
- **EU-VPS trigger (§14):** if measured p95 handshake >2 s over a week, or egress
  billing approaches the free allowance, move to a €4–5/mo EU VPS (~30–40 ms).
  The static binary and `provision.sh` already run there unchanged.

**Legal & privacy.** Bosnia's *Zakon o zaštiti ličnih podataka* is GDPR-aligned.
Consequences baked in: stylised-only public output (D1/D5), no biometrics (D9),
no raw retention, retain/delete on offboarding, and a **camera-notice spec** for
installs (a small sign at the venue stating that a camera operates and where the
public still lives). The public page footer carries the privacy line in Croatian.
Re-check with a local adviser before the first paying venue — this plan records
the shape, not legal advice.

**Currency & payments (deferred, D8).** BAM (KM), PDV 17 %. Pricing/invoicing is
a §14 trigger; the plan reserves a `billing` concern but builds nothing now.

**Domain & DNS.** `guzva.ba` — confirm registration at the .ba registry (UTIC);
`ingest.guzva.ba` is reserved from day 1 even while the bench uses the raw IP.
The `imaliguzve.ba` question from the rename still stands (§14).

**Vendor landscape.** Cafés, restaurants, hotels, guesthouses, tourist and
municipal sites. Consequence: installs are mixed-skill, often a bar phone as the
only console, and the link is a shared venue WiFi. The install gate and the
mobile-first vendor panel exist because of this.

**Connectivity.** BH Telecom / Telemach / M:tel; speeds are generally fine, the
risk is WiFi reliability and power at the mounting point — the two bench facts
that dominate the risk list.

---

## 14. Deferred, with triggers

| Deferred | Trigger to build it |
|----------|---------------------|
| EU VPS (€4–5/mo) instead of US free tier | p95 handshake >2 s over a week, or egress nearing the free allowance, or the pilot venue is customer-facing enough that 200 ms hurts |
| Autocert + domain (`ingest.guzva.ba` live) | First real install; needs DNS + the .ba registry |
| Ingest host from NVS (not compiled in) | Before the first venue where reflashing is expensive; a prerequisite for the CDN trigger |
| Billing/invoicing (BAM, PDV 17 %) | First venue that must pay, or ≥5 venues |
| Owner invite-link (self-set password) | Before onboarding a vendor you don't want to hold a password for |
| `operator` role | A second admin/helper exists |
| CI tag-triggered deploy | Deploys become >weekly |
| Reference-frame hash | Before the first paid install (privacy/quality audit trail) |
| Cloudflare/CDN in front of public pages only | Egress >~60 % of free allowance, or a traffic spike. Needs ingest on its own DNS-only hostname |
| SD frame queue + `/v1/backfill` | Pilot data shows bridgeable outages and owners care about gaps |
| Long retention / thinning / history UI | First customer asks for history (D2) |
| Thumb output | A page needs it |
| `/metrics`, `health_events`, `settings_log` | Debugging/audit needs slog can't answer |
| Postgres behind an interface | p95 ingest >500 ms sustained or ~25+ venues |
| `ObjectStore`/S3, separate image volume | Disk >70 % |
| Token rotation (24 h overlap) | ~25 venues or a token-leak scare |
| Claim-code provisioning | Installs by someone other than you |
| Staged OTA (1→3→all) | ~25 venues; until then manual canary |
| Admin TOTP | Second admin or wider admin exposure |
| `gateway` subcommand | A venue with a PC and a link needing a LAN relay |
| Reverse proxy (Caddy) | A second site must share :80/:443 |
| libvips | p95 render >200 ms sustained |
| Three-tier paging / on-call | Second person on support |
| Paid VPS | (subsumed by the EU-VPS trigger above) |

**Open questions to close (§14 → decisions):** is `guzva.ba` registered, and was
`imaliguzve.ba`? Is the pilot venue's link ≥ −65 dBm at the mounting point?
Does the first vendor want an English toggle?

Out of scope until Phase 3 at the earliest: streaming, motion/people counting,
facial recognition (permanently — D9), GPIO automation, multi-photo strips,
OAuth, AVIF/WebP, K8s, managed DB.

---

## 15. Phases

**Phase 0 — POC (done).** Local-only, 1 device.

**Phase 1 — Foundation (done, bench).** VM, protocol, server, firmware, static
public page, admin + owner consoles, backups, `make deploy`. Exit: 48 h from a
real link with recorded numbers (fresh-minute %, handshake ms, heap trend,
brownouts, post_fail rate, render ms on the e2-micro, still size); privacy tests
green; first deploy + first restore drill via scripts; a week with no GCP charge.
*Remaining: the soak numbers + render-ms logging (§14).*

**Phase 2 — Product polish & pilot (1–3 friendly venues).** *(v3.2 focus)*
- **Design system**: Tailwind pipeline (D6), shared components, dark mode, WCAG
  AA pass across all three surfaces.
- **Localisation**: Croatian catalog (D7) across public + vendor surfaces;
  formats; the privacy notice and camera-sign spec.
- **Public page**: in-place updates, states, QR poster, description/contact.
- **Admin**: fleet overview, grouped device form, venue timeline, activity view.
- **Vendor lifecycle** (§10): install gate checklist, onboarding, invite-link,
  pause/offboard, audit.
- **Overviews & logs** (§11): fleet attention sort, alert channel + dead-man,
  rendered logs, render-ms field.
- **Automation** (§12): CI PR gate, scheduled jobs, provisioning recipe.
- Firmware steps 5–7 hardening + reference-frame hash.
- Exit: 2 weeks with each pilot venue, <5 % missed minutes, zero raw leaks,
  runbook used once, a vendor uses their panel unaided.

**Phase 3 — Operate (≤10 venues).**
- Firmware: pull OTA + rollback (check the core's bootloader rollback first),
  manual canary.
- Server: claim flow, viewer/intent counters, "I'm coming", hours override.
- CI tag-triggered deploy; EU VPS if the trigger fired.
- Exit: 95 % minutes fresh, honest gaps, vendor dashboards in real use,
  MTTR <24 h without a developer visit.

**Phase 4 — Triggered work only (§14).**

---

## 16. Top risks

1. **Weak venue links (−75 dBm class) starve freshness** → install gate
   (non-negotiable), sensible intervals, quality via config, honest STALE.
2. **US-region latency to Bosnia** (D4) → keep-alive, 45–90 s intervals, gzip,
   generous fresh-connect budget; EU-VPS trigger armed.
3. **Power gremlins** (brownout/camera fails) → supply spec, degraded modes,
   counters; runbook leads with power.
4. **Privacy incident** → stylised-only, TLS only, never behind a proxy for
   ingest, no body logging, web-root allow-list, CI disk-scan, quarantine
   headers-only, no biometrics (D9), camera-notice spec.
5. **Free-tier drift or a billing surprise** → one VM/account, standard disk, US
   region, egress watched, $1 budget alert, EU-VPS/paid trigger, portable scripts.
6. **Single free VM, no SLA** → devices keep capturing; nightly backup;
   `provision.sh` rebuild; restore drill before the pilot.
7. **Silent backup rot** → drill with a timer, logged; quarterly.
8. **Scope creep** → §14 table; sell stills + freshness first.
9. **Auth bypass** → scope-by-cookie with the IDOR test; any new owner/admin
   capability re-runs it before shipping.
10. **Restart-proof migrations** → the `case 3:` outage shows migrations must be
    exercised across a *reopen*, not just a fresh DB; `TestReopenCurrentDatabase`
    is the guard, and the rule is in §1.
11. **Fleet-bricking OTA** (Phase 3) → rollback proven on a bench unit, manual
    canary, LAN-USB recovery documented.
12. **Legal/privacy drift in BiH** → keep the plan's privacy shape, re-check with
    a local adviser before the first paying venue (§13).
13. **Solo-builder bandwidth** → one approval-gated step at a time; every step has
    an acceptance check before the next starts; automation is how leverage is
    bought (§12).

---

## 17. Change log

**v3.2 → v3.3 (11 Oct 2026 — maintenance plan; no product change)**
- **No decisions changed.** v3.3 exists to record that the project entered a
  remediation pass: a four-way audit (firmware, server, UI/docs, DevOps)
  found the architecture sound but the hygiene, a set of live surface bugs,
  the missing CI substrate, and doc drift bad enough to mislead.
- **`docs/maintenance.md` is the working plan** for that pass (workstreams
  W0–W8, each item with evidence and an acceptance check). It defers to this
  file everywhere the two touch. Notable verdicts it records: the spec's
  "hourly-max reboot" is dropped (the task watchdog is the mechanism that
  was meant); secrets in firmware history must be scrubbed **before the
  first push to any remote**; the design-system's chip colours never
  compiled (dynamic class names) and the owner dash rendered raw i18n keys.

**v3.1 → v3.2 (09 Oct 2026 — product plan)**
- **Restructured from protocol-spike to product.** §§5–8 (firmware, server,
  render, ops) kept as the proven foundation; added the missing product plan.
- **New §9 Product surfaces & UX:** TailwindCSS design system (D6), public-page
  polish (+ QR poster, states, a11y, metadata), admin console rework (fleet
  overview, grouped config form), Croatian vendor console.
- **New §10 Vendor & user management:** lifecycle lead→surveyed→installed→
  active→paused→offboarded, install gate, invite-link, planes split, audit;
  billing deferred (D8).
- **New §11 Overviews, logs & audit:** fleet attention view, venue timeline,
  activity/audit tail, alerting + dead-man, render-ms field — without
  `/metrics` or a health-history table.
- **New §12 Automation & CI/CD:** PR gate, Tailwind-in-build, scheduled jobs,
  provisioning recipe, cert/expiry checks, scripted restore drill.
- **New §13 Bosnia deployment profile:** Croatian locale (D7), the US-region
  latency mitigations + EU-VPS trigger (D4), BiH privacy law / camera-notice /
  no-biometrics (D9), BAM/PDV deferred, `.ba` domain, vendor landscape, ISP
  reality.
- **Decisions added:** D6 (Tailwind), D7 (Croatian), D8 (vendor lifecycle, no
  billing), D9 (no biometrics). D4 annotated with the latency consequence.
- **Contracts added:** dogfood real (migrated) state; accessibility & honesty.
- **Status block** rewritten to current reality (server live, consoles built,
  ops workflow shipped, two hard bugs fixed, bench link marginal).

**v3.1 (09 Oct 2026 — bench cutover: Go server live on the real board)**
- Bench cutover executed (Go :443, benchmark cert, rig retired); device plane
  completed (22 keys, migration v2, `guzva cfg`, `provision -token`); per-ingest
  journal line; TLS profile pinned (tickets off, TLS 1.2); firmware read-path bug
  fixed; `.env`/unit/releases workflow.

**v3.1 (09 Oct 2026 — status update, no design change)**
- Rename imaliguzve → guzva everywhere. Go skeleton restored + migrated. Status
  block replaced the POC note. §5 connect budget 12 s. §10 Phase 1 annotations.

**v3 → v3.1**
- Hosting: dedicated GCP Always Free e2-micro (D4) with free-tier checklist,
  firewall, resource guards, $1 budget alert.
- Caddy removed: TLS via `autocert` in the binary; ingest on its own hostname.
- Public side pre-rendered static (D5); history not routable; absolute clock.
- Owner area + sessions table deferred to Phase 3 (later built without sessions).
- Pilot schema three tables; hours JSON; health history to slog; signed cookie.
- Config via env/flags; no `/metrics`. Litestream → nightly snapshot.
- Cloudflare moved to a trigger with the "never proxy ingest" rule.

**v2 → v3**
- Removed/deferred: SD queue, `/v1/backfill`, retention thinning, per-venue
  workers, `Repository`/`ObjectStore`, token rotation, claim codes, staged OTA,
  TOTP, gateway, viewer/intent tables, thumb output, paging, separate
  heartbeat/rotate endpoints. Merged heartbeat into `/v1/ingest`; quality
  negotiation into `desired_cfg`; retention 48 h.
