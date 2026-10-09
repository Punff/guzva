package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"guzva/internal/proto"
	"guzva/internal/store"
)

// cfgMain shows or changes a device's desired_cfg (the 22-key device plane
// of docs/proto-v1.md). It is the offline companion to the admin console:
// SSH-and-run, same as provision (§4: installer flow before Phase 3 claims).
//
//	guzva cfg -serial bd2a58                          # show current config
//	guzva cfg -serial bd2a58 -set framesize=5,led_pwm=80
//
// Every change increments cfg_ver (the device echoes it back as
// cfg_ver_applied) and takes effect on the device's next POST — the config
// rides the next response, no extra round-trip.
func cfgMain(args []string) int {
	fs := flag.NewFlagSet("cfg", flag.ExitOnError)
	dataDir := fs.String("data", "/var/lib/guzva", "data dir (must match serve -data)")
	serial := fs.String("serial", "", "device serial (required)")
	set := fs.String("set", "", `key=value pairs, e.g. "framesize=5,led_pwm=80" (empty = show only)`)
	fs.Parse(args)

	if *serial == "" {
		fmt.Fprintln(os.Stderr, "usage: guzva cfg -serial <serial> [-set key=value,...]")
		fmt.Fprintf(os.Stderr, "settable keys: %s\n", strings.Join(proto.SettableCfgKeys(), ", "))
		return 2
	}

	changes, err := parseCfgSet(*set)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cfg: %v\n", err)
		return 2
	}

	st, err := store.Open(storePath(*dataDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cfg: %v\n", err)
		return 1
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dev, err := st.DeviceBySerial(ctx, *serial)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cfg: device %q: %v\n", *serial, err)
		return 1
	}

	if len(changes) > 0 {
		next := dev.DesiredCfg()
		next.Ver = dev.CfgVer + 1 // ver increments on every change (§1)
		if next.Extra == nil {
			next.Extra = map[string]int{}
		}
		for k, v := range changes {
			switch k {
			case "interval_s":
				next.IntervalS = v
			case "quality":
				next.Quality = v
			default:
				next.Extra[k] = v
			}
		}
		if err := st.SetDeviceCfg(ctx, dev.ID, next); err != nil {
			fmt.Fprintf(os.Stderr, "cfg: %v\n", err)
			return 1
		}
		// v3.1: config changes are slog audit lines (§6.2).
		keys := make([]string, 0, len(changes))
		for k := range changes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("cfg  device %q  ver %d → %d  applied on next POST\n",
			*serial, dev.CfgVer, next.Ver)
		for _, k := range keys {
			fmt.Printf("      %s = %d\n", k, changes[k])
		}
		dev.CfgVer, dev.CfgIntervalS, dev.CfgQuality, dev.CfgExtra =
			next.Ver, next.IntervalS, next.Quality, next.Extra
	}

	b, _ := json.MarshalIndent(dev.DesiredCfg(), "", "  ")
	fmt.Printf("device %q (id %d, status %s)\ndesired_cfg:\n%s\napplied ver: %d\n",
		dev.Serial, dev.ID, dev.Status, b, dev.CfgAppliedVer)
	if dev.CfgAppliedVer != dev.CfgVer {
		fmt.Println("note: device has not applied this version yet (amber on the admin board)")
	}
	return 0
}

// parseCfgSet parses "k=v,k=v" into validated changes. Empty input returns
// a nil map (show-only mode).
func parseCfgSet(s string) (map[string]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]int{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("bad pair %q (want key=value)", pair)
		}
		key = strings.TrimSpace(key)
		v, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return nil, fmt.Errorf("bad value in %q: %q is not an integer", pair, val)
		}
		if err := proto.ValidateCfgKey(key, v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}
