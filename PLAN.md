# imaliguzve.ba — Dev / Production Plan v3 (lean, VPS-hosted, Go)

Supersedes v2. Same architecture (single Go binary + htmx on one cheap VPS,
ESP32-CAMs push outbound, no inbound firewall rules anywhere), cut down to what
1–10 venues actually need. Everything cut is kept in §9 with a measurable
trigger for bringing it back, so nothing is lost, only deferred.

POC state: lean ESP32 capture firmware (`src/main.cpp`), Go/htmx console
(`web/`) with poller, stdlib stylise, frame-hash freshness, venue settings in
`site.json`.

Bench facts (08 Oct 2026, still binding): RSSI −72…−75 → 12 s+ fetches, ~50%
join fails; thin USB → brownout + camera probe fails; NVS persists settings;
external antenna without RF-jumper move = zero gain; QQVGA q18 ≈ 2–30 KB/frame.

---

## 0. Decisions log

| ID | Decision | Consequence |
|----|----------|-------------|
| D1 | Stylise **server-side** | Raw JPEG crosses the wire device→VPS (TLS only), lives in RAM for one `Process` call, never written. Privacy wording fixed accordingly (§1). Styles stay changeable without a firmware release. |
| D2 | Stills history is **not** a product, but may become one | 48 h ring per venue + last-good. Schema/sidecars keep `preset@version`, hashes, `received_at` so thinning/long retention can be added later without a migration of meaning. |
| D3 | Owner auth = **per-venue password + cookie session** | bcrypt, HttpOnly/Secure/SameSite cookie, server-side sessions in SQLite, one scope-check middleware. No users/roles tables. One separate admin credential. |

---

## 1. Contracts (kept from v2, non-negotiable)

- **Freshness:** server `received_at` is canonical; device clock is advisory only.
- **Idempotency:** `UNIQUE(venue_id, capture_id)` + sha256 guard; a retry returns 409 with the same body as a 201.
- **Settings planes:** device-plane (interval/quality; confirmed by read-back; mismatch = visible amber state) vs site-plane (name/style/hours; cloud-only; zero device traffic).
- **Never publish a failure:** keep last-good frame, show STALE. Garbage never replaces a good still.
- **Degradation:** network/auth/server errors never erase identity. Only installer factory-reset erases.
- **Privacy (reworded for D1):**
  - raw JPEG is on the wire device→VPS inside TLS, exists in server RAM inside one `Process` call, is zeroed on return;
  - at rest, on any public route, in logs, and in quarantine: stylised renders only (quarantine = headers-only records);
  - request bodies are never logged (Caddy and app);
  - proven by tests and a disk scan in CI, not by policy.
- **Isolation:** push-only. The server never dials devices (no SSRF surface).

---

## 2. Architecture

```
[venue LAN]
  ESP32-CAM (lean firmware) ──HTTPS POST /v1/ingest──┐   (outbound only)
                                                     v
                                  [VPS: smallest shared-vCPU box, Ubuntu 24.04]
                                  Caddy (TLS only) → Go binary `imaliguzve`
                                     ├─ ingest   (/v1/ingest, /healthz)
                                     ├─ public   (/v/{slug})
                                     ├─ owner    (/owner/…)
                                     ├─ admin    (/admin/…)
                                     ├─ sched    (one ticker goroutine)
                                     ├─ SQLite (WAL) + Litestream → object storage
                                     └─ local disk: venues/{id}/stills/…
```

Stack: Go stdlib `net/http` (1.22 ServeMux patterns), `html/template` +
`embed`, vendored htmx (no CDN), `log/slog`, hand-rolled `/metrics`,
`modernc.org/sqlite` (CGO-free → one static binary), bcrypt from
`golang.org/x/crypto`, token buckets from `golang.org/x/time/rate`.
No framework, no Docker, no ORM.

---

## 3. Repo layout (modules = packages, dependencies flow downward only)

```
cmd/imaliguzve/       main: `serve` now; `gateway` only if its trigger fires
internal/proto/       wire types + constants; mirrors docs/proto-v1.md; used by server + fakecam
internal/store/       SQLite, embedded migrations (PRAGMA user_version); the ONLY package with SQL
internal/render/      Process(ctx, FrameIn) → Outputs; Preset registry
internal/frames/      ingest handler logic, save, prune
internal/venue/       hours, open/closed, freshness — pure functions, no I/O
internal/auth/        bcrypt, sessions, device tokens, scope middleware
internal/web/         public + owner + admin handlers, embedded templates
internal/sched/       freshness rollup, missed-poll detector, pruner, alerts
firmware/             PlatformIO project
tools/fakecam/        fast / slow / flappy / dup / partial harness
deploy/               provision.sh, Caddyfile, systemd unit, litestream.yml
docs/                 proto-v1.md, runbook.md, ops-log.md
```

Interface rule: an interface exists only when a second implementation exists
today. Currently: `Clock` (tests) and `Preset` (poster, mono). No
`Repository` or `ObjectStore` interface until Postgres/S3 are actually needed.

Handlers never touch SQL. Every `store` method that reads venue data takes a
`venue_id`; owner handlers get it from the session only, never from the URL.

---

## 4. Protocol v1 (`docs/proto-v1.md` is the single source of truth)

One endpoint carries everything:

`POST /v1/ingest` — multipart: `meta` (JSON) + optional `frame` (JPEG).
- `meta`: `capture_id` (`C<boot>-<ctr>-<rand4>`, monotonic from NVS, bound to
  identity), `fw`, `rssi`, `free_heap`, `cfg_ver_applied`, counters
  (`boot, brownout, join_fail, post_fail, tls_fail, capture_fail`), `reason`.
- No `frame` = health-only beat (used in camera-degraded mode).
- Auth: `Authorization: Bearer <device token>`; server stores only sha256 of it.
- Responses: `201` stored · `409` duplicate (same body as 201) · `422`
  rejected + reason (quarantined) · `401` revoked/unknown · `413` too big ·
  `429` + `Retry-After` · `507` disk degraded.
- Response body: `{ desired_cfg: {ver, interval_s, quality}, next_s, server_time }`.
  Downlink piggybacks on the uplink: no extra round-trip on weak links.
- `interval_s` 30–300 (default 60); `quality` replaces v2's separate
  quality-negotiation mechanism — the server just raises/lowers it via config.

Versioning: path `/v1`; only additive optional fields within a major;
`X-Proto` header; dual-serve a major for 6 months when `/v2` appears.

Later (Phase 3): `POST /v1/claim` (single-use code → token). Until then the
installer provisions the token over the LAN/USB provisioning path.

---

## 5. Firmware (M1) — dumb sensor, smart about survival

Does: capture on a timer, POST outbound, apply remote config with read-back,
survive brownout / WiFi loss / camera death without humans.

Never: serve public traffic, MJPEG, ML/stylise, face handling, open inbound
ports. Prod builds delete: port-81 stream, motion, timelapse-as-product,
RGB/gray endpoints, LAN OTA form. Kept (caged): `/api/status` (extended),
`/jpg` rate-limited, provisioning, mDNS `imaliguzve-<serial>.local`.

**No frame queue in v3.** If an upload fails the frame is dropped,
`post_fail` is counted, the next tick captures a fresh one. A minutely
barbershop still is worthless once old; the server shows an honest STALE gap.
(SD ring + backfill return only if the §9 trigger fires.)

Mechanisms kept: HTTPS via `WiFiClientSecure` + embedded CA bundle (never
`setInsecure` in prod, grep-gated); capture never waits for NTP; chunked 4–8 KB
upload with immediate `fb_return`; single in-flight request; 8 s connect /
25 s POST timeouts; VGA q14–18 cap; static per-device bearer token + capture
counter in NVS (`prov` ns); desired-cfg version with applied read-back in every
POST; camera probe fail → exponential backoff → health-only degraded mode + LED
pattern after 5 (no hot reboot loop); brownout detector on, XCLK 16 MHz
fallback, 470–1000 µF cap in the install spec; task + upload watchdogs with an
hourly-max reboot; keeps partition table `Default 4MB with spiffs` (never hugeapp).

On `401` (revoked): device keeps identity, drops to slow health-only beats
(60→600 s backoff) with an amber LED pattern until re-provisioned.

**Early spike (do first, before anything else is built on it):** TLS cost on
the real link. Measure handshake time at −65 and −72 dBm, test HTTP keep-alive
reuse of one `WiFiClientSecure` across ticks (Caddy idle timeout is
comfortably above 60 s), and heap headroom with TLS + camera buffers live.

Build order:
1. prod skeleton + flags (strip dead endpoints)
2. NVS schema + factory reset
3. non-blocking WiFi manager
4. HTTPS ingest POST + TLS spike numbers
5. camera degraded mode + counters
6. desired-config read-back
7. LAN cage + reference-frame hash
8. *(Phase 3)* pull OTA + rollback

Acceptance: ≥95% POST success at ≥ −65 dBm over 48 h; heap-leak bounded over
48 h; provision→first-frame ≤15 min; camera-death and WiFi-loss tests recover
without a power cycle.

---

## 6. Server

### 6.1 Ingest flow
`auth → rate-limit (per-IP + per-device) → MaxBytesReader → parse meta →
idempotency check → render.Process → write file + row in one logical step →
update venue.last_frame_at → respond with desired_cfg`.

Bounded work, no worker package: net/http gives one goroutine per connection;
a single global semaphore caps concurrent `Process` calls; server
`ReadHeaderTimeout`/`ReadTimeout` stop slow clients tying up resources.
Gate: fakecam run with 1 slow + 19 fast clients — fast p95 unaffected.

### 6.2 Tables (v3 set)
`venues`, `devices`, `frames` (`UNIQUE(venue_id, capture_id)`, hash,
`received_at`, preset@version, quarantine flag), `hours`, `settings_log`,
`sessions`, `health_events`. Deferred until their feature exists:
viewer_hits, intents, OTA releases/state, claim codes.

SQLite: WAL, single writer connection, `busy_timeout`, embedded migrations.

### 6.3 Retention (D2)
48 h ring of public renders per venue (config `retain_hours`, default 48) +
last-good always kept. Pruner runs hourly: one SQL select + file deletes.
`health_events`/meta 90 d. Raw is never written, so there is nothing else to
retain. Capacity estimate (replace with measured numbers in Phase 1): ~25 GB
worst case at 200 venues → the base disk is fine well past the pilot.

### 6.4 Pages
- **Public P1:** `/v/{slug}` — stylised still, fresh/STALE badge from
  server time, open/closed from hours. Frame URL carries an unguessable key.
- **Owner P1:** login; edit name/style/hours; reference-frame sign-off;
  read-only device status (last seen, RSSI, applied cfg).
- **Admin P1:** fleet board (last seen, RSSI, counters, fw version); create
  venue; issue/revoke device token; set owner password; ticket list.

### 6.5 Auth (D3)
Owner: one bcrypt password per venue → server-side session (HttpOnly, Secure,
SameSite=Lax) + per-form CSRF token. A single scope middleware injects
`venue_id` from the session. Admin: separate credential, throttled login,
separate cookie name/path. No roles table. TOTP deferred (§9).
Required test: owner-A cannot read or modify venue-B (IDOR test, per handler).

### 6.6 Observability and alerting
`/healthz` (+ TLS expiry), hand-rolled `/metrics`, per-venue board.
Missed-poll detector = one SQL query on `last_frame_at`.
- STALE badge after >3 min.
- Push alert (one channel, e.g. ntfy/Telegram): open-hours stale >15 min,
  or ≥3 venues stale at once, or checker dead-man fired.
- Dead-man: external free uptime/heartbeat service pinged by `sched`; it also
  watches `/healthz`.

---

## 7. Image pipeline (`internal/render`) — server-side (D1)

`Process(ctx, FrameIn) → Outputs`, `Clock` injected.

Stages: decode (4 MiB cap) → validate (magic/size/dims/truncation/dark/
dead-sensor; the bench 1884-B black frames → `dark` quarantine) → normalize
(bilinear to 640 w; EXIF dropped by construction and asserted) →
`Preset.Render` (pure, deterministic, versioned) → encode public 640 w q68 →
store → raw zeroed on return.

Presets: `poster` (4-level), `mono`. There is **no raw preset.** A new style =
one file + one registry line + one golden test. Style changes apply to new
frames only (documented; also a privacy feature).

Single output at first. A 320 w thumb is added when a page actually needs it.

Quarantine: JSONL, headers-only records, never frame bytes. Never publish a
failure: last-good stays, STALE shows.

Performance: ~30–50 ms/frame with the stdlib → 200 venues ≈ 0.13 core.
libvips only if p95 >200 ms sustained, behind the same signature.

Privacy proofs as tests: no-raw-persist (post-run disk scan), no-raw-route,
EXIF-stripped, input-zeroed, debug-tag-absent-in-prod, request-body-never-logged.
Also: golden renders, corrupt corpus, decode fuzz, determinism ×100, race hammer.

---

## 8. VPS ops

- **Box:** smallest shared-vCPU plan of the chosen provider (Hetzner is the
  default candidate), Ubuntu 24.04. Re-check current pricing in Phase 1 — v2's
  ~€14/mo @10 venues likely over-provisions (volume/object storage aren't needed
  yet) and should be re-estimated with real numbers.
- **TLS:** Caddy, three-line Caddyfile, TLS only. Rate limits live in Go.
- **Deploy:** tag → build → scp → `systemctl restart` (<2 s gap, absorbed by
  idempotent ingest and device retry on next tick); last 3 releases kept on disk.
  No Docker.
- **Backups:** SQLite via Litestream to object storage + nightly snapshot
  (RPO <5 min). Images are NOT backed up 1:1 (they're a 48 h ring); meta + `ref/`
  only — documented. One restore drill logged with a timer **before the pilot**,
  then quarterly.
- **Disk:** keep 10% reserve; <15% free → DEGRADED + `507`; <5% → read-only +
  alert. Separate volume only when the §9 trigger fires.
- **Security:** SSH keys only, UFW + provider firewall, unattended-upgrades,
  unguessable frame keys, throttled logins.
- **Disaster note:** fleet is unaffected by VPS loss beyond lost frames
  (devices keep capturing; next tick after restore succeeds). Restore order:
  box → IP/DNS → DB (Litestream) → secrets → verify → fleet recovers by itself.
- **Docs mandatory:** `docs/runbook.md` (leads with power for support) and
  `docs/ops-log.md`.

---

## 9. Deferred, with triggers

| Deferred | Trigger to build it |
|----------|---------------------|
| SD frame queue + backfill endpoint | Pilot data shows outages that were bridgeable and owners care about gaps |
| Long retention / hourly thinning / history UI | First customer asks for history (D2) |
| Thumb output | A page needs it |
| Postgres behind an interface | p95 ingest >500 ms sustained or ~25+ venues |
| `ObjectStore` / S3 | Disk >70% on the base disk or multi-box |
| Separate image volume | Disk >70% |
| Token rotation (24 h overlap) | ~25 venues or first token-leak scare |
| Claim-code provisioning | Installs by someone other than you |
| Staged OTA (1→3→all) | ~25 venues; until then manual canary (device #1 first) |
| Admin TOTP | Second admin or any public admin exposure |
| `gateway` subcommand | A venue with a PC and a link that needs a LAN relay |
| Viewer/intent counters, "I'm coming" | Phase 3 feature work (adds its tables then) |
| libvips | p95 render >200 ms sustained |
| Three-tier paging / on-call | Second person on support |

Out of scope until Phase 3 at the earliest: streaming, motion/people-counting,
GPIO automation, multi-photo strips, OAuth, AVIF/WebP, K8s, managed DB.

---

## 10. Phases

**Phase 0 — POC (done).** Local-only, 1 device.

**Phase 1 — Thin slice on the real VPS (1 device, real venue link).**
- Deploy: provision script, Caddy, systemd, Litestream.
- Protocol: one-page `proto-v1.md`, `POST /v1/ingest`, device token.
- Server: store + ingest + `render` skeleton (poster/mono, validation gate) +
  public `/v/{slug}` with fresh/STALE; fakecam.
- Firmware: build steps 1–4 incl. the TLS spike (non-blocking WiFi, HTTPS POST,
  drop-on-fail).
- Exit: 48 h from a real venue link with recorded numbers — fresh-minute %,
  TLS handshake ms, heap trend, brownouts, post_fail rate; privacy disk-scan
  test green; first deploy via the script. **Architecture go/no-go decision
  made from measured data.**

**Phase 2 — Pilot hardening (1–3 friendly venues).**
- Firmware: steps 5–7 (degraded camera mode, config read-back, LAN cage).
- Server: owner P1 (login, site edit, reference sign-off, status), admin P1
  (fleet board, tokens), health counters, retention pruner, scheduler + alerts,
  privacy test suite, IDOR test, fakecam isolation gate.
- Ops: restore drill logged, runbook written, install gate enforced
  (RSSI ≥ −65, ping <200 ms, supply spec, reference-frame sign-off).
- Exit: 2 weeks/venue, <5% missed minutes, zero raw leaks (disk-scan green),
  runbook used once.

**Phase 3 — Operate (≤10 venues).**
- Firmware: config read-back at fleet level, pull OTA + rollback (first check
  whether the Arduino-ESP32 core's bootloader has app rollback enabled before
  writing a custom boot counter; manual canary: device #1 first).
- Server: claim flow, viewer/intent counters, "I'm coming", hours override,
  device read-back view.
- Exit: 95% minutes fresh, honest gaps on outages, owner dashboard in real use,
  MTTR <24 h without a developer visit.

**Phase 4 — Triggered work only (§9).**

---

## 11. Top risks

1. **Weak venue links (−75 dBm class) starve freshness** → install gate
   (non-negotiable), small frames, quality via config.
2. **TLS handshake cost and heap on the ESP32 over a weak link** (new in v3) →
   Phase 1 spike first; keep-alive reuse; fall back to lower interval or quality
   if numbers demand it.
3. **Power gremlins** (brownout/camera fails seen on bench) → supply spec,
   degraded modes, counters; the support runbook leads with power.
4. **Privacy incident** — raw frames cross the wire (D1) → TLS only, no body
   logging, raw never written, CI disk-scan, quarantine headers-only.
5. **Silent backup rot** → restore drill before the pilot, then quarterly, logged.
6. **Scope creep** → §9 table; sell stills + freshness first.
7. **Auth bypass owner-A → venue-B** → session-scoped `venue_id`, store methods
   take `venue_id`, dedicated IDOR test.
8. **Fleet-bricking OTA** (Phase 3) → rollback proven on a bench unit before
   any venue; manual canary; LAN-USB recovery documented.
9. **Solo-builder bandwidth** → one approval-gated step at a time; each phase
   step has an acceptance check before the next starts.

---

## 12. Change log vs v2

Removed/deferred: SD queue, `/v1/backfill`, retention thinning, per-venue worker
queues, `Repository`/`ObjectStore` interfaces, token rotation, claim codes,
staged OTA, TOTP, gateway, viewer/intent tables (until Phase 3), thumb output,
three-tier paging, separate `/v1/heartbeat` and `/v1/token/rotate`.
Merged: heartbeat into `/v1/ingest`; quality negotiation into `desired_cfg`.
Changed: VPS from Phase 1 (no local-first stage); privacy wording (D1); admin
and owner auth simplified (D3); retention 48 h (D2).
Added: TLS spike, Phase 1 go/no-go on measured data, trigger table, decisions log.
