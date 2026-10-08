# Ops log — imaliguzve

Format: **dated entries, newest first.** One `## YYYY-MM-DD` heading per day
(local time of the box/venue), bullets underneath. Record here:

- deploys, restarts, incidents and their fixes (link the runbook step used),
- power/supply swaps at venues (runbook §1 says to log every one),
- restore drills — **mandatory before the pilot, then quarterly, with a
  stopwatch time** (PLAN §8, risk 5: silent backup rot),
- anything that changes PLAN.md decisions.

Short, factual, no speculation. Cross-reference PLAN.md sections instead of
copying detail.

---

## 2026-10-08

- **Bench TLS bring-up — first real camera → server 201 (LAN bench):**
  ufw rule for 8443/tcp added (user-run), and the camera's TCP connections
  reached the server, but every TLS handshake failed. Four fixes, in order
  found:
  1. **Epoch clock vs certificate validity** — the camera has no RTC and no
     NTP (PLAN §5), so its system time ≈ 1970; a normal cert (`notBefore` =
     issue date) fails the X.509 date check (mbedtls -9984, server logs
     `remote error: tls: bad certificate`). `tools/bench-cert.sh` now issues
     `not_before 19700101000000Z … not_after 20991231235959Z` so the cert is
     valid for any device clock in that window.
  2. **mbedtls 2.26 does not match iPAddress SANs** (`x509_crt_check_san()`
     string-matches dNSName only) while Go/OpenSSL verify an IP host against
     the iPAddress SAN only — connecting by IP therefore always hit
     CN_MISMATCH on the camera side. The bench cert now carries the IP
     **twice**, `IP:x.x.x.x` (curl/Go) and `DNS:x.x.x.x` (mbedtls); reason
     documented in the script header.
  3. **Server gave up before the camera** — Go's `ReadHeaderTimeout`
     (5 s) bounds the TLS handshake too, i.e. it hung up before the
     camera's 8 s ClientHello-retransmit budget could run out. Raised to
     15 s in `cmd/imaliguzve` (`main.go`).
  4. **One shot per 60 s tick** wasted frames on the marginal link —
     firmware now retries transport failures up to `POST_ATTEMPTS=3` in
     the same tick (1.5 s gap); the frame is still dropped when all three
     fail (PLAN: no frame queue).
- **Result:** real camera frames flow end-to-end — `C142-72`, `C142-73`
  stored `status=ok` (640×480, poster preset, rendered into
  `venues/1/stills/`), plus `C144-76`, `C144-77`, `C145-79` stored and
  quarantined `dark` (low light at the time — the quarantine/privacy path
  ran on real hardware frames). Camera-side response reads remain flaky
  (frame lands server-side, reply lost in flight); the retries absorb it.
- **Link quality is the limiting factor:** `***REMOVED***` rssi −74…−79,
  handshakes 0.9–5.7 s, occasional whole-tick misses (server logs
  `i/o timeout` / `EOF`). Reposition camera/AP before trusting the cadence.
  **VPS phase inherits the date problem** — Caddy/Let's Encrypt certs
  (`notBefore` = issue date) fail on a 1970 clock too: use a private
  backdated CA (the bench model) or give the device a time source; see
  `tools/bench-cert.sh` comments and `docs/bench.md` §5.
- **Serial-reader gotcha (bench tooling, not firmware):** opening
  `/dev/ttyUSB0` via pyserial glitches DTR/RTS and drops the board into the
  ROM download mode (silent); closing the port resets it back to run mode.
  Every "silent hang" and unexplained boot-count jump was this. Always
  drive the explicit DTR/RTS reset sequence before reading (as the bench
  scripts do), and expect the boot counter to tick up when a capture
  window closes.
- Repo reworked from the POC (Phase 0) to the v3 layout per PLAN.md:
  `cmd/imaliguzve` + `internal/` packages, `firmware/` as a PlatformIO
  project, `deploy/` and `docs/` added, POC web console superseded.
- Deploy scaffolding created (**Phase 1**, PLAN §10): `deploy/provision.sh`
  (idempotent Ubuntu 24.04 provisioning), `deploy/Caddyfile` (TLS only,
  `/metrics` kept private), `deploy/imaliguzve.service` (hardened systemd
  unit, loopback only), `deploy/litestream.yml` (DB → S3, nightly snapshots);
  ops docs `docs/runbook.md` (leads with power, PLAN §8) and this log.
- **PLAN §6.1 gate PASSED (bench, local):** fakecam with 19 fast + 1 slow
  clients, one token per device, 10 s ticks. Fast p95 **319 ms with the slow
  client** vs **345 ms without** (p50 108/102 ms, p99 428/454 ms) — the slow
  client does not affect fast latency; it was cut by the 30 s ReadTimeout on
  both attempts (`killed-by-server`). 95/95 fast posts answered 201, zero
  429s. Harness: `tools/fakecam` (fast/slow/flappy/dup/partial), devices via
  `imaliguzve provision -devices N`.
- **ESP32-CAM hardware bring-up (bench, first flash):** flashed with
  `pio run -t upload` from `firmware/` (a stray generated root
  `platformio.ini` from a failed init was removed — the project lives in
  `firmware/`). First boot **crash-looped** in lwIP:
  `assert(sys_mbox_valid) "Invalid mbox"` every ~1 s — `setup()` started the
  LAN cage (`WebServer.begin()` → `socket()`) **before**
  `WiFi.mode()/begin()`, i.e. before the lwIP tcpip thread existed; decoded
  with `xtensa-esp32-elf-addr2line` straight to `main.cpp:943`. Fix: start
  WiFi first (order documented in the `setup()` comment). After the fix:
  board joins `***REMOVED***` (a few `reason=201` no-AP attempts, then
  `got IP 192.168.1.8 rssi≈-78` — **weakish**, worth improving), camera OK
  (`[CAP]` 10–13 KB JPEGs), PSRAM present, mDNS
  `imaliguzve-bd2a58.local`, token_len 48. Added serial diagnostics: WiFi
  disconnect reason codes, one-shot AP visibility scan on first retry,
  `status=` on join retries, GOT_IP+RSSI. **Remaining blocker:**
  `INGEST_HOST` still the placeholder — ticks run, `TLS connect failed →
  frame dropped`, expected until a real HTTPS endpoint (VPS or bench TLS)
  is configured.
- **2026-10-08 ~23:15 — TLS budget fix after a total transport collapse
  (camera→server 0% for ~15 min):** from ~22:59 every handshake failed
  (server: `EOF` / `i/o timeout`; camera: `connect failed after 8.2 s /
  11.6 s`). The timings decoded exactly: lwIP RTO ≈ **3 s** on this link, so
  SYN retransmits land at 0/3/9 s and the ClientHello retransmit at 9 s —
  but the camera's budget was **8 s**, cutting the connection off *before*
  the retransmit that had a chance to succeed (3.6 s TCP + 8 s HS = the
  11.6 s attempts). Fix in `firmware/src/main.cpp`: `TLS_HANDSHAKE_S`
  8→**12** (covers the 9 s CH retransmit; server `ReadHeaderTimeout`=15 s
  still outlasts it), `TLS_CONNECT_MS` 8000→**12000** (TCP phase covers
  SYN at 0/3/9 s), `POST_RETRY_GAP_MS` 1.5→**6 s** (decorrelate
  interference bursts). **PLAN §5 "8 s connect" deviated** — justified by
  bench data; the handshake now completes (5/6 attempts reached TLS) and
  frames land again: `C148-96` 23:19:41, `C148-98` 23:24:23 (2/3 ticks vs
  0/9 before), rssi −74. Post-flash the join itself flaked once
  (`reason=8/201`, `join_fail` 1–2, backoff to 40 s) — same weak link.
  **Open observations:** (a) a POST can consume the whole 25 s
  `response FAILED` deadline even though the server *stored* the frame
  (server also logged `load venue: context canceled` on one attempt) —
  either the downlink response is lost across the camera's 3/9/27 s
  retransmit schedule (its deadline hits first) or a server-side stall in
  `load venue`; capture_id dedup makes the retry safe either way; (b) heap
  loses ~1–2 KB per *failed* attempt (188.9k→180.7k over 6 fails) — watch
  for exhaustion during a sustained outage, reset recovers. Both frames
  quarantined **`dark`** — the site picture still needs a lit scene.
- **TODO (mandatory, pre-pilot): first restore-drill with a timer.** Restore
  `imaliguzve.db` from Litestream on a scratch box, time the whole drill
  (runbook §6), and log the duration here **before the pilot starts**; then
  repeat quarterly (PLAN §8 / risk 5). Status: not yet run — no backup
  replication is live until `deploy/litestream.yml` placeholders
  (bucket/region/endpoint + `LITESTREAM_*` creds) are filled in.
