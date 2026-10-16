# Replacement plan — "Print v4": frozen device profile + band-pass privacy filter

> **Historical plan, shipped (v0.7.0, 14 Oct 2026).** The pipeline it describes is
> live in `internal/render/tisak.go`. Since then: privacy masks were retired from
> the product and the code (D11, v11; cleanup 16 Oct 2026), and per-venue look
> presets were added (D11). Where this file and the code disagree, the code and
> `docs/design.md` §5 are current.

Scope: **plan only.** Decisions in §8 were approved on 14 Oct 2026; nothing in your code changes until you approve each step in §6.
Replaces: the remote device config (`desired_cfg` / `SensorCfg` / NVS `cam` / read-back),
the per-venue render calibration (`calib.go`), and the 160×120 blur → threshold → mode
pipeline (`tisak.go`, `poster@3` / `mono@3`). Keeps: the 4-tone ramp and PNG output,
static privacy masks, the idempotent ingest contract, D1/D2/D5/D9.

Evidence sheet: `docs/print-v4-compare.png` (synthetic frame — shows the mechanism, not final
parameters).

---

## 1. What is actually wrong (from reading the five files)

| # | Finding | Evidence |
|---|---|---|
| F1 | **Lighting, not objects, decides the tone bands.** Quantising *absolute* luma with fixed 64/128/192 on an indoor frame (narrow luma range, uneven light) yields iso-luminance contours: big blobs that follow the window light. | Your screenshot looks exactly like that. In the synthetic repro the current pipeline uses **3 of 4 tones (0 % in the lightest)** and people merge into the dark floor. |
| F2 | **A stack of low-passes at too coarse a grid.** `lumaGrid` 2×2-averages the already-native 320×240 sensor frame to 160×120, then `blur121`, then `mode3`. `mode3` (3×3 majority) deletes thin structures (arms, chair backs, hair) and rounds everything into blobs. | `tisak.go` stages 2, 4, 6. |
| F3 | **The raw frame may already be weak.** A QVGA q14 JPEG of only ~4 KB (the 09 Oct log: 3,964 B) is small for an indoor scene — typical of soft focus or a dark/flat frame. The OV2640 focus ring is easy to leave at close range. No filter can recover that. | Log line `[CAP ] ok: 3964 bytes`. Check the raw `/jpg` before judging any filter. |
| F4 | **Too many knobs, in two places.** Device: 20 `SensorCfg` fields + interval, persisted in NVS, pushed remotely, with read-back. Server: `Calib{T, Bias, Invert, Masks, Preset}`. Each knob is another way to break the camera or the picture, and the server knobs exist only to compensate for a render that cannot cope with lighting. | `main.cpp` lines ~150–195, 921–1000; `calib.go`. |
| F5 | **Privacy and legibility share one dial** (grid resolution). Lowering resolution buys privacy and costs shape; raising it does the reverse. They need separate controls. | Design of `tisak.go`. |

---

## 2. Target design in one paragraph

The **device is frozen**: one compile-time camera profile, no remote camera settings.
The **server render is self-normalising**: it removes the room's lighting field and
keeps only a band of spatial scales — fine enough to show people and objects, too
coarse to show a face — then maps that to the same 4 tones. **Per-venue state shrinks
to** `style` (Tisak/Šablon) and `masks` (≤ 4 static rectangles), plus hours/pause.
Parameters are chosen once, offline, on real frames, then frozen as constants in a
new preset version.

---

## 3. Device: "Profile v1", frozen in firmware

**Constants (proposal, validated in S3):**

| Setting | Value | Note |
|---|---|---|
| Frame size | QVGA 320×240 (enum 5) | Already your default; 4:3, sensor-native for the new render. |
| JPEG quality | 14 (bench-test 12–16, then freeze) | |
| Exposure / gain / white balance | AEC on, AGC on, AWB on, ae_level 0 | Auto only; no manual gain, no remote tuning. |
| dcw, lenc | on | as today |
| Everything else (brightness, contrast, saturation, sharpness, denoise, effect, colorbar, flash PWM) | firmware constants at neutral/off | no runtime setter |
| Capture interval | constant (60 s) | Server may send a **one-shot** `sleep_s` hint like `Retry-After` (e.g. closed hours); never persisted. |
| Orientation | `PROFILE_FLIP` build flag | a mounting fact, not a runtime setting |

**Delete from `main.cpp`:** `SensorCfg` mutability and `saveSensorCfg`, the NVS `cam`
config blob and `interval` key, `applyCfgFromBody` and the `CfgU8Row/CfgI8Row` tables,
`cfg_ver_applied` in the POST, the `cfg` serial command, the full config block in
`/api/status`. **Keep:** identity, token, hostname, counters in NVS `prov`; telemetry
(`fw`, `profile_id`, `rssi`, heap); the read-only `status` command. Changing the profile
= new firmware (reflash now, OTA + rollback in Phase 3). The device reports
`profile_id` so the server always knows what produced a frame.

**Compatibility during rollout:** the server must ignore legacy fields from old
firmware (`cfg_ver_applied`), and new firmware must tolerate a server that still sends
`desired_cfg` (ignore it). No flag day.

**Physical install replaces remote tuning:** focus the lens at the venue's real viewing
distance using the LAN `/jpg` (an edge must look crisp at ~3 m); mount so no bright
window fills the frame (AEC will crush everything else); power spec as in PLAN. Add
these as wizard gates.

---

## 4. Server: "Tisak v4" (`poster@4`, `mono@4`)

Pure, deterministic, stdlib. Constants decided in S1 — **the values below are the
starting points, not conclusions.**

| # | Stage | Starting value | Purpose |
|---|---|---|---|
| 1 | Luma only (Y plane); area-average to **320×240** (identity for QVGA) | native | No resolution thrown away before the privacy filter; one fewer low-pass. |
| 2 | Static masks → luma 0 | ≤ 4 rects | Unchanged. Applied before and after, as today. |
| 3 | **Privacy low-pass**, Gaussian | σp ≈ 2 cells | The **privacy dial.** Kills features smaller than about a third of a head. |
| 4 | **Remove the lighting field:** `D = lowpass(L) − Gaussian(L, σb)` with σb ≈ 32 cells (computed on a ×8 downsample) | σb ≈ 10 % of width | The **legibility fix.** Tone now means "darker/lighter than its surroundings", independent of the room's light. Band-pass between privacy scale and object scale. |
| 5 | Tone-map `D` to 4 tones with **fixed thresholds in D-space**: neutral surroundings → S2, darker → S1 / S0, brighter → S3 | lab-derived | Same constants for every venue; no per-venue thresholds. |
| 6 | **Isolated-cell cleanup** (cell whose 8 neighbours all differ → take the majority) | — | Removes speckle without `mode3`'s damage to thin shapes. |
| 7 | Masks again; paletted 2-bit PNG, fixed ramp | unchanged | |

`mono@4`: same `D` map, one threshold (dark-relative shapes in ink on paper).

**What gets deleted:** `calib.go` (`ParseCalib`; masks get a tiny `ParseMasks`),
`Calib.T/Bias/Invert/Preset`, `quant`'s threshold/bias/invert parameters, `blur121` at
160 cells, `mode3`, and `FrameIn.Calib` → `FrameIn.Masks`. Presets bump to version 4
(D2: old frames keep `poster@3` meaning; old sidecars stay valid).

**Output size and display.** 320×240 PNG, shown **1:1 CSS px on phones, 2× on desktop**
(integer scaling is natural at 320 px and fits a 360 px phone). Public template changes:
`width="320" height="240"`, `--k` = 1 / 2.

**Bytes.** Synthetic hard frame: 543 B (current) vs 2.3 KB (proposed). Plan **4–8 KB**
for real frames until measured; test cap 12 KB. Egress: first view ≈ 16–20 KB (was ≈ 11);
viewer-hours per free GB roughly 1,500–2,500 (was ~4,500). Still comfortable for a
pilot; the CDN trigger in PLAN §14 stays as the safety net.

**The privacy rule must be restated** (the old "head ≤ 10 cells at 160" came from the
old grid). New rule, in terms of what the filter actually removes:

> **R = head width (cells) ÷ blur FWHM (cells) ≤ ~4 at the nearest seat.**

FWHM = 2.355·σp. Example (54° horizontal FOV, 3 m): ≈ 17 head cells at 320 wide; σp = 2
→ FWHM 4.7 → R ≈ 3.6 ✓. At 2 m: ≈ 25 cells → R ≈ 5.3 ✗ → move the camera or mask the
seat. The threshold ~4 is a **hypothesis to validate**, not a guarantee. Research
(McPherson, Shokri & Shmatikov, 2016, *Defeating Image Obfuscation with Deep Learning*)
shows blur/pixelation can be partly reversed by trained models, especially for a
**closed set of known people** — a salon's regulars are exactly that. So privacy
continues to rest on: band-limit + 4-tone quantisation + masks + camera geometry +
**no public history** (no frame fusion) + the consent-based recognition test below.

---

## 5. Lab-first method (so we stop tuning by hand in production)

`tools/styleproof` — offline, local only, never deployed.

- **Corpus:** 30–40 real frames from the bench unit (or a willing venue): empty room,
  1–3 people, bright, dim, window glare, a deliberately soft-focus one, a dark one, 20
  consecutive frames of a static empty room. **Never committed** (`.gitignore` + a
  pre-commit check); only procedural fixtures live in the repo.
- **Variants:** σp {1.5, 2, 2.5, 3} × σb {24, 32, 48} × a few threshold sets × cleanup
  on/off, all at the approved 320×240 → contact sheets.
- **Selection criteria (all four must pass):**
  1. *Legibility:* 5 people who don't know the venue read 10 frames — ≥ 90 % within ±1
     headcount.
  2. *Recognition:* 5 people who **know the staff** (with their consent) try to identify
     them from stylised frames — at or near chance.
  3. *Size:* p95 ≤ 12 KB.
  4. *Stability:* over 20 empty-room frames, the fraction of cells changing level between
     consecutive frames is ≤ a bound set from today's pipeline as the baseline (target
     ≤ ~3 %).
- **Human tests only (decided):** no face detector, not even as an offline test oracle (D9 stays absolute).
- **Freeze:** winning constants go into code as `const`, versioned `poster@4`. No config
  file, no per-venue overrides.

---

## 6. Steps (each with an acceptance check and your approval before the next)

| Step | Work | Accept when |
|---|---|---|
| **S0** Raw check (no code) | Look at the raw `/jpg`; fix focus/exposure/mounting; capture the corpus | People are clearly readable in the *raw* frame at ~3 m |
| **S1** Lab + choose | Build `styleproof`; run variants; pick constants | Criteria 1–4 pass on the corpus; you approve the winner by eye |
| **S2** Render v4 | `internal/render`: new pipeline, `poster@4`/`mono@4`, `Masks` replaces `Calib`; delete `calib.go`, `mode3`, old blur/quant | Goldens (procedural fixtures incl. a **lighting-gradient fixture** that fails if < 3 tones are used), invariants (320×240, ≤ 4 palette entries, no ancillary chunks, mask cells level 0), determinism ×100, size cap; render ms recorded on the e2-micro |
| **S3** Firmware Profile v1 | Strip config paths (§3); flash bench unit; 48 h soak (can run in parallel with S1–S2) | Zero camera-probe failures, counters flat, uploads healthy, `status` shows `profile_id` |
| **S4** Cleanup | Remove `desired_cfg`/`cfg_ver` from proto + server, admin device-config page, install-wizard calibration step; add the focus/framing check with the R ruler; page template 320×240 integer scaling; migration leaves `calib_json` unused (drop later); update `proto-v1.md`, PLAN, DESIGN in the same change | Old and new firmware both ingest cleanly; tests green |
| **S5** Pilot checks | Glance + recognition tests at the pilot venue; egress and render-time check | Go / no-go |

**Rollback:** preset versions coexist and the render version is a constant — revert is a
deploy. Firmware rollback is a reflash now, OTA rollback in Phase 3.

---

## 7. Risks and unknowns

- **All evidence so far is synthetic**, and I tuned the D-space thresholds by eye on one
  frame. The sheet proves the *mechanism* (relative tone fixes lighting-dominated blobs),
  not the values. Real corpus first.
- **Relative tone-mapping can mislead:** large objects get halos; a mostly-empty bright
  room and a mostly-full one are normalised against their own surroundings; noise can
  make flat regions flicker (hence criterion 4). If scene contrast varies a lot between
  venues, fall back to a slowly-updated server-side scale (one number per venue,
  maintained automatically — not a setting).
- **Contour/edge ink** showed no visible gain in the repro; decide on real frames.
- **Higher output resolution is a privacy bet** that rests on the band-limit; hence the
  R rule, masks, the recognition test, and no history.
- **Frozen firmware means a bad profile needs an OTA/reflash.** Mitigation: auto
  exposure, the focus/mount gates, and at most two profiles (e.g. normal / dim) chosen by
  build flag.
- Egress rises ~2–4×; acceptable, measured in S5.

---

## 8. Decisions (approved 14 Oct 2026)

| # | Decision | Status |
|---|---|---|
| 1 | Freeze the device profile; remove remote camera config entirely, interval included (only a one-shot `sleep_s` hint remains) | **Approved** |
| 2 | Output grid **320×240** (sensor-native, integer display scaling) | **Approved** |
| 3 | Drop per-venue thresholds/bias/invert/preset; keep only `style` + `masks` | **Approved** |
| 4 | No face detector, not even offline; selection criteria 1–2 are human tests only | **Approved** (no detector) |

**Still needed to start S0/S1:** 5–10 raw frames from the bench unit (empty, one person,
bright, dim) kept local, or a description of what the raw `/jpg` looks like (sharp? dark?).
The lab tool itself (S1) can be built before the frames arrive, since it only reads local JPEGs.
