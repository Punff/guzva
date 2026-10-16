# qa/ — the QA harness

Everything in this directory exists to answer one question with evidence:
**does the running system do what the contracts say it does?** Findings land
in [`docs/qa-findings.md`](../docs/qa-findings.md), which is the deliverable;
this directory is the instrument.

Owned by the `guzva-qa` session. Nothing here ships in the binary, nothing
here is imported by `cmd/guzva`, and nothing here talks to production except
`test_prod_audit.sh` — which is GET-only and has to be asked for by name.

## Run it

```sh
qa/run_all.sh                      # the whole local suite
qa/test_ingest.sh                  # the proto-v1 response matrix
qa/test_pagecache.sh               # conditional GET on the public page
qa/test_public_page.sh             # the page contract + freshness math
qa/test_prod_audit.sh              # GET-only audit of the live page (asks first)
go test ./qa/... -v                # hermetic audits: contrast, i18n, copy
```

`qa/run_all.sh` runs `make test` first, so it is also the answer to "is the
gate green right now".

## What is in here

| file | what it is |
|------|------------|
| `lib.sh` | scratch server on 127.0.0.1:18080, a 24-device token pool, the assertion vocabulary, and the tree-status banner every suite prints |
| `fixtures/main.go` | the synthetic JPEG corpus — stdlib `image/jpeg` only, no fixture blobs in git, every file's expected outcome printed next to it |
| `audit_test.go` | hermetic checks that run inside `make test`: state-colour contrast, catalog placeholder parity, copy that escaped the catalog |
| `test_ingest.sh` | 201/409/401/405/422/413/429 over real HTTP, plus the privacy invariants |
| `test_pagecache.sh` | conditional GET; skip-marked before item B, enforced after |
| `test_public_page.sh` | one clock, live region, poller, no-JS, Croatian states, freshness math |
| `test_prod_audit.sh` | read-only audit of the deployed page |

## Safety rails

- The server binds `127.0.0.1` and its data dir lives under
  `$TMPDIR/guzva-qa` (override with `GUZVA_QA_DIR`). Nothing writes to the
  repo's `mydata/`.
- **No test opens `/dev/ttyUSB0`.** Opening the CH340 with `stty`/`cat`
  resets the ESP32 into download mode — that cost the bench its boot counter
  on 2026-10-14. Pass-by checks read the server journal instead.
- **`test_prod_audit.sh` is GET-only** and sends no device token, so it cannot
  produce a frame against production. `grep -iE '\-X (POST|PUT|PATCH|DELETE)|ssh |scp '`
  over that file returns nothing, and that is the check to re-run if anyone
  edits it.
- **Never `git stash` and never `git add -A` here.** This tree is shared with
  six other sessions; `git stash` would take their uncommitted work with it.

## Two traps worth knowing about

Both cost real time during round 1 and are documented where they bite.

1. **A command substitution is a subshell.** Every test calls
   `r="$(qa_post …)"`, so any variable `qa_post` bumps is discarded on return.
   A token-rotation counter inside it therefore never advances, every post
   reuses device #1, and the suite fails with *sporadic 429s in the middle of
   otherwise-correct runs* — which reads like a product bug and is not.
   `qa_rotate_token` keeps its cursor in a file for exactly this reason.

2. **The per-IP floor paces this suite, not the per-device bucket.**
   `auth.DefaultLimiter` is 2/s burst 30 per IP and 0.2/s burst 4 per device.
   Posting every 0.55 s costs 1.1 tokens per 1.0 replenished, and the deficit
   compounds until *everything* is refused. `qa_pause` posts every 0.7 s.

And one that is not a trap but a choice: **`received_at` is an INTEGER of unix
seconds, not a datetime string.** Writing `datetime('now','-4 minutes')` into
it succeeds silently — SQLite is dynamically typed — and then the Go driver
cannot scan the column, which looks exactly like "the state never flipped".
`test_public_page.sh`'s `backdate` casts `strftime('%s', …)`.

## Why some tests start as "expected behaviour, not yet enforced"

A test that only checks today's behaviour is a test that freezes today's bugs.
So the conditional-GET and mean-luma assertions were written against the
behaviour the contracts *require*, marked `skip` or inverted, and flipped to
enforced the moment the implementation commit landed (`83b707d` for the
former). `qa/test_pagecache.sh` still carries both modes — run it with
`ENFORCE_CONDITIONAL_GET=0` to reproduce the pre-B observation.

## Screenshots

State renders are captured to `.openchamber/screenshots/` with a `qa-` prefix
(fresh / late / nosignal at 390 px, plus desktop). They come from the **local**
scratch server, not production: the deployed page is served with the bench
self-signed certificate (SAN covers the IP, but no trusted CA), so a browser
refuses `https://34.139.58.143/` while `curl -k` is fine. The production
audit is therefore HTTP-level — headers, status codes and HTML contract —
which is the stronger check anyway.
