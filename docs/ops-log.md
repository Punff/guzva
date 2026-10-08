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
- **TODO (mandatory, pre-pilot): first restore-drill with a timer.** Restore
  `imaliguzve.db` from Litestream on a scratch box, time the whole drill
  (runbook §6), and log the duration here **before the pilot starts**; then
  repeat quarterly (PLAN §8 / risk 5). Status: not yet run — no backup
  replication is live until `deploy/litestream.yml` placeholders
  (bucket/region/endpoint + `LITESTREAM_*` creds) are filled in.
