# LAN bench rig — local end-to-end testing (→ VPS transfer inside)

The camera always POSTs `https://<INGEST_HOST>:<INGEST_PORT>/v1/ingest` with
**full certificate verification**. To test locally without a VPS, the bench rig
gives the PC a self-signed TLS endpoint. The code path is identical to
production — only three values differ, and §5 is the checklist for moving them.

## 1. Server: TLS listener

```bash
./tools/bench-cert.sh                 # → ./mydata/bench-{cert,key}.pem
                                       #   SAN: this PC's LAN IP, 127.0.0.1,
                                       #   localhost, imaliguzve-bench
                                       #   (prints the PEM to paste into the
                                       #    firmware — see §2)

./dist/imaliguzve serve -listen :8443 \
    -tls-cert mydata/bench-cert.pem \
    -tls-key  mydata/bench-key.pem \
    -data ./mydata
```

`:8443` (not loopback!) — the camera must reach the PC over the LAN.
Plain HTTP stays the default: without the `-tls-*` flags `serve` behaves
exactly as in production (loopback behind Caddy).

**Firewall:** the bench PC may drop inbound by default (`ufw`:
`DEFAULT_INPUT_POLICY=DROP`). Without this the camera times out silently:

```bash
sudo ufw allow 8443/tcp comment 'imaliguzve bench TLS'
sudo ufw delete allow 8443/tcp        # when the bench rig comes down
```

## 2. Firmware: `BENCH_TLS`

`firmware/include/wifiSettings.h` selects the whole endpoint at once:

```c
#define BENCH_TLS 1     // 1 = bench rig, 0 = production
```

| | `BENCH_TLS 1` (bench) | `BENCH_TLS 0` (production) |
|---|---|---|
| `INGEST_HOST` | the PC's LAN IP | VPS domain |
| `INGEST_PORT` | 8443 | 443 |
| `CA_ROOT_PEM` | self-signed bench cert (§1) | ISRG Root X1 (embedded) |

Certificate verification stays on either way (`INSECURE_TLS=0` is grep-gated —
never flip it; the bench cert is *trusted*, not ignored). Re-flash after
editing: `cd firmware && pio run -t upload`.

Regenerate + re-flash when the PC's LAN IP changes (DHCP).

## 3. Test harness

```bash
go run ./tools/fakecam -addr https://127.0.0.1:8443 \
    -cacert mydata/bench-cert.pem -token <tok> -fast 1 -dur 30s
```

## 4. Verify

```bash
curl -s --cacert mydata/bench-cert.pem https://127.0.0.1:8443/healthz
curl -s --cacert mydata/bench-cert.pem https://127.0.0.1:8443/v/<slug>
```

On the camera's serial console (115200):

```
[TLS ] handshake/connect … ms (reuse=0)
[HTTP] … -> 201 ok (id=…)
```

The browser flags the self-signed cert once — expected on the bench.

## 5. Transfer to the VPS

1. **Firmware:** `BENCH_TLS 0`, set `INGEST_HOST` to the domain, re-flash.
   (Port and CA come from that branch automatically; ISRG Root X1 is already
   embedded.)
2. **Server:** deploy per `deploy/provision.sh` (Caddy owns TLS) and run
   `serve -listen 127.0.0.1:8080 …` — **no** `-tls-*` flags in production.
3. **Firewall:** 443 open (Caddy), 80 for ACME; `serve` stays loopback-only.
4. Confirm on serial: same `201 ok` lines, now against the real certificate.
