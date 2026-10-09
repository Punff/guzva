#!/usr/bin/env bash
# camtest installer — idempotent, run as root on the VPS.
#   scp deploy/testrig/* root@guzva:/root/camtest/ && ssh root@guzva '/root/camtest/setup.sh'
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PUBLIC_IP="${PUBLIC_IP:-34.139.58.143}"

echo "==> user + dirs"
id -u camtest >/dev/null 2>&1 || \
  useradd --system --home-dir /var/lib/camtest --shell /usr/sbin/nologin camtest
install -d -m0755 /opt/camtest
install -d -o camtest -g camtest -m0750 /var/lib/camtest /var/lib/camtest/ring

echo "==> files"
install -m0755 "$HERE/server.py"    /opt/camtest/server.py
install -m0644 "$HERE/index.html"   /opt/camtest/index.html
install -m0644 "$HERE/camtest.service" /etc/systemd/system/camtest.service

echo "==> TLS cert (self-signed, epoch-valid: ESP32 has no NTP clock yet)"
install -d -m0755 /etc/camtest
if [ ! -f /etc/camtest/tls.crt ]; then
  openssl req -x509 -newkey rsa:2048 -sha256 -nodes \
    -keyout /etc/camtest/tls.key -out /etc/camtest/tls.crt \
    -subj "/O=camtest-bench/CN=${PUBLIC_IP}" \
    -addext "subjectAltName=IP:${PUBLIC_IP},IP:127.0.0.1,DNS:${PUBLIC_IP},DNS:guzva,DNS:guzva.local" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" \
    -not_before 19700101000000Z -not_after 20991231235959Z
  echo "    new cert generated"
fi
chown root:camtest /etc/camtest/tls.key
chmod 640 /etc/camtest/tls.key
chmod 644 /etc/camtest/tls.crt

echo "==> device token"
if [ ! -f /etc/camtest/camtest.env ]; then
  umask 077
  printf 'CAMTEST_TOKEN=%s\nCAMTEST_INTERVAL_S=30\nCAMTEST_QUALITY=14\n' \
    "$(openssl rand -hex 16)" > /etc/camtest/camtest.env
  echo "    new token written to /etc/camtest/camtest.env"
fi
chmod 600 /etc/camtest/camtest.env

echo "==> service"
systemctl daemon-reload
systemctl enable --now camtest.service >/dev/null 2>&1 || true
systemctl restart camtest.service
sleep 1
systemctl --no-pager -l status camtest.service | head -5 || true

echo "==> local check"
curl -sk https://127.0.0.1/healthz && echo
echo "==> done. token:"
grep '^CAMTEST_TOKEN=' /etc/camtest/camtest.env
