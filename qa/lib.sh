#!/usr/bin/env bash
# qa/lib.sh — shared harness for the guzva QA round.
#
# Every test script sources this. It owns three things so the individual
# tests stay readable: a scratch data dir under /tmp/opencode (never the repo,
# never the prod VPS), a `guzva serve` on 127.0.0.1 with its own port and log,
# and a tiny assertion vocabulary that prints one line per check and exits
# non-zero on the first failure.
#
# Safety rails baked in (see AGENTS.md):
#   * the server binds 127.0.0.1 only;
#   * the token is minted into the scratch dir and never echoed to a log the
#     team shares;
#   * no test here ever touches /dev/ttyUSB0 or 34.139.58.143. The one
#     prod-touching script is test_prod_audit.sh, which is GET-only and is
#     invoked explicitly, never from run_all.sh's local suite.

set -uo pipefail

QA_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$QA_ROOT/.." && pwd)"

# Scratch root. /tmp/opencode is the session-approved scratch area; everything
# this harness writes lives under $GUZVA_QA_DIR and dies with the run.
export GUZVA_QA_DIR="${GUZVA_QA_DIR:-/tmp/opencode/guzva-qa}"
QA_PORT="${GUZVA_QA_PORT:-18080}"
QA_BASE="http://127.0.0.1:${QA_PORT}"
QA_DATA="${GUZVA_QA_DIR}/data"
QA_FIX="${GUZVA_QA_DIR}/fix"
QA_LOG="${GUZVA_QA_DIR}/server.log"
QA_BIN="${GUZVA_QA_DIR}/guzva"
QA_SLUG="${GUZVA_QA_SLUG:-bench}"
QA_DEVICES="${GUZVA_QA_DEVICES:-24}"
QA_SERVER_PID=""
QA_TOKENS=()
QA_TOKEN_I=0
QA_TOK=""
QA_RL_TOKEN=""

# ---------------------------------------------------------------- assertions

QA_PASS=0
QA_FAIL=0
QA_CURRENT=""

# qa_case opens a named case; every ok/fail below belongs to it.
qa_case() {
  QA_CURRENT="$1"
  printf '\n  %s\n' "$1"
}

qa_ok()   { QA_PASS=$((QA_PASS+1)); printf '    \033[32mPASS\033[0m %s\n' "$1"; }
qa_bad()  { QA_FAIL=$((QA_FAIL+1)); printf '    \033[31mFAIL\033[0m %s\n' "$1"; }
qa_note() { printf '    \033[33mSKIP\033[0m %s\n' "$1"; }

# qa_skip <why> marks an expected-behaviour assertion as not-yet-enforced.
# The mandate: the test is written against the EXPECTED behaviour from the
# moment it exists, and only flips to enforced when the implementation commit
# lands. Use qa_skip only with that plan, and name the commit you are waiting
# for in the reason.
qa_skip() { QA_FAIL=$((QA_FAIL+1)); printf '    \033[36mWAIT\033[0m %s\n' "$1"; }

# qa_eq <label> <got> <want>
qa_eq() {
  if [[ "$2" == "$3" ]]; then qa_ok "$1 (= $3)"; else qa_bad "$1: got '$2', want '$3'"; fi
}

# qa_has <label> <haystack> <needle> — substring, empty needle never passes.
qa_has() {
  if [[ -n "$3" && "$2" == *"$3"* ]]; then qa_ok "$1"; else qa_bad "$1: '$3' not found in: $(printf '%s' "$2" | head -c 300)"; fi
}

# qa_hasnt <label> <haystack> <needle>
qa_hasnt() {
  if [[ "$2" != *"$3"* ]]; then qa_ok "$1"; else qa_bad "$1: '$3' unexpectedly present"; fi
}

# qa_true <label> <cmd...> — runs the command, passes on exit 0.
qa_true() {
  local label="$1"; shift
  local out; out="$("$@" 2>&1)"
  if [[ $? -eq 0 ]]; then qa_ok "$label"; else qa_bad "$label: ${out:0:300}"; fi
}

qa_summary() {
  printf '\n  ── %s: %d passed, %d failing\n' "${1:-results}" "$QA_PASS" "$QA_FAIL"
  [[ "$QA_FAIL" -eq 0 ]]
}

# ------------------------------------------------------------ server control

# QA_BUILD_FROM — which tree the binary comes from:
#   work    (default) the live working tree, so a test run sees other
#           sessions' in-flight work
#   HEAD    a pristine export of the last commit, for when the working tree
#           does not compile because someone is mid-write
qa_build() {
  mkdir -p "$GUZVA_QA_DIR"
  case "${QA_BUILD_FROM:-work}" in
    HEAD) qa_build_from_head ;;
    *)    qa_build_from_work ;;
  esac
}

qa_build_from_work() {
  ( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o "$QA_BIN" ./cmd/guzva )
}

# qa_build_from_head exports HEAD into a scratch dir with `git archive` and
# builds there. This exists because another session is sometimes mid-write:
# at 14:5x on 2026-10-14, internal/pages/static.go and stale_test.go carried
# a few hundred NUL bytes each (an editor that preallocated a buffer), which
# made `go build ./cmd/guzva` fail and `make test` red for the whole repo.
#
# `git archive` rather than `git worktree add`: it touches nothing under
# .git, creates no branch, and cannot disturb the other session's checkout —
# which is the constraint that rules out git stash (AGENTS.md: never stash,
# another session's uncommitted work shares this tree).
qa_build_from_head() {
  local src="$GUZVA_QA_DIR/src-head"
  if [[ ! -d "$src" ]]; then
    mkdir -p "$src"
    ( cd "$REPO_ROOT" && git archive HEAD ) | tar -x -C "$src" || return 1
  fi
  # qa/ is not in an old HEAD export; copy the fixture generator in so
  # qa_fixtures keeps working against the same module.
  mkdir -p "$src/qa"
  cp "$REPO_ROOT"/qa/fixtures/main.go "$src/qa/fixtures_main.go" 2>/dev/null
  mkdir -p "$src/qa/fixtures"
  mv -f "$src/qa/fixtures_main.go" "$src/qa/fixtures/main.go" 2>/dev/null
  ( cd "$src" && CGO_ENABLED=0 go build -o "$QA_BIN" ./cmd/guzva )
}

# qa_tree_status prints what the working tree looks like, so a test run's
# results can be attributed to a known state of the repo.
qa_tree_status() {
  printf '  tree: %s | HEAD %s | modified: %s\n' \
    "$(cd "$REPO_ROOT" && git rev-parse --abbrev-ref HEAD)" \
    "$(cd "$REPO_ROOT" && git rev-parse --short HEAD)" \
    "$(cd "$REPO_ROOT" && git status --porcelain -- ':!qa' ':!docs/qa-findings.md' | wc -l | tr -d ' ')"
  local dirty
  dirty="$(cd "$REPO_ROOT" && git status --porcelain -- ':!qa' ':!docs/qa-findings.md' | awk '{print $2}' | tr '\n' ' ')"
  [[ -n "$dirty" ]] && printf '  dirty paths: %s\n' "$dirty"
  return 0
}

# qa_fixtures generates the JPEG corpus.
qa_fixtures() {
  ( cd "$REPO_ROOT" && go run ./qa/fixtures -out "$QA_FIX" ) || return 1
}

# qa_server_start wipes the scratch dir, provisions a venue, and starts serve.
# Slug + token are printed into $GUZVA_QA_TOKEN for the tests.
#
# The venue gets QA_DEVICES devices, not one, and that is load-bearing: the
# per-device token bucket is 0.2/s with burst 4 (internal/auth/auth.go:129).
# A single-token harness saturates it after four posts and every later
# assertion silently reads 429 — which is exactly what the first run of
# test_ingest.sh did. Tests rotate tokens and pause between posts so the
# limiter stays out of the way; the 429 case uses its own reserved device.
qa_server_start() {
  qa_server_stop >/dev/null 2>&1
  rm -rf "$QA_DATA"; mkdir -p "$QA_DATA"

  local hash
  hash="$("$QA_BIN" hashpw -password devonly)" || { echo "hashpw failed" >&2; return 1; }

  # provision prints every device token once; capture them, do not log them.
  local provout
  provout="$("$QA_BIN" provision -data "$QA_DATA" -slug "$QA_SLUG" -name "QA Bench" \
      -hours "Mo-Su 08:00-23:00" -serial qa-serial -tz Europe/Sarajevo \
      -devices "$QA_DEVICES")" || { echo "provision failed: $provout" >&2; return 1; }

  mapfile -t QA_TOKENS < <(printf '%s' "$provout" | grep -oE 'gkv1_[A-Za-z0-9_-]+')
  if [[ "${#QA_TOKENS[@]}" -eq 0 ]]; then
    echo "provision printed no token: $provout" >&2
    return 1
  fi
  QA_TOKEN_I=0
  rm -f "$GUZVA_QA_DIR/tokidx"
  # The first device is "the device" for happy-path and duplicate tests.
  GUZVA_QA_TOKEN="${QA_TOKENS[0]}"
  export GUZVA_QA_TOKEN
  # The last device is reserved for the rate-limit case so a 429 burst does
  # not drain the shared pool.
  QA_RL_TOKEN="${QA_TOKENS[$(( ${#QA_TOKENS[@]} - 1 ))]}"

  GUZVA_ADMIN_HASH="$hash" GUZVA_COOKIE_SECRET="qasecret" \
    "$QA_BIN" serve -listen "127.0.0.1:${QA_PORT}" -data "$QA_DATA" \
    >"$QA_LOG" 2>&1 &
  QA_SERVER_PID=$!

  local i
  for i in $(seq 1 50); do
    if curl -sf "${QA_BASE}/healthz" >/dev/null 2>&1; then
      echo "server up on ${QA_BASE} (pid ${QA_SERVER_PID}), venue /${QA_SLUG}/, ${#QA_TOKENS[@]} devices"
      return 0
    fi
    sleep 0.2
  done
  echo "server did not come up; log:" >&2
  tail -30 "$QA_LOG" >&2
  return 1
}

# qa_rotate_token advances the shared pool and leaves the token in $QA_TOK.
#
# The cursor lives in a FILE, not a shell variable, and that is the whole
# trick. Callers write `r="$(qa_post ...)"`, so qa_post itself runs inside a
# command substitution — a subshell — and any variable it bumps is discarded
# on return. Both earlier attempts (a plain counter, then printing the token
# to capture it) therefore reused token[0] for every post and read 429 from
# a single saturated device, twice. A file is the only cursor a subshell
# cannot roll back.
qa_rotate_token() {
  local f="$GUZVA_QA_DIR/tokidx" n
  n=0; [[ -f "$f" ]] && n="$(cat "$f")"
  n=$(( n % (${#QA_TOKENS[@]} - 1) ))
  QA_TOK="${QA_TOKENS[$n]}"
  printf '%s' "$(( (n + 1) % (${#QA_TOKENS[@]} - 1) ))" >"$f"
}

# qa_take_token <index> claims one specific pool device and moves the cursor
# past it, so a test that needs two posts from ONE identity (the 409
# duplicate case) does not collide with the pool's rotation.
qa_take_token() {
  QA_TOK="${QA_TOKENS[$1]}"
  printf '%s' "$(( ($1 + 1) % (${#QA_TOKENS[@]} - 1) ))" >"$GUZVA_QA_DIR/tokidx"
}

# qa_next_token is the read-only accessor, for tests that need the token
# outside a post.
qa_next_token() {
  QA_rotate_token
  printf '%s' "$QA_TOK"
}

# qa_pause keeps the limiter out of the assertions. The binding constraint is
# the per-IP floor (2/s, burst 30 — internal/auth/auth.go:129), not the
# per-device bucket: a token bucket hands out 2 tokens/s, so a post every
# 0.55 s costs 1.1 tokens and the deficit compounds until every request is
# refused. 0.7 s returns 1.4 tokens per post, which recovers.
qa_pause() { sleep "${QA_PAUSE:-0.7}"; }

qa_server_stop() {
  if [[ -n "${QA_SERVER_PID:-}" ]]; then
    kill "$QA_SERVER_PID" 2>/dev/null
    wait "$QA_SERVER_PID" 2>/dev/null
    QA_SERVER_PID=""
  fi
}

# qa_page_url is the public page for the scratch venue.
qa_page_url() { echo "${QA_BASE}/${QA_SLUG}/"; }

# ---------------------------------------------------------- ingest utilities

# qa_meta <capture_id> — a valid meta part (proto-v1 §meta).
qa_meta() {
  printf '{"capture_id":"%s","fw":"qa-0","rssi":-60,"free_heap":150000,"cfg_ver_applied":1,"counters":{"boot":3,"brownout":0}}' "$1"
}

# qa_cid [n] — a valid C<boot>-<ctr>-<rand4> id, unique per call.
qa_cid() { printf 'C%s-%s-%04x' "${RANDOM}" "${1:-0}" "$((RANDOM % 0x10000))"; }

# qa_post <frame-file|-> [capture_id] [token] → "HTTP_STATUS<TAB>BODY".
# "-" means a health-only beat (no frame part). With no token argument it
# rotates the shared device pool and pauses, so the limiter does not turn
# every assertion into a 429 (see qa_server_start).
qa_post() {
  local frame="$1" cid="${2:-}" tok="${3:-}"
  [[ -n "$cid" ]] || cid="$(qa_cid)"
  if [[ -z "$tok" ]]; then qa_rotate_token; qa_pause; else tok="$QA_TOK"; fi
  QA_TOK="$tok"
  qa_post_meta "$(qa_meta "$cid")" "$frame" "$tok"
}

# qa_post_meta <meta-literal> <frame-file> [token] — post an arbitrary meta
# part, so the bad_meta / bad_capture_id rows do not each need their own curl.
# An empty token rotates the pool too: the meta is what is under test, not the
# identity, and these rows are still real devices.
qa_post_meta() {
  local meta="$1" frame="$2" tok="${3:-}"
  if [[ -z "$tok" ]]; then qa_rotate_token; qa_pause; tok="$QA_TOK"; fi
  local mf out code body
  mf="$(mktemp)"
  printf '%s' "$meta" >"$mf"
  if [[ "$frame" == "-" ]]; then
    out="$(curl -sS -w $'\n%{http_code}' -X POST "${QA_BASE}/v1/ingest" \
      -H "Authorization: Bearer ${tok}" -H "X-Proto: v1" \
      -F "meta=@${mf};type=application/json")"
  else
    out="$(curl -sS -w $'\n%{http_code}' -X POST "${QA_BASE}/v1/ingest" \
      -H "Authorization: Bearer ${tok}" -H "X-Proto: v1" \
      -F "meta=@${mf};type=application/json" -F "frame=@${frame}")"
  fi
  rm -f "$mf"
  code="$(printf '%s' "$out" | tail -1)"
  body="$(printf '%s' "$out" | sed '$d')"
  printf '%s\t%s' "$code" "$body"
}

# qa_status / qa_body split qa_post's tab-joined result.
qa_status() { printf '%s' "${1%%$'\t'*}"; }
qa_body()   { printf '%s' "${1#*$'\t'}"; }

# qa_post_raw <args...> — escape hatch for headers/methods qa_post does not
# cover (bad token, explicit If-Modified-Since, oversized bodies).
qa_post_raw() { curl -sS -o /dev/null -w '%{http_code}' "$@"; }

# qa_hasf <label> <haystack> <needle> — FIXED-string substring.
#
# qa_has uses bash pattern matching, where `.` is literal but `[2.25rem]` is a
# character class and `2\.25rem` is a literal backslash. Every Tailwind
# arbitrary-value assertion (text-[2.25rem], sm:text-[3rem]) therefore has to
# go through this one instead.
qa_hasf() {
  if [[ -n "$3" && "$2" == *"$3"* ]]; then qa_ok "$1"; else qa_bad "$1: fixed '$3' not found"; fi
}

# qa_hasf_file <label> <file> <fixed needle> — same, reading a file so the
# needle can span the lines of one HTML element.
qa_hasf_file() {
  if [[ -n "$3" ]] && grep -qF -- "$3" "$2"; then qa_ok "$1"; else qa_bad "$1: fixed '$3' not found in $2"; fi
}
