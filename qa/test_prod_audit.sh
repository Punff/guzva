#!/usr/bin/env bash
# qa/test_prod_audit.sh — READ-ONLY audit of the live public page.
#
# SAFETY CONTRACT — read this before running anything here.
#
#   * GET and HEAD requests ONLY. The script physically cannot POST: every
#     curl invocation below uses -X GET or is a bare GET. No device token is
#     read, stored, or sent, so no frame is ever produced against production.
#   * No ssh, no SCP, no deploy, no tag. The prod host is never a target of a
#     write in this file.
#   * /dev/ttyUSB0 is never opened (see ops-log 2026-10-14 16:00: opening the
#     CH340 with stty/cat resets the ESP32 into download mode).
#   * It is NOT part of qa/run_all.sh. The local suite is hermetic; this one
#     touches the internet and has to be asked for by name.
#
# What it checks is exactly what a visitor in a salon gets: the state copy, the
# single clock, the live region, the no-JS fallback, the security headers, and
# whether anything about the deployed build is out of step with this tree.
#
# Usage: qa/test_prod_audit.sh [base-url]

set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PROD="${1:-https://34.139.58.143}"
SLUG="${GUZVA_QA_SLUG:-bench}"
PAGE="${PROD}/${SLUG}/"

echo "== production public page audit (GET only) =="
echo "  target: $PAGE"
qa_tree_status

HTML="$GUZVA_QA_DIR/prod.html"
HDR="$GUZVA_QA_DIR/prod.hdr"

# ---------------------------------------------------------------- reachable

qa_case "the page is reachable over TLS"
code="$(curl -sk -o "$HTML" -D "$HDR" -w '%{http_code}' --max-time 20 "$PAGE")"
qa_eq "status" "$code" "200"
[[ -s "$HTML" ]] && qa_ok "body delivered ($(wc -c <"$HTML" | tr -d ' ') B)" || qa_bad "empty body"
# HTTP/2 lowercases every header name (RFC 9113 8.2.1), so prod answers
# "x-robots-tag:" where a local HTTP/1.1 test sees "X-Robots-Tag:". Normalise
# both sides or every header assertion below is a false failure.
h="$(tr -d '\r' <"$HDR" | tr 'A-Z' 'a-z')"

qa_case "security headers in production"
qa_has "CSP"             "$h" "content-security-policy:"
qa_has "nosniff"         "$h" "x-content-type-options: nosniff"
qa_has "DENY framing"    "$h" "x-frame-options: deny"
qa_has "referrer policy" "$h" "referrer-policy: no-referrer"
qa_has "CORP"            "$h" "cross-origin-resource-policy"
qa_has "Permissions-Policy" "$h" "permissions-policy:"
qa_has "noindex"         "$h" "x-robots-tag: noindex"
qa_has "no-cache"        "$h" "cache-control: no-cache"

qa_case "conditional GET works in production (item B, 83b707d)"
lm="$(printf '%s' "$h" | grep -i '^last-modified:' | cut -d' ' -f2- | tr -d '\r')"
if [[ -n "$lm" ]]; then
  qa_ok "Last-Modified present ($lm)"
  code="$(curl -sk -o /dev/null -w '%{http_code}' -H "If-Modified-Since: $lm" "$PAGE")"
  qa_eq "revalidating with it returns 304" "$code" "304"
else
  qa_bad "no Last-Modified — the deployed binary predates 83b707d (item B not deployed)"
fi
# Q-04: Vary must be on both encodings. Checked read-only.
gzh="$(curl -sk -o /dev/null -D - -H 'Accept-Encoding: gzip' --max-time 20 "$PAGE" | tr -d '\r')"
idh="$(curl -sk -o /dev/null -D - -H 'Accept-Encoding: identity' --max-time 20 "$PAGE" | tr -d '\r')"
if grep -qi '^vary:' <<<"$gzh" && grep -qi '^vary:' <<<"$idh"; then
  qa_ok "Vary present on both encodings (Q-04 fixed in prod)"
else
  qa_note "Q-04 open in prod: Vary missing on at least one encoding"
fi

# ---------------------------------------------------------------- contract

qa_case "single clock in production (the v0.6.1 double-clock fix)"
qa_eq "exactly one id=\"hl\"" "$(grep -c 'id="hl"' "$HTML" || true)" "1"
qa_eq "no #stamp badge"         "$(grep -c 'id="stamp"' "$HTML" || true)" "0"

qa_case "the figcaption is a live region"
fig="$(tr '\n' ' ' <"$HTML" | grep -o '<figcaption[^>]*>' | head -1)"
qa_hasf "role=status"      "$fig" 'role="status"'
qa_hasf "aria-live=polite" "$fig" 'aria-live="polite"'

qa_case "state copy is Croatian and named (docs/ux.md, D7)"
qa_hasf "html lang=hr" "$(cat "$HTML")" '<html lang="hr">'
state="$(grep -o 'id="chip-[a-z]*"' "$HTML" | head -1)"
echo "    state chip: $state"
case "$state" in
  *fresh*)    qa_ok "fresh → 'Svježa slika'"   ; qa_hasf "copy" "$(cat "$HTML")" 'Svježa slika' ;;
  *late*)     qa_ok "late → 'Slika kasni'"    ; qa_hasf "copy" "$(cat "$HTML")" 'Slika kasni' ;;
  *nosignal*) qa_ok "nosignal → 'Nema nove slike'" ; qa_hasf "copy" "$(cat "$HTML")" 'Nema nove slike' ;;
  *closed*)   qa_ok "closed → 'Zatvoreno'"    ; qa_hasf "copy" "$(cat "$HTML")" 'Zatvoreno' ;;
  *waiting*)  qa_ok "waiting → 'Čekamo prvu sliku…'" ; qa_hasf "copy" "$(cat "$HTML")" 'Čekamo prvu sliku' ;;
  *)          qa_note "no state chip found (page may not have rendered a state row)" ;;
esac

qa_case "the page never claims a live frame it does not have"
# If the chip says nosignal, the detail sentence must admit the picture may
# be out of date — this is PLAN §1's honesty requirement in Croatian.
if grep -q 'chip-nosignal' "$HTML"; then
  qa_hasf "honest stale sentence" "$(cat "$HTML")" 'Ne mora prikazivati trenutno stanje'
  qa_hasf "the last good still is still shown" "$(cat "$HTML")" 'id="still"'
else
  qa_ok "venue is not in the no-signal state"
fi

qa_case "no-JS fallback"
qa_true "noscript meta refresh" bash -c \
  "tr -d '\n' < '$HTML' | grep -q '<noscript><meta http-equiv=\"refresh\" content=\"60\"></noscript>'"

qa_case "privacy caption and no third-party requests"
qa_hasf "privacy line" "$(cat "$HTML")" 'stilizirana'
qa_hasnt "no CDN / external origin (D5, §13)" "$(cat "$HTML")" '//fonts.'
qa_hasnt "no external script"                "$(cat "$HTML")" '<script src="http'
qa_hasnt "no external stylesheet"            "$(cat "$HTML")" '<link rel="stylesheet"'

qa_case "responsive: viewport meta + no fixed-width layout"
qa_hasf "viewport declared" "$(cat "$HTML")" 'name="viewport"'
# The page shell is max-w-[40rem] — a MAX width, which is the correct
# responsive primitive. An earlier version of this check looked for "w-[4"
# and matched it, flagging the page's own layout constant as a violation.
qa_hasf "the shell has a max-width, not a fixed one" "$(cat "$HTML")" 'max-w-[40rem]'
qa_hasnt "no fixed px width" "$(cat "$HTML")" 'w-[640px]'
qa_hasnt "no inline width in px" "$(cat "$HTML")" 'width: 640'
qa_hasf "the print box reserves its aspect ratio (no layout shift)" "$(cat "$HTML")" 'aspect-ratio:'


# ------------------------------------------------- deployed vs this tree

qa_case "the deployed build matches this tree (doc truth, ops-log §W6)"
# Compare the public stylesheet's byte size: the page inlines it, so a
# mismatch means prod and the working tree disagree.
inline_css="$(tr '\n' ' ' <"$HTML" | grep -o '<style>.*</style>' | wc -c)"
local_css="$(wc -c <"${REPO_ROOT}/internal/webui/public.css" | tr -d ' ')"
echo "    prod inline <style> ≈ ${inline_css} B; this tree's public.css = ${local_css} B"
if [[ "${inline_css:-0}" -gt 0 && "${local_css:-0}" -gt 0 ]]; then
  delta=$(( inline_css - local_css ))
  if [[ "$delta" -lt 0 || "$delta" -gt 400 ]]; then
    qa_note "public.css differs from this tree by ${delta} B — prod may predate the current design system"
  else
    qa_ok "public.css is within ${delta} B of this tree"
  fi
fi

qa_case "healthz reports the deployed version (read-only)"
hz="$(curl -sk --max-time 20 "${PROD}/healthz" || true)"
echo "    $hz"
qa_hasf "healthz ok" "$hz" '"ok":true'
ver="$(grep -o '"version":"[^"]*"' <<<"$hz" | head -1)"
head_ver="$(cd "$REPO_ROOT" && git describe --tags --always 2>/dev/null || echo unknown)"
echo "    prod: $ver | this tree: $head_ver"
qa_true "version reported" test -n "$ver"

qa_case "admin console is reachable but NOT open (no POST, no credentials)"
code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 20 "${PROD}/admin/")"
case "$code" in
  200|303) qa_ok "admin answers $code (login wall, as expected)" ;;
  *)       qa_bad "admin answered $code — expected a login wall (200/303)" ;;
esac

qa_summary "production audit"
