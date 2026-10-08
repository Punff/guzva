# Support runbook — imaliguzve

Audience: **non-developers** (venue support, the builder's future self).
Goal: restore a venue's stills without a developer on site.

Two rules before anything else:

1. **Power first.** Most "it's broken" reports on the bench were brownouts and
   camera probe failures from bad supplies / thin USB cables (PLAN §5 risk 3;
   PLAN §8: *"the support runbook leads with power"*). Always start at §1 —
   even if you suspect software.
2. **Never publish a failure.** A STALE badge with the last-good frame is the
   system working as designed (PLAN §1). Your job is freshness, not uptime of
   a photo that would be old anyway.

Conventions: `<domain>` = the real VPS domain; `<slug>` = the venue's page
slug (`https://<domain>/v/<slug>`). Detail lives in PLAN.md — this runbook
only sequences actions and points at the right section.

---

## 1. Power, adapter, USB cable (always start here)

Symptoms that mean power, not network or server:

- `brownout` counter climbing, device rebooting in a loop (PLAN §5 counters);
- `camera probe fail` / camera-degraded mode right after plug-in;
- page was fine until someone swapped the plug or used a laptop/phone port;
- bench fact (08 Oct 2026): **thin USB cable → brownout + camera probe fails.**

Actions, in order, one at a time:

1. Unplug the device, wait 10 s, plug it back in.
2. Use the install-spec supply: a quality 5 V adapter (≥1 A), short/thick
   cable — never a thin/long USB lead, never a hub, prefer wall power over a
   TV/PC USB port (PLAN §5 install spec).
3. If it was already installed with a marginal supply: the install spec calls
   for a 470–1000 µF capacitor across the supply — check it's there.
4. Wait one capture interval (30–300 s, default 60 s), then check §3.

If a power swap fixed it: **log it in `docs/ops-log.md`** — repeat brownouts
are the trigger to replace the supply, and recurring ones are risk data.

## 2. Check the server from the internet

From any machine (not the venue's network):

```
curl -s https://<domain>/healthz
```

Expected: `{"ok":true,"version":"…","uptime_s":…,"disk_free_pct":…}`
(proto-v1.md). Interpretation:

- **JSON with `"ok":true`** → server is fine; the problem is device or link → §3.
- **`disk_free_pct` < 15** → disk degraded: devices get `507`, pages go stale
  (PLAN §8 disk policy) → §4, free space.
- **Timeout / TLS error / no JSON** → server or Caddy down → §4.
- TLS expiry: check the padlock in a browser; Caddy renews automatically, a
  failed renewal usually means DNS no longer points at the box (PLAN §8).

## 3. Check the venue page

Open `https://<domain>/v/<slug>`:

- **Fresh badge** → server + device are both fine; if the report was about an
  old photo earlier, it has recovered — note the time and close.
- **STALE badge** → age matters (PLAN §6.6): stale > 3 min is the badge
  trigger; during venue opening hours, stale > 15 min is the alerting trigger.
  Freshness is server `received_at`, so the badge never lies with the device
  clock (PLAN §1).
- Page loads but `GET /v/<slug>/status` shows last-seen long ago → device or
  venue link is down → §5 (device) after a quick §2 re-check.

## 4. SSH checks (server side)

SSH keys only, e.g. `ssh root@<domain>` (PLAN §8). Run, in order:

```
systemctl status imaliguzve          # active? recent restarts?
journalctl -u imaliguzve -n 100 --no-pager
df -h /var/lib/imaliguzve            # keep 10% reserve (PLAN §8)
systemctl status caddy               # TLS/front end
curl -s http://127.0.0.1:8080/healthz  # bypasses Caddy — is it the proxy?
```

Reading the results:

- service `failed` → read the last journal lines; fix the cause, then
  `systemctl restart imaliguzve`. The <2 s restart gap is absorbed by
  idempotent ingest + device retry (PLAN §8), so restarting in working hours
  is safe.
- restart loop → journal will show the flag; do not loop it more than a few
  times, escalate (§7).
- `df` under 15% free → DEGRADED + `507`; under 5% → read-only + alert
  (PLAN §8). The 48 h ring prunes hourly; if it can't keep up, escalate.
- journal full of `401` from a device → its token was revoked or is unknown
  (proto-v1.md) → re-provision, §5.
- journal full of `422 … dark/dead` → camera problem at the venue, not the
  server → §5.

## 5. Device side (on the venue LAN)

Serial monitor over USB (PlatformIO project in `firmware/`, PLAN §5) gives
counters (`boot, brownout, join_fail, post_fail, tls_fail, capture_fail`) and
`reason`. Without a laptop, use the LED:

| LED pattern | Meaning | Action |
|---|---|---|
| Connecting (active blink) | joining Wi-Fi / TLS handshake in progress | wait one tick; weak link if it persists (RSSI gate −65 dBm, PLAN §11 risk 1) |
| OK (brief flash per tick) | frame accepted (201/409) | device healthy — if page still stale, look at §2–§4 |
| Amber, repeating | `401` revoked/unknown token; device keeps identity and beats slowly (60→600 s) | re-provision the device token (installer path until claim codes, PLAN §4) |
| Slow pulse | camera-degraded mode: health-only beats, no frames (PLAN §5) | camera/probe issue: check ribbon, supply, dark frames in quarantine (`journalctl` 422 reasons) |

Also check: Wi-Fi RSSI on the venue network (install gate: RSSI ≥ −65, ping
< 200 ms — PLAN §10), and that the venue's router hasn't changed credentials.

## 6. Restore from backup (server data loss)

Only when the DB is gone/corrupt — devices are NOT affected: they keep
capturing, and the next tick after restore succeeds (PLAN §8 disaster note).

Restore order (PLAN §8): **box → IP/DNS → DB (Litestream) → secrets → verify.**

1. Fresh box: re-run `deploy/provision.sh` with the same placeholders.
2. Stop the service: `systemctl stop imaliguzve`.
3. Restore the DB (config is `/etc/litestream.yml`, see `deploy/litestream.yml`):

   ```
   litestream restore -config /etc/litestream.yml \
     -o /var/lib/imaliguzve/imaliguzve.db /var/lib/imaliguzve/imaliguzve.db
   chown imaliguzve:imaliguzve /var/lib/imaliguzve/imaliguzve.db
   ```

4. Restore `ref/` from the same bucket (nightly copy) into
   `/var/lib/imaliguzve/venues/…/ref/`.
5. `systemctl start imaliguzve`, then verify `https://<domain>/healthz` and
   one venue page. The fleet recovers by itself (PLAN §8).
6. **Log the drill with a timer in `docs/ops-log.md`** — mandatory before the
   pilot, then quarterly (PLAN §8, risk 5).

## 7. Escalation

Escalate to the builder when: power swap + restart + fresh `/healthz` do not
bring the venue back within one business day, or anything shows a privacy
concern (raw frames, logs with bodies — PLAN §1/§11 risk 4).

Include, always:

- `curl -s https://<domain>/healthz` output (redact nothing — it's public data),
- venue slug + exact times with timezone,
- STALE age seen on `/v/<slug>`,
- last 100 journal lines: `journalctl -u imaliguzve -n 100 --no-pager`,
- LED pattern from §5 and, if available, the device counters.

---

## First response templates

**Venue owner — page shows STALE (power suspicion):**
> Thanks for the report — the page is showing the last good photo with a
> "stale" marker, so nothing is lost. This is most often the power adapter or
> USB cable. Could you unplug the camera, wait 10 seconds and plug it back
> in (wall adapter, not a TV/PC USB port)? It refreshes about once a minute;
> if it's still stale in 15 minutes I'll look at the connection side.

**Venue owner — confirmed server-side issue:**
> We've found the issue on our side and are working on it — the camera itself
> is fine and keeps capturing. The page will keep showing the last good photo
> with a "stale" marker until we're done; I'll confirm as soon as it's fresh
> again.

**Internal note (append to docs/ops-log.md):**
> `<date>` `<slug>` — symptom: `<stale age / 401 / probe fail>`; step reached
> in runbook: `<§n>`; cause: `<power | link | server | token>`; fix:
> `<action>`; recurrence: `<yes/no>`.
