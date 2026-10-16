# Guzva — solo agent protocol

One repo, one binary, one SQLite file. **One agent works the whole
tree directly** — no worker sessions, no dispatches, no handoffs.
(From 2026-10-14 the 6-role OpenChamber roster is retired: worker
runs repeatedly stalled mid-task or claimed work that wasn't in the
tree; the solo loop below shipped v0.6.3 in one pass and is the
proven setup. History of the team mode lives in docs/ops-log.md.)

## The loop (every change, no exceptions)

1. **Read the code first.** The contract you're changing is in the
   source, PLAN.md §1, and the doc that owns the surface
   (docs/proto-v1.md for the wire, docs/design.md + docs/ux.md for
   pages, docs/cli.md for the CLI).
2. **One item at a time.** Small commit per item, explicit paths
   (`git add <paths>`, never `-A`), `make test` green BEFORE each
   commit. If tests fail on your own change, fix before committing.
3. **Prove it, don't claim it.** Behavior changes get a test or a
   live curl/serial proof pasted into the handoff... there is no
   handoff — into the commit message or ops-log. Grep-verify markers
   exist before writing "done". Never describe work that isn't in
   the tree.
4. **Push after each green commit** (`git push origin master`), let
   CI gate it (repo: Punff/guzva). Fix CI before starting the next
   item.
5. **Deploy when a coherent set has landed:** `make tag V=vX.Y.Z` →
   `make deploy` → verify with the `make check` output + a live
   probe of the changed behavior → append the deploy entry to
   docs/ops-log.md and push.
6. **Console-visible change → screenshot it** (`.openchamber/screenshots/`)
   and keep the rendered-template tests in sync.

## Hard rules (carried over unchanged)

- PLAN.md §1 contracts hold for every change. Privacy: raw JPEG in
  RAM one call only, stylised PNG at rest, no biometrics ever (D9),
  push-only server.
- No new Go deps (stdlib + 3 direct). Interface only with a 2nd
  implementation.
- `make test` is the only gate. Green means all packages, including
  `qa/` — a failing test is a finding (fix it or file it as
  numbered Q-xx in docs/qa-findings.md), never a thing to skip.
- docs/ops-log.md is append-only and tells the truth, including
  failures and reverted work.
- Firmware touches the real board only via `pio run -t upload`;
  serial is DANGEROUS raw (`stty`/`cat` on /dev/ttyUSB0 drives the
  CH340 DTR/RTS into download mode) — use a pyserial RTS-pulse
  capture script; regenerate it if missing (the original
  `/tmp/opencode/esp32-bootlog.py` was lost to tmp cleanup).
- Prod: `ssh root@guzva`, data `/var/lib/guzva`, bench venue
  `https://34.139.58.143/bench/`, board serial `bd2a58b`.
  Never POST frames to prod from tests; never open the device
  token outside the VPS.
- `firmware/include/settings.h` holds real WiFi credentials —
  gitignored, never committed.

## Open queue (docs/qa-findings.md is the tracker)

Prod is **v0.9.1** (2026-10-15). In the tree, not deployed: `/privatnost/`
page + footer link, no-JS clock hide, `make soak`; firmware Q-10 install
window (built, NOT flashed). Next deploy = v0.9.2 with those.

**Pilot blockers (PLAN §15 Phase 2 exit):**
- **Domain + autocert (Q-08)** — needs the user: is `guzva.ba` registered?
  The QR poster on a bare IP + self-signed cert is unusable for guests.
- **Ingest host in NVS** (§14) — pair with the domain change.
- **Q-10 flash + proof** — `/jpg` 200 after boot, 403 after 15 min.
- **Venue site survey** — RSSI ≥ −65 at the mount, camera on a network
  guests can't join (Q-10 belt-and-braces); camera-notice sign; legal
  read of `/privatnost/` + a controller contact (the page has none yet).
- **Phase 1 exit numbers** — `make soak` (prod journal read; the agent's
  permission guard blocks prod reads, so the user runs it) → ops-log.
- Dead-man alert channel — needs the user to pick one (email/ntfy/…).

**Then:**
- **S0/S1 real corpus:** v4 constants were picked on the synthetic hard
  scene. Capture 30–40 local raw frames (never committed, `corpus/`), run
  `go run ./tools/styleproof -sweep -out /tmp/sheet corpus/*.jpg`, re-freeze
  as a preset bump. The human legibility/recognition tests are the gate.
- Q-08 (P2): prod serves the bench self-signed cert (autocert + domain).
- Item E remainder: per-frame mean luma on rejects (journal-only today).
- Bench-verify: dark backoff (now 300 s after 4 dark rejects) + TLS backoff
  need an induced failure to observe on hardware.
- WiFi password rotation (router) — needs the user.
