# Guzva — agent team (OpenChamber, 6 roles, all free models)

One repo, one binary, one SQLite file. Parallel agents work by **file ownership**:
only the owner edits its paths. Everyone reads everything. PLAN.md §1 contracts
hold for every change. Green gate: `make test` before any handoff.

## Roster

| # | Role | Model (free, cost 0) | Owns (only writer) | Workstreams |
|---|------|----------------------|--------------------|-------------|
| 0 | orchestrator — architect + merger | `nvidia/z-ai/glm-5.3` (variant thinking) | `PLAN.md`, `docs/maintenance.md` §3 log, `docs/ops-log.md`, tags/releases order | sequences W0–W8, resolves conflicts, runs `make tag/deploy`, owns history scrub decision |
| 1 | backend-go — ingest/store/render/pages/sched/auth | `nvidia/deepseek-ai/deepseek-v4.1-flash` | `cmd/guzva/`, `internal/{proto,store,render,frames,venue,pages,sched,auth,admin}`, `go.mod` | W1 CLI parity, W3 422 parity/state-key/sched-clock, privacy proofs (no-raw, idempotency 409, EXIF, zeroed RAM) |
| 2 | firmware-esp32 — device plane | `opencode/nemotron-3.5-lightning-free` (alt `nvidia/nvidia/nemotron-3.5-lightning-30b-a3b`) | `firmware/` | W4: pinned toolchain, WDT 150 s, PSRAM→DRAM fallback, jsonStr reason fix, Retry-After 5xx, git-stamped FW_VERSION; bench verify only on hardware |
| 3 | frontend-tisak — design system + consoles | `opencode/muse-spark-1.3-contributor-free` | `web/`, `internal/webui/`, `internal/admin/` templates, `docs/design.md`, `docs/ux.md` | W2 B1/B3/B4/H8 (state chips, favicon, no-JS forms, CSP), W7 polish (attention sort, hours labels, dark toggle, QR poster decision), WCAG AA, kit page |
| 4 | devops-qa — CI/gate/deploys/backups | `opencode/mimo-v2.6-flash-free` | `Makefile`, `Makefile.web.mk`, `.github/workflows/`, `deploy/`, `tools/{fakecam,bench-cert.sh,restore-drill.sh}` | W5: CI PR gate, `make tag/drill`, restore drill log, fakecam load gate (1 slow + 19 fast), TLS/disk guards |
| 5 | docs-i18n — truth + Croatian catalog | `opencode/ling-3.1-flash-free` (fallback `opencode/exo-free`) | `README.md`, `docs/{dev-setup,cli,runbook,proto-v1}.md`, `internal/i18n/`, public/owner copy | W2 B2 (owner.fresh/late/stale + key-exists test), W6 truth sweep, `hr` default + `en` fallback, `dd.mm.yyyy.` + 24 h + Europe/Sarajevo |
| 6 | guzva-qa — test harness + bug hunt | `nvidia/deepseek-ai/deepseek-v4.1-flash` | `qa/` (new); direct fixes ONLY in `web/` + `internal/webui/` (orchestrator-authorized while frontend idle); everything else report-only | ingest-reject matrix, pageCache 304 test, single-clock/state contracts, a11y + i18n coverage, incident-regression tests (4:3, agc_gain, dark loop) |

Spares (free, on call): `opencode/space-bunny-free`, `opencode/exo-free`,
`opencode/longcat-2.5-preview-free`, `nvidia/z-ai/glm-5.3-flash` (fast GLM for
routine orchestrator chores), `nvidia/meta/muse-glimmer-30b`, `nvidia/moonshotai/kimi-k3`.

Why these fits: GLM-5.3 = strongest free reasoner (already ran the 4-way audit,
holds whole PLAN in head) → orchestrator. DeepSeek Flash = precise code correctness
→ Go privacy invariants. Nemotron Lightning = deterministic, low-hallucination →
C++/memory/TLS budgets. Muse Spark = creative + UI → Tisak Tailwind/a11y.
MiMo Flash = fast agentic tool-runner → make/pio/drill loops. Ling Flash = light
thorough scanner → catalog/docs consistency.

## Unison protocol

1. All 6 sessions live in `/home/punff/Downloads/esp32-cam` (same OpenChamber
   location). See each other via session list. Titles start with `guzva-`.
2. Single-writer: never edit outside your Owns. Need a cross-cut? Ask
   orchestrator in `docs/ops-log.md`, it dispatches.
3. Every change: dogfood migrated DB + real board when it touches device/pages
   (PLAN §1). Privacy: raw JPEG in RAM one call only, stylised PNG at rest,
   no biometrics ever (D9), push-only server.
4. Handoff: `make test` green → append ops-log line → tell orchestrator.
   Orchestrator merges in workstream order W0→W6, one series per workstream.
5. No new Go deps (stdlib + 3 direct). Interface only with 2nd implementation.
6. Console-visible change → check via kit page + rendered-template test +
   OpenChamber screenshot (`.openchamber/screenshots/`).
7. Blocked? Write `BLOCKED: <what> — needs <who>` to ops-log, notify, stop.
   Never idle-edit another role's files.

## Kickoff order (from docs/maintenance.md §3)

W0 hygiene + W1 CLI + W2 surface bugs first (P0) → W3 server + W4 firmware (P1,
bench session for hardware verify + secret rotation) → W5 CI/drill (needs W0
history decision before first push) → W6 docs truth → W7/W8 sequenced next,
not this pass (QR poster, paused state, threshold tuning need real frames).

## Session titles (exact, for OpenChamber)

- `guzva-orchestrator`
- `guzva-backend-go`
- `guzva-firmware-esp32`
- `guzva-frontend-tisak`
- `guzva-devops-qa`
- `guzva-docs-i18n`
