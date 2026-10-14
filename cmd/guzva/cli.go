package main

// The command dispatcher (maintenance W1). Until now main() was a manual
// switch that fell through to serveMain for ANY unknown word — `guzva
// frobnicate` silently started a server. Rules now:
//
//   - `guzva` with no arguments prints help. A server that starts because
//     of a typo is the worst outcome; the systemd unit always passes an
//     explicit `serve` (deploy/guzva.service ExecStart).
//   - every subcommand is explicit; unknown words exit 2 with help.
//   - exit codes: 0 ok, 1 runtime error, 2 usage error (flag pkg included).
//
// No framework: flag.FlagSet per subcommand, one switch here. A CLI library
// would buy help formatting we already get from flag, at the cost of a
// dependency PLAN §2 says not to take.

import (
	"fmt"
	"os"
)

// usage is the top-level help. Each subcommand's own flags document
// themselves via `guzva <cmd> -h` (flag prints them).
const usage = `guzva — venue stills: one static Go binary (PLAN §2, docs/cli.md)

Usage:
  guzva <command> [flags]

Commands:
  serve       run the server: device ingest + public pages + consoles
              (the systemd unit runs exactly this)
  provision   create a venue + device, print the device token once
  hashpw      bcrypt-hash a password for GUZVA_ADMIN_HASH
  version     print the build version (same stamp /healthz reports)
  help        this usage

Per-command flags: guzva <command> -h
Docs: docs/cli.md (reference), docs/runbook.md (operations)
`

// run dispatches argv (without the program name) and returns the exit code.
// It exists so the dispatch table is unit-testable without exec-ing.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "serve":
		return serveMain(args[1:])
	case "provision":
		return provisionMain(args[1:])
	case "hashpw":
		return hashpwMain(args[1:])
	case "version":
		fmt.Println(version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "guzva: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
