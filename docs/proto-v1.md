# Protocol v1 — `imaliguzve`

Single source of truth for the device↔server wire protocol (PLAN §4).
Read alongside `PLAN.md`. One endpoint carries everything.

Versioning: path prefix `/v1`; within a major only additive optional fields may
appear; both sides send/receive `X-Proto: v1`. When `/v2` appears, `/v1` is
dual-served for 6 months.

---

## `POST /v1/ingest`

Push-only: the device dials the server, the server never dials the device.
Transport is HTTPS in production (Caddy terminates TLS, proxies to the Go
binary on loopback).

### Request

`Content-Type: multipart/form-data; boundary=…`

| Part | Required | Content |
|------|----------|---------|
| `meta`  | yes | UTF-8 JSON, ≤ 16 KiB |
| `frame` | no  | one JPEG, ≤ 4 MiB. Absent = health-only beat (camera-degraded mode) |

Headers:

```
Authorization: Bearer <device token>   # static per-device token
X-Proto: v1
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
- `fw` — firmware semver string.
- `cfg_ver_applied` — the desired-config version the device currently has
  applied (read-back). Mismatch with `desired_cfg.ver` = visible amber state.
- `reason` — set only in degraded modes (e.g. `camera-degraded`, `401-hold`),
  empty otherwise.
- Unknown additive fields are ignored by the server.

### Responses

Status is the contract; the body is always the JSON object below (plus
`reason` on 422, `error` on 4xx transport errors).

| Code | Meaning |
|------|---------|
| `201` | stored (frame processed) **or** health-only beat accepted |
| `409` | duplicate `(venue_id, capture_id)` — same body shape as `201` (a retry must not re-process) |
| `422` | rejected, quarantined — `reason` ∈ `bad_meta`, `bad_capture_id`, `bad_magic`, `too_big`, `dims`, `truncated`, `dark`, `dead`, `busy`; no bytes are ever stored |
| `401` | unknown or revoked token — device keeps identity, drops to slow health-only backoff (60→600 s), amber LED until re-provisioned |
| `413` | body over limit (`meta` > 16 KiB or `frame` > 4 MiB) |
| `429` | rate-limited — `Retry-After: 10` header |
| `507` | disk degraded (<15 % free) — device retries on next tick, no special handling |

Success body — `201` and `409` carry exactly these fields (`server_time` is
stamped per reply, so it differs between a store and a later retry):

```json
{
  "desired_cfg": { "ver": 2, "interval_s": 60, "quality": 18 },
  "next_s": 60,
  "server_time": "2026-10-08T12:00:00Z"
}
```

- `desired_cfg` — device-plane config the venue should run; ver increments on
  every change. Device applies it and echoes the version in the next
  `cfg_ver_applied`.
- `next_s` — seconds the device should sleep before the next tick (server
  echoes its own interval; the device must not poll faster).
- `server_time` — canonical; the device clock is advisory only and must never
  be used for freshness.

422 body:

```json
{
  "reason": "dark",
  "desired_cfg": { "ver": 2, "interval_s": 60, "quality": 18 },
  "next_s": 60,
  "server_time": "2026-10-08T12:00:00Z"
}
```

Rules the server guarantees:

- Freshness is `received_at` (server clock), never the device clock.
- A failed frame (422) never replaces the last good render; the public page
  keeps showing last-good + STALE.
- Raw JPEG bytes exist only in RAM inside one `Process` call, are zeroed on
  return, and are never written to disk, logged, or served.

### `GET /healthz`

Unauthenticated liveness for the dead-man watcher and uptime monitors:
`200` `{"ok":true,"version":"…","uptime_s":123,"disk_free_pct":61}`.
No auth, no venue data.

---

## Device-side rules (summary)

1. Single in-flight request; 8 s connect / 25 s POST timeouts.
2. No frame queue: upload failure ⇒ drop frame, `post_fail++`, capture fresh on
   the next tick.
3. Chunked 4–8 KiB upload from a PSRAM copy, immediate `fb_return`.
4. Apply `desired_cfg` and report `cfg_ver_applied` on every POST.
5. Never `setInsecure()` in prod (grep-gated); NTP is never on the capture
   path.
