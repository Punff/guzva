#!/usr/bin/env bash
# qa/test_ingest.sh — the ingest contract matrix (PLAN §6.1, docs/proto-v1.md).
#
# One row per documented outcome: the status, the reason string the DEVICE
# reads out of the body, and the headers a device is allowed to rely on.
# Every reject must also prove the thing that makes it safe: no bytes at rest,
# and the last good still untouched (never publish a failure).
#
# The 13 Oct incidents are regression rows, not extra commentary:
#   dims-1to1.jpg  is framesize=0 (the 11:44 wedge, ~5 h degraded)
#   dark-black.jpg is the "covered lens" case from the 16:00 dark loop
#   409            is the retry path the firmware leans on
#
# Pacing: the per-device token bucket is 0.2/s burst 4, so posts rotate
# across 24 devices and pause (qa/lib.sh). The 429 row uses its own reserved
# device on purpose — that is the one place a burst is the point.
#
# Usage: qa/test_ingest.sh        (starts its own scratch server)

set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

echo "== ingest contract matrix =="
qa_build     || { echo "build failed" >&2; exit 1; }
qa_fixtures  || { echo "fixtures failed" >&2; exit 1; }
qa_server_start || exit 1
trap qa_server_stop EXIT

F="$QA_FIX"

# ---------------------------------------------------------------- 201 / 409

qa_case "201 stored — the good frame is accepted and lands on disk"
r="$(qa_post "$F/good.jpg")"
qa_eq "status"                   "$(qa_status "$r")" "201"
qa_has "body carries desired_cfg" "$(qa_body "$r")" '"desired_cfg"'
qa_has "body carries next_s"       "$(qa_body "$r")" '"next_s"'
qa_has "body carries server_time"  "$(qa_body "$r")" '"server_time"'
qa_hasnt "no reason on an accept"  "$(qa_body "$r")" '"reason"'
curl -sf -o /dev/null "${QA_BASE}/${QA_SLUG}/latest.png" \
  && qa_ok "latest.png published" || qa_bad "latest.png missing after a 201"
qa_eq "web root holds exactly 2 files (D5 privacy)" \
  "$(find "${QA_DATA}/public/${QA_SLUG}" -type f | wc -l | tr -d ' ')" "2"

qa_case "201 on a second good frame (the pipeline is not keyed on identical bytes)"
r="$(qa_post "$F/good2.jpg")"
qa_eq "status" "$(qa_status "$r")" "201"

qa_case "409 duplicate capture_id — a retry must not re-process (§1 idempotency)"
cid="$(qa_cid 42)"
qa_take_token 1; tok="$QA_TOK"; qa_pause
r1="$(qa_post "$F/good.jpg" "$cid" "$tok")"
r2="$(qa_post "$F/good.jpg" "$cid" "$tok")"
qa_eq "first post" "$(qa_status "$r1")" "201"
qa_eq "retry post" "$(qa_status "$r2")" "409"
qa_eq "retry body is the same shape as the 201 (proto §409)" \
  "$(qa_body "$r1" | sed 's/"server_time":"[^"]*"/T/')" \
  "$(qa_body "$r2" | sed 's/"server_time":"[^"]*"/T/')"
qa_has "409 still carries desired_cfg" "$(qa_body "$r2")" '"desired_cfg"'

qa_case "201 health-only beat (no frame part — the camera-degraded path)"
r="$(qa_post -)"
qa_eq "status" "$(qa_status "$r")" "201"

# ------------------------------------------------------------------- 401/405

qa_case "401 bad token — the device keeps its identity, gets no config"
mf="$(mktemp)"; printf '%s' "$(qa_meta "$(qa_cid)")" >"$mf"
code="$(curl -sS -o "$mf.body" -w '%{http_code}' -X POST "${QA_BASE}/v1/ingest" \
  -H "Authorization: Bearer gkv1_totally_wrong_token" -H "X-Proto: v1" \
  -F "meta=@${mf};type=application/json" -F "frame=@${F}/good.jpg")"
qa_eq "status" "$code" "401"
qa_has "error body" "$(cat "$mf.body")" '"error"'
qa_hasnt "no desired_cfg leaked to an unauthenticated caller" "$(cat "$mf.body")" 'desired_cfg'
rm -f "$mf" "$mf.body"

qa_case "401 with no Authorization header at all"
mf="$(mktemp)"; printf '%s' "$(qa_meta "$(qa_cid)")" >"$mf"
code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${QA_BASE}/v1/ingest" \
  -F "meta=@${mf};type=application/json" -F "frame=@${F}/good.jpg")"
qa_eq "status" "$code" "401"
rm -f "$mf"

qa_case "405 GET /v1/ingest — ingest is POST-only"
code="$(curl -sS -o /dev/null -w '%{http_code}' "${QA_BASE}/v1/ingest")"
qa_eq "status" "$code" "405"

# ------------------------------------------------------------------- 422

# assert_reject <fixture> <expected reason> <label>
# The body MUST carry the reason (that is what the firmware's dark-loop
# escalation and the admin device board key off) AND still carry
# desired_cfg, so a device on a 422 still knows its cadence.
assert_reject() {
  local fx="$1" want="$2" label="$3"
  local r
  r="$(qa_post "${F}/${fx}")"
  qa_eq "$label: status"               "$(qa_status "$r")" "422"
  qa_has "$label: reason == $want"     "$(qa_body "$r")" "\"reason\":\"${want}\""
  qa_has "$label: cadence hint next_s" "$(qa_body "$r")" '"next_s"'
  qa_has "$label: device keeps config" "$(qa_body "$r")" '"desired_cfg"'
}

qa_case "422 dims — 1:1 (THE 11:44 INCIDENT: framesize=0 → 96×96)"
assert_reject dims-1to1.jpg dims "96x96"

qa_case "422 dims — other non-4:3 shapes and the size bounds"
assert_reject dims-16to9.jpg dims "320x180"
assert_reject dims-tiny.jpg   dims "95x95 (below MinDim)"

qa_case "422 dark — uniform black, and the mean-luma boundary at 5"
assert_reject dark-black.jpg dark "uniform black"
assert_reject dark-luma5.jpg  dark "flat luma 5 (threshold is mean < 6)"

qa_case "422 dead — uniform but NOT dark: mean ≥ 6 and every cell identical"
assert_reject dead-gray.jpg dead "flat luma 100"

qa_case "422 truncated — right magic, unusable body"
assert_reject truncated.jpg      truncated "10-byte stub"
assert_reject truncated-tail.jpg truncated "valid JPEG cut at 200 B"

qa_case "422 bad_magic — not a JPEG at all (captive portal / wrong proxy)"
assert_reject bad-magic.jpg bad_magic "html body"

qa_case "422 bad_magic — a ZERO-LENGTH frame part (KNOWN DEFECT, Q-05)"
# Expected: a present-but-empty frame part is a malformed request, and
# render.go:165 already says so ("len(in.Raw) < 2 → bad_magic"). Today it is
# intercepted one layer earlier by ingest.go:262 (`len(frameRaw) == 0` →
# health-only beat) and answered 201, so a device whose capture silently
# produced nothing is told it succeeded.
# Asserted, expected to FAIL until backend-go lands the fix:
# docs/qa-findings.md Q-05.
r="$(qa_post "$F/empty.jpg")"
if [[ "$(qa_status "$r")" == "422" ]]; then
  qa_ok "zero-length frame rejected ($(qa_body "$r" | grep -o '"reason":"[a-z_]*"'))"
else
  qa_bad "empty frame part got $(qa_status "$r") — a device that captured nothing is told it succeeded (Q-05)"
fi
qa_eq "…and today it is byte-identical to a real health-only beat" \
  "$(qa_body "$r" | sed 's/"server_time":"[^"]*"/T/')" \
  "$(qa_body "$(qa_post -)" | sed 's/"server_time":"[^"]*"/T/')"

qa_case "422 bad_meta — the meta part is not the documented JSON"
for bad in '{"capture_id":' 'not json at all' '[]' '{"capture_id":123}' ''; do
  r="$(qa_post_meta "$bad" "$F/good.jpg")"
  qa_eq "bad_meta '${bad:0:18}' → 422" "$(qa_status "$r")" "422"
  qa_has "bad_meta '${bad:0:18}' reason" "$(qa_body "$r")" '"reason":"bad_meta"'
done

qa_case "422 bad_capture_id — id must match C<boot>-<ctr>-<rand4>"
for bad in 'nope' 'C1-2-xyz' 'X1-2-abcd' 'C-1-abcd' 'C1-2-ABCDEF'; do
  r="$(qa_post_meta "$(qa_meta "$bad")" "$F/good.jpg")"
  qa_eq "capture_id '${bad}' → 422"        "$(qa_status "$r")" "422"
  qa_has "capture_id '${bad}' → reason"    "$(qa_body "$r")" '"reason":"bad_capture_id"'
done

qa_case "413 too_large — rejected on SIZE, before any decode attempt"
r="$(qa_post "$F/oversize.jpg")"
qa_eq "status" "$(qa_status "$r")" "413"
qa_has "error body" "$(qa_body "$r")" 'too_large'

# ------------------------------------------------------------------- 429

qa_case "429 rate limited — burst past the per-device bucket (reserved device)"
# auth.LimiterDefault: per-device 0.2/s burst 4 (internal/auth/auth.go:129).
# The header a device backs off on is part of the contract (proto §429).
codes=""; retry=""
for _ in $(seq 1 15); do
  got="$(qa_post "$F/good.jpg" "" "$QA_RL_TOKEN" | cut -f1)"
  codes="$codes $got"
  if [[ "$got" == "429" && -z "$retry" ]]; then
    mf="$(mktemp)"; printf '%s' "$(qa_meta "$(qa_cid)")" >"$mf"
    curl -sS -o /dev/null -D "$GUZVA_QA_DIR/h.txt" -X POST "${QA_BASE}/v1/ingest" \
      -H "Authorization: Bearer ${QA_RL_TOKEN}" -H "X-Proto: v1" \
      -F "meta=@${mf};type=application/json" -F "frame=@${F}/good.jpg" >/dev/null
    retry="$(grep -i '^retry-after:' "$GUZVA_QA_DIR/h.txt" | tr -d '\r' | awk '{print $2}')"
    rm -f "$mf"
  fi
done
echo "    codes:$codes"
qa_has "the burst produced 429"       "$codes" "429"
qa_has "the first few posts were accepted" "$codes" "201"
qa_eq "Retry-After header is proto.RetryAfterS" "$retry" "10"

# ------------------------------------------------------- privacy invariants

qa_case "privacy: no raw JPEG bytes at rest anywhere under the data dir"
leaked=0
while IFS= read -r f; do
  if head -c 2 "$f" 2>/dev/null | od -An -tx1 | grep -q 'ff d8'; then
    leaked=1; echo "    raw JPEG at rest: $f"
  fi
done < <(find "$QA_DATA" -type f -size +1k)
qa_eq "no file in the data dir starts with a JPEG SOI" "$leaked" "0"

qa_case "never publish a failure: rejects leave the last good still untouched"
before="$(md5sum "${QA_DATA}/public/${QA_SLUG}/latest.png" | awk '{print $1}')"
qa_post "$F/dark-black.jpg" >/dev/null
qa_post "$F/dims-1to1.jpg"  >/dev/null
after="$(md5sum "${QA_DATA}/public/${QA_SLUG}/latest.png" | awk '{print $1}')"
qa_eq "latest.png unchanged after two rejects" "$after" "$before"

qa_case "quarantine is headers-only: a JSONL record per rejected capture"
qa_true "quarantine.jsonl exists and is non-empty" test -s "$QA_DATA/quarantine.jsonl"
qrows="$(grep -c '"capture_id"' "$QA_DATA/quarantine.jsonl" 2>/dev/null || echo 0)"
echo "    quarantine rows: $qrows"
qa_true "at least 10 rejects were recorded" test "$qrows" -ge 10
qa_hasnt "quarantine records carry no path to a raw frame" \
  "$(head -3 "$QA_DATA/quarantine.jsonl")" '"path"'

qa_case "ingest journal: every device-visible outcome is one line (ops-log 13 Oct)"
echo "    --- last 5 journal lines ---"
tail -5 "$QA_LOG" | sed 's/^/    /'

qa_summary "ingest matrix"
