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

# --- 1. Base packages: UFW, unattended-upgrades, curl, sqlite3 ---------------
# sqlite3 CLI is the backup path (guzva-backup.service uses .backup).
log "installing base packages (ufw, unattended-upgrades, curl, sqlite3)"
apt-get update -qq
apt-get install -y -qq ufw unattended-upgrades curl sqlite3

# --- 2. Dedicated system user + data dir (server contract; PLAN §2) ------------
# Data dir holds guzva.db + venues/; owned by the service user, 0750.
if ! id -u "$APP" >/dev/null 2>&1; then
  log "creating system user '$APP'"
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP"
fi
mkdir -p "$DATA_DIR/venues"
chown -R "$APP:$APP" "$DATA_DIR"
chmod 0750 "$DATA_DIR"

# --- 3. Releases layout (PLAN §8, owned by `make deploy`) --------------------
# Versioned binaries live in $REL_DIR/<tag>, `current` points at the live one.
# Provisioning never deploys a version itself — but on a fresh box with a
# dist/ build next to the repo it installs one `manual` release so the service
# can start; every later deploy goes through `make deploy` from a workstation.
REL_DIR="/srv/guzva/bin/releases"
CUR_LINK="/srv/guzva/bin/current"
mkdir -p "$REL_DIR"
if [[ ! -e "$CUR_LINK" && -f "$REPO_DIR/dist/guzva" ]]; then
  log "installing dist/guzva as the first (manual) release"
  install -m 0755 -o root -g root "$REPO_DIR/dist/guzva" "$REL_DIR/manual"
  ln -sfn "$REL_DIR/manual" "$CUR_LINK"
elif [[ ! -e "$CUR_LINK" ]]; then
  log "no release installed — deploy one before starting the service:"
  log "  make deploy   (from a workstation checkout; see Makefile)"
fi

# --- 3b. Secrets + bench TLS --------------------------------------------------
# /etc/guzva: env file (0600, never overwritten once it exists) + TLS pair.
ETC_DIR="/etc/guzva"
mkdir -p "$ETC_DIR"
chmod 0750 "$ETC_DIR"
chown root:guzva "$ETC_DIR"
if [[ ! -f "$ETC_DIR/guzva.env" ]]; then
  log "writing $ETC_DIR/guzva.env template — set GUZVA_ADMIN_HASH before use"
  COOKIE_SECRET="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  printf '# guzva secrets — root only (0600). Set the admin hash via:\n#   guzva hashpw   (type the password on stdin, paste the hash below)\nGUZVA_ADMIN_HASH=\nGUZVA_COOKIE_SECRET=%s\n' "$COOKIE_SECRET" > "$ETC_DIR/guzva.env"
  chmod 0600 "$ETC_DIR/guzva.env"
fi
# Bench path: reuse the rig's epoch self-signed cert so already-flashed boards
# keep validating with no re-flash. When autocert + domain land (§8), this
# copy goes away and -tls-cert/-tls-key leave the unit file.
if [[ ! -f "$ETC_DIR/tls.crt" && -f /etc/camtest/tls.crt ]]; then
  log "bench TLS: copying the rig's cert pair (boards already trust it)"
  install -m 0644 -o root -g root /etc/camtest/tls.crt "$ETC_DIR/tls.crt"
  install -m 0640 -o root -g guzva /etc/camtest/tls.key "$ETC_DIR/tls.key"
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
if [[ -x "$CUR_LINK" ]]; then
  # Restart if already running, otherwise start. The <2 s gap is absorbed by
  # idempotent ingest (409 = duplicate) + device retry (PLAN §8).
  if systemctl is-active --quiet guzva.service; then
    systemctl restart guzva.service
  else
    systemctl start guzva.service
  fi
  log "guzva: $(systemctl is-active guzva.service)"
else
  log "skipping service start: no release installed yet (make deploy)"
fi

# --- 6. Backups: nightly SQLite snapshot (PLAN §8) -----------------------------
# Litestream is deferred (§9). The timer below snapshots via the sqlite3 CLI
# (.backup — consistent against a live writer), gzips, keeps the last 14.
# Cloud upload lands with the domain work; until then snapshots live on the
# box and the restore drill (mandatory pre-pilot, then quarterly, logged in
# docs/ops-log.md) still proves them.
log "installing guzva-backup.service + timer (nightly 03:17)"
install -m 0644 -o root -g root "$REPO_DIR/deploy/guzva-backup.service" \
  /etc/systemd/system/guzva-backup.service
install -m 0644 -o root -g root "$REPO_DIR/deploy/guzva-backup.timer" \
  /etc/systemd/system/guzva-backup.timer
mkdir -p /var/backups/guzva
chown guzva:guzva /var/backups/guzva
systemctl daemon-reload
systemctl enable --now guzva-backup.timer >/dev/null 2>&1 || true

# --- 6b. Swap: 1 GB safety net on the shared-vCPU box (PLAN §8) ----------------
if [[ ! -f /swapfile ]]; then
  log "creating 1 GB swapfile"
  fallocate -l 1G /swapfile
  chmod 0600 /swapfile
  mkswap /swapfile >/dev/null
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

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
