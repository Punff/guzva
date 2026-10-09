#!/usr/bin/env bash
#
# provision.sh — idempotent provisioning of the guzva VPS (PLAN §8).
#
# Target: fresh Ubuntu 24.04, smallest shared-vCPU box (Hetzner is the default
# candidate — re-check pricing in Phase 1, PLAN §8).
# Safe to re-run: every step checks before it acts; running twice is a no-op.
#
# Usage (from the repo checkout on the VPS, as root):
#   sudo DOMAIN=guzva.example.com ./deploy/provision.sh
#
# Do BEFORE running:
#   1. DNS A record for $DOMAIN points at this box (autocert HTTP-01 needs
#      it on :80 — PLAN §8; the autocert wiring itself is pending).
#   2. SSH key installed and confirmed working (SSH keys only — PLAN §8).
#   3. Build the binary and copy it next to the repo: see step 3.
#
# v3.1: no Caddy (TLS terminates in the binary, PLAN §8) and no Litestream
# (nightly SQLite snapshot instead). Their steps below are log-only stubs
# until the autocert wiring and backup timer land — PLAN Phase 1.

set -euo pipefail

# --- Placeholders: replace before running ---------------------------------------
DOMAIN="${DOMAIN:-example.com}"                   # public name for autocert (§8)
# --------------------------------------------------------------------------------

APP="guzva"
DATA_DIR="/var/lib/guzva"
BIN="/usr/local/bin/guzva"
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
  log "  autocert cannot issue a cert for it. Re-run with DOMAIN=<real domain>."
fi

# --- 1. Base packages: UFW, unattended-upgrades, curl (no proxy — §8) ---------
log "installing base packages (ufw, unattended-upgrades, curl)"
apt-get update -qq
apt-get install -y -qq ufw unattended-upgrades curl

# --- 2. Dedicated system user + data dir (server contract; PLAN §2) ------------
# Data dir holds guzva.db + venues/; owned by the service user, 0750.
if ! id -u "$APP" >/dev/null 2>&1; then
  log "creating system user '$APP'"
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP"
fi
mkdir -p "$DATA_DIR/venues"
chown -R "$APP:$APP" "$DATA_DIR"
chmod 0750 "$DATA_DIR"

# --- 3. Static binary ----------------------------------------------------------
# Built with: CGO_ENABLED=0 go build -o dist/guzva ./cmd/guzva
# (CGO off + modernc.org/sqlite → one static binary, no Docker, PLAN §2.)
if [[ -f "$REPO_DIR/dist/guzva" ]]; then
  log "installing binary dist/guzva → $BIN"
  install -m 0755 -o root -g root "$REPO_DIR/dist/guzva" "$BIN"
else
  log "no dist/guzva found — build and copy it first:"
  log "  CGO_ENABLED=0 go build -o dist/guzva ./cmd/guzva"
  log "  scp dist/guzva root@<box>:/path/to/repo/dist/  && re-run this script"
  log "continuing: everything else is provisioned; the service starts once $BIN exists"
fi

# --- 4. TLS: in-binary autocert (PLAN §8) --------------------------------------
# v3.1 removed Caddy: the guzva binary does HTTP-01 on :80 + redirect and
# caches certs under data/certs (needs CAP_NET_BIND_SERVICE on the unit).
# Pending the autocert wiring — bench uses -tls-cert/-tls-key or plain http.
log "TLS: autocert is served by the binary itself (PLAN §8) — nothing to install for $DOMAIN"

# --- 5. systemd unit ----------------------------------------------------------
log "installing systemd unit guzva.service"
install -m 0644 -o root -g root "$REPO_DIR/deploy/guzva.service" \
  /etc/systemd/system/guzva.service
systemctl daemon-reload
systemctl enable guzva.service >/dev/null 2>&1 || true
if [[ -x "$BIN" ]]; then
  # Restart if already running, otherwise start. The <2 s gap is absorbed by
  # idempotent ingest (409 = duplicate) + device retry (PLAN §8).
  if systemctl is-active --quiet guzva.service; then
    systemctl restart guzva.service
  else
    systemctl start guzva.service
  fi
  log "guzva: $(systemctl is-active guzva.service)"
else
  log "skipping service start: $BIN not installed yet"
fi

# --- 6. Backups: nightly SQLite snapshot (PLAN §8) -----------------------------
# v3.1: Litestream is deferred (§9). The nightly `sqlite3 .backup` → gzip →
# Cloud Storage timer (backup.service + backup.timer) is not written yet —
# PLAN Phase 1. Restore drill mandatory before the pilot, then quarterly
# (log it in docs/ops-log.md, PLAN §8 risk 7: silent backup rot).
log "backups: nightly snapshot timer NOT installed yet (PLAN Phase 1)"

# --- 7. Firewall: deny incoming by default -------------------------------------
log "configuring UFW"
ufw allow OpenSSH     # before enable, so we cannot lock ourselves out
ufw allow 80/tcp      # HTTP → in-binary redirect to HTTPS (autocert HTTP-01)
ufw allow 443/tcp     # HTTPS
ufw default deny incoming
ufw --force enable
# Also enable the provider-side firewall (Hetzner Cloud firewall) — PLAN §8.

# --- 8. SSH keys only (PLAN §8: security) --------------------------------------
# Write a drop-in disabling password auth; reload only once a root key exists
# so a fresh box with password auth cannot be locked out mid-run.
SSHD_DROPIN="/etc/ssh/sshd_config.d/20-guzva-keys.conf"
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
log "  - enable the provider firewall (step 7 note)"
log "  - first restore drill → log it in docs/ops-log.md (PLAN §8)"
