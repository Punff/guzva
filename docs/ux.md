# guzva — UX spec

**Superseded.** The design system, UX spec and image style now live in one
document: [design.md](design.md) (DESIGN.md v1.0). Keeping two would let them
drift, which is exactly what went wrong before.

Implementation status and the remaining work: [design-todo.md](design-todo.md).

This file is kept as a pointer so old links resolve.

---

## What replaced it

- `docs/design.md` §3 — visual language ("Tisak"), colour tokens, spacing,
  borders, depth, type, logomark, motion.
- §5 — the image style, the `poster@1`/`mono@1` pipeline, privacy reasoning,
  the install geometry gate, per-venue calibration.
- §6 — components.
- §7–§10 — public page, admin, owner, QR poster.
- §11 — the Croatian copy catalog (implemented in `internal/i18n`).
- §12–§13 — accessibility checklist and performance budgets (budgets are
  enforced by `make css`).
