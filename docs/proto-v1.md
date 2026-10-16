# Protocol v1 — `guzva`

Single source of truth for the device↔server wire protocol (PLAN §4).
Read alongside `PLAN.md`. One endpoint carries everything.

**Proven end-to-end (09 Oct 2026):** firmware ↔ the reference test rig
(23/23 e2e tests; the rig was
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
  "profile": "p1",
  "counters": {
    "boot": 7, "brownout": 0, "join_fail": 1,
    "post_fail": 3, "tls_fail": 0, "capture_fail": 0
  },
  "reason": "camera-degraded",
  "reset_reason": "brownout",
  "uptime_s": 3721,
  "wifi_reconnects": 2,
  "last_err": "tls:-0x7280"
}
```

- `capture_id` — `C<boot>-<ctr>-<rand4>`: boot counter and capture counter are
  monotonic in NVS, `rand4` is 4 lowercase hex chars. Must match
  `^C[0-9]{1,10}-[0-9]{1,10}-[0-9a-f]{4}$`. Uniqueness is scoped per venue
  (`UNIQUE(venue_id, capture_id)`), so it doubles as the idempotency key.
  Issued every tick, frames and health-only beats alike.
- `fw` — firmware semver string.
- `profile` — the frozen camera profile the firmware was built with
  (print v4, docs/print-v4.md §3). Firmware before Profile v1 sends
  `cfg_ver_applied` instead; the server ignores it.
- `reason` — set only in degraded modes (`camera-degraded`, `401-hold`),
  empty otherwise.
- Self-report (additive, firmware from 16 Oct 2026; business plan P4) —
  `reset_reason`: why the board last booted (`power_on`, `sw`, `panic`,
  `int_wdt`, `task_wdt`, `wdt`, `brownout`, `deepsleep`, `ext`, `unknown`);
  `uptime_s`: seconds since boot; `wifi_reconnects`: since boot;
  `last_err`: the most recent error code since boot (`tls:<code>`,
  `post:<status|timeout>`, `cam:init`, `cam:capture`), empty when none. The
  server keeps the latest values per device and writes changes (reboot +
  reason, brownout, firmware change, new error) to a 30-day timeline. A beat
  without these fields leaves the stored values alone.
- Unknown additive fields are ignored by the server.

### Responses

Status is the contract; the body is always the JSON object below (plus
`reason` on 422, `error` on 4xx transport errors).

| Code | Meaning |
|------|---------|
| `201` | stored (frame processed) **or** health-only beat accepted |
| `409` | duplicate `capture_id` — same body as `201` (a retry must not re-process; the frame is not stored twice) |
| `422` | rejected, quarantined — `reason` ∈ `bad_meta`, `bad_capture_id`, `bad_magic`, `truncated`, `dims`, `dark`, `dead`, `busy`, `too_big` (decode-stage); no bytes are ever stored; body still carries `next_s` |
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
  "next_s": 60,
  "server_time": "2026-10-09T14:00:00Z"
}
```

- `next_s` — seconds the device should sleep before the next tick. A one-shot
  hint: the device never persists it and must not poll faster.
- There is no `desired_cfg` (removed in print v4: the camera profile is frozen
  in firmware). Older firmware applies it only if present, so its absence
  leaves those boards on their stored settings.
- `server_time` — canonical; the device clock is advisory only and must never
  be used for freshness.
- `cmd` (optional, additive; P4) — one operator command, sent in exactly one
  201/409/422 reply and then cleared on the server: `reboot` (restart after
  handling the reply) or `install_window` (reopen the 15-minute LAN `/jpg`
  window). The device executes a known value once and ignores anything else;
  older firmware ignores the field. The server never connects to the device —
  a command waits for the next upload.

422 body:

```json
{
  "reason": "dark",
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

Out-of-band, not part of the wire protocol: the device's own LAN cage
on port 80 (`/api/status`, `/jpg`, `/ping`, `/api/factory-reset`) —
PLAN §5, installer-facing; `/jpg` and the reset only in the 15 min install
window after boot (Q-10). (The bench rig's `GET /status.json`,
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
5. Camera settings are compile-time constants (Profile v1); report
   `profile` on every POST. Ignore any `desired_cfg` an old server sends.
6. Never `setInsecure()` in prod (grep-gated); NTP is never on the capture
   path — `capture_id` and the server clock carry time.
