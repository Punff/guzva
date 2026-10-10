# guzva — DESIGN.md implementation todo

Source: docs/design.md v1.0 (companion to PLAN v3.2 §9).
**Permission: user granted blanket approval for all of DESIGN.md plus my own
evaluation (2026-10-10).** All ten ⚑ deltas are in scope.

Sequenced by value-per-risk so that a time-out still leaves a coherent, live
system. Commit after every step.

## Stage A — foundation
- [ ] **A1 tokens.css** — role tokens + s0–s3 ramp, light/dark, data-theme,
      radius 0, stepped shadows, textures, motion (§3, §4)
- [ ] **A2 pipeline** — `public.css` (pages) + `app.css` (admin), explicit
      `@source`, size budgets in `make css-check` (§4, §13)

## Stage B — visual system on the three surfaces
- [ ] **B1 kit page** `/admin/_kit` — every component, light+dark, all states (§6)
- [ ] **B2 public page** — chips (shape+colour+text), logomark+wordmark, type
      scale, reserved box, a11y (§7.1, §7.5)
- [ ] **B3 admin** — fleet/venue/device/login/token (§8)
- [ ] **B4 owner** — Croatian, same vocabulary (§9)

## Stage C — delivery
- [ ] **C1 security headers** — CSP w/ inline hashes, Referrer-Policy, nosniff,
      Permissions-Policy, CORP, frame-ancestors (§7.4)
- [ ] **C2 ⚑7 noindex** by default on public pages
- [ ] **C3 gzip** on the fly, small in-memory cache keyed path+mtime (§13)
- [ ] **C4 budgets** enforced: public.css ≤ 8 KB, app.css ≤ 30 KB (§4)
- [ ] **C5 ⚑8 system fonts** — already true; assert it, no code needed

## Stage D — honest state
- [ ] **D1 ⚑3** two stale tiers: late (3–15 min), no-signal (>15 min) (§3.2)
- [ ] **D2 ⚑4** closed/paused ⇒ no still, no polling

## Stage E — the print (centrepiece)
- [ ] **E1 ⚑1** `poster@1` Tisak: 160×120 paletted PNG, 8 stages, `latest.png`
- [ ] **E2 ⚑2** burned-in HH:MM stamp; index.html rewritten only on state flips
- [ ] **E3** goldens + invariants + 6 KB size bound (§5.11)
- [ ] **E4 ⚑5/⚑6** calib_json + static privacy masks (≤4 rects)
- [ ] **E5 ⚑9** per-venue page language (`venues.lang`)

## Stage F — device + close-out
- [ ] **F1 ⚑10** capture ≤ QVGA, interval 45–90 s defaults
- [ ] **F2 docs** — ux.md folded into design.md; PLAN.md deltas; ops-log

## Judgement calls to flag for review
- ⚑1 lowers the still to 160×120. That is a real, visible loss of detail; the
  compensating argument is 5–10× bytes + privacy-by-resolution. Needs a real
  bench frame to judge — §5.5's numbers are synthetic.
- ⚑4 hides the still when closed. Honest and private, but a guest cannot see
  the room after hours. Deliberate, per §5.6.
- ⚑2 makes the stamp part of the image, so the image is no longer pure render
  output; a venue's picture always carries its own time.
- The still ramp is fixed in both themes (§5.4) while the UI inverts. That is
  intentional but it will look "wrong" next to a dark UI at first glance.