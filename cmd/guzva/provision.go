package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"guzva/internal/auth"
	"guzva/internal/proto"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// provisionMain creates a venue + device and prints the bearer token exactly
// once. Until the Phase 3 claim flow (§4), this SSH-and-run step is how an
// installer provisions a device; the server stores only the token's sha256.
func provisionMain(args []string) int {
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/guzva", "data dir (must match serve -data)")
	slug := fs.String("slug", "", "public venue slug, e.g. jadro (required)")
	name := fs.String("name", "", "venue display name (required)")
	style := fs.String("style", "poster", "preset: poster | mono (no raw preset exists, §7)")
	interval := fs.Int("interval", proto.DefaultIntervalS, "device-plane interval_s (30-300 s)")
	quality := fs.Int("quality", proto.DefaultQuality, "device-plane JPEG quality (4-63, lower = better)")
	hours := fs.String("hours", "", `opening hours, e.g. "Mo-Fr 09:00-17:00;Sa 10:00-14:00" (empty = no badge)`)
	serial := fs.String("serial", "", "device serial (default: random — replace with the efuse MAC label)")
	token := fs.String("token", "", "import an existing token instead of minting one (cutover: the flashed board keeps its token — no re-flash)")
	tz := fs.String("tz", store.DefaultTZ, "venue IANA timezone, e.g. Europe/Sarajevo")
	devices := fs.Int("devices", 1, "how many devices to mint on this venue (fakecam gate harness)")
	fs.Parse(args)

	if *slug == "" {
		fmt.Fprintln(os.Stderr, "usage: guzva provision -slug <slug> -name <name> [-token <existing>] [-style poster|mono] [-hours ...] [-devices N] [-data dir]")
		return 2
	}
	// Same rule the admin console and store.CreateVenue enforce (W1/B5):
	// the slug becomes a directory name under the public web root, so a
	// typo like "../etc" must die here, not in a filepath.Join.
	*slug = strings.ToLower(strings.TrimSpace(*slug))
	if !store.ValidSlug(*slug) {
		fmt.Fprintln(os.Stderr, "provision: -slug must be lowercase letters, digits or dashes (≤40)")
		return 2
	}
	// tz is a venue's display clock; a bogus one silently renders UTC with
	// a warn in the journal. Validate now — same rule as the admin form.
	if _, err := time.LoadLocation(*tz); err != nil {
		fmt.Fprintf(os.Stderr, "provision: unknown -tz %q (IANA name, e.g. Europe/Sarajevo)\n", *tz)
		return 2
	}
	if *devices < 1 || *devices > 256 {
		fmt.Fprintln(os.Stderr, "provision: -devices must be 1..256")
		return 2
	}
	if *token != "" && *devices > 1 {
		fmt.Fprintln(os.Stderr, "provision: -token imports one device only; drop -devices or -token")
		return 2
	}
	if *style != "poster" && *style != "mono" {
		fmt.Fprintf(os.Stderr, "provision: unknown style %q (poster | mono; there is no raw preset)\n", *style)
		return 2
	}
	h, err := venue.ParseSpec(*hours)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision: bad -hours: %v\n", err)
		return 2
	}
	*interval = proto.ClampInterval(*interval)
	*quality = proto.ClampQuality(*quality)
	if *serial == "" {
		*serial = "esp32-" + randHex(3)
	}

	st, err := store.Open(storePath(*dataDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision: %v\n", err)
		return 1
	}
	defer st.Close()

	// Token: mint one, or import the flashed board's existing token (cutover
	// — the device keeps working, no re-flash). Auth stores only its sha256.
	// imported distinguishes the two paths below: *token is always set by
	// the time we print, so testing it directly would lie (found while
	// setting up the demo env — the import banner fired for fresh mints
	// and the "Next:" checklist never printed).
	imported := *token != ""
	if !imported {
		tok, err := auth.GenerateToken()
		if err != nil {
			fmt.Fprintf(os.Stderr, "provision: token: %v\n", err)
			return 1
		}
		*token = tok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Re-running against an existing slug is how an installer adds a device
	// (or sets up the fakecam gate); a fresh slug creates the venue.
	var venueID int64
	reused := false
	vName, vStyle := *name, *style
	if v, err := st.VenueBySlug(ctx, *slug); err == nil {
		venueID, reused = v.ID, true
		vName, vStyle = v.Name, v.Style // report what is actually stored
		fmt.Fprintf(os.Stderr, "provision: venue %q exists — adding device(s) to it\n", *slug)
	} else if !errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "provision: lookup venue: %v\n", err)
		return 1
	} else {
		if *name == "" {
			fmt.Fprintln(os.Stderr, "usage: guzva provision -slug <slug> -name <name> [-style poster|mono] [-hours ...] [-devices N] [-data dir]")
			return 2
		}
		venueID, err = st.CreateVenue(ctx, store.Venue{
			Slug: *slug, Name: *name, Style: *style, TZ: *tz,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "provision: create venue: %v\n", err)
			return 1
		}
	}
	// Hours: always set on creation; on reuse only when explicitly passed,
	// so re-running to mint a device never clobbers the venue's schedule.
	if !reused || *hours != "" {
		if err := st.SetHours(ctx, venueID, h); err != nil {
			fmt.Fprintf(os.Stderr, "provision: set hours: %v\n", err)
			return 1
		}
	}
	deviceID, err := st.CreateDevice(ctx, store.Device{
		VenueID: venueID, Serial: *serial,
		CfgVer: 1, CfgIntervalS: *interval, CfgQuality: *quality,
	}, auth.HashToken(*token))
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision: create device: %v\n", err)
		return 1
	}

	// The token is shown once — nothing recoverable is stored (§4).
	hoursLabel := *hours
	if reused && *hours == "" {
		hoursLabel = "(unchanged)"
	}
	fmt.Printf(`
venue  %s  (slug %q, style %s, hours %s)
device %q  (id %d)
token  %s

  ***  STORE THIS TOKEN NOW — IT IS SHOWN ONLY ONCE.  ***
`,
		vName, *slug, vStyle, hoursLabel, *serial, deviceID, *token)

	// Extra devices for the fakecam gate: same venue, distinct identities
	// (each hits its own per-device rate bucket, §6.1).
	for i := 2; i <= *devices; i++ {
		tok, err := auth.GenerateToken()
		if err != nil {
			fmt.Fprintf(os.Stderr, "provision: token: %v\n", err)
			return 1
		}
		serial := fmt.Sprintf("%s-%d", *serial, i)
		id, err := st.CreateDevice(ctx, store.Device{
			VenueID: venueID, Serial: serial,
			CfgVer: 1, CfgIntervalS: *interval, CfgQuality: *quality,
		}, auth.HashToken(tok))
		if err != nil {
			fmt.Fprintf(os.Stderr, "provision: create device %d: %v\n", i, err)
			return 1
		}
		fmt.Printf("device %q  (id %d)\ntoken  %s\n", serial, id, tok)
	}

	if imported {
		fmt.Printf(`
Imported existing token for %q (no re-flash needed — the board already
carries it; only its POST will now hit this server).
Public page: /%s/
`, *serial, *slug)
		return 0
	}

	fmt.Printf(`
Next:
  1. Copy firmware/include/settings.example.h → settings.h, put the token in
     DEVICE_TOKEN, set WIFI_SSID/WIFI_PASSWORD; flash over USB.
  2. Set INGEST_HOST to this server; keep CA_ROOT_PEM matched to its cert.
  3. First POST /v1/ingest should answer 201; the public page is /%s/
  4. Gate harness: go run ./tools/fakecam -addr ... -tokens <t1,t2,...>
`, *slug)
	return 0
}

func storePath(dataDir string) string { return dataDir + "/guzva.db" }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a recoverable condition for a token.
		panic(fmt.Sprintf("entropy: %v", err))
	}
	return hex.EncodeToString(b)
}
