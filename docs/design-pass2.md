# design pass 2 — surfaces v2 (public page, hours editor, print & privacy setup, device setup)

Written 12 Oct 2026, after the maintenance pass. Base system: docs/design.md
(Tisak, roles, shape+colour+sentence). This pass is about **composition and
connection**: the surfaces have the right pieces, but the pieces sit next to
each other instead of reading as one thing. Nothing here changes a token or
a component — it changes how the components compose, plus the two flows that
were never really designed (hours, calibration).

Status: implementing. Each section lists its acceptance checks.

---

## 1. Public page v2 — "the print and its label"

**The problem.** The state chip, the still, the big clock and the open/closed
line are four facts about ONE thing (the picture) scattered down the page.
A guest scanning a phone answers "how busy is it" from the picture, then has
to hunt for "and how old is that picture / are they even open".

**The move.** Compose the still and its facts as a mounted print with a
gallery label — one object, one border, one reading order:

```
┌────────────────────────────────────┐
│              THE PRINT             │  (reserved box, integer scale,
│              (still)               │   hatch overlays, shutter — unchanged)
├────────────────────────────────────┤
│ ● Svježa slika          14:07      │  LABEL: chip left, clock right
└────────────────────────────────────┘
  danas · CET · Otvoreno do 22:00       caption: day (JS-updated) + open state
```

- The chip keeps shape + colour + sentence; the clock moves INTO the label
  as the stamp's partner (`#hl`, mono, tabular, ~2rem). The 56 px floating
  headline is retired — a 56 px clock with no relation to the state above it
  was the "not connected" feeling. The stamp on the picture still agrees
  with the label by construction (render stage 7 + the poller).
- The label is the page's one live region (`role="status"`): state + time
  announce together, because together they are the fact.
- Caption below: `danas · CET` (poller-updated `#hlday`) + the open/closed
  sentence (server-rendered `#opentxt`; flips still reload the page).
- Then the info sections, only when they have content: **hours today**,
  **about the venue** (NEW: description + phone — §5 below), privacy footer.
- Waiting/empty state: label shows the waiting chip + em-dash clock; print
  shows dither. Closed: shutter print, closed chip, no caption claim.

**Legibility rules.** State sentence `text-base font-semibold` in the label
(was `text-sm` floating above the fold); clock `text-3xl sm:text-4xl mono
tnum`; caption `text-sm text-muted` with ≥8 px separation from the label;
phone number as a `tel:` link at `text-base` (a phone number is an action,
not a caption). No relative ages, no "Updated" wording (pinned by tests).

**Unchanged contracts.** D5 (inline CSS, two files), ⚑2 (rewrite only on
state flips — the label text is a function of the same state key), the
poller's IDs and data-* contract (only `#opentxt` moves into the caption),
per-page CSP hashing, WCAG AA, reduced-motion.

*Acceptance: page still rewrites only on flips; the CSP test passes
byte-exact; a frame swap updates label + caption without a reload; all four
states + waiting/closed render as one composed object on a 360 px viewport.*

## 2. Hours editor v2 — labeled, grouped, honest

**The problem.** Ten unlabeled time inputs in a grid: a screen reader says
"time edit" ten times; a human cannot tell shift 1 from shift 2 or open from
close without counting cells.

**The design.** Each day row becomes three zones:

```
┌──────────┬──────────────────┬──────────────────┐
│ Ponedjeljak      │ 1. smjena       │ 2. smjena        │
│ [✕] Zatvoreno    │ [09:00]–[17:00] │ [—]–[—]  neobavezno│
└──────────┴──────────────────┴──────────────────┘
```

- Each shift is a bordered fieldset with a visible legend (`hours.first`,
  `hours.second` + "neobavezno/optional" on the second). Time inputs carry
  real labels (sr-only "Open"/"Close" from the catalog) — the a11y fix the
  audit flagged, not a cosmetic.
- Field names are UNCHANGED (`h<i>_open1/close1/open2/close2/closed`) —
  `venue.ParseHoursForm` and every test keep working; this is a markup and
  label pass, not a protocol change.
- Closed stays a checkbox with a label and a 44 px target; a closed day
  greys its shift inputs (`aria-disabled` styling, still submitted so a
  stray value surfaces as the `closed_times` error instead of being
  silently dropped).
- "Copy Monday" keeps its server-side semantics; label from the catalog.
- Mobile: the three zones stack; the shift fieldsets sit side by side only
  from `sm` up.

*Acceptance: every input has a programmatically associated label; the
editor's parse errors still name the day + shift in both languages; the
owner panel (hr) and admin (en) both render the same partial.*

## 3. Print & privacy setup — the calibration flow designed

**The problem.** The privacy filter is four threshold numbers, a bias, an
invert checkbox and up to four rectangles typed as coordinates. The
installer cannot see what they are doing: masks are drawn against a mental
image of a grid that is only described in words ("grid is 160×120").

**The design.** The venue page's calibration section becomes a two-pane
setup: **preview left, controls right** (stacked on mobile).

- **Preview pane:** the venue's current print (`latest.png`) in the
  bordered frame, overlaid by a `<svg viewBox="0 0 160 120">` whose mask
  rectangles are hatched (`tx-hatch`) and move **live** as the x/y inputs
  change. Above it, a **threshold ramp**: a 4-segment bar in the s0–s3
  tones whose segment boundaries are t0/t1/t2 (and flip with *invert*) —
  the installer sees exactly how light maps to print tones.
- **Controls pane:** the same fields, the same names, the same server
  validation as today (admin.go's `setCalib` is the contract; nothing
  about it changes). The preview is **enhancement-only**: with JS off the
  form works exactly as before (§8's progressive rule).
- Copy explains the three ideas in one line each: *tones* (thresholds+bias+
  invert — how room light becomes print), *masks* (grid rectangles that are
  always black — where people sit too close), *grid* (160×120 cells; mask
  coordinates are cell numbers, not pixels).
- The preview script ships as an embedded static asset
  (`/admin/static/calib.js`, ~100 lines, no dependencies) — same-origin, so
  the W2 console CSP holds without `unsafe-inline`.

*Acceptance: typing mask coordinates redraws the overlay without a round
trip; thresholds/invert move the ramp; the form validates server-side
exactly as before; CSP test stays green.*

## 4. Device setup page — health, config, diagnostics

**The problem.** Four status tiles, a failures grid and a 21-field form sit
as three unexplained slabs. The connection between "what I set" (desired_cfg)
and "what the board did" (applied ver) is a footnote.

**The design.** Three cards, each titled with its job:

1. **Health** — status, firmware, signal, config `desired/applied` as one
   read-back line with the amber note explained inline ("the board confirms
   new config on its next POST — amber until it does").
2. **Capture & upload** — the desired_cfg form. The three groups (cadence /
   image / hardware) each get a one-line explainer of what the group trades
   off (cadence = freshness vs link budget; image = what the sensor sends;
   hardware = the flash LED). Save = bump ver (existing text).
3. **Diagnostics since boot** — counters with plain sentences (already good)
   plus the reading rule that is currently buried ("counters reset on
   reboot") at the top of the card, not under it.

*Acceptance: grouping unchanged in code (parseCfgForm untouched); a new
visitor can tell what each group does without the runbook.*

## 5. Venue info — description + phone (migration v6)

**The problem.** The public page answers "how busy" but not "what/where is
this" for the QR-scanning guest. design.md §7.1 specified an optional
description + contact line; the columns never existed.

**The design.** `venues.description` (≤200 chars, plain text) and
`venues.phone` (digits/+/spaces, ≤20) — site-plane, owner-editable,
shown on the public page only when set. Description renders as the page's
"about" paragraph; phone renders as a `tel:` link (a phone number is an
action). Both join the pages state key (an edit must reach the page on the
next tick) and the update forms in both consoles (owner in hr, admin in en).
`guzva provision` stays out of it deliberately: descriptions are written
where they're read, in the consoles.

*Acceptance: migration v6 reopens cleanly (TestReopenCurrentDatabase), an
owner edit of either field rewrites the page within one sched tick, empty
values render nothing.*

## 6. Fleet — needs attention first

The board is sorted by insertion order, so a stale venue hides below a
fresh one. Order becomes: **no-signal/stale → late → fresh → no frame yet**,
by state class then by last-seen age — the "is everything OK" screen reads
top-down (ops-log:136, W7 item 1).

---

## Out of this pass (kept honest)

- QR poster, invite-links, paused state, dark-mode toggle: W7 sequenced
  items, next pass.
- Server-side candidate rendering for calibration (render the NEXT frame
  under 3 candidate settings during a "setup session"): the right long-term
  answer for threshold tuning, but it touches ingest/render and the privacy
  argument (candidate variants are stylised, but the flow needs its own
  design + tests). The two-pane preview is the whole gain today at none of
  the risk.
- Streamed/auto-refresh admin board (15 s poll): trivial later; not now.
