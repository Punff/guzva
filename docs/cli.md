# guzva — CLI reference

One static binary is both the server and the ops tool (PLAN §2/§9.3: every
admin action also exists as a verb, so SSH is never the only door). Build:
`make build` → `dist/guzva`. On the VPS it lives at
`/srv/guzva/bin/current/guzva`.

General rules:

- `guzva` with no arguments prints help; an unknown command is an error
  (exit 2). The server never starts by accident — it is always an explicit
  `guzva serve` (that is exactly what the systemd unit runs).
- Exit codes: `0` ok, `1` runtime error, `2` usage error.
- Commands touching the database take `-data` and must point at the same
  directory the server was started with (`/var/lib/guzva` in production).
- Secrets are never accepted as arguments where stdin works instead.

## guzva serve

The whole service: device ingest (`POST /v1/ingest`), pre-rendered public
pages, the admin console, the owner panels (PLAN §2). Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:8080` | listen address (unit: `:443`) |
| `-data` | `/var/lib/guzva` | SQLite + stills ring + public pages |
| `-retain-hours` | `48` | render ring size (§6.3) |
| `-tls-cert` / `-tls-key` | — | PEM pair; both or neither. Enables TLS |
| `-admin-hash` | env `GUZVA_ADMIN_HASH` | bcrypt hash; empty = console locked |
| `-cookie-secret` | env `GUZVA_COOKIE_SECRET` | HMAC secret; empty = sessions reset per restart |

Env fallbacks exist for the two secrets only — they belong in
`/etc/guzva/guzva.env` (systemd `EnvironmentFile`), never in shell history.

## guzva provision

Creates a venue (or adds a device to an existing one) and prints the device
bearer token **exactly once**. The server stores only its sha256.

```
guzva provision -slug jadro -name "Jadro" [-style poster|mono] \
  [-tz Europe/Sarajevo] [-interval 60] [-quality 18] \
  [-hours "Mo-Fr 09:00-17:00;Sa 10:00-14:00"] \
  [-serial bd2a58] [-token <existing>] [-devices N] [-data /var/lib/guzva]
```

- `-slug`: lowercase letters/digits/dashes, ≤40 chars (validated; it becomes
  a directory under the public web root).
- `-name`: required for a new slug; on an existing slug the stored name is
  reported instead.
- `-style`: `poster` (Tisak, 4 tones) or `mono` (Šablon, 2 tones). There is
  no raw preset (PLAN §7).
- `-tz`: IANA name, validated.
- `-hours`: venue-opening spec; on an existing slug only when passed, so
  re-running to mint a device never clobbers the schedule.
- `-serial`: default `esp32-<rand3>`; use the board's efuse-MAC label.
- `-token`: import the token an already-flashed board carries (cutover, no
  re-flash). Mutually exclusive with `-devices`.
- `-devices`: mint N devices for the fakecam gate harness (§6.1).

## guzva cfg

Shows or changes a device's `desired_cfg` — the 22-key device plane of
`docs/proto-v1.md`. Takes effect on the device's next POST; every change
bumps `ver`, which the board echoes back as `cfg_ver_applied` (an amber
read-back on the fleet board until it does).

```
guzva cfg -serial bd2a58 -data /var/lib/guzva                 # show
guzva cfg -serial bd2a58 -set framesize=5,led_pwm=80          # change
```

Keys and ranges are the same table the admin form uses; out-of-range values
are rejected before they can reach a field unit. `guzva cfg` with no `-set`
lists every settable key.

## guzva hashpw

bcrypt-hashes a password for `GUZVA_ADMIN_HASH` (§6.5). Prefer stdin so the
password never lands in shell history:

```
guzva hashpw            # type it, Ctrl-D
echo 'secret' | guzva hashpw
```

Paste the hash into `/etc/guzva/guzva.env`, then `systemctl restart guzva`.

## guzva version

Prints the build version — the same `git describe` stamp `/healthz`
reports. Deploys verify against it (`make check`).

## guzva help

The usage block. `guzva <command> -h` prints that command's flags.
