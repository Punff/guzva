# guzva runbook — for whoever is on support (leads with power)

No secrets live in this file. Secrets live only in `/etc/guzva/guzva.env`
(root, 0600) on the VPS and in the installer's offline notes. Device tokens
are shown **once** at provision time and never stored recoverably.

## The 30-second map

- Public page: `https://34.139.58.143/bench/` (self-signed cert until
  autocert/domain lands — browsers warn, the device already trusts it)
- Admin: `https://34.139.58.143/admin/` · Health: `/healthz`
- VPS: `ssh root@guzva` (34.139.58.143) · service: `guzva.service`
  (the retired Python rig is deleted from the tree — git history keeps it)
- Data: `/var/lib/guzva` (`guzva.db` + `venues/`) · env+certs: `/etc/guzva/`
- Device: ESP32-CAM serial `bd2a58b` (gen b, rotated 12 Oct 2026),
  `http://guzva-bd2a58.local` (mDNS name follows the board, not the row),
  bench IP 192.168.1.6, serial 115200 on `/dev/ttyUSB0`

## "The feed is stale" (most common call)

1. STALE is honest, not broken: the page shows last-good + STALE whenever no
   frame arrived recently. Check age: `ls -l /var/lib/guzva/public/bench/`.
2. Check the journal — one line per device-visible response:
   `journalctl -u guzva -n 20 --no-pager | grep ingest`
   - `status=201` arriving → frames flow. The still file is replaced on
     every frame; index.html is rewritten only on a state flip (⚑2 — the
     time is burned into the picture, so the page itself need not change).
   - only `409` same `capture_id` → device retrying; usually clears next tick.
   - nothing at all → device offline or link down (see below).
   - `post_fail` climbing in successive 201 lines → transport trouble on the
     device side; `rssi` below −70 and `brownout` rising point at link/supply.
3. In a browser with JS, the page re-fetches only the still every 30 s and
   swaps it in place; without JS it reloads itself every 60 s. Either way,
   the device posts every `interval_s` (60 s) — a repeated photo between
   posts is a quiet tick, not a fault.

## Device offline (no 201s in the journal)

1. Power-cycle the board; on the serial console (`115200`) expect
   `[BOOT] … [WiFi] got IP … [TICK] … [HTTP] 201 ok`.
2. If WiFi never joins: install spec is RSSI ≥ −65. At −71 the link drops
   SYNs for seconds and connects burn the 12 s budget before succeeding —
   self-heals, but every cold boot costs ~a minute of STALE.
3. Brownout counter rising (`"brownout"` in the ingest line) → suspect the
   USB supply / cable, not the code. Spec: 470–1000 µF cap at the board.
4. Status LED (GPIO33, active LOW — on means attention):
   2 Hz blink = connecting · short flash = good POST · double-blink/1.2 s =
   401 revoked (identity kept, health-only beats) · 0.5 Hz pulse =
   camera-degraded · **off = healthy**. The white flash LED (GPIO4) follows
   `led_pwm` in desired_cfg (`0` = off).
5. Serial recipe: 115200 8N1; reset pulse is `dtr=False, rts=True→False`.
   LAN cage on port 80 (installer-facing): `/api/status`, `/jpg`, `/ping`,
   `/api/factory-reset` (caged — see firmware README before touching).

## Routine operations (all from the VPS unless noted)

```sh
# logs (one line per ingest outcome — the soak's data source)
journalctl -u guzva -f | grep ingest
# restart (gap <2 s, absorbed by idempotent ingest + device retry)
systemctl restart guzva
# show / change device config (takes effect on the device's next POST)
guzva cfg -serial bd2a58b -data /var/lib/guzva
guzva cfg -serial bd2a58b -data /var/lib/guzva -set led_pwm=0
# add a device (prints token ONCE — record it offline immediately)
guzva provision -slug <slug> -name <name> -serial <serial> -data /var/lib/guzva
# rotate the admin password (bcrypt; restart to pick up the env file)
guzva hashpw          # type pw on stdin, paste hash into /etc/guzva/guzva.env
systemctl restart guzva
```

`guzva cfg -set` accepts the 22 documented keys (`guzva cfg -serial X` with
no `-set` lists them); anything unknown or out of range is rejected before it
can reach a field unit. `ver` is server-managed — it bumps on every change and
the board echoes it back as `cfg_ver_applied` (amber on the admin board until
it does).

## Venue owners (the /owner/ panel)

- Each venue has its own password, set by you on its admin page
  (`/admin/venues/<id>`, ≥10 chars, hashed on save, never shown again).
  Hand it to the owner out-of-band; the owner signs in at `/owner/` with
  venue slug + password.
- Owners can change: name, style, timezone, opening hours. They see device
  health read-only (fresh/stale, last seen, config wanted/applied). They can
  never see tokens, change camera settings, revoke devices, or see other
  venues — enforced by the session's signed scope, not by hiding buttons.
- Resetting the password is the remote kill: the old one stops working at
  next login (there is no server-side session to revoke, same as admin).
- If an owner reports "Wrong venue or password" for a venue that never had
  a password set, that message is intentional — the login never reveals
  which venues exist or which have owner access.

## Deploy / rollback (Makefile is the only supported path)

```sh
make tag V=v0.4.0      # annotated tag first: VERSION = git describe --tags,
                       # and rollback needs a name to point back to
make deploy            # build → test → push release → restart → healthcheck
make rollback TAG=v0.4.0   # switch the `current` symlink back → restart
make backup            # nightly snapshot now; timer installs it
```

Never `scp` a binary by hand again — the cutover did that five times and it
is exactly the mess this file exists to prevent. Every deploy stamps
`main.version` from `git describe`; `curl -sk https://127.0.0.1/healthz`
shows what is running.

## Backup / restore

- Nightly: `guzva-backup.timer` snapshots SQLite (`sqlite3 .backup`),
  gzips with `data/ref/`, keeps 14 under `/var/backups/guzva/`. RPO ≈ 24 h
  is fine: the DB holds config only, frames are a disposable ring.
- **The drill is the backup:** `make drill` runs `tools/restore-drill.sh`
  on the VPS — gunzip, `integrity_check`, schema-version parity with the
  live DB, venue/device counts, and the shipped binary opening the restored
  file. Mandatory before the pilot, then quarterly; log every run in
  `docs/ops-log.md` (silent backup rot is a top risk, and until the first
  drill passes the snapshot is a hope, not a backup).
- Disaster order: new VM (`deploy/provision.sh`) → IP/DNS → latest nightly
  DB + refs → secrets (`/etc/guzva/guzva.env`) → verify → fleet recovers by
  itself (devices keep capturing through a VM outage; first tick back wins).

## When to escalate to a developer visit

- `brownout` rising with a known-good supply, or camera probe failures that
  survive a supply swap (hardware, not software).
- Repeated `422` with a new `reason` value (validation gate firing on real
  frames — needs render-pipeline eyes).
- Anything in `docs/ops-log.md` marked `ACTION:` — that file is the queue.
