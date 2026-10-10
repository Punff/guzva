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

## 2026-10-09 — admin split deployed (`d480564`)

- Super-admin: venue edit page, owner-password set, device page with the full
  21-key config form. Owner (`/owner/`): venue-details edit + read-only
  device health, one password per venue. Store migration v3 (`owner_hash`)
  applied itself on the live DB at startup.
- Verified live: owner login/dash/edit on bench (renamed Bench→Bench
  Test→Bench), owner→admin wall bounces, bad login 401 with the generic
  message. Live bench owner password: `bench-owner-01` — rotate before
  handing to the real vendor owner (venue page in /admin/).
- ACTION: the generic login message means support cannot distinguish "wrong
  venue" from "no owner set" either — check the venue page's owner status
  instead of asking the owner to retry blindly.

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

## 2026-10-09 — UI/UX pass + outage fix (`544c049`)

- OUTAGE, caught before it bit: the v3 migration chain had no `case 3:`, so
  the first boot after the admin split migrated 2→3 and every later boot
  failed to open the store. The VPS was one restart from a dead site.
  Reproduced, fixed, pinned by `TestReopenCurrentDatabase`; verified live
  with two consecutive restarts (active both times).
- Public page redesigned: in-place still updates (no more whole-document
  refresh flash), design tokens + real dark mode, reserved frame box (no
  layout shift), freshness as the headline with a day label + timezone, a11y
  (alt text, aria-live, reduced-motion), and page metadata.
- ACTION: verify the page by eye in a browser (the harness had no attached
  browser this session) — particularly the dark mode and the in-place swap.

## 2026-10-10 — overnight: design system (D6), console UX, soak data

Baseline before this block: `e7db280` (PLAN v3.2 + docs/ux.md).

### Ingest journal: the two soak gaps (`0f7feee`)

- `render_ms` on the per-ingest line — the Phase 1 exit gate wants "render
  ms on the e2-micro" and the metric it used to read was dropped in v3.1.
  A sub-millisecond render reads 0.1, never absent. Health-only beats carry
  no field (nothing rendered).
- `reject=` on 422s. Chasing the standing "2×422, reason unexamined" action:
  they were `bad_meta`, `capture_id=""`, zero counters, at 16:07/16:25/
  18:44/18:46 — all inside the buggy `tlsReadUntil` window, before the
  firmware fix. **Historical, not a live fault.** The reason lived only in
  quarantine.jsonl; the journal now answers it on its own.

### Design system + consoles (`98133e9`, `1a27c08`, `624dfeb`)

- Tailwind CLI v4.3.3 standalone, vendored as an on-demand download (111 MB
  would bloat git; `tools/bin/` is gitignored, one pinned version for
  everyone). `web/input.css` → `internal/webui/app.css` via `make css`;
  `make css-check` fails the build when the compiled sheet drifts from the
  tokens or templates, so a hand-edited app.css cannot ship.
- Consoles serve it embedded at `/admin/app.css` (ETag); the public page
  *inlines the same bytes*, because D5 allows exactly two files in the web
  root. Verified unescaped (16 KB inlined, no `ZgotmplZ`), and a test now
  fails if the public page ever links the console sheet instead.
- fleet: venue cards with badge/age/edit, device table with hover, RSSI
  below the −65 install gate called out by name, revoke behind a confirm.
- device: the 21-row config table is now two labelled groups (Cadence /
  Sensor) with ranges, and status/FW/RSSI/read-back as a 4-tile summary.
- Owner panel is Croatian (PLAN §13): dash, login, and the error strings.
- Bug fixed en route: `/<slug>` without the trailing slash resolved the
  relative `latest.jpg` to `/latest.jpg` → broken image on the one page a
  guest sees. Now 301s to the canonical form, query string preserved.
- Own slip, caught by `make test`: a duplicate `test:` target made make
  silently run the *first* recipe with a warning. Removed.

### Open

- ACTION: eyeball the live public page and both consoles in a real browser
  — no browser was attached to this session. Dark mode, in-place swap, and
  the grouped config form all need human eyes once.
- ACTION: fleet "needs attention" ordering (stale-first) is designed but
  not implemented — §11.
- ACTION: venue description/contact on the public page needs a store
  migration (v4). Every migration must add its `case N+1` *and* keep
  `TestReopenCurrentDatabase` green — that gap killed the site once.

### Live verification after `624dfeb`

Checked against the running box, not the local build:

- `GET /bench/` → 200, 21 KB, inlines the design system (`--color-ink`
  present twice = light + dark block), **no** `/admin/app.css` link, so D5
  still holds; `id="still"` + `data-ver` present, title renders.
- `GET /admin/app.css` → 200, 16 KB, `text/css` (console path).
- `GET /bench?p=1` → 301 → `/bench/?p=1`, query preserved.
- `GET /owner/login` → 200, Croatian (`lang="hr"`, `Prijava`, `Lozinka`,
  `Identifikator objekta`).
- `GET /owner/` unauthed → 303 → `/owner/login` (no dash leak).
- `GET /admin/` unauthed → 303 → `/admin/login` (unchanged).

Note: `/owner/<slug>` is not a route — the owner login is one page at
`/owner/login` with the slug as a form field. A 404 there is correct.
