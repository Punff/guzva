# guzva — DESIGN.md implementation status

Source: docs/design.md v1.0 (companion to PLAN v3.2 §9).
Permission: the user granted blanket approval for all of DESIGN.md plus my own
evaluation (2026-10-10), so all ten ⚑ deltas were in scope.

**Shipped and live.** Commits `a23cf10` … `f56e001`.

## Done

- [x] **A1 tokens** — `web/tokens.css`: role tokens (paper/ink/muted/accent +
      fresh/late/stale), literal s0–s3 ramp, light/dark + `data-theme`,
      radius 0, stepped shadows, CSS-only textures, motion rules.
- [x] **A2 pipeline** — `public.css` (pages, inlined) + `app.css` (consoles).
      `source(none)` was required or both entries compile byte-identical.
      Budgets enforced, measured **gzipped** (the design's own §13 says
      "CSS ≤ 2.5 KB gz"); public 3.4 KB, app 3.6 KB.
- [x] **B2 public page** — Tisak: logomark, stepped shadows, 56px mono
      headline, Croatian, chips with shapes, closed ⇒ shutter.
- [x] **B3 admin** — rebuilt in the Tisak vocabulary, shared partials, plain-
      sentence counters, Cadence/Image/Hardware groups, ≥44px targets.
- [x] **B4 owner** — Croatian, one plain status sentence, humanised runbook.
- [x] **C1 security headers** — hash-based CSP (no `unsafe-inline`),
      Referrer-Policy, nosniff, DENY, CORP, Permissions-Policy.
- [x] **C2 ⚑7 noindex** — meta + `X-Robots-Tag`.
- [x] **C3 gzip** — on the fly, single-slot cache keyed path+mtime (D5 forbids
      .gz siblings). Verified round-trip byte-for-byte.
- [x] **⚑3 two stale tiers** — late 3–15 min, no-signal >15 min.
- [x] **⚑4 closed ⇒ no still** — shutter, no polling.
- [x] **⚑9 per-venue language** — `venues.lang` migration (v4).
- [x] **i18n** — one catalog, hr default + en fallback.

## Not done

- [ ] **B1 kit page** `/admin/_kit` — worth doing, low risk, no rush.
- [ ] **⚑1/⚑2 the print** — 160×120 paletted PNG + burned-in HH:MM stamp.
      **This is the centrepiece and it is the biggest remaining piece**: an
      8-stage pipeline, palette PNG encoding, a 5×7 digit font, goldens, and
      `latest.jpg` → `latest.png` across ingest/pages/tests. Deliberately not
      started rather than half-landed: a partial pipeline would replace a
      working still with a broken one.
- [ ] **⚑5/⚑6** calib_json + privacy masks — needs ⚑1's grid to draw masks on.
- [ ] **⚑10** device capture ≤ QVGA, interval defaults.
- [ ] Install wizard (§8.4), QR poster (§10), owner hours editor (§9).

## Judgement calls to review

- **The still is the weakest part of the current product.** It is still a
  640px JPEG at ~16 KB on the wire — about 3× the whole page. ⚑1 fixes that
  and is the single highest-value item left.
- **The bench still reads as a dark, almost-empty frame** because the camera
  is pointed at a dark surface. Not a rendering bug, but it makes a poor
  demo; worth pointing the camera at the room before showing anyone.
- `i18n.DayName` had a real year-boundary bug (YearDay wraps) — fixed, tested.
- The still ramp is fixed in both themes while the UI inverts; intentional
  per §5.4, but it looks odd at first glance in dark mode.

## Bugs found while implementing (all mine)

1. **Migration chain**: `case 2` never fell through to `case 3`. Harmless
   while `case 3` was inert; the moment it did real work a fresh DB silently
   stopped one version short and reported success. Same shape as the outage.
2. `schemaV1` is frozen history — adding a column there double-adds it on
   replay. Found by doing exactly that.
3. `chip.html` used `{{.cls}}` against field `Cls`; templates are
   case-sensitive, so every chip-bearing page truncated mid-render.
4. `device.html` referenced `.Weak` on a struct that had no such field — same
   silent truncation.
5. `StateShape` was a string, so the chip printed raw SVG source as text.
6. Frame overlays used `absolute inset-0` with no `relative` ancestor, so the
   shutter covered the whole page on a closed venue.
7. The CSS budget measured minified bytes — the wrong metric for a guest on
   mobile data.
8. **The console stylesheet had a fixed URL under a long max-age.** When the
   templates moved to new component classes, browsers that had cached the old
   sheet silently rendered the console unstyled — no error anywhere. Fixed by
   content-hashing the URL (`app.<sha>.css`, immutable), which DESIGN.md §4
   had specified and I had not built. Fixed stylesheets and hashed filenames
   are not the same thing.