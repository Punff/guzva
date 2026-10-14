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

- Q-01 (P1): mean-luma instrumentation on dark/dead rejects —
  spec restated by QA round 1.
- Q-05 (P1): zero-length frame part answers 201 like a health
  beat (ingest.go `len(frameRaw) == 0` intercept).
- Q-04 (P2): `Vary: Accept-Encoding` only on the gzip branch.
- Item D: cfg coherence validation (`agc=1 ∧ agc_gain=0` reject,
  fixed-exposure warn) at cfg-set.
- Item E: admin board shows last reject reason + mean + since.
- Bench-verify: dark escalation + TLS backoff need an induced
  failure to observe on hardware.
- WiFi password rotation (router) — needs the user.
