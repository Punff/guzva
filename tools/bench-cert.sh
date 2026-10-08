#!/usr/bin/env bash
# Generate the self-signed LAN bench certificate (docs/runbook.md, "Bench TLS").
#
#   ./tools/bench-cert.sh [outdir]        # default ./mydata
#   IP=x.x.x.x ./tools/bench-cert.sh      # override auto-detected LAN IP
#
# SANs cover this machine's LAN IP (the camera reaches the server by IP),
# 127.0.0.1 + localhost (the fakecam harness on this box), and
# imaliguzve-bench.  Regenerate when the PC's LAN IP changes (DHCP) and
# re-flash the firmware with the new PEM (firmware/include/wifiSettings.h,
# BENCH_TLS=1).
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

openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
    -keyout "$out/bench-key.pem" -out "$out/bench-cert.pem" \
    -subj "/CN=imaliguzve-bench" \
    -addext "subjectAltName=IP:${ip},IP:127.0.0.1,DNS:localhost,DNS:imaliguzve-bench" \
    2>/dev/null

chmod 600 "$out/bench-key.pem"
echo "wrote $out/bench-cert.pem + $out/bench-key.pem (SAN includes IP:${ip}, IP:127.0.0.1)"
echo
echo "paste into firmware/include/wifiSettings.h (BENCH_TLS=1 CA_ROOT_PEM):"
echo "---------------------------------------------------------------------"
cat "$out/bench-cert.pem"
