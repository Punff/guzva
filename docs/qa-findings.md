# guzva — QA findings (session `guzva-qa`)

Owner: `guzva-qa`. Harness lives in [`qa/`](../qa/). This file is
append-only; every entry carries an id, a severity, a repro, and the role the
fix belongs to. `FIXED` entries name the commit that fixed them.

Severity: **P0** ships wrong / lies / loses data · **P1** user-visible defect
with a known cause · **P2** polish, a11y, or docs truth.

Gate for every entry below: `make test` green at the commit named, and the
repro run against a scratch server (`qa/lib.sh`, data dir under `/tmp/opencode`
— never the prod VPS, never `/dev/ttyUSB0`).

---

## Round 1 — 2026-10-14

Baseline at entry: `make test` green on `040e338` **including** backend-go's
and firmware's in-flight uncommitted work (`internal/frames/ingest.go`,
`firmware/src/main.cpp`). Neither was touched by this session.

---

### Q-01 · P1 · ingest journal · `mean_luma` logs the wrong key AND a hardcoded zero

**Status:** OPEN — backend-go's lane (`internal/frames/ingest.go`,
`internal/render/render.go`). Reported, not touched.

**Area:** item C from the 16:00–16:40 dark-loop incident — "log the computed
mean luma on `dark` rejects, so *covered lens* and *gain clamp* are
distinguishable".

**Repro (grep, at `040e338` + backend WIP):**

```console
$ grep -n 'mean_lua' internal/frames/ingest.go
410:		attrs = append(attrs, "mean_lua", meanLuma)
```

Two defects in that one line:

1. **The key is misspelled** `mean_lua`. Every other luma reference in the
   repo is `mean_luma` (`internal/frames/ingest_test.go`,
   ops-log line 926, PLAN). An operator grepping the journal after the fact —
   the exact two hours lost on 13 Oct — greps `mean_luma` and finds nothing.
2. **The value is always `0`,** because every call site passes a literal:

```console
$ grep -n 'logOutcome(' internal/frames/ingest.go | sed -n '1,12p'
118:		s.logOutcome(status, dev, meta, r.TLS, renderMS, reason, 0)   # respond() — the 422 path
145:		s.logOutcome(proto.StatusRateLimit, dev, meta, r.TLS, 0, "", 0)
```

And the plumbing to carry a real number does not exist yet:

```console
$ grep -n 'func Reject\|type RejectError' internal/render/render.go
76:type RejectError struct {
77:	Reason string
78:}
83:func Reject(reason string) error { return &RejectError{Reason: reason} }
```

`gridStats` computes the mean at `render.go:187` and throws it away — the
reject path is `Reject(proto.ReasonDark)`, which carries only the reason.
So item C as currently staged logs `mean_lua=0` on every dark/dead reject: a
number that is *worse than no number*, because 0 is indistinguishable from
"a genuinely black frame" and from "we never measured".

**Fix shape (backend-go):** add `Mean float64` to `RejectError`; a
`RejectWithMean(reason, mean)` constructor used at the `dark`/`dead` sites;
`IsReject` (or a new `RejectDetail`) surfaces it; `HandleIngest`'s reject
branch passes it into `logOutcome`; the key is spelled `mean_luma`. The
`parse`-stage callers legitimately keep `0` — no frame was decoded there.

**Why P1 and not P2:** the whole point of item C was operator legibility, and
as staged it makes the journal *look* instrumented while carrying a constant.
A reviewer skimming `reject=dark mean_lua=0` concludes "measured, and black"
and stops looking.

**Regression pinned:** `qa/test_ingest.sh` asserts the journal line for a dark
reject carries `mean_luma=` with a **non-zero, in-range** value once the fix
lands; `skip until C lands` until then.

---

### Q-02 · P1 · public page · no conditional GET, so the poller's state-flip reload detector is inert

**Status:** OPEN — backend-go's item B (`internal/pages/static.go`). Reported,
not touched. Test written, awaiting the commit: [`qa/test_pagecache.sh`](../qa/test_pagecache.sh).

**Area:** the user's hours-edit report + ops-log 2026-10-13 round 6 (B).

**Repro (code read + `curl -I` against a scratch server):**
`pageCache.serve` writes the status line itself and never consults the
request's validators:

```go
// internal/pages/static.go:170-189
func (c *pageCache) serve(w http.ResponseWriter, r *http.Request, dir string, mtime time.Time, body []byte) {
	gz, csp := c.get(filepath.Join(dir, "index.html"), mtime, body)
	...
	w.WriteHeader(http.StatusOK)      // unconditional; no Last-Modified, no ETag
```

The stills path (`http.ServeContent`, line 118) *does* honour
`If-Modified-Since` and does send `Last-Modified`, so the still half of the
poller works; only `index.html` is affected.

The consequence is not just "one wasted request per minute". The poller's
state-flip detector is:

```js
// templates/venue.html:293-302
setInterval(function () {
  fetch(location.pathname, { credentials: 'omit' }).then(function (r) {
    var lm = r.headers.get('last-modified') || '';
    var t = lm && Date.parse(lm);
    if (t && pageLM && t !== pageLM) location.reload();
  })
```

`lm` is `''` forever, so `t` is falsy forever, so the branch is **dead code**.
A tab opened at 14:00 while the venue is fresh keeps showing "Svježa slika"
after the camera dies — the `fresh → late → nosignal` flips happen only when
something else forces a reload. That is a **truthfulness** failure against
PLAN §1 ("the UI never claims a frame is live when it is not"), which is why
this is P1 and not a caching nicety.

**Expected behaviour once B lands** (this is what `qa/test_pagecache.sh`
asserts):

- `GET /<slug>/` → 200 + `Last-Modified: <mtime>` + `Cache-Control: no-cache`.
- Same request with `If-Modified-Since: <future>` → **304**, no body, and the
  CSP + security headers still present.
- Same request with `If-Modified-Since: <past>` → 200 + full body.
- The still (`latest.png`) keeps its existing `ServeContent` semantics.
- A 304 must **not** be emitted for a request whose `Accept-Encoding` changed
  in a way that matters — actually it may be: the body is identical either way
  once the client revalidates. Recording it because it is the one place a
  naive implementation gets gzip wrong (`Vary` is already set).

**Secondary (same function, P2, backend's call):** `pageCache` caches one slot
keyed by `key+mtime+len(body)`. Two venues alternating at 30 s cadence evict
each other every request, so the "cached page" is recompressed per request
under multi-venue load. Correct, just not a cache. Not worth fixing before
B.

---

### Q-03 · P2 · `internal/i18n/i18n.go` import block is malformed

**Status:** OPEN — `internal/i18n/` is in this session's remit (P2), but it is
a pure-cosmetic cleanup and `make css`/`gofmt` are green, so it is deferred
until the round's real fixes land and it does not get lost in a merge.

`gofmt` accepts it (the block is internally sorted), so the gate does not
catch it:

```go
import (
	"fmt"

	"guzva/internal/venue"
	"math"
	"strings"
	"time"
)
```

A local import sits inside the stdlib group. `goimports` would rewrite it and
the next person to run `goimports -w` gets a spurious diff. Worth one line.

---

### Q-04 · P2 · the still poller and the page poller disagree about `Vary`

**Status:** OPEN — `internal/pages/static.go` (backend's B commit will touch
this file; do not fix in parallel). Recorded so B's author sees it.

`pageCache.serve` sets `Vary: Accept-Encoding` **only on the gzip branch**
(`static.go:183`). The non-gzip branch returns 200 before that line is
reached. A shared cache that stored the identity-encoded 200 and then served
it to a gzip client gets an uncompressed body with no `Vary` to explain the
mismatch — the body is still correct HTML, so the failure mode is "the page
is 4× bigger for a while", not a broken page. Setting `Vary` before the branch
fixes it. P2 because the still and page are `no-cache`, which means a shared
cache is not supposed to be storing them at all.

---

**Reproduced** by `qa/test_pagecache.sh` (case "Vary: Accept-Encoding — set on
BOTH encodings"), which now asserts both branches and reports the identity one
as a defect rather than a harness failure.

---

### Q-05 · P1 · ingest · a zero-length frame part is answered 201, not 422

**Status:** OPEN — backend-go's lane (`internal/frames/ingest.go`). Reported,
not touched. Regression row lives in `qa/test_ingest.sh`.

**Area:** device-visible honesty (§1), the same class as the dark loop: a
device that is *not* delivering frames must not be told it is.

**Repro:**

```console
$ curl -s -o /dev/null -w '%{http_code}\n' -X POST .../v1/ingest \
    -H "Authorization: Bearer $TOK" -F 'meta=@meta.json' -F 'frame=@empty.jpg'
201
```

`qa/fixtures` writes a genuine 0-byte `empty.jpg` for exactly this.

**Root cause — one layer intercepts the contract before it is checked:**

```go
// internal/frames/ingest.go:261
// Health-only beat: no frame part (camera-degraded mode, §4).
if len(frameRaw) == 0 {
	… respond(proto.StatusStored, "")   // 201
}
```

```go
// internal/render/render.go:165
if len(in.Raw) < 2 || in.Raw[0] != 0xff || in.Raw[1] != 0xd8 {
	return Output{}, Reject(proto.ReasonBadMagic)   // 422 — unreachable for 0 bytes
}
```

`readMultipart` cannot tell "no `frame` part" from "a `frame` part of length
zero": both leave `frameRaw` empty, because `readCap` returns an empty slice
rather than `nil` for a zero-length part. The health-beat branch therefore
swallows the empty-part case, and render's own `len < 2 → bad_magic` rule
never runs for it.

**Why it matters (this is the part that makes it P1):** the 201 body is
byte-identical to a legitimate camera-degraded health beat. A device whose
capture path silently yields nothing — a driver bug, a zero-byte HTTP write,
the class of failure that produced `tls_fail=12` and silent posts on 13 Oct —
gets an unqualified success, writes no quarantine record, and logs nothing.
The server's own health board stays green. The public page ages into
`nosignal` while the device believes every tick landed. That is the same
two-hour blind spot item C exists to close, reached by a different door.

**Fix shape (backend-go):** distinguish "part absent" from "part present but
empty" in `readMultipart` — return a `hasFrame bool` alongside `frameRaw`, or
have `readCap` return a distinguishable zero-length marker — and reject the
empty part with `bad_magic`. Do **not** loosen `len(frameRaw) == 0`: the
health-only path (a genuine beat with no `frame` part) is load-bearing for
camera-degraded devices and is exercised by fakecam's `partial` client.

**Test:** `qa/test_ingest.sh`, case "422 bad_magic — a ZERO-LENGTH frame part".
It asserts the 422 **and** that the response is currently byte-identical to a
real health beat, so the fix cannot silently break the health path.

---

### Q-06 · P1 · pageCache · confirmed empirically: no `Last-Modified`, no 304

**Status:** OPEN — backend-go item B. Q-02 above is the code read; this is the
measurement. Test: `qa/test_pagecache.sh`, currently skip-marked.

**Measured against a scratch server** (`qa/lib.sh`, `guzva serve` on
127.0.0.1:18080, scratch data dir under `/tmp/opencode`):

```console
$ curl -sSI http://127.0.0.1:18080/bench/ | grep -ci '^last-modified:'
0
$ curl -s -o /dev/null -w '%{http_code}\n' \
    -H 'If-Modified-Since: Wed, 01 Jan 2031 00:00:00 GMT' http://127.0.0.1:18080/bench/
200          # want 304
```

**Control — the still, same server, same process, same moment:**

```console
$ curl -sSI http://127.0.0.1:18080/bench/latest.png | grep -i '^last-modified:'
Last-Modified: Tue, 14 Oct 2026 12:46:24 GMT
$ curl -s -o /dev/null -w '%{http_code}\n' -H 'If-Modified-Since: <that value>' \
    http://127.0.0.1:18080/bench/latest.png
304
```

So the asymmetry is real and is confined to `pageCache.serve`: `ServeContent`
on the still does the right thing in the same process, at the same time, with
the same `Cache-Control: no-cache`. Nothing about the route or the deployment
explains it — only the hand-rolled `w.WriteHeader(http.StatusOK)`.

**Unconditional headers confirmed present** (so B must not regress them):
`Cache-Control: no-cache`, `Content-Security-Policy` with the inline-block
hashes, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`,
`Cross-Origin-Resource-Policy`, `Permissions-Policy`.

---

## Round 1 matrix result

`qa/test_ingest.sh` — **86 passed, 2 failing** at the time of the commit. The
two failures are real defects, not harness noise:

| row | expected | got | finding |
|---|---|---|---|
| zero-length frame part | 422 `bad_magic` | 201, identical to a health beat | Q-05 |
| `Vary` on the identity-encoded page | present | absent | Q-04 |

Everything else in the matrix passes against the current tree:

- **201/409** — good frame stored; a retry of the same `capture_id` answers
  409 with a byte-identical body modulo `server_time`; health-only beat 201;
  the web root holds exactly two files (D5).
- **401** — bad token and no header at all, and **no `desired_cfg` leaks to an
  unauthenticated caller** (an auth failure must not degrade identity, §1).
- **405** — `GET /v1/ingest`.
- **422** — `dims` for 96×96 (the 11:44 incident), 320×180 and 95×95; `dark`
  for uniform black **and** for the boundary frame at mean luma 5 (`Classify`
  is `mean < 6`, so 5 is dark and 6 would not be); `dead` for flat luma 100
  (uniform but ≥ the threshold); `truncated` for a 10-byte stub and for a
  valid JPEG cut at 200 B; `bad_magic` for an HTML body; `bad_meta` for five
  malformed meta parts; `bad_capture_id` for five malformed ids. Every one of
  them carries both the cadence hint (`next_s`) and `desired_cfg` — the
  proto's promise that a rejected tick is still a usable tick.
- **413** — oversized body, rejected on size before any decode attempt.
- **429** — a burst against the reserved device answers
  `201 201 201 201 429…` with `Retry-After: 10`, matching `proto.RetryAfterS`.
- **Privacy** — no file anywhere under the data dir begins with a JPEG SOI
  (raw frames never reach disk); the last good `latest.png` is byte-identical
  before and after two rejects (never publish a failure); 21 quarantine rows,
  headers-only, with no `path` field.

### Harness notes for whoever runs this next

Two bugs cost most of the round's wall clock and are worth not repeating:

1. **A command substitution is a subshell.** `r="$(qa_post …)"` discards any
   variable `qa_post` bumps, so a rotation counter inside it never advances
   and every post reuses `token[0]`. The symptom is *sporadic 429s in the
   middle of otherwise-correct runs*. `qa_rotate_token` keeps its cursor in a
   file (`$GUZVA_QA_DIR/tokidx`) because a file is the one thing a subshell
   cannot roll back.
2. **The per-IP floor is the binding constraint, not the per-device bucket.**
   2/s with burst 30 means a post every 0.55 s costs 1.1 tokens per 1.0
   replenished, and the deficit compounds until *everything* is refused. The
   harness posts every 0.7 s across 24 devices; the 429 row deliberately uses
   one reserved device instead.

---

### Q-02 / Q-06 · RESOLVED by `83b707d` — conditional GET verified, test now enforced

`83b707d pages: conditional GET — settings edits reach open pages (hours-edit
report)` landed 2026-10-14 15:32 and `qa/test_pagecache.sh` was flipped from
skip-marked to **enforced** in the same session.

**Result: 22 passed, 1 failing.** The one failure is Q-04, which B did not
touch. Verified working:

- `GET /<slug>/` → 200 with `Last-Modified` (mtime, truncated to the second —
  HTTP dates carry no finer resolution).
- `If-Modified-Since` in the future → **304, zero-byte body**.
- `If-Modified-Since` in the past → 200 with the full body.
- The still is untouched and still conditional (`ServeContent`).
- The web root still holds exactly two files after all of it.
- The unconditional headers survive on the 304: `Cache-Control: no-cache`,
  `nosniff`, `DENY`, `noindex`, `Referrer-Policy`, `CORP`,
  `Permissions-Policy`.

**One test assertion of mine was wrong and was corrected rather than "fixed"
in the code.** I had asserted that `Content-Security-Policy` must appear on
the 304. It does not, deliberately — the commit message says "CSP/Content-Type
are 200-only", and that is correct: RFC 9111 §4.3.4 makes a 304 *update* the
stored entry's headers, so the CSP from the 200 that populated the cache is
still in force. Sending a CSP on a bodyless 304 that nothing renders would be
cargo cult. The test now asserts the headers that genuinely must survive
(the caching/framing set) and records why the CSP is exempt.

**Q-01 status note.** Backend's item-C WIP (`mean_lua` + a hardcoded `0`) is
**no longer in the tree** — `logOutcome` is back to six parameters and no
luma value is logged at all. So Q-01 is not "fixed but wrong", it is
"not implemented": there is currently no mean-luma instrumentation anywhere.
The finding stands as the spec for that work, and the typo note matters
whenever it is written.

### Q-04 · still open after B

`Vary: Accept-Encoding` is still set only inside the gzip branch of
`pageCache.serve`, so a client that does not advertise gzip receives a 200
with no `Vary`. Confirmed again against `83b707d`. Moving the single
`w.Header().Set("Vary", …)` above the `if !acceptsGzip(r)` branch fixes it;
it is a one-line move in a file backend owns. P2, because both the page and
the still are `no-cache`, so no conforming shared cache should be storing
either — the defect only bites a misconfigured intermediary.
