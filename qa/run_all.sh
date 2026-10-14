#!/usr/bin/env bash
# qa/run_all.sh — the local, hermetic QA suite.
#
# Runs every check that needs nothing but this repo and a loopback port:
#
#   1. make test          the repo's own gate (fmt + vet + unit + css)
#   2. go test ./qa/...   hermetic audits: state-colour contrast, catalog
#                         placeholder parity, copy-escaping-the-catalog
#   3. test_ingest.sh     the proto-v1 response matrix over real HTTP
#   4. test_pagecache.sh  conditional GET on the public page
#   5. test_public_page.sh  the page contract + freshness math
#
# NOT run here: test_prod_audit.sh. It touches the internet and is invoked by
# name, on purpose (see its safety contract at the top of the file).
#
# Each suite prints its own pass/fail line and the exit status is the OR of
# them, so CI (or a person) gets one number.
#
# Usage: qa/run_all.sh
#        QA_BUILD_FROM=HEAD qa/run_all.sh    # if another session's WIP does
#                                            # not compile

set -uo pipefail
QA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$QA_DIR/.." && pwd)"
cd "$REPO"

fails=()
run() {
  local name="$1"; shift
  printf '\n\033[1m══ %s ══\033[0m\n' "$name"
  if "$@"; then
    return 0
  fi
  fails+=("$name")
  return 1
}

echo "qa suite — $(git rev-parse --short HEAD) on $(git rev-parse --abbrev-ref HEAD)"
git status --porcelain | grep -v '^?? qa/' | grep -v 'qa-findings' | sed 's/^/  dirty: /'

run "make test"        make test
run "qa audits"        go test ./qa/...
run "ingest matrix"    "$QA_DIR/test_ingest.sh"
run "pageCache"        "$QA_DIR/test_pagecache.sh"
run "public page"      "$QA_DIR/test_public_page.sh"

printf '\n\033[1m══ summary ══\033[0m\n'
if [[ ${#fails[@]} -eq 0 ]]; then
  printf '  \033[32mall suites green\033[0m\n'
  exit 0
fi
for f in "${fails[@]}"; do
  printf '  \033[31mFAILED\033[0m %s\n' "$f"
done
printf '\n  see docs/qa-findings.md — a red row here is usually a numbered\n'
printf '  finding with a repro, not a mystery.\n'
exit 1
