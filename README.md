# guzva

A camera answers one question: *ima li gužve?* — is it busy? An ESP32-CAM
at a venue captures a still every minute, pushes it over TLS to one Go
binary, and the server destroys the photograph and publishes a **stylised
320×240 print** — its gallery label carries the time, never the image.
Guests scan a QR code and see
whether the terrace is full — a four-tone silhouette, never a photo, never a
face, never stored raw.

```
[venue LAN]                                [one small VM]
ESP32-CAM ──HTTPS POST ingest──▶  guzva (Go, one static binary)
  capture every 60 s                   ├─ ingest    /v1/ingest      (push-only)
  frozen camera profile                ├─ public    homepage + pre-rendered venue pages
  survive anything                     ├─ admin     /admin/…        (super-admin)
                                       ├─ owner     /owner/…        (per-venue)
                                       └─ sched     30 s tick: pages, pruner
```

- **Privacy is the product shape:** the raw JPEG exists in server RAM for
  one render call and is then dropped, never written; only a paletted 4-tone PNG is ever written,
  served, or logged. No biometrics, ever (decision D9). Proven by tests and
  a disk scan in CI, not by policy.
- **Honesty over freshness:** a stale feed says STALE with the time of its
  last frame; a failure never replaces a good picture; a closed venue shows
  no picture at all.
- **One static binary, one SQLite file, no Docker, no framework.**

## Status

Pre-pilot. Protocol v1 with a frozen camera profile (print v4), the Tisak
render pipeline, the public site (homepage venue list, venue pages, privacy
policy), the admin and owner consoles and the deploy/rollback tooling are
live on a bench VPS with one real board. [PLAN.md](PLAN.md) is the product
plan and decision log; the open queue and pilot blockers are in
[AGENTS.md](AGENTS.md); findings live in [docs/qa-findings.md](docs/qa-findings.md).

## Quickstart

```sh
make test      # fmt + vet + all tests + design-system staleness gate
make build      # static linux/amd64 binary → dist/guzva
make css        # rebuild the embedded stylesheets (Tailwind CLI, auto-downloaded)
```

Firmware: [firmware/README.md](firmware/README.md) (PlatformIO; copy
`settings.example.h` → `settings.h` first — it is gitignored, it holds
device secrets).

Production deploy (the only supported path):

```sh
make deploy           # test → build → push release → restart → healthcheck
make rollback TAG=…   # flip the symlink back
```

## Repository map

| Path | What lives there |
|------|------------------|
| `cmd/guzva/` | the binary: `serve`, `provision`, `hashpw`, `version` ([docs/cli.md](docs/cli.md)) |
| `internal/proto/` | wire types — mirrors [docs/proto-v1.md](docs/proto-v1.md) |
| `internal/store/` | SQLite + embedded migrations — the only SQL in the tree |
| `internal/render/` | Tisak pipeline: decode → luma grid → privacy blur → lighting field → Weber contrast → tones → cleanup → outlines → PNG |
| `internal/frames/` | ingest: auth, idempotency, quarantine, ring files |
| `internal/pages/` | public site: homepage list, pre-rendered venue pages, privacy policy, 404 |
| `internal/admin/` | super-admin + owner consoles (htmx, signed-cookie sessions) |
| `internal/i18n/` | one message catalog: Croatian product, English fallback |
| `web/` | design tokens (Tailwind v4 source) → compiled into `internal/webui/` |
| `firmware/` | ESP32-CAM firmware (PlatformIO + arduino-esp32) |
| `tools/` | fakecam (load gate), styleproof (render lab), soak (Phase 1 numbers from the journal), bench-cert.sh, restore-drill.sh |
| `deploy/` | provision.sh, systemd units, backup timer |

Dependencies flow downward only; `internal/store` is the sole SQL speaker;
an interface exists only when a second implementation exists today.

## Documents

- [PLAN.md](PLAN.md) — the product plan: decisions, contracts, phases, deferrals
- [docs/print-v4.md](docs/print-v4.md) — the shipped render/profile plan (historical, annotated)
- [docs/qa-findings.md](docs/qa-findings.md) — numbered QA findings (Q-xx)
- [docs/proto-v1.md](docs/proto-v1.md) — the wire protocol (single source of truth)
- [docs/design.md](docs/design.md) — the design system ("Tisak") and all three surfaces
- [docs/cli.md](docs/cli.md) — every `guzva` subcommand
- [docs/runbook.md](docs/runbook.md) — operations, leads with power
- [docs/ops-log.md](docs/ops-log.md) — append-only operational history
- [docs/dev-setup.md](docs/dev-setup.md) — from a fresh clone to a running bench
- [docs/archive/](docs/archive/) — finished working plans (maintenance pass, design pass 2, design todo)

## Contracts that are not negotiable

From PLAN §1 — every change to this repo is measured against these:

1. **Freshness** is server-time only; the device clock is advisory.
2. **Idempotency:** a retried capture returns 409 with the same body as the 201.
3. **Never publish a failure** — keep the last good still, say STALE.
4. **Privacy:** raw frames live in RAM for one render call; at rest, in
   logs, and on any public route there are stylised renders only.
5. **No biometrics, ever.** No detection, no faces, no plates (D9).
6. **Push-only:** the server never dials devices (no SSRF surface).
7. **Dogfood real state:** every change runs against a migrated database
   and, when it touches the device or the pages, against the real board.

Two past incidents define the culture: a migration that would have failed
every restart after an upgrade, and a stylesheet that silently went stale
under a fixed URL. Both are pinned by regression tests; both lessons are
recorded in the ops log.
