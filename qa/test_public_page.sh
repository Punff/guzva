#!/usr/bin/env bash
# qa/test_public_page.sh — the public venue page contract (PLAN D5, §6.4,
# DESIGN §3.2/⚑3/§7).
#
# This page is the whole product surface. Everything asserted here is a
# promise the page makes to a stranger standing in a salon:
#
#   * ONE clock. The in-frame #stamp badge was removed at v0.6.1 (the
#     "double clock" fix); #hl below the print is the page's only time, and
#     it is display-scale (round 6).
#   * Every state is NAMED, not merely coloured (§3.2: shape + colour +
#     sentence), in Croatian (D7).
#   * The live region announces a new frame without stealing focus.
#   * With JS off the page still refreshes (noscript meta-refresh).
#   * Freshness math matches venue.Classify: fresh → late at 3 min → stale at
#     15 min. Driven by backdating received_at in the SCRATCH database, never
#     by waiting and never by touching production.
#
# Usage: qa/test_public_page.sh
#        QA_BUILD_FROM=HEAD qa/test_public_page.sh   # if another session's
#                                                     # WIP does not compile

set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

echo "== public page contract =="
qa_tree_status
qa_build     || { echo "build failed" >&2; exit 1; }
qa_fixtures  || { echo "fixtures failed" >&2; exit 1; }
qa_server_start || exit 1
trap qa_server_stop EXIT

PAGE="$(qa_page_url)"
HTML="$GUZVA_QA_DIR/page.html"
DB="${QA_DATA}/guzva.db"
[[ -f "$DB" ]] || DB="$(find "$QA_DATA" -maxdepth 1 -name '*.db' | head -1)"

fetch_page() { curl -sS -o "$HTML" "$PAGE"; }

# ---------------------------------------------------------------- the clock

qa_case "one clock: #hl exists exactly once and #stamp is gone"
qa_post "$QA_FIX/good.jpg" >/dev/null
fetch_page
qa_eq "exactly one id=\"hl\"" "$(grep -c 'id="hl"' "$HTML")" "1"
qa_eq "no #stamp element"          "$(grep -c 'id="stamp"' "$HTML" || true)" "0"
qa_hasnt "no stamp badge markup anywhere" "$(cat "$HTML")" 'stamp'

qa_case "the clock is display-scale, not a caption-sized label (round 6)"
# Tailwind arbitrary values are matched as FIXED strings: qa_has does bash
# pattern matching, where [2.25rem] is a character class.
qa_hasf_file "2.25rem base size"          "$HTML" 'id="hl"'
qa_hasf_file "the hl span carries the caption scale" "$HTML" 'text-[1.75rem] leading-none font-bold tnum" id="hl"'
qa_hasf_file "tabular figures (a clock must not jitter)" "$HTML" 'font-bold tnum" id="hl"'
qa_hasnt "no monospace (tabular figures do the aligning)" "$(cat "$HTML")" 'class="[^"]*font-mono'
# The h1 is the SAME two steps — that equality is the round-6 ruling.
qa_hasf_file "the h1 type scale" "$HTML" '<h1 class="text-[2rem] leading-[1.1] font-extrabold tracking-tight'

qa_case "the clock sits below the print, inside the figure, with the state"
qa_true "figcaption contains #hl" bash -c \
  "tr -d '\n' < '$HTML' | sed -n 's/.*<figcaption[^>]*>\(.*\)<\/figcaption>.*/\1/p' | grep -q 'id=\"hl\"'"
qa_true "the figure wraps the still and the clock" bash -c \
  "grep -q '<figure' '$HTML' && grep -q 'id=\"still\"' '$HTML'"
qa_true "the still precedes the clock" bash -c \
  "awk '{print index(\$0,\"id=\\\"still\\\"\")} ,1' '$HTML' | head -1 >/dev/null && \
   test \$(grep -bo 'id=\"still\"' '$HTML' | head -1 | cut -d: -f1) -lt \$(grep -bo 'id=\"hl\"' '$HTML' | head -1 | cut -d: -f1)"

# ------------------------------------------------------------- live region

qa_case "figcaption is a polite live region"
# The attributes sit on the line AFTER <figcaption …>, so the element is
# pulled out whole before matching.
fig="$(tr '\n' ' ' <"$HTML" | grep -o '<figcaption[^>]*>' | head -1)"
echo "    $fig"
qa_hasf "role=status on the figcaption"      "$fig" 'role="status"'
qa_hasf "aria-live=polite on the figcaption" "$fig" 'aria-live="polite"'

# ------------------------------------------------------------------- poller

qa_case "the poller is present and wired to the catalog (no second dictionary)"
qa_has "data-tz"    "$(cat "$HTML")" 'data-tz="Europe/Sarajevo"'
qa_has "data-still" "$(cat "$HTML")" 'data-still="latest.png"'
qa_has "data-poll"  "$(cat "$HTML")" 'data-poll="30"'
qa_has "data-lang"  "$(cat "$HTML")" 'data-lang="hr"'
qa_has "offline copy comes from data-*" "$(cat "$HTML")" 'data-offline="'
qa_has "new-frame announcement string"    "$(cat "$HTML")" 'data-newpic="'
qa_true "the script is inlined (D5: exactly two files in the web root)" bash -c "grep -q '<script>' '$HTML'"
qa_true "the stylesheet is inlined (D5)" bash -c "grep -q '<style>' '$HTML'"
qa_eq "the web root still holds exactly two files" \
  "$(find "${QA_DATA}/public/${QA_SLUG}" -type f | wc -l | tr -d ' ')" "2"

qa_case "no-JS fallback: the meta refresh is inside <noscript> only"
# The template comment explains why: Chrome arms the refresh timer at PARSE
# time, so a bare <meta http-equiv=refresh> would reload the document every
# 30 s even with the in-place swap running (3 navigations in 95 s).
qa_true "noscript wraps a meta refresh" bash -c \
  "tr -d '\n' < '$HTML' | grep -q '<noscript><meta http-equiv=\"refresh\" content=\"60\"></noscript>'"
qa_hasnt "no unconditional meta refresh (would double-navigate with JS on)" \
  "$(tr '\n' ' ' <"$HTML" | sed 's/<noscript>.*<\/noscript>//g')" 'http-equiv="refresh"'

# ------------------------------------------------------------ state + copy

qa_case "fresh state: Croatian, named, shape + colour + sentence"
qa_has "state text 'Svježa slika'" "$(cat "$HTML")" 'Svježa slika'
qa_has "chip id carries the state" "$(cat "$HTML")" 'id="chip-fresh"'
qa_has "state shape is an inline svg" "$(cat "$HTML")" '<svg viewBox="0 0 12 12"'
qa_has "state row is above the print" "$(cat "$HTML")" 'chip--lg'
qa_has "late overlay exists in CSS" "$(cat "$HTML")" 'tx-hatch-light'
qa_has "the open-state sentence rides beside the chip" "$(cat "$HTML")" 'Otvoreno'

qa_case "the page never shows a relative age (static file, §6.4)"
qa_hasnt "no English 'min ago'" "$(cat "$HTML")" 'min ago'
qa_hasnt "no Croatian 'prije'"  "$(cat "$HTML")" 'prije'
qa_has "absolute clock time instead" "$(cat "$HTML")" 'id="hl"'

qa_case "hours card renders today's row for assistive tech"
qa_has "hours section" "$(cat "$HTML")" 'Radno vrijeme'
qa_has "today's row is marked" "$(cat "$HTML")" 'aria-current="date"'
qa_has "danas marker"      "$(cat "$HTML")" 'danas'

qa_case "privacy caption is on the page (the privacy-first promise, D1)"
qa_has "privacy line" "$(cat "$HTML")" 'namjerno grubo stilizirana'

qa_case "lang and meta description follow the catalog"
qa_has "html lang=hr"  "$(cat "$HTML")" '<html lang="hr">'
qa_has "title tagline" "$(cat "$HTML")" 'Ima li gužve?'
qa_has "description carries the state" "$(cat "$HTML")" '<meta name="description"'
qa_has "noindex meta" "$(cat "$HTML")" '<meta name="robots" content="noindex, nofollow">'
qa_has "theme-color"  "$(cat "$HTML")" '<meta name="theme-color"'
qa_has "inline SVG favicon (no third-party request, §13)" "$(cat "$HTML")" 'rel="icon" href="data:image/svg+xml'

# ------------------------------------------------------------ freshness math

# backdate <minutes> rewrites the stored frame's received_at in the SCRATCH
# db. This is the only way to exercise the 3-minute and 15-minute boundaries
# without a 15-minute test, and it touches nothing but $QA_DATA.
#
# received_at is an INTEGER of unix seconds (see the schema), NOT a datetime
# string. `datetime('now','-4 minutes')` writes text into that column,
# SQLite accepts it happily because it is dynamically typed, and the Go
# driver then reads a type it cannot scan — which looks exactly like "the
# state never flipped". Cast strftime('%s', …) to INTEGER.
backdate() {
  local mins="$1" expr
  expr="CAST(strftime('%s','now','-${mins} minutes') AS INTEGER)"
  sqlite3 "$DB" "UPDATE frames SET received_at   = $expr WHERE status='ok';"
  sqlite3 "$DB" "UPDATE venues SET last_frame_at = $expr;"
}

# The page is rewritten by the sched tick, so a backdated frame needs one
# tick to become visible. Poll the served page until the state key moves.
await_rewrite() {
  local want="$1" i
  for i in $(seq 1 60); do
    fetch_page
    if grep -q "$want" "$HTML"; then return 0; fi
    sleep 1
  done
  return 1
}

qa_case "freshness math: 4 minutes old → 'late' (StaleAfter is 3 min)"
backdate 4
if await_rewrite 'chip-late'; then
  qa_ok "state flips fresh → late past 3 min"
  qa_has "late copy"           "$(cat "$HTML")" 'Slika kasni'
  qa_has "late detail sentence" "$(cat "$HTML")" 'Zadnja slika je iz'
  qa_has "gentle hatch overlay, not the dead-sensor one" "$(cat "$HTML")" 'tx-hatch-light'
  qa_has "the still is still shown" "$(cat "$HTML")" 'id="still"'
else
  qa_bad "never reached the late state within 60 s — sched tick or backdate is wrong"
fi

qa_case "freshness math: 20 minutes old → 'nosignal' (NoSignalAfter is 15 min)"
backdate 20
if await_rewrite 'chip-nosignal'; then
  qa_ok "state flips late → nosignal past 15 min"
  qa_has "no-signal copy" "$(cat "$HTML")" 'Nema nove slike'
  qa_has "honest detail: may not show current state" "$(cat "$HTML")" 'Ne mora prikazivati trenutno stanje'
  qa_has "last-good still is STILL shown (never publish a failure)" "$(cat "$HTML")" 'id="still"'
else
  qa_bad "never reached the nosignal state within 60 s"
fi

qa_case "never publish a failure: the still survives the state flip"
qa_true "latest.png still on disk" test -s "${QA_DATA}/public/${QA_SLUG}/latest.png"
curl -sf -o /dev/null "${QA_BASE}/${QA_SLUG}/latest.png" \
  && qa_ok "still is still served" || qa_bad "still 404s after going silent"

qa_case "a fresh frame restores the fresh state (recovery, not a one-way trip)"
qa_post "$QA_FIX/good2.jpg" >/dev/null
fetch_page
qa_has "back to 'Svježa slika'" "$(cat "$HTML")" 'Svježa slika'

# ------------------------------------------------------------- accessibility

qa_case "semantic landmarks"
qa_has "<header>" "$(cat "$HTML")" '<header'
qa_has "<footer>" "$(cat "$HTML")" '<footer'
qa_hasf_file "<h1> is the venue name, kept compact so the picture is in the first screen" "$HTML" '<h1 class="text-[1.5rem]'
qa_has "hours are a native disclosure (works without JS)" "$(cat "$HTML")" '<details'
qa_has "the disclosure carries a visible label" "$(cat "$HTML")" '<summary'
qa_has "<figure>/<figcaption> for the print" "$(cat "$HTML")" '<figcaption'

qa_case "the still has real alt text (not a filename)"
qa_has "alt carries the venue name and time" "$(cat "$HTML")" 'alt="Stilizirana slika prostora QA Bench'
qa_hasnt "no empty alt" "$(cat "$HTML")" 'alt=""'

qa_case "decorative SVG is hidden from assistive tech"
# Count <svg …> opening tags that themselves carry aria-hidden. Counting the
# attribute globally does not work: the hatch/dither overlays are divs, not
# svgs, so the two numbers legitimately differ.
svg_total="$(grep -o '<svg[^>]*>' "$HTML" | wc -l | tr -d ' ')"
svg_hidden="$(grep -o '<svg[^>]*aria-hidden="true"[^>]*>' "$HTML" | wc -l | tr -d ' ')"
qa_eq "every inline <svg> is aria-hidden ($svg_hidden/$svg_total)" "$svg_hidden" "$svg_total"
qa_has "the logomark is aria-hidden" "$(cat "$HTML")" 'aria-hidden="true" class="shrink-0"'

qa_case "the offline banner is announced, not just coloured"
qa_has "banner is role=alert" "$(cat "$HTML")" 'role="alert"'

qa_case "info card renders only when it has content (no empty card, no tel: ghost)"
# The QA venue is provisioned with no phone/description/location, so the
# whole card must be absent — an empty bordered box would be a lie about
# what the venue offers. The tel: link itself is covered by pages' own tests.
qa_hasnt "no info card for a venue with no contact details" "$(cat "$HTML")" 'O salonu'
qa_hasnt "no dangling tel: link" "$(cat "$HTML")" 'href="tel:'

qa_summary "public page"
