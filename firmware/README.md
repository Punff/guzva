# guzva firmware — ESP32-CAM

One job: capture a still on a timer, POST it to the ingest server over
TLS, apply server-driven config, and survive brownouts, WiFi loss and
camera death without a human (PLAN §5 — "dumb sensor, smart about
survival").

**Never does:** serve public traffic, MJPEG, motion detection, any
face/people handling (D9), open inbound ports beyond the installer cage.
There is no frame queue: a failed upload drops the frame and the next tick
captures fresh; the server shows an honest STALE gap.

## Build & flash

```sh
cd firmware
cp include/settings.example.h include/settings.h   # gitignored — fill it in
pio run                 # build (pins: espressif32 7.0.1, esp32-camera 2.0.4)
pio run -t upload       # flash; monitor: pio device monitor (115200 8N1)
```

`FW_VERSION` is stamped from `git describe` by `tools/fwversion.py` on
every build (into the generated, gitignored `include/fw_version.h`) and
rides every ingest as the meta `fw` field — the fleet board can tell
generations apart. "dev" means the build ran outside the repo.

## settings.h (the only secrets file)

Holds WiFi credentials, the ingest host, the device bearer token and the
CA that verifies the server. It is **gitignored** — copy from
`settings.example.h` (the bench CA is public material and ships there;
the private key never leaves the VPS). The token's runtime precedence is
NVS (`prov` namespace, key `token`) over the compiled-in fallback —
re-provisioning a board means `guzva provision -token …` on the server
plus an NVS write or reflash, never a new secret in git.

The bench cert is epoch-valid (1970→2099) because the board has no RTC and
no NTP on the capture path; the full story, including why the IP appears
in SAN twice, is in `../tools/bench-cert.sh`.

## Serial console (USB, 115200)

| Command | Effect |
|---------|--------|
| `status` | one JSON blob: identity, counters, config, link |
| `net` | probes LAN (installer PC :8080) and ingest reachability |
| `reset` | factory reset: clears `prov` + `cam` NVS, reboots (keeps WiFi — that is compiled in) |

The token is never printed — only its length.

## LED patterns (GPIO33, active LOW — *off* means healthy)

2 Hz blink = connecting · short flash = good POST · double-blink/1.2 s =
401 revoked (identity kept, health-only beats, 60→600 s backoff) ·
0.5 Hz pulse = camera-degraded (probe backoff 10→1800 s, auto-recovery on
a good frame) · off = idle/healthy. The white flash LED (GPIO4) is held
off.

## LAN cage (port 80 — installer-facing, unauthenticated by design)

`/api/status`, `/jpg` (rate-limited 1/3 s), `/ping`,
`/api/factory-reset?confirm=1` (anyone on the bench LAN can wipe — that is
the threat model: the cage exists for installing, not for the internet).

## Stability mechanisms

- **Watchdog backstop (W4):** the task WDT is reconfigured at boot
  (`deinit` → 150 s / panic → `add(loopTask)`) and fed once per `loop()`.
  Every blocking path has its own deadline (12 s connect, 25 s POST, 3
  attempts/tick); the WDT only fires on a stall those deadlines don't
  cover, and a fire reboots into an honest STALE gap instead of a silent
  hang. The serial banner prints `wdt=150s`.
- **Camera:** probe failures back off exponentially; 5 in a row enter
  degraded health-only mode; recovery is automatic. PSRAM alloc failure
  falls back to DRAM before it is counted as a capture failure (a
  PSRAM-less board is not a dead camera).
- **Power:** SDK brownout detector untouched; brownouts counted to NVS and
  reported in every meta. Install spec: 470–1000 µF cap at the board.
- **Link:** WiFi join backoff 10→60 s; the tick only runs while connected;
  keep-alive reuse under 45 s idle; TLS session tickets stay OFF
  server-side (they stall this core's mbedtls — ops-log).
- **Identity survives everything** except factory reset: 401 = slow
  health beats, never a wipe (§1 degradation contract).

## Camera profile (frozen — docs/print-v4.md §3)

Profile `p1`: QVGA 320×240, JPEG q14, auto exposure/gain/white balance,
`ae_level` 0, dcw + lens correction on, everything else at driver
defaults. There is no remote camera config: the server's render removes
the room's lighting itself, so nothing needs tuning per venue. The profile
id rides every POST (`profile`) and `/api/status`. Upside-down mount:
set `PROFILE_VFLIP` / `PROFILE_HMIRROR` in that board's `settings.h` (both
= 180°). A dark room (4 dark rejects in a row) slows
the cadence to 300 s until a frame passes.

**Install instead of tune:** focus the lens at the real viewing distance
using the LAN `/jpg` (an edge must look crisp at ~3 m), and mount so no
bright window fills the frame — auto exposure will crush everything else.

## OTA

Not implemented (Phase 3, PLAN §5). The partition table already reserves
app0/app1 + otadata; until pull-OTA ships, firmware updates are USB
reflashes, and the coredump partition is likewise reserved but unread.
