# guzva.ba — DESIGN.md (design system · UX · image style)  v1.0

Companion to `PLAN.md` v3.2 (§9 "Product surfaces & UX"; decisions D1, D5–D7, D9).
When approved it becomes `docs/ux.md` (the file §3/§9 of the plan reserve for it).

**Status:** proposal. Everything marked **⚑** changes a PLAN decision or contract and
needs your explicit approval before it is built or written into the plan (all
collected in §14). Everything else is detail *inside* decisions you already made.

**What is measured vs. assumed.** The image-style numbers in §5 come from a
*synthetic* scene (no real venue frames exist in this repo, and none should ever be
committed). They show the order of magnitude and the failure modes, not final
sizes. Phase-1 exit data on real frames replaces them (§5.11).

---

## 1. Context that shapes the design

| Constraint | Source | Design consequence |
|---|---|---|
| Guest = a phone on mobile data (BH Telecom / Telemach / m:tel), arriving from a QR code or a message | PLAN §9.2, §13 | Mobile-first. First view ≤ ~11 KB on the wire. No webfont, no framework. |
| Owner = a bar/shop phone, mixed digital skill | §9.4, §13 | Owner console is Croatian, one-handed, big targets, plain sentences. |
| Admin = you, often **at the install, on a phone** | §10 | The install wizard is a first-class mobile screen with live gauges. |
| e2-micro: 964 MB RAM, shared vCPU | §8 | Public path does zero template/image work per request (D5). Pre-render on change only. |
| ~1 GB/month free egress, US region, 150–200 ms RTT to Bosnia | D4, §13 | Tiny still; conditional requests; poll only while the tab is visible; stop when idle. |
| Web root = two files per venue, CSS inlined | D5 | Public CSS+JS inline, system fonts only, no extra files. The still changes format ⚑ (§5.5). |
| Croatian, `dd.mm.yyyy.`, 24 h, `Europe/Sarajevo` | D7 | One message catalog; no hand-written strings in handlers. |
| No biometrics, ever | D9 | No detection-based features. Privacy masks are **static geometry** only. |
| htmx + Tailwind, no SPA | D6 | htmx on consoles only. Public page = vanilla JS ≤ 1.5 KB. |
| Solo builder | §16.13 | Few components, copy-paste class recipes, one kit page to eyeball them all. |

**Regional notes that matter:** screenshots of the page circulate in group chats
(Viber/WhatsApp are widely used in the region), so the picture must date itself
(§5.6). Tourist venues (guesthouses, hotels) may want English: handled per venue,
not with a toggle (§11). Cyrillic is out of scope for v1.

---

## 2. Principles

1. **Honest.** Never imply liveness. Every state has a name, a shape *and* a
   sentence; colour is never the only signal (PLAN §1).
2. **Tiny.** Every byte is paid for in egress and RTT. If it needs a font, an icon
   file or a library, it needs a reason.
3. **Quiet.** The page answers one question — *is it busy right now?* — and gets out
   of the way. No cookie banner (no cookies), no popups, no tracking.
4. **Own look, from the product's own material.** The picture is a 4-tone print; the
   whole interface is built from those four tones plus one signal colour.
5. **Mobile-first, thumb-first.** 44 px targets, one column, content before chrome.
6. **One system, three surfaces.** Public page, owner console, admin share tokens,
   components and copy rules; they differ in density, not in vocabulary.

---

## 3. Visual language — "Tisak" (print)

**Concept.** The still is a **cyanotype-style blue print on cream paper** at a visible
pixel grid; the UI is the *paper around the print*: cream surfaces, ink-blue
borders with hard stepped shadows (no blur), and a single barber-pole **red** as the
only signal colour. Everything sits on a **4 px grid** and the one illustrative
motif is the pixel (logomark, status shapes, hatch and dither textures — all drawn
in CSS/SVG, zero image bytes).

### 3.1 Colour tokens

Light theme — the ONLY theme (user decision, Oct 2026: the dark theme and
the `data-theme` toggle plan were dropped; `color-scheme: light`):

| Token | Hex | Role | Contrast (on `paper` / `paper-2`) |
|---|---|---|---|
| `paper` | `#F2EDE2` | page background | — |
| `paper-2` | `#E8E1D2` | raised surfaces, table stripes | — |
| `ink` | `#12202D` | text, borders, shadows | 14.2 / 12.7 |
| `muted` | `#46535F` | secondary text | 6.8 / 6.1 |
| `accent` | `#AE2B16` | primary actions, focus, "signal" | 5.7 / 5.1 |
| `fresh` | `#15694A` | status: fresh | 5.7 / 5.1 |
| `late` | `#7F5000` | status: late | 5.9 / 5.3 |
| `stale` | `#4F5B66` | status: no signal | 6.0 / 5.3 |

Button label on `accent`: `paper` 5.7 : 1. Text on `ink` buttons: `paper` 14.2 : 1.

The still ramp is **fixed** (§5.4) and always sits on its own paper
card, so the picture looks identical wherever it is opened and shared
screenshots stay consistent.

### 3.2 Status semantics (colour + shape + label)

| State | Shape (12 px SVG) | Colour | Label (hr) | Still treatment |
|---|---|---|---|---|
| Fresh | filled square | `fresh` | Svježa slika | none |
| Late (3–15 min ⚑) | half-filled square | `late` | Slika kasni | light hatch |
| No signal (>15 min ⚑) | hatched square | `stale` | Nema nove slike | strong hatch overlay |
| Closed | shutter bars | `ink` | Zatvoreno | **no still shown** |
| Paused | two bars | `muted` | Salon je na pauzi | **no still shown** |
| Waiting (no frame yet) | dither square | `muted` | Čekamo prvu sliku… | dither skeleton |

PLAN §6.6 says "STALE after >3 min"; the two tiers are both STALE — the second tier
reuses the plan's own 15-minute alert threshold, so the page and the alert agree.

### 3.3 Spacing, borders, depth, shape

- Base unit **4 px**. Scale: 4 · 8 · 12 · 16 · 24 · 32 · 48 · 64.
- **Radius 0.** Square everything. (Pixel-notched corners via `clip-path` were
  rejected: they clip box-shadows and need `drop-shadow` filters, which are costly on
  low-end phones.)
- Borders **2 px `ink`**. Depth = hard offset shadow `4px 4px 0 ink` (`2px 2px 0` for
  small controls). No blur anywhere.
- Press state: shadow collapses to `0` and the element moves `2px 2px` (a physical
  "button press"), 80 ms, `steps(2)`.
- Focus ring: `outline: 3px solid accent; outline-offset: 2px` plus a 1 px `paper`
  inner gap so it reads on any surface.
- Textures (CSS only, ~100 B each): **hatch** `repeating-linear-gradient(135deg,
  transparent 0 3px, var(--s1) 3px 4px)`; **dither** 8 px two-tone checkerboard via
  `conic-gradient`. Used for skeletons, stale overlays, empty states, section rules.

### 3.4 Type — system fonts only

No webfonts (D5 forbids a third file; inlining ~20 KB of font in every page is worse
than a system face; system fonts cover **č ć š ž đ** by construction).

```
sans:  ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif
mono:  ui-monospace, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace
```

| Role | Size / line | Weight | Notes |
|---|---|---|---|
| Time headline | 56 / 1 px (mono) | 700 | `font-variant-numeric: tabular-nums` |
| H1 venue name | 32 / 36 (md 40 / 44) | 700 | tracking −0.01em, wraps, never truncates |
| H2 | 24 / 28 | 700 | |
| Body | 16 / 24 | 400 | never below 16 px on mobile (no iOS zoom on inputs) |
| Caption / micro-label | 13 / 18 | 600 | uppercase labels get +0.08em tracking |

The personality comes from palette, grid, shadows, textures and the pixelated still —
not from a typeface. A subsetted display face for the *consoles only* is a possible
later upgrade (§14, not proposed now).

### 3.5 Logomark and wordmark

"The tally label" — a thermal label printing three rising crowd bars, the peak in
signal red: *is there a crowd?* Ink tile, paper label, perforated tear edge along the
bottom. 16×16 grid (1 cell = 1 px at the 16 px favicon), `shape-rendering: crispEdges`.
Source in Appendix C. Used as inline SVG favicon (`data:` URI, both consoles) and in
the public page header at 32 px (2 px per cell). Render at multiples of 16 px so
every cell stays exactly one pixel.

Wordmark: the text **guzva** (lowercase, spelled like the domain) in the system sans
at 700, beside the mark. Prose and tagline keep the proper spelling: **gužva**,
"Ima li gužve?".

### 3.6 Motion

- Default: nearly none. New frame = instant swap (crisp, no crossfade) + the time
  chip does a 3-step opacity blink (`steps(3)`, 240 ms).
- Skeleton: static dither (no shimmer).
- `prefers-reduced-motion: reduce` removes all animation, keeps all information.

---

## 4. Tokens and Tailwind

Tailwind compiled once at build with the **standalone CLI** (no Node), `go:embed`ed
(D6). Two entry files so the public page stays tiny:

```
web/
  tokens.css        # variables (this section) — imported by both
  public.css        # @source ../internal/pages/*.html   → inlined into index.html
  app.css           # @source ../internal/admin/**/*.html → served /admin/static/app.[hash].css
  app.[hash].css    # generated, embedded, `immutable`
```

Tailwind v4 CSS-first syntax (if you are on v3, the same tokens go into
`tailwind.config.js` `theme.extend`):

```css
@import "tailwindcss";

@theme {
  --color-*: initial;                       /* drop the default palette: smaller CSS */
  --color-paper: var(--paper);  --color-paper-2: var(--paper-2);
  --color-ink: var(--ink);      --color-muted: var(--muted);
  --color-accent: var(--accent);
  --color-fresh: var(--fresh);  --color-late: var(--late);  --color-stale: var(--stale);
  --color-s0: #12202D; --color-s1: #33607F; --color-s2: #7EA5C0; --color-s3: #F2EDE2;
  --radius-*: initial;                      /* squares only */
  --shadow-step: 4px 4px 0 0 var(--ink);
  --shadow-step-sm: 2px 2px 0 0 var(--ink);
  --font-sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  --font-mono: ui-monospace, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
  --breakpoint-sm: 30rem; --breakpoint-md: 48rem; --breakpoint-lg: 64rem;
}

:root { --paper:#F2EDE2; --paper-2:#E8E1D2; --ink:#12202D; --muted:#46535F;
        --accent:#AE2B16; --fresh:#15694A; --late:#7F5000; --stale:#4F5B66;
        color-scheme: light; }
```

The semantic names (`paper`, `ink`) are *roles*, not colours. `s0–s3` are
literal and never change.

**Rules that keep CSS small and predictable**
- Never build class names by string concatenation in Go; write full class names so
  the scanner sees them (state classes via a lookup map of whole strings).
- No `@apply` towers; components are ~12 template partials with their utility lists
  (§6), not a CSS component layer.
- Budgets (Makefile.web.mk fails above): `public.css` ≤ **4 KB gzipped**, `app.css` ≤ **12 KB gzipped** (§13 states the 2.5 KB gz ambition; the enforced gate carries the rationale);
  `app.css` ≤ **30 KB minified**.
- `make css` is part of `make build`; CI fails if generated CSS is stale (plan §12).

---

## 5. The image style — "Tisak" and "Šablon"

PLAN names two presets, `poster` (4-level) and `mono`. Their registry IDs stay;
their display names are **Tisak** (print, 4 tones) and **Šablon** (stencil, 2 tones).

### 5.1 Goals, non-goals

**Goals**
1. **Privacy by construction.** Information is destroyed irreversibly in the first
   pipeline stages; what remains cannot be sharpened back into a face.
2. **Legibility.** From a glance: how many people are in the room and roughly where.
3. **Brand.** The same four tones colour the whole site.
4. **Tiny and deterministic.** Pure function, byte-reproducible, cheap on a shared vCPU.

**Non-goals.** Recognising anything: no face/people/plate detection or counting
(D9). Nothing here "finds" a person; the style just *coarsens everything equally*.

### 5.2 Pipeline (`poster@2`)

All stages are pure, integer-friendly, stdlib-only. Input: decoded JPEG; output:
`160×120` paletted PNG.

| # | Stage | Parameters | Why |
|---|---|---|---|
| 1 | Take **luminance only** (the `Y` plane of the decoded `YCbCr`; any other colour model → `color.GrayModel`) | — | Colour (skin, hair, clothes, branding) is discarded at the first step and never converted. EXIF is already gone. |
| 2 | **Area-average** to a **160×120 grid** (4:3 inputs only; exact for 160/320/640/800/1600 wide, fixed-point Q8 edge weights otherwise; non-4:3 → `422 dims`) | grid = 160×120 = 19,200 cells | The mosaic *is* the grid. 4× oversampling from VGA gives clean averaging; QVGA (2×) is plenty. |
| 3 | **Privacy masks** — overwrite listed rectangles with a constant luma | ≤ 4 rects in grid coords ⚑ | Static, installer-drawn geometry for regions where people sit too close (§5.3). Applied *before* blur so nothing inside leaks — and **again after the mode filter** in the shipped pipeline (edge bleed would otherwise smuggle gradients back; invariant-tested). |
| 4 | **Blur** 3×3 binomial `[1 2 1]²/16` | σ ≈ 0.85 cells | Kills sub-cell detail and sensor noise before thresholding. |
| 5 | **Quantise** to 4 tones with thresholds `t = (64, 128, 192)` by default; per-venue `calib` overrides (`t`, `bias`, `invert`) ⚑ | 2 bits/cell | Flat bands, ~2 bits of tone. Fixed thresholds: **no per-frame auto-levels** (§5.10). |
| 6 | **Mode filter** 3×3 (majority; ties keep the centre) | — | Removes isolated speckle where a smooth area sits on a threshold. |
| 7 | **Timestamp stamp** — burn `HH:MM` (venue-local) into the bottom-left | 5×7 digit font, `S3` digits on `S0` plate, 29×11 cells ≈ 1.7 % of the area ⚑ | A screenshot shared in a chat dates itself; also removes any per-frame HTML rewrite (§7.3). |
| 8 | **Encode** paletted PNG, 4 fixed palette entries, best compression | Go's encoder picks 2-bit depth for ≤4 colours; no ancillary chunks | Tiny, lossless, no JPEG ringing on flat art. |

`mono@2` (Šablon): same stages with 2 tones (`S0`/`S3`), one threshold, 1-bit PNG.

A note on versions: the shipped presets are `@2`. Version 1 was the 640-px
JPEG era; versions were bumped when the Tisak pipeline landed, so old
sidecar rows keep meaning what they recorded (PLAN D2 outranks this doc
where the two disagree).

Sketch of the package surface (not final code):

```go
// internal/render/tisak.go — poster@2
const GW, GH = 160, 120

func lumaGrid(img image.Image) (g []uint8, err error)  // stages 1–2
func applyMasks(g []uint8, m []Rect)                   // 3
func blur121(g []uint8) []uint8                        // 4
func quant(g []uint8, t [3]uint8, invert bool) []uint8 // 5
func mode3(q []uint8, levels int) []uint8              // 6
func stamp(q []uint8, hhmm string)                     // 7
func encode(q []uint8) []byte                          // 8  (palette = fixed S0..S3)
```

`Preset.Render` stays pure: inputs are the luma grid, the venue's `calib`, and the
venue-local `HH:MM` string — all passed in, so determinism ×100 and goldens still hold.

### 5.3 What it removes — and the install geometry gate

| Removed | How |
|---|---|
| Colour | luminance-only from stage 1 |
| Fine detail | 160×120 grid + blur; a VGA frame has 16× more samples |
| Tonal detail | 4 tones (2 bits) |
| Metadata | EXIF dropped by construction, PNG has no ancillary chunks (asserted) |
| Close-up regions | static privacy masks (stage 3) |

What remains: silhouettes, posture-scale shapes, light/dark regions.

**Honest limit.** Familiar faces can be recognised at surprisingly few pixels, so the
style alone is not the privacy argument; **camera placement is part of it**. The
install gate (PLAN §10 "owner sign-off on the framing") gets one measurable rule:

> At the nearest seat, a head must be **≤ 10 cells wide** on the 160-cell frame.

With head width 0.16 m and horizontal field of view θ, the minimum distance is

```
d_min = 1.28 / tan(θ/2)   metres
```

| Horizontal FOV θ | d_min |
|---|---|
| ≈ 54° (typical OV2640 module, often quoted ≈65° diagonal — measure yours) | ≈ 2.5 m |
| 90° | ≈ 1.3 m |
| 120° | ≈ 0.7 m |

The wizard (§8.4) overlays a 10-cell ruler on the stylised preview so the installer
checks this by eye; where the nearest seat violates it, they draw a mask rectangle
over that seat. This is a design target, not a legal guarantee (PLAN §13 stands:
re-check with a local adviser before the first paying venue).

### 5.4 Palette (fixed)

| Level | Name | Hex | L* | Role |
|---|---|---|---|---|
| 0 | `s0` ink | `#12202D` | 11.6 | darkest: people, hair, furniture shadows, stamp plate |
| 1 | `s1` | `#33607F` | 38.9 | dark mid |
| 2 | `s2` | `#7EA5C0` | 65.9 | light mid |
| 3 | `s3` paper | `#F2EDE2` | 93.9 | lightest: walls, windows, stamp digits |

Steps are even in lightness (27.3 / 27.0 / 28.0 L*), so each tone is an equally
distinguishable band; adjacent-tone contrast is 2.5 / 2.6 / 2.2 : 1, intentionally
"image-like", not text-like. Palette order is part of the preset version: changing a
colour ships as `poster@2`.

### 5.5 Output and bytes (⚑ replaces the 640×q68 JPEG)

PLAN §7 currently encodes the public render as 640 w JPEG q68. Proposal: the public
file is the **160×120 paletted PNG**, shown 2–4× with `image-rendering: pixelated`.

Synthetic-scene results (indicative only — real frames will land somewhere in or
above this range; plan on **3 KB**):

| Output | clean scene | busy/textured scene |
|---|---|---|
| Current plan: 4-tone, 640×480, JPEG q68 | 8.8 KB | 15.8–20.8 KB |
| **Tisak: 160×120, 4-tone, 2-bit PNG** | **0.7 KB** | **1.7–2.1 KB** |
| Bayer-dithered 4-tone PNG (rejected) | 0.9 KB | 2.7–3.4 KB |
| **Šablon: 160×120, 1-bit PNG** | **0.3 KB** | **0.6–1.0 KB** |

Roughly 5–10× smaller than the planned JPEG, which matters twice: egress (§13) and a
weak link — a 3 KB upload-side-effect-free download to guests, and the 48 h ring
shrinks to ~9 MB per venue at a 60 s interval.

Consequences to approve together (⚑): the web-root file is `latest.png` instead of
`latest.jpg`; the web-root allow-list test and the "thumb output" deferral are
updated accordingly (a thumbnail is pointless at 3 KB).

Also worth knowing on the device side: the 09 Oct log shows a **3,964-byte** capture,
so uploads are already small. Since the render grid is 160×120, there is no benefit
in the device ever capturing above QVGA; capturing smaller keeps uploads cheap on the
weak link. (The `scDefault` comment in `main.cpp` says `framesize 4 = FRAMESIZE_VGA`
while a ~4 KB JPEG suggests a smaller size — worth confirming the enum mapping in
your esp32-camera version. Low priority.)

### 5.6 Stale, closed and "waiting" treatments

- **Late** (3–15 min): the PNG is unchanged; the page overlays a light hatch
  (`--s1` lines, 1 px every 4) and swaps the badge.
- **No signal** (>15 min): strong overlay — paper at ~55 % + the hatch — so the old
  still reads as *old*. The stamp stays legible because it is part of the image and
  sits on a 14 : 1 plate.
- **Closed / paused**: **no still is shown at all** and polling stops. Privacy (no
  after-hours interior), honesty (nothing to claim), and bytes. A CSS shutter
  (horizontal ink bars on `paper-2`) fills the frame box. ⚑ (§14)
- **Waiting for first frame**: dither skeleton in the same 4:3 box.

The **stamp** (stage 7) is the keystone of honesty: every picture carries its own
time, wherever it is shared. Venue-local time, 24 h, no date (the stale tiers and
the headline handle "when").

### 5.7 Display rules

```html
<img id="still" src="latest.png" width="160" height="120"
     alt="Stilizirana slika prostora Salon Kadrić, snimljena u 21:48"
     class="block w-[calc(160px*var(--k))] [image-rendering:pixelated]">
```

- Integer scale `k`: `--k:2` below 480 px (320 px wide — fits a 360 px phone with
  18 px margins), `3` from 480, `4` from 768. Non-integer scales make uneven "pixels".
- Reserve the box (`width`/`height` + `aspect-ratio: 4/3`): zero layout shift.
- Border 2 px `ink` + `shadow-step`; the card behind it is always `s3` paper.
- High-DPR phones (3×) scale 160→960 device px exactly; at fractional DPRs
  `pixelated` stays crisp with ≤1-device-px unevenness — accepted.

### 5.8 Presets registry and versioning

`poster@2` (Tisak), `mono@2` (Šablon). Sidecars record `preset@version`, as in
PLAN §7. Style *and* version changes apply to new frames only. New style = one file +
one registry line + one golden test (unchanged contract).

### 5.9 Per-venue calibration (⚑ `venues.calib_json`)

Default thresholds 64/128/192 work when the device's auto-exposure keeps the scene
near mid-grey. Where they don't (dark floors, bright windows), the **calibration
step** in the wizard (§8.4) helps *without ever storing a raw frame*:

1. Admin puts the venue in "calibrate" for ~2 minutes.
2. Each incoming frame is rendered in RAM into **candidate stylised sheets**
   (bias −24 / 0 / +24 × Tisak/Šablon) and a 256-bin histogram; only the *candidate
   PNGs* (stylised, allowed at rest) and the histogram numbers are kept under
   `data/ref/candidates/`.
3. The installer taps the best candidate; the venue stores
   `calib_json = {"t":[a,b,c],"bias":0,"invert":false,"masks":[…],"preset":"poster"}`.

Suggested helper: place thresholds in histogram valleys (multi-Otsu) as the *default
candidate*. All of it is numbers and stylised images — consistent with PLAN §1 privacy.
Migration: one nullable column (the plan's migration rule: exercised across a reopen).

### 5.10 Considered and rejected

| Idea | Why not |
|---|---|
| Highlight people by diffing against a reference frame ("silhouettes in red") | It is people detection in all but name → D9. |
| Per-frame auto-levels / histogram equalisation | Brightness pumps between frames and destroys information (an empty room and a full one would get the same tone mix). |
| Dithering by default | Noisier, 40–60 % bigger, harder to read at tiny sizes. Kept as a possible `poster@2` variant only if real frames look muddy. |
| JPEG for the stylised output | Ringing on flat art, 5–10× the bytes. |
| Blur-only anonymisation | Blur is reversible-ish and keeps colour/gradients. We quantise too. |
| Face blurring | Needs a detector → D9. |
| Higher default resolution (e.g. 320×240) | Weakens privacy and multiplies bytes for no occupancy-legibility gain. A future `grid` parameter would ship as a new preset version. |
| Colour output | Skin/clothing colour is identifying and costs bytes. |

### 5.11 Tests and acceptance

Automated (additions to PLAN §7's list):
- **Golden PNGs** byte-for-byte, from **procedurally generated** fixtures. Never commit
  real venue frames.
- **Invariants:** exactly 160×120; ≤ 4 palette entries in fixed order; no ancillary
  chunks (`tEXt/eXIf/iCCP`); stamp cells only in the stamp rectangle; mask cells are
  level 0 whatever the input.
- **Size bound:** a render > 6 KB fails the test (guards accidental dither/noise).
- **Determinism ×100**, race hammer, corrupt-corpus — as planned.

Human (Phase 2 pilot, with the venue's permission, frames viewed only locally):
- **Glance test:** 5 people who don't know the venue read 10 stylised frames with
  known headcounts; pass = ≥ 90 % within ±1 person.
- **Geometry gate:** the 10-cell head check at sign-off, recorded on the venue.
- **Tool:** `tools/styleproof` renders a local JPEG into the contact sheet of
  candidates (what the wizard does live), so parameters are chosen from real frames,
  not guessed here.

---

## 6. Components

Twelve template partials; each is a utility list, not a CSS class zoo. (Class
recipes below use v4 names; `ink`/`paper` are the *role* tokens of §4.)

| Component | Recipe (Tailwind) | Notes |
|---|---|---|
| **Card ("Okvir")** | `border-2 border-ink bg-paper shadow-step p-4` | Raised surface. Nested cards use `bg-paper-2`, no shadow. |
| **Still frame** | card + `p-0 bg-s3` + `img` of §5.7 | Always `s3` behind the print. |
| **Status chip** | `inline-flex items-center gap-2 border-2 border-ink bg-paper px-2 py-1 text-sm font-semibold` + 12 px SVG shape | Shape + colour + text; text always `ink`, colour only on the shape. |
| **Button primary** | `bg-accent text-paper border-2 border-ink shadow-step-sm px-4 min-h-11 font-semibold active:translate-x-0.5 active:translate-y-0.5 active:shadow-none` | 44 px min height. |
| **Button secondary** | as primary with `bg-paper text-ink` | |
| **Button danger** | outlined `border-accent text-accent`, confirm step | Revoke/offboard need a second tap. |
| **Input / select** | `border-2 border-ink bg-paper px-3 min-h-11 text-base` | 16 px: no iOS zoom. Error = `border-accent` + message + icon. |
| **Toggle / checkbox** | pixel square (SVG sprite), 24 px hit area ≥ 44 px | Real `<input>`, visually restyled; works without JS. |
| **Table (dense)** | `w-full text-sm border-2 border-ink` stripes `odd:bg-paper-2` | Below `md` it becomes stacked cards (§8.1). |
| **Meter (RSSI)** | 4 pixel bars (SVG) + number | Number always shown. |
| **Flash / toast** | card + status shape, `role="status"` | Server-rendered after POST; auto-dismiss only for success. |
| **Empty state** | dither block + one sentence + one action | Never a bare "No data". |
| **Skeleton** | dither checkerboard, static | Matches the final box exactly. |

A **kit page** (`/admin/_kit`, dev builds only) renders every component in
each state; it is the visual regression check for a solo builder and the
first thing to build (§15 step 1).

---

## 7. Public page — the product's face

### 7.1 Anatomy (mobile, 360 px)

```
┌────────────────────────────────────────┐
│ ▣ guzva               Ima li gužve?    │  header: mark + wordmark, tagline right
├────────────────────────────────────────┤
│ Muški Frizerski Salon Jadro            │  H1, venue name, display size
│ ■ Svježa slika      Otvoreno do 20:00  │  status row: chip + open line, plain
│ ┌────────────────────────────────────┐ │
│ │  ┌──────────────────────────────┐  │ │
│ │  │   160×120 still @ k=2        │  │ │  mounted print: hard border,
│ │  │   (no clock in the picture)  │  │ │  paper mat, step shadow;
│ │  └──────────────────────────────┘  │ │  the picture never holds a time
│ └────────────────────────────────────┘ │
│  21:48                                 │  the ONE clock (role=status)
│  Zadnja slika je iz 21:48.             │  state detail, only when honest
│ ┌────────────────────────────────────┐ │
│ │ RADNO VRIJEME                      │ │  hours card, letterspaced heading
│ │ Ponedjeljak          08:00–20:00   │ │  day left, mono times right
│ │ Utorak · danas       08:00–20:00   │ │  today bold + aria-current
│ │ Subota                      —      │ │  closed: gray + dash
│ └────────────────────────────────────┘ │
│ ┌────────────────────────────────────┐ │
│ │ O SALONU                           │ │  info card, same treatment
│ │ +387 61 234 567                    │ │  tel: link (no call button)
│ │ Ul. Mira 5, 71000 Sarajevo         │ │  location
│ │ Frizerski salon — ≤140 ch          │ │  description
│ └────────────────────────────────────┘ │
│ ▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨▨ │  hatched divider
│ Slika je namjerno grubo stilizirana    │  privacy line (PLAN §13),
│ radi privatnosti. Izvorne fotografije  │  no link — no target exists
│ se ne pohranjuju.                      │
└────────────────────────────────────────┘
```

Desktop: same single column, max width 40 rem, still at k=4 (640 px).
Reserved slot under the still for the Phase-3 "Dolazim" action (hidden until built).

### 7.2 State matrix

| State | Status chip | Frame area | Extra line | Polling |
|---|---|---|---|---|
| Fresh + open | ■ Svježa slika | still | (open line beside the chip) | yes |
| Late | ◧ Slika kasni | still + light hatch | Zadnja slika je iz {time}. | yes |
| No signal | ▨ Nema nove slike | still + strong hatch | Zadnja slika je iz {time}. Ne mora prikazivati trenutno stanje. | yes (slower) |
| Closed | ▬ Zatvoreno | CSS shutter, **no still** | Otvara se {sutra} u {time} | no (long `meta refresh`) |
| Paused | ‖ Salon je na pauzi | CSS shutter, no still | — | no |
| Waiting | ░ Čekamo prvu sliku… | dither skeleton | — | yes |
| Client offline | (chip unchanged) | still unchanged | banner: Nema internetske veze. Pokušavamo ponovno… | backoff 30→120 s |
| Idle >10 min | (chip unchanged) | still unchanged | Osvježavanje je pauzirano. [Nastavi] | stopped until tap |
| JS off | as served | still | — | `<noscript>` meta refresh 60 s |

### 7.3 Delivery protocol (⚑ refines PLAN §6.4)

Goal: the cheapest possible honest page in a 1 GB/month world.

- `index.html` contains **everything static for the current state** (state chip,
  hours, description, inline CSS + JS, inline favicon). It is rewritten **only on a
  state flip** (late / no signal / closed / open / paused / waiting) or a settings
  edit — **not per frame**, because the label clock is re-derived per frame from
  the still's `Last-Modified` (the still carries no burned-in time, ⚑2).
- Per frame, the browser does one *conditional* request for `latest.png`:
  `fetch('latest.png', {cache:'no-cache'})` → **304 ≈ 0.3 KB** when nothing changed,
  **200 ≈ 1–3 KB** when a new frame arrived. The `Last-Modified` header (= the
  frame's `received_at`) drives the live time below the print — the page's one
  and only clock — via `Intl` in the venue's timezone (`data-tz`).
- One more conditional request for `index.html` every 60 s. If its `Last-Modified`
  changed, **reload the page** — state flips are rare, so a full reload is simpler
  and cheaper than DOM diffing. (Tier changes therefore reach a visitor within
  ~90 s of the 30 s scheduler tick; acceptable.)
- Polling runs only while `document.visibilityState === 'visible'`, stops after
  10 min idle (tap to resume), backs off on network errors.
- The new still is shown from the fetched `blob:` (one transfer, no second image
  request). CSP therefore allows `img-src 'self' blob:`.

Poller sketch (≈1 KB minified; not final):

```js
(() => {
  const d = document.body.dataset, img = $('still'), hl = $('hl');
  const poll = +d.poll || 30, fmt = new Intl.DateTimeFormat('hr-HR',
      {timeZone: d.tz, hour: '2-digit', minute: '2-digit', hour12: false});
  let last = '', prev = '', backoff = poll, idleAt = Date.now() + 6e5, page = '';
  async function tick() {
    if (document.hidden) return later();
    if (Date.now() > idleAt) return showIdle();
    try {
      const r = await fetch('latest.png', {cache: 'no-cache'});
      const lm = r.headers.get('last-modified');
      if (lm && lm !== last) {
        last = lm; URL.revokeObjectURL(prev);
        img.src = prev = URL.createObjectURL(await r.blob());
        hl.textContent = headline(new Date(lm), fmt);      // 21:48 · danas · CET
      }
      backoff = poll; banner(false);
    } catch (e) { backoff = Math.min(backoff * 2, 120); banner(true); }
    later();
  }
  // every 60 s: conditional GET of index.html; if Last-Modified changed → location.reload()
})();
```

**No-JS fallback:** `<noscript><meta http-equiv="refresh" content="60"></noscript>`.
The page stays honest because the stamp is inside the image.

### 7.4 Head, metadata, security headers

- `lang="hr"`; `<meta name="viewport" content="width=device-width,initial-scale=1">`.
- `<title>{Venue} — Ima li gužve? · guzva.ba`; description from the venue
  description or a default sentence.
- `theme-color` `#F2EDE2`; inline SVG favicon (§3.5) as a `data:` URI.
- **Indexing: default `noindex, nofollow`** (meta + `X-Robots-Tag`) ⚑ — a venue's
  interior page shouldn't surface in search by default; an owner opt-in can come later.
- Open Graph: **text-only** card (title, description) with the shared static brand
  image served from the embedded assets (outside the venue web root). A stylised 160×120
  still is below the minimum size most previews want.
- Headers: `Content-Security-Policy: default-src 'none'; img-src 'self' blob: data:;
  style-src 'sha256-…'; script-src 'sha256-…'; connect-src 'self'; base-uri 'none';
  form-action 'none'; frame-ancestors 'none'` (hashes of the two inline blocks, which
  are identical for every venue because per-venue data lives in `data-*` attributes);
  `Referrer-Policy: no-referrer`; `X-Content-Type-Options: nosniff`;
  `Permissions-Policy: camera=(), microphone=(), geolocation=()`;
  `Cross-Origin-Resource-Policy: same-origin`.

### 7.5 Accessibility

- Status chip is text, not colour; shapes are `aria-hidden`.
- Headline and chip live in one `aria-live="polite"` region updated **per new frame
  only** (not per poll).
- `alt` names the venue and the time and is updated with each frame.
- Focusable: the idle "Nastavi" button, the privacy link, `tel:` link. Skip link not
  needed (a page this short), landmarks `header/main/footer` kept.

---

## 8. Admin console (`/admin`, English)

Dense, keyboard-friendly, usable one-handed on a phone at an install. Server-rendered
Go templates; every action is a plain form POST + redirect (works without JS); htmx
only to *refresh regions* and show live install data.

### 8.1 Fleet overview (home)

```
guzva admin   Fleet  Venues  Activity                        ● 3 need attention
──────────────────────────────────────────────────────────────────────────────
Sort: needs attention ▾     Show: all ▾                       updated 21:49:12
┌──────────────┬──────────────┬───────────┬──────────┬─────────────┬─────────┐
│ Venue        │ State        │ Last img  │ Signal   │ Config      │ Alert   │
├──────────────┼──────────────┼───────────┼──────────┼─────────────┼─────────┤
│ Salon Kadrić │ ▨ No signal  │ 21:20     │ ▂▃▁ −76  │ v12→v13 ⚠   │ stale   │
│ Café Dino    │ ◧ Late       │ 21:44     │ ▂▃▄ −68  │ v9 ✓        │ —       │
│ Pansion Una  │ ■ Fresh      │ 21:48     │ ▂▃▄▅−58  │ v4 ✓        │ —       │
│ Hotel Plava  │ ‖ Paused     │ —         │ —        │ v2 ✓        │ muted   │
└──────────────┴──────────────┴───────────┴──────────┴─────────────┴─────────┘
```

- Sorted by "needs attention": no signal → late → config mismatch (amber) →
  open-hours-closed anomalies → fresh → paused.
- Region refresh: `hx-get="/admin/fleet/rows" hx-trigger="every 15s [!document.hidden]"`.
- Below `md` the table becomes stacked cards (venue, state chip, last image, RSSI).
- Every number is plain: "28 min ago", never `b181·bo4·j0`.

### 8.2 Venue page

Tabs as plain links: **Overview · Hours & details · Owner · Device · Install ·
Timeline · Poster**. Overview shows the public page in an `<iframe>`-free preview
(the still + chip, same partials as the public page), the next action ("install gate:
signal not yet passed"), and the plane split: *site-plane (owner may edit)* vs
*device-plane (admin only)*, labelled as such.

### 8.3 Device page

Grouped `desired_cfg` form (not a 22-row table), each field with unit, range, default,
inline validation (ranges pinned to `docs/proto-v1.md` by the existing test):

- **Cadence** — interval (30–300 s; recommended 45–90 s).
- **Image** — framesize, quality, brightness/contrast/saturation/sharpness, denoise,
  effect, white balance, exposure (AEC/AE level), gain (AGC/gain), mirror/flip, DCW,
  lens correction.
- **Hardware** — flash LED PWM, test pattern.

A pending change shows `v12 → v13` in amber until the device echoes it (read-back
contract). Counters are explained in a table of plain sentences: "WiFi joins that
failed since boot", "Uploads that failed", "TLS connects that failed", "Power dips
detected". Token **mint** shows the secret once in a card with a Copy button;
**revoke** is a two-step danger action.

### 8.4 Install wizard (⚑ calibration + masks, the rest is PLAN §10)

A vertical stepper; each step has a **gate** and shows live data refreshed every 5 s
(`hx-trigger="every 5s"`); a step turns green only when its gate passes.

```
Install — Salon Kadrić                        step 3 of 7
────────────────────────────────────────────────────────
✔ 1  Power      brownouts since boot: 0          gate: 0
✔ 2  Signal     −61 dBm  ▂▃▄▅   ping 38 ms       gate: ≥ −65 dBm
▶ 3  First picture  waiting… last image 21:46
○ 4  Framing & privacy   [preview + 10-cell ruler + mask rectangles]
○ 5  Calibrate           [candidate sheet: tap the best]
○ 6  Owner sign-off      ☐ owner has seen this picture   [name] [Sign]
○ 7  Handover            [owner link] [QR poster] [camera notice]
```

Step 4 renders the live stylised preview at k=3 with an overlay: a **10-cell-wide
bracket** to hold against the nearest head, and tap-and-drag **mask rectangles**
(grid coordinates, max 4, snapped to cells). Step 6 stores the **reference-frame
hash** and a timestamped sign-off (PLAN §14 "reference-frame hash" trigger). Failing
a gate shows the next action in a sentence: "−74 dBm is too weak. Move the camera or
add an access point; below −65 dBm the picture will often be late."

### 8.5 Activity & timeline

Timeline per venue: recent frames (the ring, as a contact strip of tiny stylised
thumbnails — they *are* tiny), state flips, config changes, sign-off. Activity:
bounded tail of the audit stream ("who changed what, when").

### 8.6 Login

One field pair, one generic failure message, throttle notice with seconds remaining.

---

## 9. Owner console (`/owner`, Croatian)

Mobile-first: used from a phone behind a bar. No tokens, camera settings, other venues
or billing (enforced by the scoped cookie, PLAN §6.5).

**Home** — the public preview card (same partial as the public page) and one status
sentence: *"Slika je svježa. Zadnja slika: 21:48."* or *"Slika kasni. Pogledaj što
možeš učiniti."* Below: three big buttons — **Podaci o salonu**, **Radno vrijeme**,
**Zdravlje uređaja**.

**Radno vrijeme (hours editor)** — seven rows, one thumb each:

```
Ponedjeljak   [ 09:00 ] – [ 20:00 ]   ☐ zatvoreno   + smjena
Utorak        [ 09:00 ] – [ 20:00 ]   ☐ zatvoreno   + smjena
…
[ Kopiraj ponedjeljak na radne dane ]            [ Spremi promjene ]
```

Up to two intervals per day (split shifts); `<input type="time">` (native pickers
on phones); "closed" is a checkbox, not an empty field; validation names the day.
Saving shows a flash and the updated public preview (the page re-renders on save).

**Podaci o salonu** — name, public description (≤ 140 characters with a counter),
contact line, timezone (default `Europe/Sarajevo`), picture style (Tisak/Šablon with
the two thumbnails side by side), page language (hr/en ⚑ §11).

**Zdravlje uređaja** — read-only: last picture, signal in words ("dobar / slab"),
and **"Slika je stara — što učiniti?"**: 1) *Provjeri je li kamera uključena u
struju.* 2) *Provjeri radi li WiFi u salonu.* 3) *Ako ništa ne pomogne, nazovi
{kontakt}.* (the runbook, humanised; the contact comes from the venue record).

Invite/activate (PLAN §10): a one-page "Postavi lozinku" form (password + repeat,
strength hint, show/hide toggle) — same components, one primary action.

---

## 10. QR poster and camera notice

A4/A5 printable page per venue at `/admin/venues/{id}/poster` (and linked from the
wizard): `@page { size: A5 }`, `print-color-adjust: exact`, no JS.

```
┌───────────────────────────────┐
│ ▣ guzva                       │
│                               │
│   Ima li gužve?               │  display, 56 px at A5
│   Skeniraj i pogledaj.        │
│                               │
│   ┌───────────┐               │
│   │  QR code  │               │  ink on pure white, quiet zone ≥ 4 modules,
│   │           │               │  ≥ 3 cm square at A5
│   └───────────┘               │
│   guzva.ba/salon-kadric       │  the URL in text too
│                               │
│ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ │  dither rule
│ U prostoru je kamera. Javna   │  camera notice (PLAN §13)
│ slika je grubo stilizirana;   │
│ izvorne fotografije se ne     │
│ pohranjuju.                   │
└───────────────────────────────┘
```

- QR as **inline SVG generated in Go** (a small QR library — e.g. `rsc.io/qr`;
  verify its licence/maintenance before adopting); error correction level M.
- Never put the QR on the cream `paper` — scanners prefer white; the surrounding
  poster can be paper-coloured.
- The camera-notice sign is the same copy at A6 for a window/counter (PLAN §13).

---

## 11. Copy and localisation

One catalog (`internal/i18n`), `hr` default, `en` fallback. Keys, not strings, in
templates. Admin stays English. Formats: `dd.mm.yyyy.`, 24 h, `Europe/Sarajevo`.

| Key | hr |
|---|---|
| `tagline` | Ima li gužve? |
| `state.fresh` | Svježa slika |
| `state.late` | Slika kasni |
| `state.nosignal` | Nema nove slike |
| `state.closed` | Zatvoreno |
| `state.paused` | Salon je na pauzi |
| `state.waiting` | Čekamo prvu sliku… |
| `open.until` | Otvoreno do {time} |
| `closed.opens` | Otvara se {when} u {time} |
| `when.today` / `when.tomorrow` | danas / sutra |
| `stale.detail` | Zadnja slika je iz {time}. Ne mora prikazivati trenutno stanje. |
| `net.offline` | Nema internetske veze. Pokušavamo ponovno… |
| `idle.paused` / `idle.resume` | Osvježavanje je pauzirano. / Nastavi |
| `alt.still` | Stilizirana slika prostora {venue}, snimljena u {time} |
| `privacy.line` | Slika je namjerno grubo stilizirana radi privatnosti. Izvorne fotografije se ne pohranjuju. |
| `privacy.link` | O privatnosti |
| `hours.title` | Radno vrijeme |
| `owner.save` / `owner.saved` | Spremi promjene / Promjene su spremljene |
| `owner.health.old` | Slika je stara — što učiniti? |

Notes:
- **Weekdays after "u"** take the accusative: *u ponedjeljak, u utorak, u srijedu, u
  četvrtak, u petak, u subotu, u nedjelju.*
- **No relative-time sentences** ("prije 5 minuta") — they need three plural forms and a
  case (`prije 1 minutu / 2 minute / 5 minuta`). The UI uses absolute times plus
  abbreviations (`min`, `h`) where an age is unavoidable.
- bs/sr catalogs are one file each when needed (PLAN D7). Cyrillic: not in v1.
- **Per-venue page language (⚑ small):** `venues.lang` (`hr` default, `en`) selects
  the catalog used when pre-rendering that venue's `index.html`. Tourist venues get
  English without a toggle, a second file, or any D5 change.
- Copy is reviewed as a set before the pilot; the catalog is the single place to edit.

---

## 12. Accessibility checklist (WCAG AA target)

- Contrast: all text pairs in §3.1 ≥ 4.5 : 1 (computed, not eyeballed);
  non-text boundaries (borders, focus ring) ≥ 3 : 1.
- Targets ≥ 44×44 CSS px; spacing ≥ 8 px between adjacent targets.
- Focus always visible (§3.3); logical tab order; no keyboard traps; no
  hover-only information.
- Colour is never the only signal (§3.2); status text is real text.
- `lang` set; headings in order; landmarks present; forms have visible labels and
  associated errors.
- `prefers-reduced-motion`, `prefers-color-scheme`, `forced-colors` (borders stay,
  shadows may vanish — layout still works), 200 % text zoom without horizontal scroll.
- The still has a meaningful `alt`; the headline/chip live region is polite and
  per-frame.
- Print stylesheet for the poster; consoles hide nav on print.

---

## 13. Performance and delivery budgets

| Item | Budget | Notes |
|---|---|---|
| Public `index.html` incl. inline CSS + JS | ≤ 20 KB raw, **≤ 8 KB gz** | CSS ≤ 4 KB gz (enforced), JS ≤ 1 KB gz |
| Still (`latest.png`) | ~1–3 KB, hard cap 6 KB | §5.11 test |
| **First view** | **≈ 11 KB** | vs ~30–50 KB with HTML + 640 JPEG |
| Steady refresh (nothing new) | ≈ 0.3–0.6 KB | two conditional requests per cycle |
| Steady refresh (new frame) | ≈ 1–3 KB | |
| Console CSS | ≤ 30 KB min, hashed, `immutable` | cached after first visit |
| htmx | vendored (~14 KB gz), consoles only | none on the public page |
| Third-party requests | **0** | no fonts, analytics, CDNs (also keeps Bosnia/EU privacy simple) |

**Egress arithmetic** (assumptions: 3 KB still, 60 s device interval, a visitor
stays 2 minutes): a guest visit ≈ 11 KB + 2 × 3 KB ≈ 17 KB → **~55k visits per
free GB**. A viewer who keeps the tab open and visible costs ≈ 60 frames/h × 3 KB +
conditionals ≈ 0.2 MB/h → ~4,500 viewer-hours per GB. Both are far above a pilot's
needs; PLAN §14's CDN trigger (egress > ~60 %) stays the safety net, and idle
stop + visibility pause are what keep a forgotten tab from burning it.

**Caching and compression**

| Resource | Cache-Control | Notes |
|---|---|---|
| `index.html` | `no-cache` (revalidate) | `Last-Modified` → 304 |
| `latest.png` | `no-cache` | `Last-Modified` = frame `received_at` |
| Console assets | `public, max-age=31536000, immutable` | hashed filenames |
| Shared embedded pages (`/privatnost`) | `public, max-age=3600` | |

- gzip for HTML and console assets: compressed **on the fly with a small in-memory
  cache** keyed by path + mtime (the web root must stay exactly two files, so no
  `.gz` siblings); PNG is not recompressed. Console assets may be precompressed at
  build time and embedded.
- HTTP/2 comes with the stdlib TLS server; a 304 is ≈ 250 B.
- Pre-render cost: one template execution per *state flip*, never per request, never
  per frame — on a shared vCPU that is noise.

---

## 14. Plan deltas that need your approval (⚑)

Nothing here is written into `PLAN.md` until you say so.

| # | Change | Touches | Benefit | Cost / risk |
|---|---|---|---|---|
| ⚑1 | Public still = **160×120 paletted PNG** (`latest.png`); web-root allow-list + disk-scan tests updated; thumb stays deferred | §6.4, §7 | ~5–10× fewer bytes (egress, ring, weak links); privacy by resolution | Lower resolution is a deliberate look; real-frame check needed |
| ⚑2 | **Burned-in `HH:MM` stamp**; `index.html` no longer rewritten per frame (only on state flips) | §6.4, §7 | Screenshots self-date; less disk/CPU churn; no-JS stays honest | ~1.7 % of the picture is covered |
| ⚑3 | Two stale tiers (3–15 min "late", >15 min "no signal") | §6.6 | Page and alert agree; gentler wording first | One more state in tests |
| ⚑4 | **Closed / paused ⇒ no still, no polling** | §6.4 | Privacy after hours; bytes; honesty | Visitors can't peek at a closed venue |
| ⚑5 | `venues.calib_json` (thresholds, bias, invert, masks, preset) + calibration candidates under `data/ref/` | §6.2, §7, §10 | Legible pictures on awkward rooms without storing raw frames | One migration (guard: reopen test) |
| ⚑6 | **Static privacy masks** (≤ 4 rects) | §7, §10 | Fixes "head too close" at the camera, D9-safe | Installer effort at setup |
| ⚑7 | Public pages **`noindex`** by default | §6.4 | Interiors don't surface in search | Owner opt-in needed later |
| ⚑8 | **System fonts only** on all surfaces | §9.1 | D5-compatible, zero bytes, perfect diacritics | Less typographic character |
| ⚑9 | Per-venue page language (`venues.lang`) | §9.4, D7 | English for tourist venues, no toggle | One column |
| ⚑10 | Device capture ≤ QVGA, interval 45–90 s as the recommended defaults | §5, §13 | Smaller uploads on weak links | Slightly fewer frames |

Also noticed, not a delta: PLAN §13 asks whether `guzva.ba` is registered; wordmark,
QR poster and URLs assume `guzva.ba`.

---

## 15. Build order (one approval-gated step at a time)

Each step has an acceptance check before the next begins (PLAN §16.13).

1. **Tokens, Tailwind pipeline, kit page.** `web/tokens.css`, two builds, size gates,
   `/admin/_kit` with all components. *Accept:* CSS budgets pass;
   contrast table reproduced; kit looks right on a real phone.
2. **Render presets.** `poster@2`, `mono@2` + stamp in `internal/render`, goldens,
   invariants, size bound; render-ms on the e2-micro. *Accept:* privacy tests green;
   p95 render and real-frame sizes recorded (replaces §5.5's synthetic numbers).
3. **Public page.** Template, states, poller, CSP hashes, `fakecam` driving every
   state. *Accept:* first view ≈ 11 KB on the wire; 304 behaviour verified with
   `curl -I`/devtools; Lighthouse-style a11y pass by hand.
4. **Admin fleet + venue pages.** *Accept:* "needs attention" sort is right on
   fakecam states; works one-handed at 360 px.
5. **Install wizard + calibration + masks.** *Accept:* a full install of the bench
   unit end to end on a phone; sign-off stored.
6. **Owner console (Croatian).** Hours editor, details, health. *Accept:* a person
   outside the project edits hours unaided on a phone.
7. **QR poster + camera notice.** *Accept:* printed A5 scans from 1.5 m on two phones.
8. **Audit.** a11y checklist, glance test with the pilot venue, egress check against
   the billing report.

---

## 16. Open questions and unverified items

- **Lens FOV** of your camera module (drives `d_min`). Measure at the pilot
  mounting point rather than trust the datasheet.
- **Real frames:** every number in §5.5 is synthetic. Step 2's measurements decide
  whether dithering, contour or a different grid is needed.
- **Croatian copy** wants one read-through as a set by a fluent reader (§11).
- **QR library** choice and licence (§10).
- Tailwind **v3 vs v4**: syntax in §4 is v4; tell me which you run.
- Domain registration status (PLAN §13/§14).
- The exact enum mapping behind `scDefault.framesize` (§5.5).

---

## Appendix A — how the numbers in §5 were produced

A procedural 640×480 "salon" (gradients, window, mirror, counter, chairs, four
people as head/torso shapes) with Gaussian blur and sensor noise (σ = 5–14), plus a
busier variant with multi-scale texture and random hard-edged clutter. Pipeline:
4×4 box downscale → 3×3 binomial blur → thresholds 64/128/192 → 3×3 mode filter →
palette PNG (Pillow, 2-bit/1-bit, `optimize`). Compared against the same quantised
image upscaled 4× and JPEG-encoded at q68 (the plan's output). Limits: synthetic
scenes are flatter than real rooms; faces were not evaluated for recognisability;
Go's `image/png` should land within a few percent of these PNG sizes but was not run.

## Appendix B — contrast checks (WCAG 2.x relative luminance)

All values in §3.1 were computed, not estimated. Failures found and fixed during
design: the first `accent/fresh/late` candidates dipped to 4.1–4.4 : 1 on `paper-2`
and were darkened; the first still ramp had uneven lightness steps (27 / 32 / 24 L*)
and `s2` was darkened to even them out.

## Appendix C — logomark (clean source)

"The tally label": 16×16 grid, ink tile, paper label with a perforated tear edge,
three rising crowd bars (two `s1`, the peak in accent).

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" shape-rendering="crispEdges">
<rect x="0" y="0" width="16" height="16" fill="#12202D"/>
<rect x="2" y="2" width="12" height="11" fill="#F2EDE2"/>
<rect x="2" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="4" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="6" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="8" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="10" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="12" y="13" width="1" height="1" fill="#F2EDE2"/>
<rect x="3" y="9" width="2" height="3" fill="#33607F"/>
<rect x="7" y="7" width="2" height="5" fill="#33607F"/>
<rect x="11" y="5" width="2" height="7" fill="#AE2B16"/>
</svg>
```

Favicon: `<link rel="icon" href="data:image/svg+xml,…">` (URL-encode the SVG). Keep this
clean text as the source: files exported through some tools gain an extra metadata
block that adds several KB.

`tools/styleproof` (shipped) renders a candidate contact sheet from real
frames — six renders of one input, each with its paste-ready `calib_json`.
It shows four renders of one synthetic
scene — camera frame, Tisak, Šablon, and stale — plus the palette and state chips.
