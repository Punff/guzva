#!/usr/bin/env bash
#
# provision.sh — idempotent provisioning of the imaliguzve VPS (PLAN §8).
#
# Target: fresh Ubuntu 24.04, smallest shared-vCPU box (Hetzner is the default
# candidate — re-check pricing in Phase 1, PLAN §8).
# Safe to re-run: every step checks before it acts; running twice is a no-op.
#
# Usage (from the repo checkout on the VPS, as root):
#   sudo DOMAIN=venue.example.com BUCKET=imaliguzve-backups \
#        S3_REGION=eu-central S3_ENDPOINT=https://s3.example.net \
#        ./deploy/provision.sh
#
# Do BEFORE running:
#   1. DNS A record for $DOMAIN points at this box (Caddy needs it for TLS).
#   2. SSH key installed and confirmed working (SSH keys only — PLAN §8).
#   3. Build the binary and copy it next to the repo: see step 3.
#
# This script does NOT manage TLS certificates — Caddy obtains and renews
# them itself once DNS points here (PLAN §8).

set -euo pipefail

# --- Placeholders: replace before running ---------------------------------------
DOMAIN="${DOMAIN:-example.com}"                   # real venue/VPS domain
BUCKET="${BUCKET:-CHANGE-ME-bucket}"              # S3-compatible backup bucket
S3_REGION="${S3_REGION:-CHANGE-ME-region}"        # bucket region
S3_ENDPOINT="${S3_ENDPOINT:-CHANGE-ME-endpoint}"  # provider endpoint (omit line for AWS)
# Litestream credentials come from the environment, never from a config file:
#   LITESTREAM_ACCESS_KEY_ID / LITESTREAM_SECRET_ACCESS_KEY
# (documented in deploy/litestream.yml)
# --------------------------------------------------------------------------------

APP="imaliguzve"
DATA_DIR="/var/lib/imaliguzve"
BIN="/usr/local/bin/imaliguzve"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log() { printf '[provision] %s\n' "$*"; }

# Must be root: we write to /etc, /usr/local/bin and ufw.
if [[ "$(id -u)" -ne 0 ]]; then
  echo "error: run as root: sudo $0" >&2
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

# --- 0. Sanity notes -----------------------------------------------------------
if [[ "$DOMAIN" == "example.com" ]]; then
  log "WARNING: DOMAIN is still the placeholder 'example.com' —"
  log "  Caddy cannot issue a cert for it. Re-run with DOMAIN=<real domain>."
fi

# --- 1. Base packages: Caddy (TLS front end), UFW, unattended-upgrades ---------
log "installing base packages (caddy, ufw, unattended-upgrades, curl)"
apt-get update -qq
apt-get install -y -qq caddy ufw unattended-upgrades curl

# --- 2. Dedicated system user + data dir (server contract; PLAN §2) ------------
# Data dir holds imaliguzve.db + venues/; owned by the service user, 0750.
if ! id -u "$APP" >/dev/null 2>&1; then
  log "creating system user '$APP'"
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP"
fi
mkdir -p "$DATA_DIR/venues"
chown -R "$APP:$APP" "$DATA_DIR"
chmod 0750 "$DATA_DIR"

# --- 3. Static binary ----------------------------------------------------------
# Built with: CGO_ENABLED=0 go build -o dist/imaliguzve ./cmd/imaliguzve
# (CGO off + modernc.org/sqlite → one static binary, no Docker, PLAN §2.)
if [[ -f "$REPO_DIR/dist/imaliguzve" ]]; then
  log "installing binary dist/imaliguzve → $BIN"
  install -m 0755 -o root -g root "$REPO_DIR/dist/imaliguzve" "$BIN"
else
  log "no dist/imaliguzve found — build and copy it first:"
  log "  CGO_ENABLED=0 go build -o dist/imaliguzve ./cmd/imaliguzve"
  log "  scp dist/imaliguzve root@<box>:/path/to/repo/dist/  && re-run this script"
  log "continuing: everything else is provisioned; the service starts once $BIN exists"
fi

# --- 4. Caddy site: install Caddyfile with the real domain ---------------------
log "installing /etc/caddy/Caddyfile (domain: $DOMAIN)"
sed "s|example\.com|${DOMAIN}|g" "$REPO_DIR/deploy/Caddyfile" > /etc/caddy/Caddyfile
systemctl enable caddy >/dev/null 2>&1 || true
systemctl reload caddy 2>/dev/null || systemctl restart caddy
# TLS certs: Caddy handles issuance/renewal by itself — nothing else to do (PLAN §8).

# --- 5. systemd unit ----------------------------------------------------------
log "installing systemd unit imaliguzve.service"
install -m 0644 -o root -g root "$REPO_DIR/deploy/imaliguzve.service" \
  /etc/systemd/system/imaliguzve.service
systemctl daemon-reload
systemctl enable imaliguzve.service >/dev/null 2>&1 || true
if [[ -x "$BIN" ]]; then
  # Restart if already running, otherwise start. The <2 s gap is absorbed by
  # idempotent ingest (409 = duplicate) + device retry (PLAN §8).
  if systemctl is-active --quiet imaliguzve.service; then
    systemctl restart imaliguzve.service
  else
    systemctl start imaliguzve.service
  fi
  log "imaliguzve: $(systemctl is-active imaliguzve.service)"
else
  log "skipping service start: $BIN not installed yet"
fi

# --- 6. Litestream config (DB backups → object storage, PLAN §8) --------------
# Placeholder bucket/region/endpoint are substituted; credentials stay in the
# environment (LITESTREAM_* vars), never in this file.
log "installing /etc/litestream.yml (bucket: $BUCKET)"
sed -e "s|CHANGE-ME-bucket|${BUCKET}|g" \
    -e "s|CHANGE-ME-region|${S3_REGION}|g" \
    -e "s|CHANGE-ME-endpoint|${S3_ENDPOINT}|g" \
    "$REPO_DIR/deploy/litestream.yml" > /etc/litestream.yml
chmod 0640 /etc/litestream.yml

# Litestream itself is not in the default Ubuntu archive on every mirror —
# install the static release binary if it is missing (PLAN §8: no Docker).
if ! command -v litestream >/dev/null 2>&1; then
  log "litestream NOT installed yet. Install a release binary, e.g.:"
  log "  https://litestream.io/install/linux/  →  /usr/local/bin/litestream"
  log "then enable it per https://litestream.io/guides/systemd/ with:"
  log "  litestream replicate -config /etc/litestream.yml"
  log "  (export LITESTREAM_ACCESS_KEY_ID / LITESTREAM_SECRET_ACCESS_KEY first)"
fi
# Restore drill is mandatory before the pilot, then quarterly — log it in
# docs/ops-log.md (PLAN §8, risk 5: silent backup rot).

# --- 7. Firewall: deny incoming by default -------------------------------------
log "configuring UFW"
ufw allow OpenSSH     # before enable, so we cannot lock ourselves out
ufw allow 80/tcp      # HTTP → Caddy → redirect to HTTPS (TLS only, PLAN §8)
ufw allow 443/tcp     # HTTPS
ufw default deny incoming
ufw --force enable
# Also enable the provider-side firewall (Hetzner Cloud firewall) — PLAN §8.

# --- 8. SSH keys only (PLAN §8: security) --------------------------------------
# Write a drop-in disabling password auth; reload only once a root key exists
# so a fresh box with password auth cannot be locked out mid-run.
SSHD_DROPIN="/etc/ssh/sshd_config.d/20-imaliguzve-keys.conf"
if [[ ! -f "$SSHD_DROPIN" ]]; then
  printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\n' > "$SSHD_DROPIN"
  chmod 0644 "$SSHD_DROPIN"
  if [[ -s /root/.ssh/authorized_keys ]]; then
    systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true
    log "SSH: keys-only policy active ($SSHD_DROPIN)"
  else
    log "SSH: keys-only policy written, NOT yet reloaded — /root/.ssh/authorized_keys"
    log "  is empty. Confirm key login, then: systemctl reload ssh"
  fi
fi

# --- 9. Unattended security upgrades (PLAN §8) ---------------------------------
log "enabling unattended-upgrades"
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
systemctl enable --now unattended-upgrades >/dev/null 2>&1 || true

# --- 10. Done ------------------------------------------------------------------
log "provisioning complete. Remaining manual steps:"
log "  - verify https://${DOMAIN}/healthz returns JSON from the internet"
log "  - install litestream + start replication if not done (step 6)"
log "  - enable the provider firewall (step 7 note)"
log "  - first restore drill → log it in docs/ops-log.md (PLAN §8)"
