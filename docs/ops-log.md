# ops log — append-only. Newest at the bottom. `ACTION:` = open item.

## 2026-10-09 — bench cutover: Go serves :443 in front of the live board

- Imported the flashed board's token via `guzva provision -token` (no
  re-flash); re-applied the rig's cfg as `interval_s=45, quality=12,
  framesize=5, brightness=1, special_fx=2, led_pwm=80` (rig ver 7 → Go ver 2).
- `camtest` stopped + disabled; `guzva.service` enabled. Cert/key copied
  `/etc/camtest/tls.*` → `/etc/guzva/tls.*` (firmware CA still validates).
- Binary deployed by hand (`scp` + `mv` + `restart`) — superseded by
  `make deploy`; see runbook. First release under the new workflow must be
  logged here with its `git describe` tag.
- Deployed versions: `1a276c9` (device plane) → `1a276c9+aes256` (reverted
  cipher pin) → `1a276c9+readfix` (current; equals repo `b669a5b` modulo
  comment-only firmware edits).

## 2026-10-09 — TLS: Go session tickets stall the ESP32 (bench-measured)

- With tickets on, every handshake ran to the device's 12 s budget. With
  `SessionTicketsDisabled` + TLS 1.2 pinned, connect ≈ 2–3 s. `serve` keeps
  this profile; **autocert must not reintroduce tickets without re-testing**.
- Tried and reverted: AES-256-GCM pin (red herring), HTTP/1.1-only (same).
  Go's default `aes128-gcm` round-trips fine.

## 2026-10-09 — firmware read-path bug (valid 201s discarded)

- `tlsReadUntil` tested its needle at the buffer tail only; a 313 B response
  put `\r\n\r\n` mid-read → valid 201 held 25 s, discarded, retried as 409,
  frame dropped. Fixed: scan all candidate positions + carry post-needle
  bytes into the body read (`respCarry`). Cracked by an instrumented build
  printing `got=313 bytes 'HTTP/1.1 201 Created'`.
- Verified: repeated 201s, `post_fail` flat at 0, cfg ver=3 applied,
  `led_pwm=0` pushed live via `guzva cfg -set` (flash LED off, persists NVS).

## 2026-10-09 — first `make deploy` (devops workflow live)

- Shipped `a98e497` (tidy + workflow commit) through the Makefile: test →
  build → push `releases/a98e497/` → flip `current` → restart → healthcheck
  (healthz version match + page 200 + admin answering). No hand-scp.
- `provision.sh` proven idempotent on the live box beforehand; it closed real
  gaps (ufw + sqlite3 never installed, no swap, no backup timer — all now in
  place). One-time migration: seeded `releases/` from the running binary,
  unit now names only the `current` symlink.
- Known wart: first migration attempt pointed `current` at the binary instead
  of a release dir (`Not a directory`); fixed, Makefile pins the
  `releases/<tag>/guzva` convention.

## 2026-10-09 — parked items (do not lose)

- ACTION: 48 h soak + exit-gate numbers (fresh-minute %, connect ms, heap
  trend, brownouts, post_fail rate, render ms on the e2-micro, still size).
- ACTION: render-ms has no log line (`observeRender` feeds dropped metrics;
  fold into the ingest line, do not resurrect `/metrics`).
- ACTION: 2×422 in the journal with the reason field unexamined.
- ACTION: autocert + domain, `setup.sh`/`make deploy` (now: Makefile),
  backup timer, budget alert; ingest host still compiled in (§4 wants NVS);
  no reference-frame hash.
- ACTION: `guzva.ba` domain refs — revert only if `imaliguzve.ba` is the
  registered domain (open since the rename).
- ACTION: public page `<meta refresh=30>` vs `interval_s=45` — ~1 in 3
  reloads repeats the photo; wire refresh to the venue interval or leave it.
- Bench link: RSSI ≈ −71, 12 s connects, cold boot ≈ 70 s STALE. Install
  spec (RSSI ≥ −65) applies at venue time, not in code.
