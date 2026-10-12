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

## 2026-10-10 — browser verification + stale-image fix

No browser is attached to this session (the Code Mode `browser` namespace and
OpenChamber's `browser.*` actions are both absent, and no MCP server exposes
one). Worked around rather than waited on: Chrome 153 from
`~/.cache/puppeteer` driven over the DevTools protocol with
`/tmp/opencode/cdp.mjs` — dark-mode emulation, authenticated screenshots,
console errors, failed requests. Reusable; no install.

### What looking at it found (`593b0a1`, deployed + verified)

- Config range hints printed ONE bound ("30" under interval_s, "4" under
  quality, which read as a fourth option). Now `min…max` from one place.
- A board that had never checked in rendered "0 dBm" — a perfect reading for
  a device that had never spoken. Now "—", and gate styling keys off a Weak
  flag so an absent reading cannot be graded.
- Every console load 404'd on /favicon.ico.

### Stale images (`620a4ea`, deployed + verified)

Reported as "stale images, struggles picking it up again after a new deploy".
Reproduced by watching a real browser for four minutes instead of curl:

- The bytes WERE current, but `data-loading` stayed "1" on nearly every
  sample — including samples with `complete=true, naturalWidth=640`. The
  photo was up to date and *veiled*, which reads as stuck.
  - the load listener was attached AFTER insertion, so a cache-hit image
    (event already fired) left the veil on;
  - the server renders `data-loading="1"` to avoid a blank-box flash, and
    NOTHING settled it on arrival, so a first-time visitor kept the shimmer
    until an unrelated later swap fired a load.
  Fixed with a `settle()` helper (complete / load / error).
- One cache policy for two file kinds, wrong for both:
  - index.html `public,max-age=30` — it is rewritten on every state change
    and inlines the CSS and JS, so that window is "the deploy is invisible".
    Now `no-cache` (revalidates; the common case is a cheap 304).
  - latest.jpg is addressed by a unique ?v=, so its bytes can never change
    under that URL. Now `immutable` for a year.
- The cache-buster was received_at in SECONDS. Under a year-long immutable
  cache, two frames in the same second would pin a stale photo permanently.
  It is now the frame row id — unique, monotonic, collision impossible.

Re-verified cold: first sample is `loading=null` (was `1`), headers are
`no-cache` / `immutable`. The swap path could not be re-verified live
because the board went silent at 09:18:37 (RSSI −71…−78, the known marginal
bench link) — the page correctly held the last frame and flipped to STALE,
which is the system being honest rather than broken.

- ACTION: owner password for `bench` is set in the DB but `bench-owner-01`
  no longer authenticates — rotated at some point. Needs the real value, or
  permission to reset it. Owner DASHBOARD is therefore still unverified.
- ACTION: bench board post_fail/tls_fail climb steadily (p44 t42 since boot
  211). Consistent with the weak link; a real install must clear the
  RSSI ≥ −65 gate.
- ACTION: mobile public page has a large gap between the timestamp and the
  footer (footer pinned to viewport bottom). Design call, not a bug —
  confirm it is wanted.

### Stale-image investigation, continued (measured, not guessed)

The page reloaded itself every 30s (3 full navigations in 95s, navType
"reload") even with the in-place swap running. Two dead ends, both mine,
recorded because the measurements are the useful part:

- d1fc1ad rewrote the meta refresh to content="300". The attribute DID change
  on the wire; the reloads continued at the identical cadence.
- b653a2e removed the element from the DOM. Same identical 3 reloads.
- Control: the 404 root (no meta refresh) = 0 navigations in 95s. That is
  what proved the meta refresh was the cause and the two fixes above were
  ineffective.
- Rig control: a plain meta refresh=5 with JS disabled = 4 reloads in 22s,
  so the harness does detect refreshes.

Chrome arms the refresh timer when the element is PARSED, so neither
rewriting nor removing it afterwards disarms the timer. 9cfde4f moves it
inside <noscript>, which is parsed only when scripting is off. Verified with
JS on: 0 navigations in 95s, down from 3.

KNOWN GAP: the no-JS arm of that fallback is UNVERIFIED, not proven. A probe
showed <noscript> content is not parsed as elements even under Chrome's
--disable-javascript, so this rig cannot exercise a genuinely JS-less
browser either way. Spec says it should work; do not record it as proven.

### latest.jpg is mutable — so it must not be immutable

A sync monitor (db frame id == page ?v= == sha(latest.jpg) == sha(newest
render) == sha(served bytes)) ran across a real version-changing restart:
zero desync samples.

But serving latest.jpg as immutable for a year was unsafe: the file is
OVERWRITTEN each frame, so a client holding a stale ?v= (old index.html, a
backgrounded tab) would fetch CURRENT bytes under the OLD version's URL and
pin the mismatch for the whole immutable lifetime. Both files revalidate
now; correctness is worth one 16 KB re-fetch.

## 2026-10-10 — DESIGN.md implemented (Tisak)

The design system arrived as a proposal with ten ⚑ deltas gated on approval.
The user granted blanket approval, so all ten were in scope. Status:
[design-todo.md](design-todo.md). Deployed through `f56e001`.

Shipped: tokens, the split Tailwind pipeline, all three surfaces in the print
language, Croatian copy from one catalog, two stale tiers, closed ⇒ no still,
noindex, hash-based CSP, security headers, gzip.

First view on the wire: **5,500 bytes gzipped** (17,576 raw) for the whole
page, verified round-trip. Headers and round-trip checked with curl.

NOT shipped, deliberately: ⚑1/⚑2 (the 160×120 paletted PNG and the burned-in
stamp). That is the centrepiece and the largest remaining piece — an 8-stage
pipeline, palette encoding, a digit font, goldens, and latest.jpg →
latest.png across ingest/pages/tests. Half of it would replace a working still
with a broken one, so it wants its own focused pass with real bench frames to
judge it against. The still is still a 640px JPEG at ~16 KB, about 3× the
page itself: it is currently the heaviest thing we serve.

### Bugs found while implementing (all mine, all now fixed and tested)

1. `case 2` of the migration chain never fell through to `case 3`. Harmless
   only because case 3 was inert; adding ⚑9 made it real and a fresh DB would
   have silently stopped one version short while reporting success — the same
   shape as the outage that bricked the site on restart.
2. `schemaV1` is frozen history. Adding a column to it double-adds on replay.
3. `chip.html` used `{{.cls}}` against an exported field `Cls`; templates are
   case-sensitive, so every page carrying a chip truncated mid-render.
4. `device.html` referenced `.Weak` on a struct without that field — same
   silent truncation.
5. `StateShape` was a plain string, so the status chip printed raw SVG source
   as visible text.
6. Frame overlays used `absolute inset-0` with no positioned ancestor, so on a
   closed venue the shutter covered the entire page.
7. The CSS budget measured minified bytes. Wrong metric: what a guest on
   Bosnian mobile data pays for is the gzipped bytes, which is also what the
   design's §13 states.

The last two were found only by rendering the page and looking at it. Tests
caught the rest, but none of them would have caught the CSS one.

## 2026-10-10 — console rendered unstyled (Firefox), cause: a fixed CSS URL

Reported as "admin panel is all over the place", with a screenshot showing no
styling at all: no card borders, unstyled inputs, no table headers, and BOTH
the desktop table and the mobile cards visible at once.

Diagnosis. `hidden` not applying was the decisive clue. `display:none` is
basic CSS, so the whole stylesheet was ABSENT rather than partly parsed —
which ruled out the other candidate (Firefox <128 lacking `@property`,
`color-mix(in oklab)`) because those would only cost the hatch gradients.
Mobile Brave was fine because it had fetched the sheet fresh; the desktop
Firefox had cached it when it was still served `Cache-Control:
public, max-age=86400`, from before the templates moved to .card/.btn/.inp.
No error, no log line, no failing test: every new component simply did
nothing.

Fix. DESIGN.md §4 specified `app.[hash].css` for exactly this and the hash was
never implemented. The URL is now sha256(app.css)[:8], registered literally at
build time and served immutable — the URL changes when the bytes do, so a
stale sheet can no longer outlive its build. The un-hashed /admin/app.css
still answers for old bookmarks.

Also removed the exotic colour syntax from the tokens: the hatch textures
were the only thing of mine needing color-mix(in oklab) (Firefox 113 / Safari
16.2) and are now literal rgba. That matters beyond this bug — the owner
opens this panel on whatever phone is behind the bar.

Lesson worth keeping: a stylesheet served at a FIXED url under a long
max-age is a silent dependency between two deploys. Nothing fails loudly; the
UI just quietly stops existing.

## 2026-10-10 — Tisak pipeline live (⚑1/⚑2), kit gallery (B1)

Deployed through `7e361d2`. Real bench frames now store as poster@2:
160×120 paletted PNGs, 2-bit colormap, ~160 bytes on a dark bench (vs ~16 KB
JPEG before). The stamp burns venue-local HH:MM into the picture; the page
headline agrees with it by construction (same instant, one clock read).

- public/bench holds exactly index.html + latest.png again; the old
  latest.jpg was removed by the publish path itself.
- The closed state renders the shutter with no still (verified live: bench
  hours 08:00–20:00, page showed "Zatvoreno / Otvara se sutra u 08:00").
- Firmware boot defaults fixed in source (framesize 4 was 240×240 square, not
  VGA — its frames would dims-reject until desired_cfg converges; now QVGA +
  60 s). pio run SUCCESS. Takes effect on next physical reflash only.
- /admin/_kit gallery behind GUZVA_KIT=1 (404 in production, verified).
  Rendered locally: all four chip states, cards, buttons, error input,
  table, textures, flash — all correct.

Pipeline numbers on procedural fixtures: tisak-clean 323 B, busy 306 B,
sablon 215 B (6 KB bound). Live bench frames: ~160 B (dark room compresses
to nearly nothing — the size bound matters for bright rooms, not this one).

## 2026-10-11 — hours editor live (DESIGN.md §9)

Deployed `4200955`. All three venue forms (admin create, admin edit, owner)
use one shared editor partial: 7 day-rows, native time pickers, two intervals
per day, closed checkboxes, server-side copy-Monday. The spec-string field is
gone; ParseSpec stays for compat.

Verified live: 7 rows on the venue page prefilled 08:00 from bench hours,
copy button present on the fleet create form, DB at user_version 5 with hours
intact across the v4→v5 migration.

Errors are structured (day + code) and sentenced per language: Croatian in the
owner panel ("Ponedjeljak: …"), English in the console. All-closed rows =
empty Hours = no badge, same as the old empty spec.

## 2026-10-12 — maintenance pass W0–W5 (docs/maintenance.md)

Trigger: a four-way audit (firmware, server, UI/docs, DevOps) found the
architecture sound but the hygiene, a set of live bugs, the missing CI
substrate and doc drift worth a planned pass. Plan: docs/maintenance.md;
every workstream below landed as its own commit series with its tests.

- **W0 hygiene:** `settings.h` untracked (it carried the WiFi password +
  device token against its own header warning; `settings.example.h` is the
  committed template, real file gitignored), the 4.2 MB `styleproof` ELF
  untracked, the retired Python rig and the vestigial tailwind config
  deleted, bench-cert.sh's dead pointer fixed.
  `ACTION:` the secrets in git history are burned — history scrub is
  MANDATORY before the first push to any remote (maintenance §4), and the
  bench WiFi password + device token rotate at the next physical session.
- **W1 CLI:** real dispatcher — `guzva <typo>` used to silently start the
  server; now help/version/exit 2. `provision` validates -slug + -tz like
  the admin form, and `store.CreateVenue` enforces the slug rule for every
  writer (an operator typo could previously publish pages outside the web
  root). docs/cli.md shipped.
- **W2 surfaces:** the state-chip colour classes never compiled (assembled
  `text-{{.Cls}}` — invisible to the Tailwind scanner), so every chip on
  every surface rendered its shape in ink, the kit gallery included; they
  are now explicit CSS in tokens.css and pinned by webui tests.
  `owner.fresh/late/stale` did not exist and rendered as raw keys on the
  owner dash (never caught because the bench owner password was lost) —
  catalog test now scans every fetch in Go + templates. Consoles got the C1
  header set (CSP with no unsafe-inline — the inline styles went to
  classes), no `/admin/static/` listing, and no-JS `method/action` on every
  hx-post form. One favicon pixel was `#3607F`.
- **W3 correctness:** parse-stage 422s now advance the beat when the meta
  unmarshalled (bad_capture_id) and key a quarantined row so retries answer
  409 — they previously froze last_seen and re-processed forever. Dead code
  out (auth ctx helpers, sameDayMinusOne, PresetNames, Prometheus + its
  write-only histogram, test_pattern case). render speaks proto's reason
  constants. sched got a pure advance() + tests. Comments and design.md now
  match the shipped things (preset @2, double masks, styleproof, budgets).
- **W4 firmware (needs a bench session to verify on hardware):** task WDT
  deinit→150 s→subscribe loopTask — nothing recovered an unexpected stall
  before; PSRAM alloc failure falls back to DRAM instead of faking camera
  death; the 422 reason call passes the quote jsonStr expects (server
  reasons always printed `?`); platform + driver pinned, `-w` dropped
  (warning-clean today); FW_VERSION stamps `git describe` via a generated
  header (CPPDEFINES ate the quotes — first attempt failed the build);
  5xx honors Retry-After. The spec's "hourly-max reboot" is dropped in
  favour of the watchdog; decision recorded in maintenance.md.
- **W5 devops:** CI workflow landed ready (no remote exists yet — it runs
  from the first push, which must follow the W0 scrub). The restore drill
  is a script now, proven both ways locally (clean snapshot PASSes,
  truncated one fails); `make drill` runs it on the VPS. `make tag` starts
  the release flow — zero tags existed, every release dir was a bare SHA.

`ACTION:` run `make drill` on the VPS and log the pass here (first drill
ever). `ACTION:` bench session for W4: reflash, confirm the WDT banner
(`wdt=150s`), the real FW_VERSION in meta, and rotate the burned secrets.
Not done this pass, sequenced in the plan: W7 (fleet attention sort,
paused state, venue description/contact, QR poster) and W8 (filter tuning
from real bright-room frames).

## 2026-10-12 — v0.3.0 deployed; token rotated + burned secret dead; first restore drill PASS; W4 verified on hardware

Release flow used for the first time: `make tag V=v0.3.0` (the repo's first
tag ever — every release dir before this was a bare SHA) → `make deploy` →
healthcheck OK (version v0.3.0, page + admin answer). The restart gap was
absorbed by device retry as designed; no missed 201s in the journal.

**Token rotation (the W0 ACTION, done at the bench session):** new device
row `bd2a58b` minted for venue bench, token imported, board re-flashed with
it in settings.h (v0.3.0). First POST 201 at 07:06:19 with `fw=v0.3.0` —
the version stamp working. Old row `bd2a58` revoked; a replay of the
git-history token answers **401** (caught live in the journal as
"auth: token revoked" at 07:08:34). The burned device token is dead. NVS
survived the reflash: boot=217, brownout=4 — history intact.
Bench cadence restored on the new row (`cfg -set interval_s=45,quality=12`,
ver 2 on next POST) so the runbook stays true.

**W4 on hardware:** boot banner shows `=== guzva firmware v0.3.0 ===` and
`wdt=150s` (the task-watchdog backstop is armed; a stall now reboots into an
honest STALE instead of hanging). TLS connect 1.9 s, render ~8–10 ms on the
server, 201s steady at RSSI −70…−73 (the usual marginal link). Sensor
values from NVS persisted across the flash (the rig-era fs=5/br=1/fx=2).

**First restore drill (the pre-pilot mandate): PASS.** `make drill` against
tonight's snapshot: gunzip → integrity_check → schema parity → the shipped
binary opens the restored store. First run FAILED for a real reason —
live devices=3 vs snapshot=2, because the rotation happened after the 03:17
snapshot — and that taught the drill its own lesson: count parity with a
live DB must be a warning (backup lag is RPO working as documented), not a
failure. Impossible states (restored > live, restored empty) still fail.
Script fixed, re-run: PASS with the lag warning.

`ACTION:` the WiFi password (***REMOVED***) remains in git history and live —
rotation needs the router, which this session cannot reach. The scrub of
settings.h + the styleproof blob from history is still mandatory before
the first push to any remote (no remote exists yet).
`ACTION:` `pio device monitor` is broken by a duplicate PIO Core
(6.1.19 vs 6.2.0) — upload works, monitor crashes; clean up the cores.
Serial reads via pyserial worked for verification in the meantime.

## 2026-10-12 — design pass 2 live (v0.3.1): public page, hours editor, print & privacy setup, device page

Deployed `v0.3.1`. The migration v6 (venue description + phone) ran on the
live box at boot: user_version 6, reopen clean. The bench page now composes
as the print + its gallery label (state chip + clock in one bar, caption
below), which is the whole point of the pass: the facts about the picture
read as one object instead of four scattered elements.

- Public page: label bar live (`chip-fresh` + `#hl` in a figcaption), the
  old floating 56 px clock retired. Description/phone empty on bench — the
  section renders nothing, exactly the D5-honest behavior.
- Hours editor: every time input labeled, shifts as titled fieldsets
  (1./2. smjena, neobavezno), same parser contract.
- Print & privacy setup: preview pane + calib.js (3.7 KB, same-origin,
  CSP-clean) — masks draw over the current print live; tone ramp shows the
  threshold mapping. Server validation untouched.
- Device page: Health / Capture & upload / Diagnostics with per-group
  explainers; fleet sorts needs-attention first.
- CSS: console form/table components moved from tokens.css to the app
  entry — the public sheet was at 98% of its gz budget carrying .card/.inp/
  .tbl it never used; now 92% with the new composition in.

Verified live: figcaption + chip-fresh + clock on /bench/, user_version 6,
calib.js 200. `ACTION:` eyeball the three surfaces in a real browser (the
rendered-template tests cover content, not taste) — chip contrast in the
label bar at 360 px is the one thing to squint at.

## 2026-10-12 — orchestrator watch: frontend-tisak handoff, gate RED

- Watched `guzva-frontend-tisak` (`ses_ecfef6c4…`) to idle. Its report:
  overhaul items 1–5 already shipped in v0.3.1 (verified live vs sandbox,
  not re-implemented); screenshots
  `.openchamber/screenshots/tisak-{pub-desktop,pub-m360,fleet,venue,device,kit,admin-login}.png`;
  kit renders, zero JS/console/network errors.
- One real defect found, reverted with recipe (same W2/B1 shape): `calib.js`
  sets runtime classes (`absolute/inset-0/h-full/pointer-events-none`) absent
  from `app.css`, so the mask overlay renders as a static block below the
  picture and stretches the preview box. Fix: explicit
  `#calib-preview svg{position:absolute;inset:0;…}` in `web/app.css`, drop the
  runtime class, pin with `TestCalibOverlayNeedsNoRuntimeClasses`, `make css`,
  re-screenshot. `ACTION:` re-dispatch frontend-tisak for this one commit.
- `BLOCKED` claim triaged: NO second frontend session exists in session.list
  (single `guzva-frontend-tisak`); the 2 dirty files (`venue.html` info-card,
  `web/tokens.css` dark-muted + `.chip--lg`) match the ses_ecff9370 left-off
  mods, not a concurrent writer. Verdict: unattributed dirty tree, not a
  single-writer violation — needs owner confirm, downgraded from BLOCKED.
- Gate RED: `make test` fails at `css-check` (tokens changed, compiled CSS
  stale). Frontend-tisak must run `make css` + commit its 2 files before any
  merge. No commits made by anyone this watch; `HEAD` still `25bce48`.

## 2026-10-12 — orchestrator: all 5 tasked, first handoffs in

- `764986b` frontend round 1 landed (chip-led label bar, info card,
  dark-muted bump). Dirty tree is now round 2 + one unattributed test edit.
- **backend-go STOPPED clean, no edits** — `pages_test.go` diff is another
  writer's logic change (drops `Radno vrijeme` ✓, adds `Otvoreno do` ✓,
  adds failing `tel:` — fixture never sets Phone). Ruled: (a) backend takes
  ownership, fixes fixture via existing `UpdateVenue` path, gofmt, commits;
  (b) `tel:` assertion stands; (c) `pages.go:179-186` comment gets the true
  justification (JS poller + meta-refresh re-render, not the deleted stamp).
  No state-key change. Stamp verification itself HOLDS (zero StampLoc/
  Stamped refs, @3 goldens + 409 + no-failure + state-key tests pass).
- **devops-qa:** demo GREEN (fakecam window had lapsed, relaunched; `:8080`,
  page, admin guard all healthy). Release checklist ready: proposes `v0.4.0`
  (render-contract bump), fire sequence `test → tag → backup → deploy`,
  rollback anchor `v0.3.1` verified, SSH/disk probed. Correction to my
  earlier note: scrub gates only the first *push*, not tag/deploy.
- **firmware-esp32:** confirm CLEAN — 22-key surface matches proto doc,
  `TestCfgKeysMatchDoc` holds, no edits.
- **frontend-tisak:** re-applying overlay fix (busy). **docs-i18n:**
  restarted, baseline survey running.
- Gate still RED (gofmt + 1 failing test) → tag/deploy wait on backend's
  commit + frontend round 2.

## 2026-10-12 — orchestrator: backend `c877c65` green, frontend round 2 in flight

- **backend-go `c877c65`** (2 files, staged by path): fixture owns Phone via
  `UpdateVenue`, `tel:` asserted on unescaped HTML. Two corrections to my
  ruling, both evidenced: `html/template` escapes `+`→`&#43;`, so the
  unattributed literal could never match (was never run); and my (c)
  sentence was false — `static.go:101` serves index.html with no-cache +
  modtime and rewrites only on state-key change, so a no-JS visitor gets
  304 with a frozen clock. Comment records this as a Known limit of
  `25bce48`. `ACTION:` the no-JS clock gap is a genuine §6.4 honesty
  question from the stamp removal (per-frame rewrite vs accept) —
  frontend's call after round 2, sequenced by orchestrator. Stamp
  verification HOLDS (zero StampLoc/Stamped refs, @3 goldens, 409,
  no-failure, state-key tests pass).
- **frontend-tisak** (busy): overlay fix re-applied on round 1, now chasing
  a harness issue (login redirect with valid profile cookie). Dirty:
  `calib.js`, `admin/templates/venue.html`, `webui/app.css`, `web/app.css`,
  untracked `calib_test.go`.
- **docs-i18n:** ack + survey running, baseline test in background.
- Backend's commit is green (13 pkgs ok per handoff); full-tree gate waits
  on frontend round 2 commit. Tag proposal `v0.4.0` held.

## 2026-10-12 — `v0.4.0` deployed (stamp-free render + label-bar passes)

- Fire sequence per devops-qa checklist: `make test` green (13 pkgs ok,
  css budgets hold, gofmt/vet clean) → `d2f68a6` ops-log commit (tag
  refuses dirty) → `make tag V=v0.4.0` → `make backup`
  (`guzva-20261012-1331.db.gz`) → `make deploy`.
- Healthcheck OK: `version v0.4.0`, page + admin answer. Rollback anchor:
  `releases/v0.3.1` kept (last 3). Expect an honest STALE blip on the
  fleet board over the restart (marginal bench link).
- Contains: `25bce48` stamp removal (poster@3/mono@3), `764986b` round 1
  (chip-led label bar, info card), `2dcb78f` overlay fix, `c877c65`
  fixture + comment truth. Known limit shipped: no-JS clock can freeze
  between state flips (ACTION open, §6.4 question).
- NOT done: push (no remote; scrub still mandatory first), docs-i18n
  truth sweep (rides the next release).

## 2026-10-12 — orchestrator: user issue round planned + delegated (post-v0.4.0)

- Admin: (1) one-shift hours editor (2nd shift removed; out-of-bounds
  fixed) — backend contract + compat first, frontend editor after;
  (2) admin EN copy polish (short/tasteful); (3) 24h everywhere.
- Public: (4) poster defaults less destructive (backend evaluates bias/
  thresholds with goldens, small safe change); (5) drop redundant
  `danas · CEST`; (6) 7 small weekly day-cards, closed grayed out
  (backend data funcs → frontend cards); (7) light theme default;
  (8) telephone own section (location only if a field exists — backend
  confirms, no invented storage).
- Order: backend-go Phase A contracts → frontend-tisak Phase B UI →
  docs-i18n copy/review → tag/deploy. Firmware stands down (nothing
  device-side). Devops-qa keeps the demo warm.

## 2026-10-12 — orchestrator: backend Phase A landed (3 commits, tree clean)

- `371a29c` hours: single-interval contract WITHOUT migration — stored
  rows keep parsing/classifying, `MultiDay` flags legacy lunch-break days,
  dual-generation field names accepted, `DayForm.Open/Close/Split` added
  with `Open1..Close2` as populated deprecated aliases (first attempt
  500'd the console via `hours.html`; fixed additive, console green
  throughout). Week cards pure+tested (`WeekCards`, `DayText`;
  `hoursToday` shares the formatter).
- `77e60a6` venue v7 `location` (free text ≤160ch, newline-collapsed;
  lat/lon refused on §1 privacy). Dogfooded v6→v7 on live-DB copy.
- `812a9dc` render evidence only: thresholds `{64,128,192}` already
  quarter luma; raising them *increases* ink (5.9%→29% in probe). No
  safer default exists without real frames — **filter fix needs W8.1
  bench frames, not this pass** (user-visible caveat, recorded).
- 24h: Go side already 24h everywhere (pinned `TestDayTextIs24Hour`);
  remaining `hour12`/time-input rendering is JS-side (frontend's).
- Frontend unblocked on: one-shift editor, week cards, location fields,
  JS 24h. `hours.err.second` stays live (legacy path, docs-i18n's key).

## 2026-10-12 — orchestrator: docs-i18n `a131292` landed (truth sweep)

- 3 files: README (7-stage pipeline, label-carries-time, `version`
  subcommand), runbook (⚑2 reason = Last-Modified clock), proto-v1
  (rig retired, 507 Retry-After matches firmware W4).
- B2 gate green (`TestFetchedKeysExist` + identical-key-sets, -count=1).
  `hours.err.second` live in both catalogs.
- 5 findings forwarded to frontend-tisak (owner_login full-HR form,
  owner_dash QR placeholder, Šablon island ×2, poller `fmtDay` undotted
  on en, stale stage-7 comment). Catalog keys for Phase B strings:
  docs-i18n adds on frontend landing.
- Still open: user's answers to frontend's three decisions (requested,
  not yet received) → backend briefing pending.

## 2026-10-12 — orchestrator: frontend Phase B lane-1 (4 commits, clean)

- `b2e24eb` caption drop + phone section; `73a9887` admin copy trim +
  glosses + stamp-comment fix; `e25ebc1` one-shift editor + admin
  location field; `864b0a7` owner copy via catalog (red-flagged by
  design — 3 keys missing, gate red exactly there, verified).
- Frontend verdicts: light-default already light-first (b ✓); JS clocks
  clean, 15:04 + hour12:false only (e ✓); caption removal vacates
  docs-i18n item 4. One `TestFormsWorkWithoutJS` flake mid-lane, green
  on repeats — watch item.
- Dispatched: docs-i18n adds the 3 keys (hr+en); backend-go wires
  owner_login `Cat` + `view.Location`/`view.Week` (+ state key). Then
  frontend renders Location row + week cards + owner_login keys.

## 2026-10-12 — orchestrator: docs-i18n `66833de` merged (gate un-red)

- 3 keys landed hr+en (`hours.splitwarn`, `owner.location`,
  `owner.about.placeholder`); `TestFetchedKeysExist` + identical-sets
  PASS at `-count=1`; `make test` green. `864b0a7`'s red-by-design
  cleared. Spare-model handover (ling→nemotron) recovered a mangled
  en-catalog edit correctly via git restore.
- Backend plumbing in flight (`pages.go` dirty): `Cat` for
  owner_login + `view.Location` (+ state key) + `view.Week`. Then
  frontend renders: Location row, week cards, owner-login catalog
  copy, `owner.login.*` keys (docs-i18n standing by).
