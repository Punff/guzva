#!/usr/bin/env bash
# Generate the self-signed LAN bench certificate (docs/runbook.md, "Bench TLS").
#
#   ./tools/bench-cert.sh [outdir]        # default ./mydata
#   IP=x.x.x.x ./tools/bench-cert.sh      # override auto-detected LAN IP
#
# SANs cover this machine's LAN IP (the camera reaches the server by IP),
# 127.0.0.1 + localhost (the fakecam harness on this box), and
# guzva-bench.
#
# VALIDITY SPANS EPOCH ON PURPOSE: the camera has no RTC and no NTP
# (PLAN §5 — "capture never waits for NTP"), so its clock reads
# seconds-since-boot ≈ 1970.  A normal cert (notBefore = issue date)
# fails X.509 date verification there ("certificate is not yet valid",
# mbedtls -9984).  notBefore 1970 … notAfter 2099 makes the bench cert
# valid for any device clock in that window.  Production certs (Caddy /
# Let's Encrypt, notBefore ≈ issue date) hit the same wall — see
# docs/ops-log.md bring-up entry for the VPS-phase time story.
#
# THE IP GOES IN SAN TWICE ON PURPOSE: mbedtls 2.26 (arduino-esp32 2.x,
# IDF 4.4) matches the TLS hostname ONLY against dNSName entries — its
# x509_crt_check_san() explicitly ignores iPAddress ("We may handle
# other types here later") — while Go/OpenSSL verify an IP host against
# the iPAddress SAN only.  The camera connects by IP, so the IP must be
# present as BOTH IP:… (curl/Go) and DNS:… (mbedtls string compare) or
# the camera rejects with CN_MISMATCH / "bad certificate".
#
# Regenerate when the PC's LAN IP changes (DHCP), and re-flash the
# firmware with the new PEM (firmware/include/wifiSettings.h, BENCH_TLS=1).
#
# Transfer to the VPS: set BENCH_TLS 0 and INGEST_HOST/PORT back to the
# domain/443 — production verifies against the real CA, not this cert.
set -euo pipefail

out="${1:-./mydata}"
mkdir -p "$out"

ip="${IP:-$(ip -4 -o addr show scope global | awk '{sub(/\/.*/, "", $4); print $4; exit}')}"
if [ -z "$ip" ]; then
    echo "bench-cert: no global IPv4 found; set IP=... explicitly" >&2
    exit 1
fi

openssl req -x509 -newkey rsa:2048 -sha256 -nodes \
    -not_before 19700101000000Z \
    -not_after 20991231235959Z \
    -keyout "$out/bench-key.pem" -out "$out/bench-cert.pem" \
    -subj "/CN=guzva-bench" \
    -addext "subjectAltName=IP:${ip},DNS:${ip},IP:127.0.0.1,DNS:127.0.0.1,DNS:localhost,DNS:guzva-bench" \
    2>/dev/null

chmod 600 "$out/bench-key.pem"
echo "wrote $out/bench-cert.pem + $out/bench-key.pem (SAN includes IP:${ip}, IP:127.0.0.1)"
echo
echo "paste into firmware/include/wifiSettings.h (BENCH_TLS=1 CA_ROOT_PEM):"
echo "---------------------------------------------------------------------"
cat "$out/bench-cert.pem"
