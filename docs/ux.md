# guzva — UX spec (PLAN §9)

The product's three surfaces and their shared rules. The point of this file is
that copy and states are designed once, not written ad hoc per handler.

## Surfaces

| Surface | Path | Audience | Language | Plane |
|---------|------|----------|----------|-------|
| Public page | `/{slug}/` | a guest at the venue | Croatian (hr) | site |
| Vendor (owner) panel | `/owner/` | venue owner, usually on a phone | Croatian (hr) | site |
| Super-admin console | `/admin/` | you, and later an operator | English | both |

## Design system (Tailwind, D6)

- Tokens as CSS variables — colour (incl. dark), spacing, radius, type scale,
  elevation, motion. The Tailwind theme maps onto them; Tailwind CLI purges to a
  single stylesheet (`web/app.css`). Admin/owner serve it at
  `/admin/static/app.css`; the public page inlines the purged CSS (D5 forbids a
  third file).
- Interactivity via htmx (embedded) and progressive enhancement: every console
  action is a plain form POST without JS. No SPA, no runtime JS compiler.
- Components (Go template partials, not a JS library): badge, card/frame,
  button, form row, table, toggle, empty state, toast/flash, skeleton.

## States — every state reachable + named

fresh · stale · no-frame · degraded · revoked · paused · error.
- Colour is never the only signal (icon/label always present).
- A stale feed shows the last-good frame + STALE; a bare-clock time is
  disambiguated by a day label (today / yesterday / dd.mm.yyyy), never by a
  relative age ("prije X min") which a static file cannot keep correct.
- Motion: subtle, and silent under `prefers-reduced-motion`.
- Accessibility: WCAG AA — keyboard reachable, focus-visible, alt text that
  names the venue, `aria-live` for frame updates.

## Date / time / number formats (D7)

24 h clock (`21:48`), `dd.mm.yyyy.`, timezone always shown for a timestamp
(`CET`/`CEST`), thousands separated with a thin space. All mechanical in
`internal/i18n` — one catalog for hr + en, never inline `fmt` of a date in a
handler.

## Privacy copy (D9)

One Croatian sentence on the public page, one paragraph for owners: a stylised
render is shown; the raw image is never stored, published or logged; no faces,
no recognition, no tracking. The camera-notice spec for installs lives in
`docs/` and is referenced from the install checklist (§10).

## QR poster (public page distribution)

A5/A4 printable per venue, generated server-side: the page URL, the venue name,
the privacy line. One template, one print-ready PDF. This is how a guest reaches
the still without the venue staff explaining anything.
