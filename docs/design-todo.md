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
- [x] **B1 kit page** `/admin/_kit` — components, both themes; shapes
      resolve through the `shape()` template func — one source of truth.
- [x] **Owner hours editor** (§9) — seven rows, native time pickers, split
      shifts, closed checkboxes, copy-Monday, day-naming errors in both
      languages; the spec-string input is gone from all three forms.

## Not done

Truth pass 11 Oct 2026 (the file previously listed ⚑1/⚑2, ⚑5/⚑6 and ⚑10 as
"not done" — they shipped in `efd609e`…`f14997d`/`aa256fd`; the checkboxes
below reflect git log and ops-log:328–348, not memory):

- [x] **⚑1/⚑2 the print** — 160×120 paletted PNG + burned-in HH:MM stamp,
      live through the Tisak pipeline (`efd609e`, `c5ee954`, `ea6f3ac`,
      `888c3e5`); ring files are `.png`, pages publish `latest.png`.
- [x] **⚑5/⚑6 calib + privacy masks** — schema v5 `calib_json`, admin form
      with threshold/bias/invert validation and round-trip (`2423e4f`,
      `f14997d`); masks are applied twice around blur+mode by the pipeline.
- [x] **⚑10 device capture ≤ QVGA** — firmware defaults QVGA, 60 s
      (`aa256fd`; takes effect on physical reflash, ops-log:339–341).
- [ ] Install wizard (§8.4) — `tools/styleproof` is the CLI substitute
      (contact sheet + paste-ready calib_json); the in-console wizard is W7
      of docs/maintenance.md.
- [ ] QR poster (§10) — W7.

## Judgement calls to review

- **The chip colour layer never compiled.** `text-{{.Cls}}` in parts.html
  and `text-{{.StateClass}}` in the public page assemble class names the
  Tailwind scanner cannot see, so `text-fresh/late/stale/…` exist in
  neither stylesheet and every chip renders in inherited ink — including in
  the kit gallery, whose job was to catch exactly this. Fixed as
  maintenance W2 (explicit state classes in tokens.css).
- **`owner.fresh/late/stale` were missing from the i18n catalog** — the
  owner dash rendered the raw keys. The dash had never been verified live
  (the bench owner password was lost, ops-log:207–209). Both fixed in W2.
- **The bench still reads as a dark, almost-empty frame** because the
  camera points at a dark surface. Not a rendering bug, but it makes a poor
  demo and it proves nothing about contrast on a bright room — W8 exists
  to tune from real frames.
- The still ramp is fixed in both themes while the UI inverts; intentional
  per §5.4 (a picture shared in a chat must look identical anywhere).
- i18n: `owner.save/saved/health.old`, `alt.still/alt.nowframe`,
  `hours.title` existed but templates hard-coded the strings — wired in W2.


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