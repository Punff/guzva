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

`ACTION:` the WiFi password (network name redacted — it was quoted here
pre-scrub) remains in git history and live —
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

## 2026-10-12 — orchestrator: backend plumbing landed + reworded (`98dbcbf`/`b3a20ee`)

- `98dbcbf` owner login model `{Err, Cat}` (i18n.HR deliberate — no
  venue identified pre-auth, D7 default); `b3a20ee`
  `view.Location` (IN the state key, mtime-asserted) +
  `view.Week = WeekCards(cat.DayNames())` (no key component — derives
  from Hours + language, both keyed). Full-path dogfood on a faithful
  v6 copy: migration clean, page 200s, key flips + settles.
- **Hash churn:** an authorized reword removed a false mid-session
  claim from `98dbcbf`'s message (it had recorded "merge tree-sync
  reverted my work" — disproven by reflog; no HEAD movement, no
  resets; reconciled facts: no actor attribution possible, claim
  dropped). Old hashes `f674f76`/`d32b966` dangle, nothing pushed,
  `b3a20ee` byte-identical content. Nobody may cite the old pair.
- **WAL lesson (devops: fold into restore-drill notes):** a raw `cp`
  of a SQLite file in WAL mode silently drops recent writes — the
  first dogfood copy lost the venue row. Use the backup API
  (`src.backup(dst)`) or checkpoint first. Same trap bites a
  live-server backup.
- Dispatched: frontend final render (Location row, 7 day-cards grayed
  closed, owner-login via Cat) + docs-i18n `owner.login.err.throttle`/
  `badcreds` keys; backend swaps the two literal call sites after.

## 2026-10-12 — orchestrator: v0.5.0 — frontend render round in, docs-i18n override

- Frontend landed `a6f11f0` (public week cards Mo-first, closed grayed,
  location row, tel block; proven 360px + desktop; public 3793/4096 gz)
  and `1c9530f` (owner login through `{{.Cat.Get}}`, red-flagged by
  design). Flag for later: "today" highlight on the week strip would
  need `view.TodayIdx` — user design call, not requested.
- **EMERGENCY OVERRIDE (single-writer exception, user-authorized):**
  docs-i18n went unresponsive (3 sends, zero replies, spare model not
  consuming). Orchestrator added the 8 keys hr+en to `internal/i18n/
  i18n.go` directly per frontend's exact spec (`owner.login.title/tag/
  venue/password/submit/note` + `err.throttle`/`err.badcreds`). Gate
  GREEN (13 pkgs, budgets hold). Follow-ups for docs-i18n when it
  responds: review the wording + own the file again.
- Backend follow-up (post-deploy): swap the two literal login-error
  sentences in `owner.go` to the new `owner.login.err.*` keys.

## 2026-10-12 — `v0.5.0` deployed (user-issue round: hours, cards, location, copy)

- Fire sequence: gate green (13 pkgs) → `022b987` (override keys + ops
  log) → `make tag V=v0.5.0` → `make backup`
  (`guzva-20261012-1917.db.gz`) → `make deploy`. Healthcheck OK:
  `version v0.5.0`, page + admin answer, disk 42 %. Rollback anchor:
  `releases/v0.4.0` kept (last 3).
- Contains the whole user-issue round: one-shift hours editor
  (`371a29c` + `e25ebc1`), weekly day cards + public location row
  (`a6f11f0`, schema v7 `77e60a6`), light-default verified, admin copy
  trims (`73a9887`), caption de-redundancy + phone section (`b2e24eb`),
  owner console fully through the catalog (`98dbcbf`, `1c9530f`,
  `022b987`), calib overlay fix (`2dcb78f`). Poster thresholds NOT
  changed (no real frames — W8.1 bench session owns that).
- Open: docs-i18n unresponsive (override on record; wording review when
  it returns); backend `owner.login.err.*` call-site swap (next
  release); push still gated on history scrub + no remote.

## 2026-10-13 — orchestrator: round 3 merged (theme/alignment/logo/highlight)

- Frontend 5 commits, gate 13/13 green, budgets hold (public 3697/4096
  gz, app 4481/12288 gz): `b117f9b` light-only theme (user decision;
  dark blocks + meta removed, contrast re-computed ≥4.5:1, dark-
  emulating shots now render light = proof of removal);
  `769cfb8` admin alignment (fleet age+Edit grouped, calib grid
  2-col mobile + shortened labels; other pages clean);
  `32f9e2e`+`4ec705d` new logomark "the tally label" (16×16 grid,
  1 device px/cell favicon, identical data-URI both heads, kit sizes
  16–128); `ca50c2e` today-row bold + "· danas" +
  `aria-current="date"` via `view.TodayIdx` + `StrToday` (no new
  strings). `proto.ReasonDark` untouched by design.
- Backend `d1f26f4` `view.TodayIdx` state-keyed (midnight flip forces
  re-render — the Location-style ruling, documented in its commit).
- Orchestrator `55b5c20` synced PLAN.md + maintenance W7 item (dark
  toggle void). Two model handovers crossed the round cleanly
  (muse-spark → mimo-flash mid-Task-3).
- **Round 4 dispatched** (user mockup): header tagline, state chip,
  frame time-badge, hours card treatment (highlight already in),
  INFO card (user: no Nazovi button — phone/address/description
  section instead), hatched footer divider. Rulings pinned in
  `/tmp/opencode/mockup-round4-spec.md`; mockup image is source.
  "danas · CET" stays removed (user's earlier explicit call).
- After round 4: orchestrator PLAN.md evolution pass (user directive).

## 2026-10-13 — orchestrator: round 4 merged (public page per user mockup)

- Frontend 5 commits, gate 13/13 green at `4639d18`, tree clean:
  `f504afe` header tagline (`tagline` key already existed) +
  display-size title; `60b9f39` status row above the print (state
  chip + open-until plain), paper mat, in-frame time badge (JS
  poller keeps it in sync); `704f06a` hours + info as two bordered
  cards (`HoursTitle`/`AboutTitle` keys, no new strings — rulings
  6+7 merged into one commit so every commit is a complete page);
  `38a32e8` hatched footer divider + privacy caption (no
  "O privatnosti" link — no privacy route exists; flagged);
  `4639d18` kit section + design.md §7 truth sweep (also fixed two
  §7.3 stale clock claims predating this round).
- Mockup rulings held: no Nazovi button (info card instead), "danas ·
  CET" stays removed. Two tooling catches by frontend itself: a
  cached Chrome profile served stale HTML (fresh profile re-shoot),
  and a fakecam startup race (frames not landing → chip showed
  "Nema nove slike"; restarted after server health).
- Orchestrator spot-check green (i18n/pages/admin/webui at
  -count=1). Releasing v0.6.0, then the PLAN.md evolution pass.

## 2026-10-13 — `v0.6.0` deployed (rounds 3+4: light-only, tally logo, mockup page)

- Fire sequence: gate green → `3589464` (ops log) → `make tag
  V=v0.6.0` → `make backup` → `make deploy`. Healthcheck OK:
  `version v0.6.0`, page + admin answer, disk 39 %. Rollback anchor
  `releases/v0.4.0` kept (last 3).
- Contains: light-only theme (`b117f9b`), admin alignment
  (`769cfb8`), tally-label logomark (`32f9e2e`+`4ec705d`), today row
  (`ca50c2e` + backend `d1f26f4`), mockup public page
  (`f504afe`/`60b9f39`/`704f06a`/`38a32e8`/`4639d18`), backend
  login-err keys (`178e853`).
- Handoff review: **INFO card heading = `venue.about` key ("O
  salonu"/"About") instead of literal "INFO" — APPROVED** (zero new
  strings, semantically the venue's own card; revisit only if the
  user asks). Zero i18n keys needed this round — every mockup string
  resolved from existing catalog keys.
- Housekeeping: round-3's `r7-today-row-m360.png` proof was
  overwritten by a stale-profile shot mid-round; the today row is
  still proven inside `r9-pub-m360.png`. fakecam startup race noted
  (connection-refused death; restart after server health — devops:
  worth a guard in the fakecam runner).

## 2026-10-13 — round 5 (single clock) + scrub + FIRST PUSH to GitHub

- **Round 5** (user: "double clock — remove the picture time
  stamp"): frontend 1 commit — `#stamp` in-frame badge deleted, `#hl`
  below the print is the page's one clock (poller contract kept; live-
  verified 15:29→15:30 tick, zero console errors). Post-rewrite hash:
  `a3505c4` (was `ee15f26`). Kit + design.md §7 followed; budgets
  hold (public 3720/4096 gz, app 4620/12288 gz).
- Orchestrator synced the three PLAN refs + maintenance refs that
  described the removed badge (`5462fcf`, was `1acf2cd`). ops-log's
  round-4 entry stays as written (append-only: it was true at
  v0.6.0).
- **design.md drift sweep** (frontend, its offer, orchestrator-
  dispatched): 17 verified edits — §5.2 seven-stage pipeline (stamp
  stage gone, v1/v2/v3 versioning note), §5.4 palette (no "stamp
  plate/digits"), §5.6 "the label, not the picture, carries the
  time" + full v0.4.0→v0.6.1 history, §5.11 invariant list, §7.3
  no-JS honesty, §14 ⚑2 ✅ resolved, §5.8/§15 registry versions
  fixed to @3. `a1ca8eb`.
- **Pre-push security** (`11fc19d`, was `a2b3133`): SSID literal out
  of `settings.example.h` + this log (the pre-scrub ACTION line
  quoted it).
- **History scrub executed** (user authorized via the remote URL +
  "set it up properly"): `git filter-repo` purged
  `firmware/include/settings.h`, `firmware/include/wifiSettings.h`,
  `include/wifiSettings.h`, root `styleproof` ELF (4.2 MB) from all
  125 commits; replace-text wiped the WiFi password, SSID and the
  revoked `213cc440…` token everywhere they ever appeared. Verified:
  0 secret values, 0 secret paths across full history; all 5 tags
  preserved; `.git` 6.2→2.5 MB. Backup tar:
  `/tmp/opencode/guzva-pre-scrub-final.tar.gz`. Pre-scrub hashes die
  (notable: `ee15f26→a3505c4`, `1acf2cd→5462fcf`, `a2b3133→11fc19d`,
  `d08706c→44a1945`).
- **CI first-run fixes** (devops, `44a1945`, was `d08706c`): settings
  .h copied from example before `pio run` (gitignored in fresh
  checkout); `pipx` for platformio (PEP 668 on ubuntu-24.04); setup
  -go added to the firmware job (its privacy step ran `go test` with
  no Go installed — found by devops' own re-scan).
- **FIRST PUSH** (user gave https://github.com/Punff/guzva.git;
  public repo): OAuth token lacked `workflow` scope for ci.yml →
  remote is SSH (`git@github.com:Punff/guzva.git`). master + all 5
  tags pushed. Repo metadata via gh: description + 9 topics
  (esp32/crowd-monitoring/go/tailwindcss/…); README renders clean.
- **CI run #1 RED, run #2 GREEN.** #1: `TestGoldenPoster` +
  `TestGoldenMono` failed on the runner only. Root cause (backend,
  proven by pinned-toolchain reproduction, not assumed):
  `goldenCheck` hashed raw PNG BYTES and the stdlib deflate stream
  differs between Go 1.27.1 (local) and 1.26.0 (go.mod, CI) —
  pixels identical, bytes not. Fix `349f8b4`: goldens compare
  decoded pixels + PALETTE (backend caught that hashing `Pix` alone
  stores palette *indices*, so a colour change would slip; Stride
  dropped as transport). Verified green under both toolchains locally
  AND in CI: run #2 go + firmware both ✅.
- Open: WiFi password rotation (router); docs-i18n wording review
  when it returns; v0.6.1 tag + deploy follows this entry.

## 2026-10-13 — `v0.6.1` deployed (single clock + CI green on GitHub)

- Fire sequence: CI run #2 green (go + firmware) → `21ab3be` (this
  log) → `make tag V=v0.6.1` → tag pushed → `make backup` → `make
  deploy`. Healthcheck OK: `version v0.6.1`, page + admin answer,
  disk 39 %. Rollback anchor `releases/v0.4.0` kept.
- Contains: the single-clock page (`a3505c4`), truthful design.md
  (`a1ca8eb`), pixel-basis goldens (`349f8b4`), PLAN/maintenance
  single-clock sync (`5462fcf`), CI first-run fixes (`44a1945`),
  pre-push SSID hygiene (`11fc19d`). Prod and GitHub now match:
  `https://github.com/Punff/guzva` (SSH remote; scrubbed history).

## 2026-10-13 — round 6 (display clock) + the "stuck at 11:44" incident

- **Round 6** (user: "the clock is small and disproportioned"): the
  below-print clock is now display-scale — `#hl` at 2.25rem/3rem bold
  tabular, the SAME steps as the venue title, measured off the 2×
  mockup (~38pt), flush-left under the print. Poller/aria/no-JS
  contracts untouched; budgets hold (3695/4096 gz). `eaa055d`, CI
  run #3 ✅ (1m43s — warm caches).
- **The incident (user report: "camera stuck at 11:44; restarts and
  deploys don't fix it"). Root cause: a single admin calibration
  edit at 11:44 local** (`admin: device cfg updated ver:2` journal
  line, 09:44Z) set `framesize=0`. On the pinned esp32-camera build
  that is 96×96 — 1:1 — which can never pass the render contract
  (tisak.go: `sw*3 != sh*4` → Reject("dims")). The field unit then
  spent ~5 h degraded/422-rejecting while the page honestly showed
  "Nema nove slike" + the 11:44 still (never-publish-a-failure
  working exactly as designed — the server never lied once).
- **Recovery sequence (bench):** full erase + reflash
  (v0.6.1-1-g76f97c2-dirty; wiped the wedged sensor state —
  `camera=ok` at boot), then the board's WiFi went deaf (`scan: 0
  networks` while wlan-Ant was provably up) — user reseated the
  antenna → `got IP, rssi=-81`, and RSSI has since improved to −68
  (the −80s were the antenna all along). One transitional
  camera-degraded post on the ver=4 apply, then clean 201s with
  render_ms ~6.
- **Feed restored** via the proper app path: `guzva cfg -set
  framesize=5` on the VPS (ver 3→4; 5 proven 4:3-accepted), device
  applied on next POST, `latest.png` live again 15:34:25Z.
- **Dispatched to backend (fixes, in flight):** (A) the cfg key
  table must reject non-4:3 framesizes at WRITE time (both the admin
  form and `guzva cfg`) — this class of value can never reach a
  field unit again; (B) round-6 finding: `pageCache.serve` sends no
  Last-Modified/ETag and never 304s, so the poller's state-flip
  reload detector is INERT (static.go:89's comment describes a path
  that doesn't exist) — open tabs never see fresh→stale flips.
- Bench hygiene: the reflash reset boot/brownout counters (now
  boot=8, brownout=0); WiFi password rotation still owed (router).

## 2026-10-14 — autonomous pass: reclaims, guzva-qa, v0.6.3

- Overnight bench: boot 16 posting clean 201s at 45 s cadence,
  rssi −68…−74, render 6–9 ms — the ver-6 config (`framesize=5,
  aec=1, agc=1, agc_gain=15`) held all night. CI green on all three
  yesterday pushes (runs 38422612750 / 38424147331 / 38433145854).
- Reclaims dispatched to the two workers after yesterday's false/incomplete
  handoffs: firmware owed the actual warm-up gate (the `[WARM]` lines it
  claimed were never in the tree — grep-proven) + the `[SENS]` warning;
  backend's run had failed mid-item-C (callers all passed 0, `mean_lua`
  typo) — recovered with strict C → B → A/D → E order, B (pageCache
  conditional GET — **the user's hours-edit report**) first among
  user-visible items.
- **guzva-qa created** (7th session, `nvidia/deepseek-ai/deepseek-v4.1-flash`,
  cost 0): owns `qa/`, orchestrator-authorized direct fixes in `web/` +
  `internal/webui/` while frontend is idle; report-only elsewhere (backend
  and firmware are mid-flight in their lanes). Mandate: test harness,
  ingest-reject matrix, the pageCache-304 expectation (skip until B lands,
  then enforce), incident-regression tests for the three values that broke
  yesterday.
- Plan for the autonomous window: land the reclaims → gate → push → CI →
  v0.6.3 deploy → flash + bench-verify firmware's three checklists →
  then W6 docs truth with a revived docs-i18n (fallback model — the
  original session is dead).

### 16:00–16:40 — the dark loop, and the third value from one bad save

- After the user's restart the board posted cleanly once
  (`boot=9`, 201, `render_ms` 7.2), then every subsequent frame came
  back `422 reject:"dark"` — ~25 min, three rejects per tick, through
  two restarts. `camera=ok`, real ~2.1 KB JPEGs, WiFi up, config
  applying: electronics healthy, image underexposed.
- Self-inflicted detour worth remembering: **opening `/dev/ttyUSB0`
  with `stty`/`cat` resets the ESP32 into download mode** (DTR/RTS
  toggling on the CH340). Raw serial "monitoring" was knocking the
  board off the air; the boot counter climbed 9→15 largely because of
  my own probes. Only `esp32-bootlog.py`'s deliberate RTS pulse is
  safe; for pass-by checks use the server journal.
- Root cause, in the end, was **config, not hardware**: the same 11:44
  calibration save had also pinned `agc_gain: 0`. The firmware applies
  the AGC gain register unconditionally, so `aec=1, agc=1` with
  `agc_gain=0` means auto-gain pinned to minimum — the OV2640 could not
  reach the light and every frame stayed under `DarkMeanLuma = 6`
  (render.go:34). Restoring `aec=1, agc=1` alone was not enough;
  `agc_gain=15` fixed it within one tick. Clean 201s at `ver 6`
  (`framesize=5, aec=1, agc=1, agc_gain=15`), `latest.png` live.
- **The real lesson: per-key validation is not enough.** One save
  smuggled three contract-breaking values past a form that range-checks
  each field in isolation. Dispatched:
  - backend — (A) 4:3 framesize at write time; (C) log the computed
    mean luma on `dark` rejects (today `dark` carried no magnitude, so
    "covered lens" and "gain clamp" were indistinguishable from the
    logs — two hours lost to that blind spot); (D) exposure-group
    **coherence** validation: reject `agc=1 && agc_gain=0`, warn on
    fixed-exposure saves; (E) surface reject reason + mean + since on
    the admin device board, so a dark device is legible, not silent.
  - firmware — warm-up captures discarded after camera init and after
    exposure-affecting config applies; escalate to health-only beats
    after 4 consecutive `dark` rejects (today's twenty silent posts are
    what this prevents); TLS-failure backoff (`tls_fail=12` at full
    cadence today); and a named `[SENS]` warning when a config applies
    gain-control-on with `agc_gain=0`.
- The page never lied once through any of it: "Nema nove slike" plus
  the last good still, which is the whole point of
  never-publish-a-failure.

### 2026-10-13 — architectural decision: remove owner camera config

User directive: **take camera config away from the owner**. One fixed,
privacy-first sensor profile baked into the firmware (framesize=5
QVGA 320×240, aec=1, agc=1, agc_gain=15, wb_mode=auto, lenc=1,
denoise=0, etc. — the exact working baseline we just proved at ver 6),
with at most **minor, safe tweaks** (e.g. a single "brightness offset"
±2 if the room is consistently dim/bright). The 22-key desired_cfg
surface and the admin calibration form are removed/replaced.

Scope per role (owner of each area does the work, orchestrator sequences):

- **firmware-esp32**: bake the fixed profile into `SensorCfg` defaults,
  remove the apply-path for the now-fixed keys, keep only the allowed
  tweak key(s) in the device-plane proto. Boot log must show the
  frozen values (one line, no per-key noise). Size/IRAM delta noted in
  handoff.
- **backend-go**: shrink `desired_cfg` in `proto/proto.go` to the
  tweak key(s) only; drop the 22-key validation table; the admin
  `cfg` endpoint and `guzva cfg` CLI become no-ops or return the fixed
  profile; device still echoes `cfg_ver_applied` for the keys it still
  accepts (if any) so the amber read-back on the fleet board stays
  honest.
- **frontend-tisak**: remove the calibration section from the admin
  venue page; replace with a read-only "Sensor profile: fixed
  privacy-first (320×240, auto-exposure, auto-gain ceiling 15, lens
  correction on, denoise off)" card plus the one tweak control if
  approved. WCAG AA, Croatian copy, kit page proof.
- **devops-qa**: CI must gate that the firmware still compiles with
  the reduced config surface and that the proto shrink doesn't break
  the device handshake (the device still sends the same telemetry, the
  server still answers with the shrunken cfg — no wire-format break).

This is a Phase 2 item (after W0–W6 close). I'll tag it in PLAN.md §14
when the current pass ships. No code changes today — this note is the
decision record so the workstreams don't drift back toward
re-adding the form.

## 2026-10-14 — `v0.6.3` release candidate: restart-recovery reliability + the hours-edit fix + guzva-qa round 1

Nine commits from the autonomous afternoon pass (040e338…5cabe4e).

**Firmware (`f597df4`, `1cadcbf`, `2243609`, `09a1ba5`) — restart-recovery
reliability.** Four commits, each grep-verified and `pio run` green
(RAM 19.1 %, Flash 77.6 %):
- Warm-up gate: the first 2 captures after boot AND after any
  exposure-affecting config apply are discarded locally (`[WARM]`
  lines) so the OV2640 auto-exposure converges before the first POST.
  Discarded frames never touch dark/post_fail/tls counters.
- Dark escalation: 4 consecutive `dark` rejects from the server →
  `[CAM]` warning + health-only beats until a frame passes.
- TLS transport backoff: consecutive failures double 60 → 900 s,
  reset on any 201/409. The dead-radio hammer pattern is gone.
- `[SENS]` serial warning when `agc=1 ∧ agc_gain=0` (the exact
  pair that caused yesterday's dark loop) — on top of the server-side
  reject the backend owes (not yet landed, see below).
The two contract gaps found in review were fixed before commit:
`warmDiscards` resets on config-apply, `warmConsecutiveDark` resets
on 201/409.

**Backend (`83b707d`) — the user-reported hours-edit bug.**
`pageCache.serve` never sent `Last-Modified` and never answered 304,
so `venue.html`'s 60 s flip detector could not fire: settings edits
(hours, closing time, flips, the midnight "danas" move) never reached
already-open pages. Fixed: `Last-Modified` from the index.html mtime
(the same mtime the state key moves on), `If-Modified-Since` → empty
304 with security headers. New `TestIndexHtmlConditionalGet`,
mutation-checked. Live curl proof in the commit handoff. Item C
(mean-luma instrumentation) is NOT in this release — the backend
session burned its runs; re-dispatched separately.

**guzva-qa round 1 (`d778468`, `36441c1`, `5cabe4e`) — 196 assertions.**
Harness (`qa/`): hermetic Go fixture generator, ingest matrix (85/86
pass), pageCache conditional-GET (22/23, enforced against `83b707d`),
public-page contract 68/68, hermetic a11y + i18n audits (both
mutation-checked; shipped palette 5.70:1 … 14.16:1, clears AA).
Findings in `docs/qa-findings.md`. Open P1s for dispatch:
- **Q-05 (backend)**: a zero-length frame part gets 201 — same
  blind spot item C closes, different door (`ingest.go:262`).
- **Q-01 (backend)**: mean-luma instrumentation absent; spec restated.
- **Q-04 (backend, P2)**: `Vary: Accept-Encoding` on the gzip branch
  only — one-line move.
- **Q-07 (orchestrator)**: prod is v0.6.2; the hours-edit symptom is
  live in production until this ships — this deploy.
QA also corrected its own false positives twice (CSP on 304 is
correctly absent per RFC 9111 §4.3.4; HTTP/2 lowercases headers) —
the code was right, the assertions weren't.

**Process note.** The openchamber tool flapped through the pass
(create/send/session.list intermittently unavailable), and the
backend session (openrouter/nemotron drift) produced two handoffs
claiming work that was not in the tree — caught by grep-verify each
time; its model was restored to the roster fit
(`nvidia/deepseek-ai/deepseek-v4.1-flash`) and the B-only dispatch
landed clean. A `git stash pop` incident (NUL padding in two files)
was disclosed and recovered from HEAD by the backend session; the
tree was verified NUL-free before commit.

Ships as `v0.6.3`: firmware reflash + bench verify of the three
checklists still owed after deploy.

### Deploy — `v0.6.3` live (2026-10-14 14:00 local)

- CI run 38575683255 green (10 commits `2fd0443..1b96718`), tag pushed.
- Server: `make deploy` OK, healthcheck `version v0.6.3, page + admin answer`,
  disk 39 %.
- **Hours-edit fix verified live**: `GET /bench/` → 200 with
  `Last-Modified: Wed, 14 Oct 2026 13:59:13 GMT`; future
  `If-Modified-Since` → 304. The flip detector can fire in production
  now (QA-07 closed).
- Firmware reflash (`pio run -t upload`, hash verified) → **boot 20**,
  brownout 0. Bench-verified warm-up gate in production: captures
  C20-516/517 posted as health-only beats (no `render_ms` — frames
  discarded while the OV2640 exposure converges), C20-518 posted a
  real frame (`render_ms 9.6`). Exactly the designed behaviour:
  2 discards, then normal cadence.
- Feed healthy at handoff: 201s at ~50 s cadence, rssi −69…−71,
  free_heap 145 K.
- Still owed (follow-ups, not blockers): bench-verify checklists for
  dark-escalation + TLS backoff (need an induced failure to observe);
  firmware serial bootlog script `/tmp/opencode/esp32-bootlog.py`
  was lost (tmp cleanup) — regenerate before the next bench session;
  backend Q-01/Q-04/Q-05 re-dispatch; WiFi password rotation.

### Solo pass — Print v4 + cleanup (2026-10-14, not deployed)

Code only; prod still runs `v0.6.3`, the bench board still runs v0.6.3
firmware. Commits `5c67796..` on master, CI green on each push.

- **Render v4** (`f9957a0`): 320×240 band-pass, Weber-contrast tones,
  masks-only venue state (schema v8 backfills calib_json masks ×2).
  Constants chosen on the synthetic hard scene only — NOT on real frames.
  Local render 14 ms for one QVGA frame (dev laptop; e2-micro unmeasured).
- **Device-config plane removed server-side** (`c8eb598`): no desired_cfg,
  no `guzva cfg`, schema v9 `devices.profile`. Verified locally: legacy
  meta with `cfg_ver_applied` still ingests (422 dark path exercised), and
  v0.6.3 firmware's apply-if-found parser leaves its NVS config untouched
  when desired_cfg is absent (read in main.cpp before the change).
- **Firmware Profile v1** (`38c7a05`): built with `pio run`, not flashed.
  Fixes the dark-escalation no-op (now a 300 s backoff).
- **Rollback note:** v8/v9 are additive, but a `v0.6.3` binary refuses a v9
  database ("unknown schema version"). Rolling back after deploying this
  needs the on-deploy snapshot restore, not just `make rollback`.
- Admin: fleet creation forms folded into disclosures, device page is
  read-only health; public print 1:1 on 360 px phones. Q-03, Q-04 fixed.


### Deploy — `v0.7.0` live + board on Profile v1 (2026-10-14 16:46–17:08 UTC)

- CI green on `3bd5b2d`; `make backup` first (`guzva-20261014-1643.db.gz` —
  the rollback point: v0.6.3 refuses a v9 database). Tag `v0.7.0`.
- `make deploy` OK: `healthcheck OK: version v0.7.0, page + admin answer`.
  Live probe: schema 7 → 9 in place; bench calib_json had `Masks: null`, so
  masks_json stayed empty; v0.6.3 firmware kept posting 201 with no
  desired_cfg in the reply; its frames stored `poster@4` 320×240;
  **render_ms 23–41 on the e2-micro** (first real measurement);
  `Vary: Accept-Encoding` on the identity page response (Q-04 live).
- Firmware `v0.7.0` flashed (`pio run -t upload`, all hashes verified).
  Serial boot: `profile=p1`, camera ok, token kept (len 32), warm-up gate
  discarded the first capture.
- **Outage 16:52–17:00, partly self-inflicted.** Every `tools/bootlog.py`
  attach rebooted the board (boot 28 → 31: a Linux tty open asserts
  DTR/RTS before pyserial can hold them low), and each reboot costs two
  warm-up discards + a WiFi rejoin. The link was also bad in that window:
  beacon-loss disconnects (reason 200/201), one scan hearing no networks,
  TCP probes with no SYN-ACK, TLS handshakes failing ssl_err=-1. The user
  noticed the board "off"; it recovered on its own.
- Steady state from 17:05: 201 every ~65 s, rssi −75 → −69, tls_fail flat
  at 2 since boot 31, raw frames ~5 KB (v0.6.3 config: ~3.5 KB).
  Lesson: diagnose the link from the server journal, never by attaching
  serial to a board that is mid-recovery.
- Still owed: 48 h soak on p1; real-corpus tuning of the v4 constants.

### Solo pass — owner statistics + UI polish (2026-10-14, not deployed)

Prod stays on `v0.7.0` while the user soak-tests the board on Profile v1
(a deploy restarts the server; not done mid-test without asking).

- `13d5a77` visit counting (D10), schema v10. `f0bc521` stdlib QR encoder,
  all versions 1–10 round-tripped through zbarimg (CI installs zbar).
- `705b2b9` owner dashboard: 7-day KPIs with trends, 14-day + hour-of-day
  SVG charts, tips, share card with QR + copy, printable poster, camera
  uptime, settings folded. `2f1a69a` fleet views column, owner-login copy,
  public print exactly 2× on desktop.
- **CI was red for two commits (`13d5a77`, `f0bc521`)**: visits_test.go
  imports unsorted. `make test` had failed locally, but I filtered its
  output and missed the gofmt line. Fixed in `705b2b9`; I now gate on the
  exit code of `make test`, not on grepped output.


### Deploys `v0.8.0` + `v0.8.1`, bench firmware flipped (2026-10-14 17:31–17:50 UTC)

- `v0.8.0` (owner stats, QR/poster, UI polish): backup first, `make check`
  OK, schema 9 → 10 (visits tables). Live: the public page counted real
  traffic within the hour (17:00 UTC: 8 views, 54 polls, 1 QR — the QR
  view is my deploy probe — 2 distinct visitors).
- User feedback on real v4 prints: image upside down; objects melt into
  the background. Raw `/jpg` frames captured to `corpus/` (gitignored,
  local only) confirmed a top-to-bottom flip.
- `v0.8.1` = render v5 (outlines, `95c3f64`): `make check` OK.
- Firmware `v0.8.0-2-gdc4ae96` with `PROFILE_VFLIP 1` in the bench
  board's settings.h: first upload failed "port busy" (the CH340 had just
  re-enumerated), retry flashed OK with hashes verified.
- **Board offline since 17:44 UTC**: /dev/ttyUSB0 gone, no ping on
  192.168.1.6 — unplugged/unpowered (the user is soak-testing it). Last
  stored frame is still poster@4 from v0.7.0 firmware; the public page
  correctly shows "Slika kasni". The flip and outlines are unverified on
  hardware until it is powered again.

### Bench link outage + recovery, first poster@5 frame (2026-10-14 17:44–18:29 UTC)

- After the 19:44-local unplug/move the board could not hold WiFi: LAN ping
  60–100 % loss at 0.6–3.6 s RTT (router itself: 8 ms, 0 %), server-side
  TLS handshakes from the home NAT ending in EOF / 15 s i/o timeout,
  firmware escalating its backoff. The CH340 also dropped off USB on its
  own (kernel log: disconnect + re-attach as ttyUSB1), which is why one
  upload hit "port busy" — platformio.ini pins ttyUSB0.
- Reflashed the same build (`--upload-port /dev/ttyUSB1`, hashes OK):
  no change over 15 min — software ruled out (WiFi sleep already off,
  TX power unchanged since v3, identical behaviour across two flashes).
- The user then re-powered/moved the board (boot 40): 201s with
  tls_fail 0 from 18:27, first real frame 18:29:28 — `poster@5`
  (outlines), 5.1 KB, render 30.7 ms, firmware v0.8.1-1-g75faa27 with
  PROFILE_VFLIP. Orientation to be confirmed by eye on the live page.
- Lesson recorded: the bench link problem is physical (supply/cable/
  placement); diagnose with LAN ping + server journal + kernel USB log,
  never serial.

### Deploy — `v0.8.2` (2026-10-14 19:40 UTC) + overnight check (2026-10-15 08:10 UTC)

- `v0.8.2`: render v6 (σp 1.75, user decision), public page "four inks"
  redesign, aborted-upload classification. Backup first; `make check` OK.
- Overnight: 293 `poster@6` frames 19:41 → 08:09 UTC, board on boot 41
  throughout (no reboots since it left the laptop's flaky USB), rssi
  −75, 0 "ingest aborted" lines. visits_hourly: 128 views recorded.

### Deploy — `v0.9.0` (2026-10-15 08:28 UTC)

- Public page: live "N osoba gleda" from an X-Watching header on the still
  poll (memory only, no DB); compact venue name, picture higher on phones.
- Admin: delete venue (slug-confirmed; rows cascade, files removed via
  hook — proven on the local rig), edit/move/delete device, per-venue
  picture-style presets (D11, schema v11 `look_json`), masks retired,
  legibility pass (sentence-case labels, ink primary buttons, compact
  hours editor).
- Owner: three numbers over complete days vs the week before, readable
  day/hour charts, tips/percentages/minutes removed.
- Backup first; `make check` OK; schema 10 → 11; live probes: still
  answers `x-watching`, page has the plural strings + compact h1, admin
  and owner logins 200; board posted C41-1201 at 08:30:03 → `poster@6`.
- Board health note: boot 41 since 2026-10-14 ~19:30 (no reboots), but
  post_fail 54 over that time — uploads mostly succeed on retry; the
  WiFi link at the bench spot is still marginal (rssi −73…−75).
- QA run_all before deploy: 78 + 23 + 68 live checks pass; CI green.

### Deploy — `v0.9.1` (2026-10-15 11:09 UTC)

- Watcher badge (others = X-Watching − 1, beside the clock), one camera per
  venue (console rule), signal bars on every camera view, more detailed
  owner charts (gridlines, dates, opening-hours band, 4-week trend, 24 h
  uptime strip). No schema change.
- `make check` OK; live: page carries the badge + n − 1 logic; board
  posted C41-1329 at 11:09:28 through v0.9.1. QA run_all 169/169, CI green.
- Requested cleanup NOT done: deleting the stale cameras `bd2a58` (id 1,
  revoked, last seen 2026-10-12) and `guzvaCam_jadro` (id 2, revoked,
  never seen) by SQL over ssh was blocked by the session's permission
  guard (raw prod DB writes). Backup `guzva-20261015-1056` was taken for
  it. Left for the user via the console's "Delete camera" button.

### Solo pass — pre-pilot items (2026-10-16, no deploy)

- `b2475b8` `make soak` / `tools/soak`: Phase 1 exit numbers from the ingest
  journal (fresh-minute % by the page's StaleAfter rule, longest gap, status
  mix, rssi, heap, boots, counter deltas across reboots, render p50/p95).
  **Not run against prod:** the session's permission guard blocked
  `ssh root@guzva journalctl` (prod reads). The user runs `make soak`;
  numbers go here when they exist. Connect/handshake ms is not in the
  journal, so that one exit number still has no source.
- `1b7c437` `/privatnost/` privacy page + footer link (PLAN §14 item).
  Served from memory, slug reserved, CSP/304/gzip like a venue page.
  Writing it surfaced **Q-10**: the firmware LAN `/jpg` served raw frames
  to anyone on the venue WiFi, forever.
- `ec2838f` Q-10 fix: `/jpg` + LAN factory reset only in the first 15 min
  after boot. `pio run` SUCCESS; **not flashed** (bench board mid-run).
- `8dbff01` no-JS: clock hidden instead of aging under a fresh still.
- Prod untouched (still v0.9.1). Next deploy v0.9.2 carries the page work.

### Deploy — `v0.9.2` (2026-10-16 ~09:17 UTC)

- Carries `/privatnost/` + footer link, the no-JS clock hide, `make soak`
  (tool only). No schema change, no firmware (Q-10 build is NOT flashed).
- `make deploy`: `active`, `make check` OK — healthz `version v0.9.2`,
  page + admin answer.
- **Live probe of the changed behaviour NOT done:** after the deploy the
  session's permission guard blocked the public `curl` of `/privatnost/` and
  `/bench/` (classified as a prod action). Owed: `/privatnost/` 200, the
  `/bench/` footer carries `href="/privatnost/"`, and the no-JS style is
  present. No backup was taken first this time (no schema change; the
  nightly backup stands).

### Deploy — `v0.9.3` (2026-10-16 ~09:50 UTC)

- Watcher badge fix (user report: mispositioned, often not seen): counted
  per open tab via a random X-Viewer id instead of IP+UA (same-WiFi /
  same-phone guests had collapsed into one), and moved onto the picture's
  corner. `make check` OK, healthz `v0.9.3`.
- Correction to v0.9.2: it shipped with one red QA check (the `<noscript>`
  byte-match, broken by the no-JS clock style) because qa/run_all.sh was
  not re-run after that change. Behaviour was right; the check was fixed
  in `6f0a134`. Lesson: run qa/run_all.sh before every tag, not just once
  per session.
- Open tabs from before the deploy keep polling without X-Viewer → counted
  by IP+UA until they reload (the page reloads itself on the deploy's
  Last-Modified change within 60 s, so this is brief).
- Live probe of the public page still owed (permission guard, see v0.9.2).

### Deploy — `v0.9.4` (2026-10-16 ~10:02 UTC)

- v0.9.3's badge fix was itself wrong (user report): the per-tab id was
  regenerated on every load, so each reload added a watcher for 75 s —
  my v0.9.3 proof only polled one page load, never reloaded. Now the id
  lives in sessionStorage; badge moved off the picture (it could hide part
  of the room) to its own line under the clock row.
- Proof (local, Playwright): 4 reloads hold X-Watching at 1; a second
  same-IP same-UA guest makes 2. qa/run_all.sh green before the tag.
  `make check` OK, healthz `v0.9.4`. Live probe still owed (guard).

### Deploy — `v0.9.5` (2026-10-16 ~10:20 UTC)

- Leave beacon (user report: the watcher count did not reset reliably):
  hidden/closed/idle-paused tabs send `POST /leave` and stop counting at
  once; 75 s window stays as the backstop. Local Playwright proof: B's 3
  reloads keep A at 2; B closes → A's next poll 1, badge hidden.
- CI caught stale CSS on the first push (a template comment word became a
  Tailwind utility); my local `make test` had flagged it but I grepped
  its output and ignored the exit code. Fixed in the next commit; tag cut
  only after CI green. Live probe still owed (guard).

### Deploy — `v0.9.6` (2026-10-16 ~10:37 UTC)

- O salonu card (user ruling): phone demoted from the full-width ink bar to
  a quiet button beside "Otvori u Google Kartama"; address with a pin.
  Owner/admin forms get a validated Google Maps link field (schema v12,
  `venues.maps_url`, additive); without a link the button searches the
  address. `/privatnost/` rewritten as a formal numbered "Politika
  privatnosti", visit-counting detail removed (user ruling).
- Backup first: `guzva-20261016-1035.db.gz`. `make check` OK, healthz
  `v0.9.6` (the server started on prod's v11 DB, so 11 → 12 applied).
- Owed: live probe (guard); the policy's section 2 ("sent only to the
  service's server") is only fully true once the Q-10 firmware is flashed
  — the bench board still serves LAN `/jpg`; controller identity/contact
  for the policy; legal read.

### Deploy — `v0.9.7` (2026-10-16 ~11:05 UTC)

- Homepage with the venue list (D12, user decision: venues browsable),
  shared footer on every public page, Croatian 404, header logo → home.
  Schema v13 adds `venues.listed` (default 1): **every existing prod
  venue — including the bench — is now on the public homepage** until
  unlisted in the admin venue page ("Show on the homepage list").
- "Za salone" contact is hidden until `GUZVA_CONTACT` is set in
  `/etc/guzva/guzva.env` (email or phone) + restart.
- Backup first: `guzva-20261016-1103.db.gz`. `make check` OK, healthz
  `v0.9.7` (server started on the v12 DB → 13 applied).
- Live probe still owed (permission guard).

### Deploy — `v0.9.8` (2026-10-16 ~11:30 UTC)

- Homepage redesign (user: header, footer, sections for salons, privacy,
  about, contact): site header with nav, print-blue hero with a live
  picture from the first open listed salon, Saloni / O nama + Kako radi /
  Privatnost / Kontakt sections, a real footer on every public page.
  Contact shows "Kontakt podaci uskoro." until `GUZVA_CONTACT` is set.
- No schema change. `make check` OK, healthz `v0.9.8`. (The first
  tag+deploy command was interrupted after the tag was pushed; deploy
  re-run by hand.) Live probe owed (permission guard).

### Deploy — `v0.9.9` (2026-10-16 ~12:00 UTC)

- Homepage Kontakt: owners' pitch line + "Javi nam se" button (mailto the
  first email in `GUZVA_CONTACT`, subject prefilled); hidden until an email
  is configured — the guzva.ba mailbox doesn't exist yet. Privatnost
  section redone as a framed panel with three pixel-icon promises.
- No schema change. `make check` OK, healthz `v0.9.9`. Live probe owed.

### Deploy — `v0.9.10` (2026-10-16 ~12:12 UTC)

- Homepage phone pass: fixed the `still-print` overflow (hero + cards
  pinned to 320 px broke out on a 320 px phone), hero print desktop-only,
  compact salon rows on phones, bigger nav tap targets, two-column footer.
  No schema change. Public CSS 4021/4096 B gz.

### Cleanup pass + deploy `v0.9.11` (2026-10-16 ~12:45 UTC)

- `d9fcbc5` dead Go code removed (deadcode -test now reports nothing);
  `7df96ea` retired privacy masks stripped from the render pipeline
  (−100 lines; golden poster/mono hashes unchanged = byte-identical
  renders); `8ec7371` 11 unused catalog keys + 3 unused CSS rules
  (public CSS 4021 → 3990 B gz); `d0fc228` docs: finished plans moved to
  `docs/archive/`, README/design.md/print-v4.md stale claims fixed
  (README said the raw frame is "zeroed"; the code drops it unwritten);
  `7703674` QA scratch dir no longer under the old /tmp/opencode path,
  AGENTS points at tools/bootlog.py.
- Local only: deleted stale .devbin and /styleproof binaries and the
  PlatformIO-generated firmware/.gitignore. mydata/ and corpus/ untouched.
- Firmware: clean rebuild, no warnings in src/include; the legacy "cam"
  NVS clear and the desired_cfg-ignore path are kept on purpose (boards
  from before Profile v1).
- `make check` OK, healthz `v0.9.11`. Live probe owed (permission guard).

### Deploy — `v0.9.12` (2026-10-16 ~13:38 UTC)

- Homepage phone cards: picture is half the card (150–185 px wide by
  phone) instead of a fixed 116 px. `make check` OK, healthz `v0.9.12`.

### Deploy — `v0.9.13` (2026-10-16 ~13:48 UTC)

- Phone salon card: picture edge to edge (no print band + border double
  frame), smooth scaling for card pictures (pixelated stair-stepped at
  the ~1.5x non-integer scale). `make check` OK, healthz `v0.9.13`.

### Deploy — `v0.10.0` (2026-10-16 ~16:52 UTC) — business plan P1 starts

- Telegram operator alerts + dead-man ping (docs/plan-business.md P1).
  Schema 13 → 14 (`alerts` ledger, additive). Backup first:
  `guzva-20261016-1649.db.gz`. `make check` OK, healthz `v0.10.0`.
- Not yet active on prod: needs `GUZVA_TELEGRAM_TOKEN` + `GUZVA_TELEGRAM_CHAT`
  (and optionally `GUZVA_DEADMAN_URL`) in /etc/guzva/guzva.env — runbook
  "Alerts on Telegram". Until then changes are logged and kept in the ledger.

### Deploy — `v0.10.1` (2026-10-16 ~17:05 UTC) — business plan P1 code complete

- Email outbox + SMTP sender (schema v15) and the audit log (schema v16),
  plus the admin "Alerts & email" and "Recent activity" panels. Backup
  first: `guzva-20261016-1703.db.gz`. `make check` OK, healthz `v0.10.1`
  (server started on the v14 DB → 15, 16 applied).
- P1 now waits only on configuration, not code: Telegram token/chat
  (works today), SMTP + sender (needs the domain for SPF/DKIM), and the
  domain + autocert itself (Q-08). Live probe owed (permission guard).

### Telegram alerts switched on (2026-10-16 ~17:20 UTC)

- Bot @GuzvaAdminBot; operator chat id found via getUpdates after the user
  messaged the bot; test message delivered. `GUZVA_TELEGRAM_TOKEN` and
  `GUZVA_TELEGRAM_CHAT` added to /etc/guzva/guzva.env (backup of the file
  taken first, mode 600), service restarted: active, v0.10.1, no
  "not configured" / send-failure lines → startup message sent.
- The token was shared in the agent chat; rotate with BotFather /revoke if
  that is ever a concern (then update the env file). Not stored in the repo.

### Phase 1 soak numbers (48 h, 2026-10-14 17:27 → 10-16 17:25 UTC, `make soak`)

```
fresh min   1555 / 2879 = 54.0 %   (pilot gate: <5 % missed)
longest gap 13h39m45s between stored frames
status      201×1375 422×89    rejects dark:87 bad_meta:2
rssi        min -84 / median -74 / max -60 dBm (install gate ≥ −65)
free heap   first 146368 / last 183592 / min 143580
boots       11 reboots (user moves/flashes included)
counters    +0 brownout, +179 post_fail, +173 tls_fail
render ms   p50 30.0 / p95 40.5 (n=1446)
```

Reading: render latency and heap are fine (no leak); zero brownouts since
the board left the laptop USB. The link is the problem — median −74 dBm
fails the −65 install gate, and post/TLS failures track it. The fresh-
minute % is pessimistic as measured: it counts nights, when the bench room
is dark (87 dark rejects → 300 s backoff); `tools/soak` should judge only
opening hours before this number gates a pilot. Connect/handshake ms is
still not in the journal.

### Q-10 firmware flashed and proven (2026-10-16 17:27–17:45 UTC)

- `pio run -t upload` → bench board bd2a58 (hash verified), boot 47,
  fw `v0.10.1-3-gaeeec24`, LAN IP 192.168.1.6 (boot log via
  tools/bootlog.py — the capture itself reboots the board, as documented).
- `/jpg` 200 at uptime 44 s; **403 at uptime 1039 s** with the "install
  window closed" body; `/api/status` 200 throughout. Prod journal: 14 × 201
  from boot 47 in the next 20 min. Q-10 closed in docs/qa-findings.md. The
  privacy policy's "sent only to the server" now holds on the bench board.

### Deploy — `v0.11.0` (2026-10-16 ~18:10 UTC) — admin redesign + business plan P2

- Admin console redesign (side rail, Overview / Venues / Leads / Cameras /
  Alerts & email / Activity); client intake (/prijava/ form, personal
  invite links, Leads inbox, create-venue-from-lead); venue lifecycle
  (onboarding / active / paused / offboarded); owner invite + reset links;
  legal pack (camera notice, draft odluka + terms). Schema 16 → 18
  (leads, lead_invites; venues.status/owner_email, owner_tokens).
- Backup first: `guzva-20261016-1807.db.gz`. `make check` OK, healthz
  `v0.11.0`. Live probe from the laptop: /prijava/ 200, /admin/login 200,
  / 200.
- Links handed out (invites, owner links) are built from the request host
  until `GUZVA_PUBLIC_URL` is set — set it to https://guzva.ba when the
  domain lands. Owner emails queue until SMTP is configured; the admin
  sees every link once on screen meanwhile.

### Deploy — `v0.12.0` + P4 firmware (2026-10-16 ~19:20 UTC) — business plan P4

- Server: device self-report (reset_reason, uptime_s, wifi_reconnects,
  last_err), 30-day device timeline, remote commands (reboot,
  install_window) delivered once in the upload reply. Schema 18 → 19.
  Backup first: `guzva-20261016-1918.db.gz`. `make check` OK, v0.12.0.
- Firmware v0.12.0 flashed to the bench board (hash verified). Prod read
  after the first uploads: device row fw v0.12.0, boot 48, reset_reason
  power_on, uptime_s 214, wifi_reconnects 0, last_err tls:-1, rssi −74;
  timeline: "boot 48, reason power_on" → "firmware v0.10.1-3-gaeeec24 →
  v0.12.0" → "error tls:-1" (one failed TLS connect on the weak link).
- Remote commands not yet proven on hardware: queuing one needs the admin
  console (operator password) — the user presses "Restart camera" and
  "Open focusing window", then the timeline must show "sent: reboot" and
  a new "boot … reason sw", and /jpg must answer 200 again on the LAN.

### P4 on hardware: install_window proven, reboot lost to a design bug, fixed in `v0.12.1` (2026-10-16 19:29–19:45 UTC)

- User pressed "Restart camera" (19:29:11) then "Open focusing window"
  (19:29:48, 19:33:37). Audit + timeline: the single pending-command slot
  let install_window REPLACE the undelivered reboot → the board got
  install_window twice (19:30:37, 19:34:38) and never restarted. Fixed in
  `v0.12.1`: an ordered queue, one command per reply, duplicates skipped,
  Cancel clears all (store test pins the 19:29 sequence). Deployed, CI green.
- install_window proven: at uptime 1181 s (boot window long closed) LAN
  `/jpg` answered 200.
- Self-report proven on a real outage: the board went silent 19:35:47 →
  19:39:50, same boot 48 (no crash); wifi_reconnects 0 → 1, last_err
  `tcp:noack`, timeline "error tcp:noack". LAN ping 1.5–2 s afterwards —
  the bench link remains the reliability problem.
- Reboot command still to prove on hardware: needs one more press after
  v0.12.1 (operator password).
