# guzva — maintenance plan v1 (the remediation pass)

Written 11 Oct 2026, after a four-way specialist audit (firmware, Go server,
UI/UX + docs, build/DevOps). This file is the working plan for cleaning the
project up; PLAN.md stays the product plan (v3.2), and nothing here changes a
PLAN decision. Work is grouped into workstreams W0–W8; each item names its
evidence, its fix, and its acceptance check. Progress is recorded in §3.

**The audit's verdict, honestly:** the hard parts are sound — the protocol,
the Tisak pipeline, the deploy discipline, the test culture (95 test
functions, every past incident pinned by a regression test). The mess is
real but concentrated in four places:

1. **Hygiene** — secrets and a 4.2 MB binary are committed; there is no git
   remote, no tags, no CI of any kind; backups live only on the same VPS and
   a restore has never been drilled.
2. **A handful of live bugs** that survived because surfaces were never
   verified in a browser (the chip colours never compiled; the owner dash
   renders raw i18n keys).
3. **Inconsistency** — validation rules that exist in the admin console but
   not in the CLI; comments and docs that contradict shipped code; the same
   colour/shape logic written twice.
4. **Firmware debt** — no watchdog backstop, unpinned toolchain, two real
   bugs, and a provisioning story that is "recompile and reflash".

---

## 0. Findings that drove this plan (the audit, one page)

### Hygiene / security

| # | Finding | Evidence |
|---|---------|----------|
| H1 | `firmware/include/settings.h` is tracked and holds the live WiFi password and device token, against its own header warning | settings.h:4–5, 9, 67 |
| H2 | A 4.2 MB unstripped ELF (`styleproof`) is tracked at the repo root — the largest blob in history; no Makefile target builds it | `git ls-files` |
| H3 | No git remote (the repo has never left this machine — also the only copy of the code), zero tags, no `.github/`, no CI; PLAN §12's PR gate does not exist | `git remote -v` empty |
| H4 | Nightly backups land only on the same VPS; the mandated restore drill was never run; no script exists | runbook §Backup, `grep restore docs/ops-log.md` |
| H5 | `deploy/testrig/` (the retired Python rig, 735 lines) still sits in `deploy/` reading as deployable; `provision.sh` §3b still references the rig's certs | PLAN.md:20, provision.sh |
| H6 | `tools/bench-cert.sh` points at `firmware/include/wifiSettings.h` — a file that does not exist | bench-cert.sh tail |
| H7 | `web/tailwind.config.js` is vestigial (v4 CLI is flag-driven) and references a nonexistent `web/input.css` | tailwind.config.js:5 |
| H8 | Consoles ship with no CSP/security headers (only public pages have them); `/admin/static/` allows a directory listing; inline `style=` attributes are scattered through templates | grep CSP internal/admin |

### Live bugs

| # | Finding | Evidence |
|---|---------|----------|
| B1 | State-chip colour classes are assembled by concatenation (`text-{{.Cls}}`), which the Tailwind scanner cannot see — `text-fresh/late/stale/nosignal/waiting/closed/unknown` do not exist in either compiled stylesheet; every chip renders its silhouette in inherited ink. DESIGN.md §3.2 forbade exactly this pattern and predicted exactly this failure | parts.html:42, venue.html:60, grep in internal/webui/*.css |
| B2 | `owner.fresh/late/stale` are fetched but missing from the catalog — the owner dashboard's one-sentence summary renders the literal key | owner.go:153–159, i18n.go |
| B3 | Console favicon has an invalid 5-digit hex (`#3607F` for `#33607F`) — one wrong pixel in every console page | parts.html:6 |
| B4 | No-JS fallback broken: create-venue, mint-device, revoke, admin venue details and owner details forms carry `hx-post` with no `method`/`action` — DESIGN §8 promised every action works without JS | fleet.html:50/79/100/133, venue.html:20, owner_dash.html:24 |
| B5 | `guzva provision` does not validate `-slug` (an operator typo like `../etc` writes public pages outside the web root) or `-tz`; the admin console validates both — three forms, one rule, but the CLI is the fourth form and broke it | provision.go:37–59 vs admin.go:330, 351 |
| B6 | `guzva <unknown-word>` silently starts the server | main.go:46–61 |
| B7 | Parse-stage 422s (`bad_meta`, `bad_capture_id`) write only quarantine JSONL — no frames row, no beat update — while render-stage 422s do both; contradicts store.go's "always applied" comment, and the device never shows as last-seen | ingest.go:173–175 vs 264/277 |
| B8 | The pages state key omits the still filename/dims: a v1→v2 row flip without a restart deletes `latest.jpg` but does not rewrite `index.html`, so the poller 404s until the next state flip | pages.go:169–211 |
| B9 | Firmware `jsonStr` call passes `"reason":` without the value's opening quote — the 422/413 reason is always empty (masked by a `?` fallback) | main.cpp:264–275, 1111 |
| B10 | Owner dash duplicates the install-gate literal (`-65`) instead of the shared const | owner.go:175 vs admin.go:207 |

### Firmware gaps

| # | Finding | Evidence |
|---|---------|----------|
| F1 | No task watchdog anywhere; loopTask WDT is off by default — an unexpected stall hangs the device forever. All current blocking paths are deadline-bounded, but there is no backstop | main.cpp (no esp_task_wdt), core main.cpp |
| F2 | `platform = espressif32` and `esp32-camera` unpinned (builds by accident of install date); `-w` suppresses all compiler warnings | platformio.ini:8, 24, 34 |
| F3 | PSRAM alloc failure is counted as a capture failure — a PSRAM-less or exhausted board blunders into "camera-degraded" misdiagnosis with no DRAM fallback | main.cpp:1027, 1045–1051 |
| F4 | `TLS_HANDSHAKE_S = 12` and `TLS_CONNECT_MS = 12000` — the same budget written twice in two units | main.cpp:98–99 |
| F5 | `FW_VERSION` is a hardcoded `"1.0.0"` — every reflash claims 1.0.0 | main.cpp:64 |
| F6 | The NVS token read path exists but no write path does — settings.h's "installer can re-provision" is aspirational; provisioning is recompile+reflash | main.cpp:346, settings.h:65 |
| F7 | The spec's "hourly-max reboot" does not exist (the only restart is factory-reset); the OTA slot + coredump partition sit unused | grep ESP.restart |
| F8 | Retry-After is honored on 429 but not 5xx/507; response bodies >2 KB drain silently truncated | main.cpp:1115–1120, 606 |

### Docs drift

| # | Finding | Evidence |
|---|---------|----------|
| D1 | design-todo.md lists ⚑1/⚑2, ⚑5/⚑6, ⚑10 as "not done" and calls the still "a 640px JPEG" — all shipped per git log and ops-log | design-todo.md:38–55 vs ops-log.md:328–348 |
| D2 | No README at repo root; no dev-setup doc; no firmware README (the runbook references one); no CLI reference | `find -iname README*` |
| D3 | runbook stale: "page rewrites on each frame" (no), the 30 s reload math (superseded by ⚑2 + the JS poller), dead firmware-README pointer | runbook.md:24, 29–30, 48 |
| D4 | design.md drift: preset versions (poster@1 vs shipped @2), single mask stage vs the shipped double application, a `style-sample.png` that was never committed, CSS budget stated at 2.5 KB gz vs enforced 4 KB | design.md:393, 254, 964, §13 |
| D5 | Misc: static.go comments still say latest.jpg; web/public.css header says "≤ 8 KB minified" while the Makefile enforces gzipped; proto-v1.md omits `too_big`; auth.go/sched.go/prune.go header comments describe superseded phases | static.go:5, 52; auth.go:5; sched.go:1–5 |

---

## 1. Rules for this pass

- PLAN §1 contracts hold throughout: never publish a failure; raw never at
  rest; idempotent ingest; dogfood against real state.
- Every fix that can be pinned by a test gets one; a bug found twice in the
  same shape gets a test that makes the shape impossible.
- No new Go dependencies; stdlib + the three existing direct deps.
- One workstream, one commit series; each commit leaves the tree green.
- Console-visible changes get checked through the kit page and a rendered
  template test, because "never verified in a browser" is how B1–B3 survived.

---

## 2. Workstreams

### W0 — Hygiene and secrets (P0, first)

1. **Untrack `styleproof`** (H2): `git rm --cached`, add to `.gitignore`.
   The binary is rebuilt from tools/styleproof; dist/ is already ignored.
2. **Untrack `firmware/include/settings.h`** (H1): replace with
   `settings.example.h` (placeholders + the full comment contract), gitignore
   the real file, make the build fail with a clear message if it is missing.
   **Rotation ACTION:** the WiFi password and the device token in git history
   must be treated as burned. History scrub is *mandatory before the first
   push to any remote* (H3 makes this the last moment it is cheap). Until
   then, exposure is contained to this machine.
3. **Delete `deploy/testrig/`** (H5) and the rig-cert copy step in
   `provision.sh`; drop `web/tailwind.config.js` (H7); fix the bench-cert.sh
   pointer (H6).
4. `.gitignore`: add `styleproof`, `firmware/include/settings.h`.

*Acceptance:* `git ls-files` contains no binaries and no settings.h; a fresh
clone + `cp settings.example.h settings.h` builds the firmware; `make test`
green.

### W1 — The `guzva` CLI, polished (P0)

The binary is the ops tool (PLAN §9.3: "everything also exists as a CLI
verb"). Today it is a manual `switch` that silently serves on typos.

1. A real dispatcher in `cmd/guzva`: `serve` (default when no args, for the
   systemd unit), `provision`, `cfg`, `hashpw`, `version`, `help`; unknown
   subcommand → usage error, exit 2 (B6). `guzva version` prints what
   `/healthz` would.
2. Provision validation parity (B5): slug regex + tz check shared with the
   admin console (one validator, moved to where both can call it — the CLI
   keeps zero package deps on internal/admin; the regex + tz rule moves to
   `internal/store` or a tiny shared spot).
3. Usage text per subcommand; consistent exit codes (0 ok, 1 runtime, 2
   usage) — already true, now also tested.
4. Tests: dispatcher table, provision validation (slug/tz/style), cfg parse
   (exists) kept green.
5. `docs/cli.md` — the reference the runbook links to.

*Acceptance:* `guzva` with no args prints help (not the server banner);
`guzva frobnicate` exits 2; `guzva provision -slug ../etc` is refused;
`guzva version` runs.

### W2 — Surface bugs, the visible mess (P0)

1. **Chip colours** (B1): add explicit role-based state classes to
   `web/tokens.css` (`@layer utilities`): `.text-fresh/-late/-stale/-
   nosignal/-closed/-waiting/-unknown/-paused`, each one line mapping to its
   role var. Plain CSS, not generated utilities — the design system owns
   state colour, both entries inherit it, and the runtime-assembled class
   names resolve. Add all states to the kit gallery so the regression page
   actually regresses.
2. **i18n** (B2 + hard-coded copy): add `owner.fresh/late/stale` (hr + en);
   wire the owner dash's hard-coded strings through existing keys
   (`owner.save/saved/health.old`); wire the public page's alt text and
   `Radno vrijeme` through `alt.still/alt.nowframe/hours.title`; add a test
   that every key fetched by Go exists in every catalog (no raw keys ever
   render).
3. **Favicon hex** (B3): `%233607F` → `%2333607F`.
4. **No-JS fallback** (B4): every `hx-post` form gains real
   `method="post" action="…"` — htmx still intercepts; without JS the form
   POSTs and the existing 303 redirects do the rest.
5. **Console security headers** (H8): CSP for /admin + /owner (no
   `unsafe-inline` for scripts; styles via the existing hashed-attribute
   approach or refactored classes), X-Frame-Options DENY, nosniff,
   Referrer-Policy, Permissions-Policy; kill the `/admin/static/` directory
   listing; replace inline `style=` colour attributes with the new state
   classes (which B1 adds) so the CSP is clean.
6. **RSSI const** (B10): owner.go uses `InstallGateRSSI`.

*Acceptance:* served pages contain `text-fresh` etc. and the compiled CSS
defines them (grep both); owner dash shows the sentence, not the key; kit
page shows all states coloured; `curl -sI /admin/` shows the headers; a
tests pass pinning each.

### W3 — Server correctness and dead code (P1)

1. **422 parity** (B7): parse-stage 422s update the beat (the device talked
   and authenticated — liveness must show) and insert a quarantined row like
   render-stage ones; retry semantics documented in proto-v1.md, which also
   drops the `no_body`/`empty_frame` reasons the server never emits.
2. **Pages state key** (B8): include the still filename (and dims) so a
   file-kind flip forces one rewrite.
3. **sched**: inject a clock (like every other package), add tests.
4. **Dead code sweep**: `Metrics.Prometheus` (no /metrics route), auth
   `WithDevice/DeviceFrom` (zero callers), `sameDayMinusOne` (dead),
   `PresetNames` (test-only), the `test_pattern` case (not a proto key),
   unused i18n keys (wire or remove — wire happens in W2, remove the rest),
   duplicate `randHex`/`itoa`/clamp-helpers where cheap.
5. **Comment truth sweep** (D5): auth.go, sched.go, prune.go, ingest.go:78,
   static.go jpg→png, web/public.css budget note; unify proto reason
   constants with render's literals (one table, both sides).
6. `provision`'s "Next:" text stops telling the installer to edit
   settings.h (W0 makes it a copy-from-example step).

*Acceptance:* `make test` green including the new tests; a duplicate-meta
POST advances last_seen; gofmt/vet clean; grep finds no `/metrics` promise.

### W4 — Firmware stabilization (P1; needs a bench session to fully verify)

1. **Pin the toolchain** (F2): `espressif32==7.0.1`-class pin (platform
   `7.0.1`, `esp32-camera` at the installed driver version); drop `-w` and
   fix what surfaces (suppress only specific noisy warning classes if the
   arduino core floods).
2. **jsonStr reason fix** (B9): call with the value's opening quote.
3. **Watchdog backstop** (F1): subscribe loopTask to the task WDT with a
   timeout above the worst blocking envelope (~130 s tick → WDT at 150 s),
   feed once per `loop()`. A true stall reboots within one interval instead
   of hanging forever. Decision recorded: the spec's "hourly-max reboot" is
   *dropped as written* — the WDT is the mechanism that was meant; an
   unconditional uptime reboot would erase the honest-STALE record for no
   gain (F7).
4. **PSRAM fallback** (F3): on SPIRAM alloc failure fall back to
   `MALLOC_CAP_8BIT` before counting a capture failure.
5. **Constants** (F4): derive `TLS_CONNECT_MS` from `TLS_HANDSHAKE_S`.
6. **FW_VERSION** (F5): a small `extra_script` stamps `git describe` into a
   define at build time; serial console and meta `fw` report it.
7. **Retry-After on 5xx/507** (F8) + log drained-truncated bodies.
8. Serial buffer 32→64 B with an overflow warning (minor, same commit).

*Acceptance:* `pio run` clean without `-w` (or with a minimal suppression
list); watchdog armed (serial banner says so); a covered-up sensor sim
still recovers; reflash shows the real version in meta.

### W5 — CI/CD and the backup story (P1)

1. **Remote**: create a private remote (GitHub), push. *Prerequisite: W0's
   history decision.* The repo currently has zero off-site copies — this is
   also the code's only backup.
2. **CI** (`.github/workflows/ci.yml`): gofmt, vet, `go test -race ./...`,
   `make css-check` (proves committed CSS fresh), `go build` linux/amd64,
   firmware `pio run` (with toolchain cache). This is PLAN §12's PR gate,
   finally existing.
3. **Tags + releases**: `make tag` creates an annotated `vX.Y.Z` before
   `make deploy` (today VERSION falls back to a bare SHA because no tag
   exists); the runbook documents tag→deploy→ops-log.
4. **Restore drill** (H4): `tools/restore-drill.sh` — scripted, logged:
   snapshot → restore into a temp dir → open store → assert venue count →
   log the drill in ops-log.md. First drill run is a W5 exit item. Off-box
   copy: documented as the §14 trigger it already is (Cloud Storage), with
   the unit's keep-14 unchanged.
5. `provision.sh`: drop the dead rig-cert step (with W0).

*Acceptance:* a pushed commit goes green in CI; `git tag` shows vX.Y.Z;
ops-log contains a logged restore drill with a checksum-verified snapshot.

### W6 — Documentation that tells the truth (P1)

1. **README.md** at root: what this is (one paragraph), the architecture
   diagram pointer, quickstart (make test/build/deploy), repo map, the
   doc index. The front door PLAN.md never was.
2. **docs/dev-setup.md**: clone → toolchain → `make test` → tailwind
   download note → platformio → bench flow (bench-cert.sh) → first run.
3. **firmware/README.md**: flash, provision (settings.h copy), serial
   console commands, LED patterns, LAN cage, the settings.h contract.
4. **docs/cli.md**: every subcommand with flags and examples (W1).
5. **Truth fixes**: design-todo.md rewritten to match reality (⚑ statuses);
   runbook's three stale spots; design.md's four factual drifts (D4);
   web/public.css header; proto-v1.md reason list (with W3).
6. ops-log.md: one entry per workstream, lessons in the established voice.

*Acceptance:* a newcomer goes clone → README → dev-setup → `make test`
green without asking a question; `grep` finds no doc contradicting shipped
code from the D-list.

### W7 — UI/UX coherence program (P2, sequenced next — not this pass)

The bugs above are fixed in W2; the *program* below is the next focused
pass, in priority order, each with its design-doc anchor:

1. Fleet "needs attention" sort + 15 s auto-refresh (§8.1; ops-log:136).
2. Venue description/contact + language picker on the admin venue page
   (⚑9 shipped the column, no UI touches it; ops-log:138).
3. Paused state (design §3.2): venue flag + chip + alert suppression — the
   vendor-lifecycle "pause" (PLAN §10) made visible.
4. Hours editor a11y: labels for the ten time inputs (W2 if cheap, else here).
5. ~~Dark-mode manual toggle (§3.1)~~ — void as of v0.6.0: the dark theme
   was removed entirely (user decision, light-only); nothing to toggle.
6. Token page Copy button (§8.3); throttle message with seconds (§8.6).
7. QR poster generation (§10) — the biggest single item; needs a QR encoder
   (stdlib-only constraint says: a tiny vendored encoder or an offline
   generation step; decide then).
8. Install wizard / calibration candidates in-console (§8.4) — styleproof
   exists as the CLI substitute; the wizard is the polish.

### W8 — Image-filter research program (P2)

The question: which stylisation gives the best *visibility* (a guest can
read "how busy" at a glance) at maximum *privacy* (nothing recoverable).
The shipped answer is defensible — luminance-only, 160×120 area average,
[1 2 1]² blur, fixed per-venue thresholds (explicitly no auto-levels, so an
empty and a full room cannot be made to look the same), 3×3 mode filter,
burned-in HH:MM stamp (removed at v0.4.0 — the page carries the time
instead), ≤4 static masks applied twice, paletted PNG; no
biometrics ever (D9). Research items, sequenced:

1. **Threshold selection from real frames** — styleproof contact sheets
   against a bright, busy room (the bench's dark surface proves nothing
   about contrast behaviour; ops-log:346). Document a per-preset
   recommendation table in a new `docs/filter-notes.md`.
2. **Evaluation candidates** (each measured against: legibility of
   "busy" from 2 m on a phone, byte size, irreversibility):
   adaptive-per-venue (already in: bias/thresholds), ordered-dither for
   gradients (design rejects by default — revisit with evidence), 2-tone
   Šablon vs 4-tone Tisak on dark venues (invert + bias may already cover
   it), an 8-tone variant if 4 tones flatten busy rooms.
3. **Privacy proofs stay mechanical** — the existing invariant tests
   (no-raw-on-disk, EXIF-stripped, masks-exact-level-0, determinism) are
   the acceptance for any new preset; a new preset = one file + registry
   line + goldens (PLAN §7).
4. **Document the placement math** — the head ≤ 10 cells privacy rule with
   the d_min table belongs in the install docs (design §5.3), so the
   privacy argument travels with the camera, not with the code.

---

## 3. Progress log

| Date | Workstream | What happened |
|------|------------|---------------|
| 2026-10-11 | plan | Four audits (firmware, server, UI/docs, DevOps) → this plan. Baseline `make test` green at `c6f66b8`. |
| 2026-10-11 | W0 | `settings.h` untracked → `settings.example.h` template (secrets gitignored; rotation + pre-push history scrub recorded as ACTIONs), 4.2 MB `styleproof` ELF untracked, retired testrig + vestigial tailwind config deleted, bench-cert.sh pointer fixed. `a55bde0` |
| 2026-10-11 | W1 | Real dispatcher (help/version, unknown ≠ serve), provision validates slug+tz, `store.CreateVenue` enforces the slug rule for every writer, docs/cli.md. `0176eb6` |
| 2026-10-11 | W2 | State-chip classes ship as explicit CSS (B1) + webui test pins both sheets; owner-dash i18n keys + full owner-console catalog wiring (B2); console CSP + no dir listing + no-JS forms (B4, H8); favicon hex; pages state key + LMT fix (B8). `691cbd9` |
| 2026-10-11 | W4 | Firmware: task-WDT backstop, pinned toolchain, `-w` dropped, git-stamped FW_VERSION, PSRAM→DRAM fallback, jsonStr reason fix, Retry-After on 5xx. `pio run` SUCCESS, zero warnings. Hardware verification pending (bench ACTION). `1b1f235` |
| 2026-10-11 | docs | README.md (front door), dev-setup.md, firmware/README.md (runbook referenced one that didn't exist). `e5f5783` |
| 2026-10-12 | W3 | Parse-stage 422 parity (beat + quarantined row + 409 on retry), dead code out, render speaks proto reason constants, sched pure advance() + tests, comment/design-doc truth sweep. `0dfad83` |
| 2026-10-12 | W5 | CI workflow landed ready (no remote yet), restore drill scripted and proven both ways, `make tag`/`make drill`, runbook truth fixes. `907a6b2` |
| 2026-10-13 | plan | PLAN.md truth sweep → **v3.4**: Status block rewritten to shipped reality (rounds 1–4), §9.1/§9.2 describe the v0.6.0 page, §15 Phase 2 split shipped/remaining, §14 +5 rows (scrub, real frames, privacy route, no-JS clock, admin i18n). |

**Open after this pass** (ACTIONs, also in ops-log): run the first real
`make drill` on the VPS and log it; bench session to verify W4 on hardware
and rotate the burned secrets; create the remote **after** the history
scrub; W7 and W8 below.

**Closed 2026-10-12 (design pass 2, shipped as v0.3.1):** the W7 surface
rework landed early — public page composed as print+label (design-pass2 §1),
hours editor labeled (§2), print & privacy setup with a live mask preview
and tone ramp (§3 + the first W8 deliverable), device page regrouped (§4),
venue description/phone via migration v6 (§5), fleet attention sort (§6).
Remaining W7/W8: QR poster, paused state, dark toggle, server-side
calibration candidates, real-frame threshold tuning (docs/design-pass2.md).

**Closed 2026-10-12 (the ops session):** v0.3.0 deployed and healthchecked;
the device token rotated end-to-end (old git-history token revoked and
proven 401 on replay; bench device row is now `bd2a58b`); W4 verified on
hardware (boot banner `v0.3.0`, `wdt=150s`, steady 201s); **first restore
drill PASS** — which also fixed the drill itself (count parity with a live
DB is a warning; only impossible states fail). Still open: the WiFi
password rotation (needs the router) and the pre-push history scrub.

**Closed 2026-10-13 (user rounds 1–4, shipped v0.5.0 + v0.6.0):**
one-shift hours (parse-don't-migrate), schema-v7 `location`, owner
console fully catalog'd (orchestrator override for `owner.login.*`
while docs-i18n was unresponsive — wording review owed), week cards →
7-row table with state-keyed today row, admin alignment pass, the
tally-label logomark, light-only theme (dark removed — user decision;
W7 "dark toggle" item void), and the public page rebuilt to the user's
mockup (mounted print, status row, RADNO VRIJEME + info
cards, hatch footer — zero new i18n keys; the in-frame time badge
v0.6.0 briefly shipped was removed at v0.6.1 as a double clock).
PLAN.md evolved to
v3.4 for it. Still open from this list: **push is blocked** (burned
secrets in history, no remote), fakecam startup-race guard in the
runner, the `r7-today-row` proof filename was clobbered (feature
proven inside `r9-pub-m360.png`), and no privacy route for the
footer caption link.

---

## 4. Deferred, with triggers (extends PLAN §14)

| Deferred | Trigger |
|----------|---------|
| Git history scrub (filter-repo for settings.h + styleproof blobs) | Immediately before the first push to any remote (W5) — do it while history is local-only |
| Token rotation (device + admin secrets) | At the next physical bench session (needs a reflash to take effect); mandatory before a real venue |
| Firmware OTA | Phase 3 as planned (PLAN §5/§14) — the slot already exists; W4 adds nothing |
| Cloud (off-box) backups | PLAN §14 trigger stands; the restore drill (W5) is the prerequisite proof |
| Tag-triggered CI deploy | Deploys >weekly (PLAN §14) |
| The whole W7/W8 program | After W0–W6 land and the next bench soak is green |
