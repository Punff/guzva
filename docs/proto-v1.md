# Protocol v1 — `guzva`

Single source of truth for the device↔server wire protocol (PLAN §4).
Read alongside `PLAN.md`. One endpoint carries everything.

**Proven end-to-end (09 Oct 2026):** firmware ↔ the reference test rig
(23/23 e2e tests, live `desired_cfg` apply with read-back; the rig was
retired in the W0 hygiene pass — git history keeps it). The Go server
implements exactly this.

Versioning: path prefix `/v1`; within a major only additive optional fields
may appear; both sides send/receive `X-Proto: v1` (on responses too). When
`/v2` appears, `/v1` is dual-served for 6 months.

Transport: HTTPS, HTTP/1.1, `Connection: keep-alive`. The device reuses one
TLS connection across ticks. In production the Go binary terminates TLS
itself (`autocert`, PLAN §8) — ingest is never behind a third-party
TLS-terminating proxy (PLAN §1).

---

## `POST /v1/ingest`

Push-only: the device dials the server, the server never dials the device.

### Request

`Content-Type: multipart/form-data; boundary=…`

| Part | Required | Content |
|------|----------|---------|
| `meta`  | yes | UTF-8 JSON, ≤ 16 KiB |
| `frame` | no  | one JPEG, ≤ 4 MiB. Absent = health-only beat (camera-degraded mode) |

Headers:

```
Authorization: Bearer <device token>   # static per-device token, sha256 at rest
X-Proto: v1
Connection: keep-alive
```

`meta` fields:

```json
{
  "capture_id": "C7-1284-9f2a",
  "fw": "1.0.0",
  "rssi": -72,
  "free_heap": 143216,
  "cfg_ver_applied": 2,
  "counters": {
    "boot": 7, "brownout": 0, "join_fail": 1,
    "post_fail": 3, "tls_fail": 0, "capture_fail": 0
  },
  "reason": "camera-degraded"
}
```

- `capture_id` — `C<boot>-<ctr>-<rand4>`: boot counter and capture counter are
  monotonic in NVS, `rand4` is 4 lowercase hex chars. Must match
  `^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$`. Uniqueness is scoped per venue
  (`UNIQUE(venue_id, capture_id)`), so it doubles as the idempotency key.
  Issued every tick, frames and health-only beats alike.
- `fw` — firmware semver string.
- `cfg_ver_applied` — the desired-config version the device currently has
  applied (read-back). Mismatch with `desired_cfg.ver` = visible amber state.
- `reason` — set only in degraded modes (`camera-degraded`, `401-hold`),
  empty otherwise.
- Unknown additive fields are ignored by the server.

### Responses

Status is the contract; the body is always the JSON object below (plus
`reason` on 422, `error` on 4xx transport errors).

| Code | Meaning |
|------|---------|
| `201` | stored (frame processed) **or** health-only beat accepted |
| `409` | duplicate `capture_id` — same body as `201` (a retry must not re-process; the frame is not stored twice) |
| `422` | rejected, quarantined — `reason` ∈ `bad_meta`, `bad_capture_id`, `bad_magic`, `truncated`, `dims`, `dark`, `dead`, `busy`, `too_big` (decode-stage); no bytes are ever stored; body still carries `desired_cfg` |
| `401` | unknown or revoked token — device keeps identity, drops to slow health-only backoff (60→600 s), amber LED until re-provisioned |
| `413` | body over limit (`meta` > 16 KiB or `frame` > 4 MiB) |
| `429` | rate-limited — `Retry-After: <seconds>` header |
| `507` | disk degraded (<15 % free) — device retries on next tick, honoring `Retry-After` when sent (as on 429) |

`bad_meta`, `bad_capture_id`, `bad_magic`, `truncated` are pinned by the
ingest tests today; `dims`, `dark`, `dead`, `busy` come from the Go render
validation gate (PLAN §7). (`no_body`/`empty_frame` once appeared in drafts;
the server treats a missing meta part as `bad_meta` and an empty frame as a
health beat, so those reasons were never emitted.)

Success body — `201` and `409` carry exactly these fields (`server_time` is
stamped per reply, so it differs between a store and a later retry):

```json
{
  "desired_cfg": {"ver": 7, "interval_s": 45, "quality": 12},
  "next_s": 60,
  "server_time": "2026-10-09T14:00:00Z"
}
```

- `desired_cfg` — device-plane config; `ver` increments on every change.
  Device applies it and echoes the version in the next `cfg_ver_applied`.
- `next_s` — seconds the device should sleep before the next tick (server
  echoes its own cadence; the device must not poll faster).
- `server_time` — canonical; the device clock is advisory only and must never
  be used for freshness.

422 body:

```json
{
  "reason": "dark",
  "desired_cfg": {"ver": 7, "interval_s": 45, "quality": 12},
  "next_s": 60,
  "server_time": "2026-10-09T14:00:00Z"
}
```

Rules the server guarantees:

- Freshness is `received_at` (server clock), never the device clock.
- A failed frame (422) never replaces the last good render; the public page
  keeps showing last-good + STALE.
- Raw JPEG bytes exist only in RAM inside one `Process` call, are zeroed on
  return, and are never written to disk, logged, or served.

### `desired_cfg` keys (device plane)

Apply-if-found: any subset may appear. The device applies what it recognizes,
clamps to range, ignores unknown keys (so the admin board can add fields only
after a firmware release knows them). Every field change bumps nothing by
itself — only the server bumps `ver`. Persisted to NVS on change.

| Key | Type | Range | Effect (OV2640 unless noted) |
|-----|------|-------|------------------------------|
| `ver` | uint | — | config version → echoed as `cfg_ver_applied` |
| `interval_s` | uint | 30–300 | capture cadence (device also stores in NVS `cam/interval`) |
| `quality` | uint | 4–63 | JPEG quality, lower = better |
| `framesize` | uint | 0–10 | FRAMESIZE_QVGA…UXGA (OV2640 max) |
| `brightness` | int | −2–2 | |
| `contrast` | int | −2–2 | |
| `saturation` | int | −2–2 | |
| `sharpness` | int | −2–2 | NULL-guarded in the driver |
| `denoise` | uint | 0/1 | |
| `special_fx` | uint | 0–6 | 0 normal … 6 sepia |
| `wb_mode` | uint | 0–4 | 0 auto … 4 home |
| `awb` | uint | 0/1 | white balance enable |
| `aec` | uint | 0/1 | exposure control enable |
| `agc` | uint | 0/1 | gain control enable |
| `ae_level` | int | −2–2 | |
| `agc_gain` | uint | 0–30 | |
| `hmirror` | uint | 0/1 | |
| `vflip` | uint | 0/1 | |
| `dcw` | uint | 0/1 | downsize |
| `lenc` | uint | 0/1 | lens correction |
| `colorbar` | uint | 0/1 | test pattern |
| `led_pwm` | uint | 0–255 | flash LED (device-side, not the sensor) |

Out-of-band, not part of the wire protocol: the device's own LAN cage
on port 80 (`/api/status`, `/jpg`, `/ping`, `/api/factory-reset`) —
PLAN §5, installer-facing. (The bench rig's `GET /status.json`,
`/latest.jpg`, `/ring/*.jpg`, `POST /upload` helpers retired with it
in W0.)

### `GET /healthz`

Unauthenticated liveness for the dead-man watcher and uptime monitors:
`200` `{"ok":true,"version":"…","uptime_s":123,"disk_free_pct":61}`.
No auth, no venue data.

---

## Device-side rules (summary)

1. Single in-flight request; 12 s connect/handshake, 25 s POST timeouts;
   one failure-only 6 s TCP probe to classify a failed connect.
2. Reuse one keep-alive TLS connection across ticks (idle window < 45 s);
   re-arm `setHandshakeTimeout()` before every connect.
3. No frame queue: upload failure ⇒ drop frame, `post_fail++`, capture fresh
   on the next tick. Transport retries (3/tick, 6 s gaps) before dropping.
4. Chunked 4–8 KiB upload from a PSRAM copy, immediate `fb_return`.
5. Apply `desired_cfg` (clamp + persist + apply) and report
   `cfg_ver_applied` on every POST.
6. Never `setInsecure()` in prod (grep-gated); NTP is never on the capture
   path — `capture_id` and the server clock carry time.
