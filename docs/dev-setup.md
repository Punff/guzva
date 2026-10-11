# guzva — dev setup

From a fresh clone to a running bench, without asking anyone anything.

## What you need

- Go ≥ 1.26 (the module pins `go 1.26.0`)
- make, gzip, curl
- PlatformIO (for firmware work only — `pip install platformio` or the VSCode extension)
- A serial adapter for the board (CH340-style, 5 V tolerant is not needed —
  3.3 V logic, but the board wants 5 V power)

## Server only (no hardware)

```sh
git clone <repo> && cd esp32-cam
make test     # fmt + vet + unit tests + css staleness gate — must be green
make build    # static binary → dist/guzva
```

The first `make test` (and any `make css`/`make build`) downloads the
Tailwind v4 CLI (~110 MB) into `tools/bin/` — gitignored, one-time. If you
are offline and only touching Go, `go test ./...` is equivalent minus the
CSS staleness check; `make css` refreshes the committed stylesheets in
`internal/webui/` after any template or token change — CI fails if you
forget.

### Run it locally

```sh
guzva_hash="$(go run ./cmd/guzva hashpw -password devonly)"
GUZVA_ADMIN_HASH="$guzva_hash" GUZVA_COOKIE_SECRET=devsecret \
  go run ./cmd/guzva serve -listen 127.0.0.1:8080 -data ./mydata
# admin console: http://127.0.0.1:8080/admin/   (password: devonly)
```

`mydata/` is gitignored. Everything else (public pages, owner panels)
needs a venue + device; the CLI reference is [docs/cli.md](cli.md):

```sh
go run ./cmd/guzva provision -data ./mydata -slug bench -name Bench -hours "Mo-Su 08:00-23:00"
# → prints a device token ONCE; the device page explains the rest
```

### Fake devices without hardware

`tools/fakecam` is the load/abuse harness (fast/slow/flappy/dup clients):

```sh
go run ./tools/fakecam -addr https://127.0.0.1:8080 -cacert mydata/bench-cert.pem \
  -token <token> -fast -dur 60s
```

## Firmware (bench board)

Read [../firmware/README.md](../firmware/README.md) — the short version:

```sh
cd firmware
cp include/settings.example.h include/settings.h   # gitignored; fill in secrets
pio run                        # build
pio run -t upload              # flash over USB (115200 console on /dev/ttyUSB0)
```

The bench TLS story (why the CA in settings.example.h spans 1970→2099) is
[../tools/bench-cert.sh](../tools/bench-cert.sh).

## Layout rules that keep the codebase sane

- Dependencies flow downward only (cmd → internal → nothing).
- SQL lives in `internal/store` and nowhere else.
- User-facing strings live in `internal/i18n` and nowhere else — a test
  scans every `Get`/`F` fetch, so a missing key fails CI, not a user's page.
- State chips are shape + colour + sentence; the colour classes are plain
  CSS in `web/tokens.css` (never assembled utilities — that failed once).
- Every bug fix lands with the test that pins it; the ops-log records the
  story. Two incidents define the culture: the missing `case 3:` migration,
  and the stale stylesheet — both in [ops-log.md](ops-log.md).

## Deploying

Only ever via the Makefile (see [../docs/runbook.md](runbook.md)):
`make deploy` from a workstation with SSH access to the VPS. Never scp a
binary by hand. Tag first: `make tag` (annotated tag → the release dir
name → rollback target).
