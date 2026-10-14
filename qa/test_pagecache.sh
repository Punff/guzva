#!/usr/bin/env bash
# qa/test_pagecache.sh — conditional GET on the public page (PLAN D5, §6.4).
#
# WHY THIS TEST EXISTS
# --------------------
# The venue page is served by pageCache.serve (internal/pages/static.go).
# The stills path uses http.ServeContent, which honours If-Modified-Since
# and emits Last-Modified. The page path does neither: it writes the status
# line itself and always answers 200.
#
# That is not a caching nicety. The poller's state-flip detector
# (internal/pages/templates/venue.html:293-302) is
#
#     fetch(location.pathname) → read 'last-modified' → compare with
#     document.lastModified → differ ? location.reload() : nothing
#
# With no Last-Modified header that comparison is dead code, so an open tab
# never sees a fresh→late→nosignal flip. PLAN §1 requires that "the UI never
# claims a frame is live when it is not", so this is a truthfulness defect.
#
# STATUS: ENFORCED since `83b707d pages: conditional GET` (2026-10-14 15:32).
#   The test was written against the EXPECTED behaviour while the page sent
#   no Last-Modified and never 304'd, and was skip-marked until that commit
#   landed. It is now the default; set ENFORCE_CONDITIONAL_GET=0 to re-run
#   the pre-B observation mode.
#
#   Result of flipping it: 22 passed, 1 failing. The one failure is Q-04
#   (`Vary` missing on the identity-encoded 200), which B did not touch.
#
# Usage: qa/test_pagecache.sh
#        ENFORCE_CONDITIONAL_GET=0 qa/test_pagecache.sh   # pre-B mode

set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# Enforced by default as of 83b707d (item B). Override to 0 for pre-B mode.
ENFORCE_CONDITIONAL_GET="${ENFORCE_CONDITIONAL_GET:-1}"

echo "== public page conditional GET =="
qa_tree_status
qa_build     || { echo "build failed" >&2; exit 1; }
qa_fixtures  || { echo "fixtures failed" >&2; exit 1; }
qa_server_start || exit 1
trap qa_server_stop EXIT

PAGE="$(qa_page_url)"
STILL="${QA_BASE}/${QA_SLUG}/latest.png"
# A page must exist before any of this means anything.
qa_post "$QA_FIX/good.jpg" >/dev/null
qa_true "venue page renders" curl -sf -o /dev/null "$PAGE"

hdrs="$GUZVA_QA_DIR/page.hdr"
body="$GUZVA_QA_DIR/page.html"
curl -sS -D "$hdrs" -o "$body" "$PAGE"

# ------------------------------------------------------------ unconditional

qa_case "baseline GET — the page is 200 and carries the unconditional headers"
qa_has "Cache-Control: no-cache (index.html inlines CSS+JS)" \
  "$(tr -d '\r' <"$hdrs")" "Cache-Control: no-cache"
qa_has "CSP present"                "$(tr -d '\r' <"$hdrs")" "Content-Security-Policy:"
qa_has "nosniff"                    "$(tr -d '\r' <"$hdrs")" "X-Content-Type-Options: nosniff"
qa_has "DENY framing"               "$(tr -d '\r' <"$hdrs")" "X-Frame-Options: DENY"
qa_has "referrer policy"            "$(tr -d '\r' <"$hdrs")" "Referrer-Policy: no-referrer"
qa_has "noindex (⚑7: a venue interior is not for search engines)" \
  "$(tr -d '\r' <"$hdrs")" "X-Robots-Tag: noindex"

qa_case "Vary: Accept-Encoding — set on BOTH encodings (KNOWN DEFECT, Q-04)"
# pageCache.serve sets Vary inside the gzip branch only, so a client that
# does not advertise gzip gets a 200 with no Vary at all. Both responses
# share a cache key from a naive intermediary's point of view.
gz="$GUZVA_QA_DIR/gz.hdr"
curl -sS -D "$gz" -o /dev/null -H 'Accept-Encoding: gzip' "$PAGE"
qa_has "Vary present when gzip IS accepted" "$(tr -d '\r' <"$gz")" "Vary: Accept-Encoding"
if grep -qi '^vary:' "$hdrs"; then
  qa_ok "Vary also present when gzip is NOT accepted"
else
  qa_bad "identity-encoded 200 has no Vary — a shared cache can serve it to a gzip client (Q-04)"
fi

# ---------------------------------------------------------- the conditional

if [[ "$ENFORCE_CONDITIONAL_GET" != "1" ]]; then
  qa_case "conditional GET — pre-B observation mode (ENFORCE_CONDITIONAL_GET=0)"
  # Report the current state as an observation, not a pass.
  qa_note "mode: observation only — set ENFORCE_CONDITIONAL_GET=1 (the default) to enforce"
  qa_note "today the page sends Last-Modified: $(grep -ci '^last-modified:' "$hdrs" | tr -d ' ')/1"
  qa_note "today a future If-Modified-Since gets: $(curl -sS -o /dev/null -w '%{http_code}' -H 'If-Modified-Since: Wed, 01 Jan 2031 00:00:00 GMT' "$PAGE") (want 304)"

  qa_case "stills DO honour conditional GET (the control: ServeContent path)"
  still="$(curl -sSI "$STILL" | tr -d '\r')"
  qa_has "still sends Last-Modified" "$still" "Last-Modified:"
  lm="$(printf '%s' "$still" | grep -i '^last-modified:' | cut -d' ' -f2-)"
  code="$(curl -sS -o /dev/null -w '%{http_code}' -H "If-Modified-Since: $lm" "$STILL")"
  qa_eq "still revalidates to 304" "$code" "304"
  qa_note "this is exactly what the page path should do and does not"

  qa_summary "pageCache (not enforced)"
  exit $?
fi

qa_case "304 — a future If-Modified-Since is not modified"
code="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H 'If-Modified-Since: Wed, 01 Jan 2031 00:00:00 GMT' "$PAGE")"
qa_eq "status" "$code" "304"
size="$(curl -sS -o /dev/null -w '%{size_download}' \
  -H 'If-Modified-Since: Wed, 01 Jan 2031 00:00:00 GMT' "$PAGE")"
qa_eq "body size" "$size" "0"

qa_case "304 keeps the headers a browser must re-store"
# Which headers a 304 has to carry is not "all of them". Per RFC 9111 4.3.4
# a 304 UPDATES the stored entry's headers, so the CSP and Content-Type from
# the 200 that originally populated the cache stay in place -- omitting them
# here is correct, not a regression. (An earlier revision of this test
# asserted CSP on the 304 and failed; the assertion was wrong, not the code.)
# What MUST survive is the set that governs caching and framing: those are
# set by the caller before serve() runs, so they are on both paths.
h2="$GUZVA_QA_DIR/page304.hdr"
curl -sSI -H 'If-Modified-Since: Wed, 01 Jan 2031 00:00:00 GMT' "$PAGE" >"$h2" 2>/dev/null
h304="$(tr -d '\r' <"$h2")"
qa_has "Cache-Control: no-cache survives" "$h304" "Cache-Control: no-cache"
qa_has "nosniff survives"                 "$h304" "X-Content-Type-Options: nosniff"
qa_has "DENY framing survives"            "$h304" "X-Frame-Options: DENY"
qa_has "noindex survives"                 "$h304" "X-Robots-Tag: noindex"
qa_has "referrer policy survives"         "$h304" "Referrer-Policy: no-referrer"
qa_has "CORP survives"                    "$h304" "Cross-Origin-Resource-Policy"
qa_has "Permissions-Policy survives"      "$h304" "Permissions-Policy"
qa_has "Last-Modified is on the 304 too"  "$h304" "Last-Modified:"

qa_case "200 — a past If-Modified-Since is served in full"
code="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H 'If-Modified-Since: Wed, 01 Jan 2020 00:00:00 GMT' "$PAGE")"
qa_eq "status" "$code" "200"

qa_case "Last-Modified is present on the page (the poller reads exactly this)"
curl -sSI "$PAGE" >"$h2" 2>/dev/null
qa_has "Last-Modified" "$(tr -d '\r' <"$h2")" "Last-Modified:"

qa_case "the still keeps its own semantics (regression guard for D5)"
still="$(curl -sSI "$STILL" | tr -d '\r')"
qa_has "still is not immutable (the file behind ?v= is mutable)" \
  "$still" "Cache-Control: no-cache"

qa_case "web root still holds exactly two files after all of the above"
qa_eq "file count" "$(find "${QA_DATA}/public/${QA_SLUG}" -type f | wc -l | tr -d ' ')" "2"

qa_summary "pageCache"
