// Package privacy is the PLAN §5 grep-gate, expressed as a test so it runs
// on every `go test ./...` and again before any commit:
//
//  1. production firmware must never call setInsecure() outside an
//     active `#if INSECURE_TLS` guard;
//  2. the PlatformIO build must pin -DINSECURE_TLS=0.
//
// The package contains no production code — only the gate.
package privacy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot walks up from this test file to the directory holding PLAN.md.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "PLAN.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (PLAN.md) not found above test")
		}
		dir = parent
	}
}

var setInsecureRe = regexp.MustCompile(`setInsecure\s*\(`)

// cppSources returns every firmware C/C++ file worth scanning (build
// artifacts skipped).
func cppSources(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(filepath.Join(root, "firmware"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".pio" || info.Name() == ".git") {
			return filepath.SkipDir
		}
		switch filepath.Ext(path) {
		case ".cpp", ".h", ".hpp", ".c", ".ino":
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSetInsecureOnlyBehindGuard evaluates preprocessor state line by line.
// A call is legal only while the whole active chain contains an
// `#if INSECURE_TLS` frame — `#else` of that guard, any `#if !INSECURE_TLS`,
// and unreachable branches are all rejected.
func TestSetInsecureOnlyBehindGuard(t *testing.T) {
	root := repoRoot(t)
	sources := cppSources(t, root)
	if len(sources) == 0 {
		t.Fatal("no firmware sources found — did the layout move?")
	}

	type frame struct {
		isTLSGuard bool // `#if INSECURE_TLS` exactly (not !, not defined())
		active     bool // this branch is reachable
	}
	allActive := func(stack []frame) bool {
		for _, f := range stack {
			if !f.active {
				return false
			}
		}
		return true
	}
	insideTLSGuard := func(stack []frame) bool {
		if !allActive(stack) {
			return false
		}
		for _, f := range stack {
			if f.isTLSGuard {
				return true
			}
		}
		return false
	}

	calls := 0
	for rel, src := range sources {
		var stack []frame
		for i, line := range strings.Split(src, "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx] // comments may mention the call freely
			}
			trimmed := strings.TrimSpace(code)

			switch {
			case strings.HasPrefix(trimmed, "#if"):
				cond := strings.TrimSpace(strings.TrimPrefix(trimmed, "#if"))
				stack = append(stack, frame{
					isTLSGuard: cond == "INSECURE_TLS",
					active:     allActive(stack),
				})
				continue
			case strings.HasPrefix(trimmed, "#else"), strings.HasPrefix(trimmed, "#elif"):
				if n := len(stack); n > 0 {
					outerReachable := allActive(stack[:n-1])
					stack[n-1].active = outerReachable && !stack[n-1].active
					if strings.HasPrefix(trimmed, "#elif") {
						stack[n-1].isTLSGuard = false // unknown condition: not the guard
					}
				}
				continue
			case strings.HasPrefix(trimmed, "#endif"):
				if n := len(stack); n > 0 {
					stack = stack[:n-1]
				}
				continue
			}

			if setInsecureRe.MatchString(code) {
				calls++
				if !insideTLSGuard(stack) {
					t.Errorf("%s:%d calls setInsecure() outside an active #if INSECURE_TLS guard:\n  %s",
						rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
	if calls == 0 {
		t.Error("no setInsecure() call sites found — the gate lost its subject (firmware changed shape?)")
	}
}

// TestProdBuildDisablesInsecureTLS pins the build flag itself: even a
// guarded call must be compiled out in the shipped build.
func TestProdBuildDisablesInsecureTLS(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "firmware", "platformio.ini"))
	if err != nil {
		t.Fatalf("read platformio.ini: %v", err)
	}
	if !strings.Contains(string(b), "-DINSECURE_TLS=0") {
		t.Error("firmware/platformio.ini must pin -DINSECURE_TLS=0 in build_flags (PLAN §5)")
	}
}
