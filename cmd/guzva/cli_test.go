package main

import (
	"strings"
	"testing"
)

// The dispatcher contract (W1): a server must never start by accident.
// `guzva <typo>` used to fall through to serveMain and boot the listener.
func TestDispatchUnknownCommandIsUsageError(t *testing.T) {
	code := run([]string{"frobnicate"})
	if code != 2 {
		t.Fatalf("unknown command: exit code = %d, want 2", code)
	}
}

func TestDispatchFlagsAreNotCommands(t *testing.T) {
	// A stray flag as the first word is the same class of typo — help, not serve.
	for _, arg := range []string{"-listen", "--help-please", "-x"} {
		if code := run([]string{arg}); code != 2 {
			t.Errorf("run(%q): exit code = %d, want 2", arg, code)
		}
	}
}

func TestDispatchHelpAndBareGuzva(t *testing.T) {
	// help output is asserted via run()'s return code only; serve is not
	// callable here (it would bind a listener), so the serve case is the
	// unit's ExecStart line, pinned by deploy/guzva.service.
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		if code := run(args); code != 0 {
			t.Errorf("run(%v): exit code = %d, want 0", args, code)
		}
	}
}

func TestDispatchVersion(t *testing.T) {
	// Capture-free smoke: version must exit 0; the value itself is stamped
	// by -ldflags at build time and defaults to "dev".
	code := run([]string{"version"})
	if code != 0 {
		t.Fatalf("version: exit code = %d, want 0", code)
	}
}

func TestUsageNamesEverySubcommand(t *testing.T) {
	// The usage block is the front door; a subcommand missing from it is
	// undiscoverable. Keep in sync with run() in cli.go.
	for _, cmd := range []string{"serve", "provision", "cfg", "hashpw", "version", "help"} {
		if !strings.Contains(usage, cmd) {
			t.Errorf("usage: subcommand %q missing from the help text", cmd)
		}
	}
}
