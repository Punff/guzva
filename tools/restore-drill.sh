#!/usr/bin/env bash
# restore-drill.sh — prove the nightly backup is actually restorable
# (maintenance W5/H4; runbook §"Backup / restore" mandates a drill before
# the pilot, then quarterly, logged in docs/ops-log.md).
#
# Runs ON THE VPS (where the DB and the backups live), from the repo
# checkout, as root or the guzva user:
#
#   sudo ./tools/restore-drill.sh            # drill the newest nightly
#   sudo ./tools/restore-drill.sh /var/backups/guzva/guzva-2026-10-11.db.gz
#
# What a pass proves, in order:
#   1. the newest nightly snapshot exists and gunzips to a sane SQLite file
#   2. it passes integrity_check (not a truncated/partial copy)
#   3. the schema version matches the binary the fleet is running
#   4. the venue/device counts match the live DB (ring frames may differ —
#      they are disposable; the DB holds config)
#   5. a real Go binary can OPEN the restored file and read a venue
#
# A fail exits non-zero with the step that failed. Log every drill in
# docs/ops-log.md — an untested backup is a hope, not a backup.

set -euo pipefail

APP=guzva
# Overridable so the drill itself can be exercised anywhere (CI, a laptop);
# defaults are the production paths on the VPS.
DATA_DIR="${GUZVA_DATA_DIR:-/var/lib/guzva}"
BACKUP_DIR="${GUZVA_BACKUP_DIR:-/var/backups/guzva}"
BIN="${GUZVA_BIN:-/srv/guzva/bin/current/guzva}"
LIVE_DB="$DATA_DIR/guzva.db"
RESTORE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/guzva-drill.XXXXXX")

log() { printf '[drill] %s\n' "$*"; }
fail() { printf '[drill] FAIL: %s\n' "$*" >&2; exit 1; }
cleanup() { rm -rf "$RESTORE_DIR"; }
trap cleanup EXIT

# --- 1. pick the snapshot -----------------------------------------------------
SNAP="${1:-$(ls -1t "$BACKUP_DIR"/*.db.gz 2>/dev/null | head -1 || true)}"
[[ -n "$SNAP" && -f "$SNAP" ]] || fail "no snapshot found in $BACKUP_DIR (has the timer ever run?)"
log "snapshot: $SNAP"

RESTORED="$RESTORE_DIR/guzva.db"
gunzip -c "$SNAP" > "$RESTORED"
[[ $(stat -c%s "$RESTORED") -gt 0 ]] || fail "gunzip produced an empty file"
log "gunzip ok ($(stat -c%s "$RESTORED") bytes)"

# --- 2. integrity -------------------------------------------------------------
OK=$(sqlite3 "$RESTORED" 'PRAGMA integrity_check;' 2>&1)
[[ "$OK" == "ok" ]] || fail "integrity_check: $OK"
log "integrity_check ok"

# --- 3. schema version --------------------------------------------------------
if [[ -f "$LIVE_DB" ]]; then
  LIVE_V=$(sqlite3 "$LIVE_DB" 'PRAGMA user_version;')
else
  LIVE_V=$(sqlite3 "$RESTORED" 'PRAGMA user_version;') # fresh box: skip the compare
  LIVE_V=""
fi
RESTORED_V=$(sqlite3 "$RESTORED" 'PRAGMA user_version;')
if [[ -n "$LIVE_V" && "$LIVE_V" != "$RESTORED_V" ]]; then
  fail "schema drift: backup user_version=$RESTORED_V, live=$LIVE_V — restore would hit a migration"
fi
log "schema ok (user_version=$RESTORED_V)"

# --- 4. content parity --------------------------------------------------------
if [[ -f "$LIVE_DB" ]]; then
  for table in venues devices; do
    LIVE_N=$(sqlite3 "$LIVE_DB" "SELECT COUNT(*) FROM $table;")
    REST_N=$(sqlite3 "$RESTORED" "SELECT COUNT(*) FROM $table;")
    [[ "$LIVE_N" == "$REST_N" ]] || fail "$table count: live=$LIVE_N restored=$REST_N"
    log "$table count ok ($REST_N)"
  done
fi

# --- 5. the binary can open it -------------------------------------------------
if [[ -x "$BIN" ]]; then
  # The probe serial cannot exist, so the expected outcome is a clean
  # "not found" — anything else means the store refused to open (corrupt
  # file, schema newer than the binary, …).
  OUT=$("$BIN" cfg -data "$RESTORE_DIR" -serial "__drill_probe__" 2>&1 || true)
  case "$OUT" in
    *"not found"*) log "binary opened the restored store (probe serial not found, as expected)" ;;
    *) fail "binary could not open the restored store: $OUT" ;;
  esac
else
  log "no installed binary — skipped the open check (run again after a deploy)"
fi

log "PASS — snapshot $SNAP restores cleanly. Log this drill in docs/ops-log.md."
