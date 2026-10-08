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
- **TODO (mandatory, pre-pilot): first restore-drill with a timer.** Restore
  `imaliguzve.db` from Litestream on a scratch box, time the whole drill
  (runbook §6), and log the duration here **before the pilot starts**; then
  repeat quarterly (PLAN §8 / risk 5). Status: not yet run — no backup
  replication is live until `deploy/litestream.yml` placeholders
  (bucket/region/endpoint + `LITESTREAM_*` creds) are filled in.
