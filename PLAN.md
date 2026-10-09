# imaliguzve.ba — Dev / Production Plan v3.1 (lean, free-tier VM, Go)

Supersedes v3. Same architecture (single Go binary on one VM, ESP32-CAMs push
outbound, no inbound firewall rules at venues), now targeted at a dedicated
Google Cloud Always Free e2-micro, with TLS inside the binary (no Caddy), a
static public side, and a smaller pilot schema. Everything cut or deferred is in
§9 with a measurable trigger for bringing it back.

Status (09 Oct 2026 — supersedes the old POC-state note):
- **Bench rig live on the VPS** (`deploy/testrig/`, systemd `camtest`,
  `ssh root@guzva`): Python proto-v1 receiver on :443 (self-signed,
  epoch-valid cert, raw IP 34.139.58.143) + :80→301, 23/23 e2e tests green,
  live `desired_cfg` push via `cfg.json` verified end-to-end. **Scaffolding,
  not §6** — it exists to prove the protocol and the firmware; the Go server
  is still unbuilt.
- **Firmware build steps 1–6 done, step 7 minus reference-frame hash**: capture
  cadence, multipart HTTPS ingest, NVS identity, non-blocking WiFi, camera
  degraded mode + counters, full OV2640 `desired_cfg` with read-back (ver=7
  applied live), LAN cage (`/api/status`, `/jpg`, `/ping`, caged factory
  reset), mDNS, serial CLI, status LED. Three real bugs found and fixed:
  one corrupt base64 char in the embedded CA PEM (handshake died as
  `X509 format invalid`); a fixed 12 s single-SYN connect budget (now a
  failure-only 6 s probe, so healthy ticks open one connection); and a
  server-side accept-loop jam in the rig (TLS handshake ran inside
  `accept()` on one thread → backlog 5 full → kernel silently dropped every
  new SYNs while established keep-alives kept working — fixed with
  per-connection handshake, backlog 128). Commits `86b7dd7`, `eb771bb`.
- **TLS spike (real link):** keep-alive reuse across ticks works; fresh tick
  ≈ conn 2.5 s + write 14 ms + response 4 s; RSSI −57…−70 at the bench.
- **Known gaps vs this plan:** ingest host compiled into `settings.h` (§4
  wants it in NVS from day 1); no reference-frame hash; Go server, autocert,
  `setup.sh`/`make deploy`, backups — all not started. (`proto-v1.md` written
  09 Oct from the proven rig+firmware.)

Bench facts (08 Oct 2026, still binding): RSSI −72…−75 → 12 s+ fetches, ~50%
join fails; thin USB → brownout + camera probe fails; NVS persists settings;
external antenna without RF-jumper move = zero gain; QQVGA q18 ≈ 2–30 KB/frame.
Relocation result (09 Oct): at RSSI ≥ −57 joins are instant and packet loss
vanishes — link quality was real but *not* the whole story (see the accept-loop
jam above). Brownout counter is live and reads 3 → supply spec still suspect.

---

ssh info: root@guzva

---

## 0. Decisions log

| ID | Decision | Consequence |
|----|----------|-------------|
| D1 | Stylise **server-side** | Raw JPEG crosses the wire device→VM (TLS only), lives in RAM for one `Process` call, never written. Styles stay changeable without a firmware release. |
| D2 | Stills history is **not** a product, but may become one | 48 h ring per venue + last-good. Sidecars keep `preset@version`, hashes, `received_at`, so longer retention can be added later. |
| D3 | Owner auth = per-venue password + cookie session | **Designed, built later** (Phase 3, when a venue asks to self-edit). Pilot: you are the only editor, via admin. |
| D4 | Host on a **dedicated GCP Always Free e2-micro**; punff is gone, so the box is Guzva-only | Go terminates TLS itself (`autocert`), no Caddy. See the free-tier checklist in §8. |
| D5 | Public side = **pre-rendered static files** | Public requests never touch SQLite, templates, or the render pipeline. History is not publicly routable at all. |

---

## 1. Contracts (non-negotiable)

- **Freshness:** server `received_at` is canonical; device clock is advisory only.
- **Idempotency:** `UNIQUE(venue_id, capture_id)` + sha256 guard; a retry returns 409 with the same body as a 201.
- **Settings planes:** device-plane (interval/quality; confirmed by read-back; mismatch = visible amber state) vs site-plane (name/style/hours/timezone; cloud-only; zero device traffic).
- **Never publish a failure:** keep last-good frame, show STALE. Garbage never replaces a good still.
- **Degradation:** network/auth/server errors never erase identity. Only installer factory-reset erases.
- **Privacy:**
  - raw JPEG is on the wire device→VM inside TLS, exists in server RAM inside one `Process` call, is zeroed on return;
  - at rest, on any public route, in logs, and in quarantine: stylised renders only (quarantine = headers-only records);
  - request bodies are never logged;
  - the web root contains only `index.html` + `latest.jpg` per venue; the 48 h ring lives outside it;
  - proven by tests and a disk scan in CI, not by policy.
- **Isolation:** push-only. The server never dials devices (no SSRF surface).
- **Ingest host rule:** ingest is never placed behind a third-party TLS-terminating proxy (a CDN would see raw frames). Public pages may be.

---

## 2. Architecture

```
[venue LAN]
  ESP32-CAM ──HTTPS POST ingest.imaliguzve.ba/v1/ingest──┐   (outbound only)
                                                         v
                      [GCP e2-micro, Always Free, Ubuntu 24.04, dedicated]
                      Go binary `imaliguzve`  (autocert TLS on :80/:443)
                         ├─ ingest  (/v1/ingest, /healthz)
                         ├─ public  (static files: data/public/{slug}/index.html + latest.jpg)
                         ├─ admin   (/admin/…, signed-cookie session)
                         ├─ sched   (30 s tick: state flips → page rewrite, missed-poll, pruner, alerts)
                         ├─ SQLite (WAL)  ── nightly snapshot ──> Cloud Storage bucket
                         └─ disk: data/public/ · data/stills/ (48 h ring) · data/ref/ · data/certs/
```

Stack: Go stdlib `net/http` (1.22 ServeMux patterns), `html/template` + `embed`,
`time/tzdata` embedded, `log/slog` to stdout (journald), `modernc.org/sqlite`
(CGO-free → trivial cross-compile), `golang.org/x/crypto` (bcrypt + `autocert`),
`golang.org/x/time/rate`. Config = env vars + stdlib `flag`; secrets in
`/srv/guzva/.env` (mode 600, systemd `EnvironmentFile`).
No framework, no Docker, no ORM, no reverse proxy.

---

## 3. Repo layout (modules = packages, dependencies flow downward only)

```
cmd/imaliguzve/       main: `serve` (flags/env, slog, autocert wiring)
internal/proto/       wire types + constants; mirrors docs/proto-v1.md; used by server + fakecam
internal/store/       SQLite, embedded migrations (PRAGMA user_version); the ONLY package with SQL
internal/render/      Process(ctx, FrameIn) → Outputs; Preset registry
internal/frames/      ingest logic, save, prune
internal/venue/       hours, timezone, open/closed, freshness — pure functions, no I/O
internal/pages/       pre-render public index.html + latest.jpg (atomic temp+rename)
internal/admin/       admin handlers + embedded templates
internal/auth/        device tokens, admin login, signed cookie, rate limits
internal/sched/       30 s tick, pruner, missed-poll, alerts
firmware/             PlatformIO project
tools/fakecam/        one small Go client: fast / slow / flappy / dup modes
deploy/               Makefile, imaliguzve.service, backup.service + .timer, setup.sh
docs/                 proto-v1.md, runbook.md, ops-log.md
```

Interface rule: an interface exists only when a second implementation exists
today. Currently: `Clock` (tests) and `Preset` (poster, mono). No `Repository`,
`ObjectStore`, config, or metrics packages until needed.

Handlers never touch SQL. Layout on the box:
`/srv/guzva/{bin/releases/<tag>, bin/current → symlink, data/, .env}`, owned by a
dedicated `guzva` user.

---

## 4. Protocol v1 (`docs/proto-v1.md` is the single source of truth)

`POST /v1/ingest` — multipart: `meta` (JSON) + optional `frame` (JPEG).
- `meta`: `capture_id` (`C<boot>-<ctr>-<rand4>`, monotonic from NVS, bound to
  identity), `fw`, `rssi`, `free_heap`, `cfg_ver_applied`, counters
  (`boot, brownout, join_fail, post_fail, tls_fail, capture_fail`), `reason`.
- No `frame` = health-only beat (camera-degraded mode).
- Auth: `Authorization: Bearer <device token>`; server stores only sha256.
- Responses: `201` stored · `409` duplicate (same body as 201) · `422` rejected +
  reason (quarantined) · `401` revoked/unknown · `413` too big · `429` +
  `Retry-After` · `507` disk degraded.
- Response body: `{ desired_cfg: {ver, interval_s, quality, …}, next_s, server_time }`
  (full device-plane key set — `docs/proto-v1.md`, proven live 09 Oct 2026).
- `interval_s` 30–300 (default 60); quality is adjusted through `desired_cfg`.

Hostnames: ingest lives on its own name (`ingest.imaliguzve.ba`) from day 1,
stored in device NVS (not compiled in), so a later CDN for the public name needs
no reflash. Both names point at the same VM; `autocert` serves both.

Versioning: path `/v1`; only additive optional fields within a major; `X-Proto`
header; dual-serve a major for 6 months when `/v2` appears.

Later (Phase 3): `POST /v1/claim`. Until then the installer provisions the token
over the LAN/USB provisioning path.

---

## 5. Firmware (M1) — dumb sensor, smart about survival

Does: capture on a timer, POST outbound, apply remote config with read-back,
survive brownout / WiFi loss / camera death without humans.

Never: serve public traffic, MJPEG, ML/stylise, face handling, open inbound
ports. Prod builds delete: port-81 stream, motion, timelapse-as-product,
RGB/gray endpoints, LAN OTA form. Kept (caged): `/api/status` (extended), `/jpg`
rate-limited, provisioning, mDNS `imaliguzve-<serial>.local`.

**No frame queue.** A failed upload drops the frame, counts `post_fail`; the next
tick captures a fresh one. The server shows an honest STALE gap. (SD ring +
backfill return only if the §9 trigger fires.)

Mechanisms: HTTPS via `WiFiClientSecure` + embedded CA bundle (never
`setInsecure` in prod, grep-gated); capture never waits for NTP; chunked 4–8 KB
upload with immediate `fb_return`; single in-flight request; 12 s
connect/handshake + 25 s POST timeouts (measured 09 Oct); VGA q14–18 cap; static per-device bearer token, ingest hostname
and capture counter in NVS (`prov` ns); desired-cfg version with applied
read-back in every POST; camera probe fail → exponential backoff → health-only
degraded mode + LED pattern after 5 (no hot reboot loop); brownout detector on,
XCLK 16 MHz fallback, 470–1000 µF cap in the install spec; task + upload
watchdogs with an hourly-max reboot; partition table `Default 4MB with spiffs`
(never hugeapp).

On `401` (revoked): keep identity, slow health-only beats (60→600 s backoff),
amber LED until re-provisioned.

**Early spike (first, before anything is built on it):** TLS cost on the real
link to a US-region VM (expect roughly 100–150 ms RTT — measure it). Time the
handshake at −65 and −72 dBm, test HTTP keep-alive reuse of one
`WiFiClientSecure` across ticks, and check heap headroom with TLS + camera
buffers live.

Build order: (1) ✓ prod skeleton + flags → (2) ✓ NVS schema + factory reset →
(3) ✓ non-blocking WiFi manager → (4) ✓ HTTPS ingest POST + TLS spike numbers →
(5) ✓ camera degraded mode + counters → (6) ✓ desired-config read-back →
(7) LAN cage ✓ / reference-frame hash ✗ → (8, Phase 3) pull OTA + rollback.

Acceptance: ≥95% POST success at ≥ −65 dBm over 48 h; heap-leak bounded over
48 h; provision→first-frame ≤15 min; camera-death and WiFi-loss tests recover
without a power cycle.

---

## 6. Server

### 6.1 Ingest flow
`auth → rate-limit (per-IP + per-device) → MaxBytesReader → parse meta →
idempotency check → render.Process → write ring file + frames row → atomically
replace data/public/{slug}/latest.jpg → update venue.last_frame_at → rewrite
index.html if state changed → respond with desired_cfg`.

Bounded work, no worker package: net/http gives one goroutine per connection; a
global semaphore caps concurrent `Process` calls; server `ReadHeaderTimeout` /
`ReadTimeout` stop slow clients tying up resources. Gate: fakecam run with
1 slow + 19 fast clients — fast p95 unaffected.

### 6.2 Tables (pilot set: three)
- `venues`: id, slug, name, tz, style, `hours_json`, ref_hash, last_frame_at.
- `devices`: id, venue_id, token_sha256, fw, last_seen_at, last_rssi,
  cfg desired/applied versions, latest counters. (Health *history* goes to the
  slog stream, not a table.)
- `frames`: venue_id, capture_id, hash, received_at, preset@version, quarantine
  flag; `UNIQUE(venue_id, capture_id)`.

No sessions table (admin uses a signed cookie), no `settings_log` (changes are
slog audit lines), no `health_events`. Added only with their features: users/
sessions (owner area), viewer_hits, intents, OTA releases/state, claim codes.

SQLite: WAL, single writer connection, `busy_timeout`, embedded migrations.

### 6.3 Retention (D2)
48 h ring per venue in `data/stills/{venue_id}/` (config `retain_hours`, default
48) + last-good always kept. Hourly pruner: one SQL select + file deletes. Raw
is never written. Capacity estimate (replace with measured numbers in Phase 1):
well under the 30 GB disk for the pilot.

### 6.4 Pages
- **Public (static, D5):** `data/public/{slug}/index.html` + `latest.jpg`, served
  by `http.FileServer` rooted at `data/public` — no DB access, no per-request
  rendering. The page shows: venue name, still (`latest.jpg?v=<received_at>`),
  badge (Fresh / STALE / Closed), today's hours in venue-local time, and an
  absolute "Updated HH:MM" (never a relative age, which a static file can't keep
  correct). `<meta refresh 30>`. `Cache-Control: public, max-age=30`.
  `index.html` is rewritten only when state changes (frame arrives, or the
  scheduler sees fresh→STALE / open→closed). Writes are temp-file + rename.
  After a restart, `sched` re-derives state within one tick.
- **Admin P1:** fleet board (last seen, RSSI, counters, fw); create venue (slug,
  tz, style, hours); issue/revoke device token (shown once); record the
  reference-frame sign-off (done in person at install; ref hash stored on the
  venue, audit line logged).
- **Owner area:** deferred (D3, Phase 3).

### 6.5 Auth
- **Device:** bearer token, sha256-hashed at rest, revocable.
- **Admin:** single bcrypt credential from `.env`; throttled login (token
  bucket); HMAC-signed session cookie (`HttpOnly`, `Secure`, `SameSite=Strict`)
  with secret from `.env`. No CSRF tokens needed for a single-admin
  `SameSite=Strict` app. Limitation, accepted: logout can't revoke server-side
  except by rotating the secret.
- **Owner (Phase 3):** per-venue bcrypt password → server-side session (adds a
  sessions table) + one scope middleware injecting `venue_id`; IDOR test
  owner-A vs venue-B is required before it ships.

### 6.6 Observability and alerting
`/healthz` (+ TLS expiry) and the fleet board; no `/metrics` until a trigger.
Missed-poll detector = one SQL query on `last_frame_at`.
- STALE badge after >3 min.
- Push alert (one channel, e.g. ntfy/Telegram): open-hours stale >15 min, or ≥3
  venues stale at once, or dead-man fired.
- Dead-man: an external free heartbeat service pinged by `sched`; it also watches
  `/healthz`. (Check its current free limits when you pick one.)

---

## 7. Image pipeline (`internal/render`) — server-side (D1)

`Process(ctx, FrameIn) → Outputs`, `Clock` injected.

Stages: decode (4 MiB cap) → validate (magic/size/dims/truncation/dark/dead-
sensor; the bench 1884-B black frames → `dark` quarantine) → normalize (bilinear
to 640 w; EXIF dropped by construction and asserted) → `Preset.Render` (pure,
deterministic, versioned) → encode public 640 w q68 → store → raw zeroed on
return.

Presets: `poster` (4-level), `mono`. There is **no raw preset.** A new style =
one file + one registry line + one golden test. Style changes apply to new
frames only (documented; also a privacy feature).

Single output at first; a 320 w thumb is added when a page needs it.

Quarantine: JSONL, headers-only records, never frame bytes. Never publish a
failure: last-good stays, STALE shows.

Performance: ~30–50 ms/frame with the stdlib → 200 venues ≈ 0.13 core on a
fast core; on the shared-vCPU e2-micro, **measure in Phase 1**. libvips only if
p95 render >200 ms sustained, behind the same signature.

Privacy proofs as tests: no-raw-persist (post-run disk scan), no-raw-route,
web-root-contains-only-allowed-files, EXIF-stripped, input-zeroed,
debug-tag-absent-in-prod, request-body-never-logged. Also: golden renders,
corrupt corpus, decode fuzz, determinism ×100, race hammer.

---

## 8. VM ops (GCP Always Free e2-micro, dedicated)

**Free-tier checklist** (conditions as found in public sources on 09 Oct 2026;
re-check on the billing page, terms can change):
- machine type e2-micro, region one of `us-west1` / `us-central1` / `us-east1`;
- **standard** persistent disk ≤30 GB (not balanced/SSD);
- only **one** e2-micro VM running in the account;
- one in-use external IPv4: the free tier reportedly covers one IP's hours per
  month; additional or idle reserved IPs are billed (~$0.005/h for an in-use IP
  otherwise). An ephemeral IP is released when the VM stops, so update DNS if the
  VM is ever stopped;
- egress ≈1 GB/month free (North America origin), then ≈$0.12/GB; ingest
  (inbound) is not the concern, public views are;
- no load balancer, Cloud NAT, snapshot schedules, extra static IPs;
- create a **billing budget alert at about $1** so any leak shows up early.

**Network/firewall:** VPC firewall allows tcp 80 and 443 only; SSH restricted to
your IP. UFW inside the VM mirrors it. Nothing at the venues is opened.

**TLS:** `autocert` in the binary (HTTP-01 on :80 + redirect, certs cached in
`data/certs/`). The service gets `AmbientCapabilities=CAP_NET_BIND_SERVICE`.
Rate limits live in Go.

**Resource guards:** systemd `MemoryMax=700M`, `Restart=on-failure`, a 1 GB swap
file as a safety net.

**Deploy (`make deploy`):** `GOOS=linux GOARCH=amd64 go build` → `scp` to
`/srv/guzva/bin/releases/<tag>` → switch the `current` symlink → `systemctl
restart` (<2 s gap, absorbed by device retry on the next tick). Last 3 releases
kept. `deploy/setup.sh` creates the user, directories, unit files and timers
(idempotent). No Docker.

**Backups:** a systemd timer runs nightly `sqlite3 .backup` (or `VACUUM INTO`) →
gzip with `data/ref/` → upload to a Cloud Storage bucket in a free US region
(≈5 GB free) using the VM's service account scoped to that one bucket (no key
files on disk); keep the last 14. RPO ≈ 24 h, which is fine because the DB holds
config only and frames are a disposable ring. One restore drill logged with a
timer **before the pilot**, then quarterly. Litestream is deferred (§9).

**Disk:** keep 10% reserve; <15% free → DEGRADED + `507`; <5% → read-only +
alert.

**Security:** SSH keys only, unattended-upgrades, admin login throttled, web root
limited to the two public files per venue.

**Disaster note:** devices keep capturing through a VM outage; the first tick
after restore succeeds. Restore order: new VM (same `setup.sh`) → DNS/IP → latest
nightly DB + refs → secrets → verify → fleet recovers by itself. The deploy is
portable: if the free tier changes, the same binary and scripts run on any
few-euro VPS.

**Docs mandatory:** `docs/runbook.md` (leads with power for support) and
`docs/ops-log.md`.

---

## 9. Deferred, with triggers

| Deferred | Trigger to build it |
|----------|---------------------|
| Owner area (login, hours/style editing, sessions table, IDOR test) | A venue asks to self-edit |
| Cloudflare (or other CDN) in front of **public** pages only | Billing shows egress >~60% of the monthly free allowance, or a traffic spike. Needs: ingest already on its own DNS-only hostname; verify cert issuance/renewal for the proxied name (HTTP-01 on :80, else an origin cert) |
| Litestream continuous backup | Owner area live, or losing up to 24 h of config edits would hurt |
| SD frame queue + backfill endpoint | Pilot data shows bridgeable outages and owners care about gaps |
| Long retention / hourly thinning / history UI | First customer asks for history (D2) |
| Thumb output | A page needs it |
| `/metrics`, `health_events`, `settings_log` tables | Debugging or audit needs that slog can't answer |
| Postgres behind an interface | p95 ingest >500 ms sustained or ~25+ venues |
| `ObjectStore` / S3, separate image volume | Disk >70% on the free 30 GB disk |
| Token rotation (24 h overlap) | ~25 venues or first token-leak scare |
| Claim-code provisioning | Installs by someone other than you |
| Staged OTA (1→3→all) | ~25 venues; until then manual canary (device #1 first) |
| Admin TOTP | Second admin or any wider admin exposure |
| `gateway` subcommand | A venue with a PC and a link that needs a LAN relay |
| Reverse proxy (Caddy) | A second site must share ports 80/443 on the box |
| Viewer/intent counters, "I'm coming" | Phase 3 feature work (adds its tables then) |
| libvips | p95 render >200 ms sustained |
| Three-tier paging / on-call | Second person on support |
| Paid VPS (€/mo) | Free-tier terms change, a budget alert fires, or the shared-vCPU box can't hold p95 |

Out of scope until Phase 3 at the earliest: streaming, motion/people-counting,
GPIO automation, multi-photo strips, OAuth, AVIF/WebP, K8s, managed DB.

---

## 10. Phases

**Phase 0 — POC (done).** Local-only, 1 device.

**Phase 1 — Thin slice on the real VM (1 device, real venue link).**
- VM: free-tier checklist (§8), `setup.sh`, `make deploy`, autocert, firewall,
  budget alert, nightly backup timer.
  *Status: firewall (tcp 80/443) open on `guzva` and reachability proven from
  multiple external nodes; testrig `setup.sh` done. autocert / `make deploy` /
  backup timer / budget alert: not started — today the rig is self-signed on a
  raw IP.*
- Protocol: one-page `proto-v1.md`, `POST /v1/ingest`, device token, ingest host
  in NVS.
  *Status: endpoint, bearer token and multipart contract proven end-to-end by
  the rig + firmware (incl. 401/409/422/429/507 handling in firmware);
  `proto-v1.md` written 09 Oct. Ingest host still compiled in, not NVS.*
- Server: `store` (3 tables) + ingest + `render` skeleton (poster/mono,
  validation gate) + `pages` (static public output) + `sched` tick + fakecam.
  *Status: not started (the Python rig is throwaway scaffolding).*
- Firmware: build steps 1–4 incl. the TLS spike.
  *Status: done — steps 1–6 + most of 7; TLS numbers recorded in the status
  block at the top of this plan.*
- Exit: 48 h from a real venue link with recorded numbers — fresh-minute %, TLS
  handshake ms (US-region RTT), heap trend, brownouts, post_fail rate, render ms
  on the e2-micro, still size; privacy disk-scan + web-root tests green; first
  deploy and first restore drill done via the scripts; after one week the GCP
  billing report shows no charges. **Architecture go/no-go decided from measured
  data.**

**Phase 2 — Pilot hardening (1–3 friendly venues).**
- Firmware: steps 5–7 (degraded camera mode, config read-back, LAN cage).
- Server: admin P1 (fleet board, tokens, venue create, reference sign-off),
  retention pruner, scheduler alerts + dead-man, privacy test suite, fakecam
  isolation gate.
- Ops: runbook written, install gate enforced (RSSI ≥ −65, ping <200 ms, supply
  spec, reference-frame sign-off recorded).
- Exit: 2 weeks/venue, <5% missed minutes, zero raw leaks (disk-scan green),
  runbook used once.

**Phase 3 — Operate (≤10 venues).**
- Firmware: pull OTA + rollback (first check whether the Arduino-ESP32 core's
  bootloader has app rollback enabled before writing a custom boot counter;
  manual canary: device #1 first).
- Server: owner area (D3) with sessions + IDOR test, claim flow, viewer/intent
  counters, "I'm coming", hours override, device read-back view.
- Exit: 95% minutes fresh, honest gaps on outages, owner dashboard in real use,
  MTTR <24 h without a developer visit.

**Phase 4 — Triggered work only (§9).**

---

## 11. Top risks

1. **Weak venue links (−75 dBm class) starve freshness** → install gate
   (non-negotiable), small frames, quality via config.
2. **TLS handshake cost and heap on the ESP32, over a US-region round trip** →
   Phase 1 spike first; keep-alive reuse; lower interval/quality if the numbers
   demand it.
3. **Power gremlins** (brownout/camera fails on bench) → supply spec, degraded
   modes, counters; the support runbook leads with power.
4. **Privacy incident** — raw frames cross the wire (D1) → TLS only, never behind
   a third-party proxy, no body logging, raw never written, web root allow-list,
   CI disk-scan, quarantine headers-only.
5. **Free-tier drift or a billing surprise** (new) → one VM per account, standard
   disk, free US region, egress watched, $1 budget alert, §9 "paid VPS" trigger,
   portable deploy scripts.
6. **Single free VM, no SLA** (new) → devices keep capturing; nightly backup +
   `setup.sh` rebuild; restore drill before the pilot.
7. **Silent backup rot** → restore drill with a timer, logged; quarterly after.
8. **Scope creep** → §9 table; sell stills + freshness first.
9. **Auth bypass** → pilot has no owner accounts (static public pages + single
   admin); owner area ships only with its IDOR test.
10. **Fleet-bricking OTA** (Phase 3) → rollback proven on a bench unit before any
    venue; manual canary; LAN-USB recovery documented.
11. **Solo-builder bandwidth** → one approval-gated step at a time; each phase
    step has an acceptance check before the next starts.

---

## 12. Change log

**v3.1 (09 Oct 2026 — status update, no design change)**
- Status block at the top replaces the POC-state note: bench rig live on the
  VPS, firmware steps 1–6 (+LAN cage) done, TLS spike numbers recorded,
  known gaps listed (ingest host in NVS, reference-frame hash, proto-v1.md,
  Go server).
- §5: connect budget corrected to 12 s (measured); build order checkmarked.
- §10 Phase 1: per-bullet done/not-started annotations.

**v3 → v3.1**
- Hosting: dedicated GCP Always Free e2-micro (D4) with a free-tier checklist,
  firewall, resource guards and a $1 budget alert.
- Caddy removed (punff gone): TLS via `autocert` in the binary; ingest on its own
  hostname, stored in NVS.
- Public side pre-rendered as static files (D5); history not routable; absolute
  "Updated HH:MM" in venue-local time.
- Owner area and sessions table deferred to Phase 3 (D3 unchanged, built later).
- Pilot schema cut to three tables; hours as JSON on `venues`; health history to
  slog; admin on a signed cookie, no CSRF tokens.
- Config via env/flags; no `/metrics`, no config/observ/worker packages.
- Litestream → nightly SQLite snapshot to a free Cloud Storage bucket.
- Cloudflare moved to a trigger, with the "never proxy ingest" rule recorded.
- Added: `make deploy`, `setup.sh`, systemd limits, Phase 1 billing and restore
  gates, render-time measurement on the shared vCPU.

**v2 → v3**
Removed/deferred: SD queue, `/v1/backfill`, retention thinning, per-venue worker
queues, `Repository`/`ObjectStore` interfaces, token rotation, claim codes,
staged OTA, TOTP, gateway, viewer/intent tables, thumb output, three-tier paging,
separate heartbeat/rotate endpoints. Merged heartbeat into `/v1/ingest` and
quality negotiation into `desired_cfg`. VPS from Phase 1; privacy wording fixed
for server-side stylising; retention 48 h.
