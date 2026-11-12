// file: internal/plugins/maintenance/fixer_stores_guard_test.go
// version: 1.0.0
// guid: 91c6e2a7-3d84-4b05-a1f9-7e0c5d2b8a43
// last-edited: 2026-11-12

package maintenance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFixerStores_NoFixerReachesAStoreExceptThroughTheShell is the static half
// of the shell-stores contract. The tripwire tests in stores_fixer_test.go
// run each fixer with deps that fail the test on any store accessor, and prove
// the paths they execute; this pass walks the call graph from every fixer's
// Plan, Replan, Apply, BeginApply and AfterApply and reports every call of a
// FixerStores accessor on anything but FixerStores, and every other ServerDeps
// method, that those methods can reach (tools/fixerstores explains how).
//
// Every report must be on the allowlist, testdata/fixer_stores_allowlist.txt,
// with a reason, and every allowlist entry must still be reported, so the
// list only shrinks: a fixer that starts reading a plugin store, or reaching a
// new dep, fails here until someone has looked at it.
//
// It complements the tripwire tests and does not replace them. The pass is
// variable type analysis (VTA): it is unsound for reflection (a call made
// through reflect or found by name is not an edge) and imprecise for function
// values stored in struct fields (it can add callees that never run, which
// the allowlist records as false positives, and miss ones that do). What it
// adds over the tripwires is the code they do not execute; what the tripwires
// add over it is every call that really happens, whatever it went through.
//
// Where it runs: the test skips under -short, so `make test-short` and the
// short Go jobs never see it. Its package already calls testing.Short(), so
// scripts/ci/fixture_test_packages.py selects it for `make test-fixtures`, the
// "Fixture Tests (no -short, race)" jobs (1/3, 2/3, 3/3) of ci.yml, which run
// without -short; the maintenance tests are split across the three by test
// name and this one lands in whichever shard its name hashes to. The pass
// needs the Go toolchain and the x/tools module pinned in tools/fixerstores
// (a module of its own, so x/tools stays out of the root go.mod).
func TestFixerStores_NoFixerReachesAStoreExceptThroughTheShell(t *testing.T) {
	if testing.Short() {
		t.Skip("walks the whole call graph of the plugin (minutes, several GB); runs in the fixture tests, without -short")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	rep := runFixerStoresPass(t, root)

	// The pass found the fixers the registry registers, no more and no fewer:
	// a fixer type it missed would be a hole, one the registry lacks a dead
	// type to look at.
	var registered []string
	for _, f := range New(nil).Repairs().List() {
		registered = append(registered, reflect.TypeOf(f).String())
	}
	sort.Strings(registered)
	require.Equal(t, registered, rep.Fixers, "the fixer types the pass analysed are not the registry's")
	require.GreaterOrEqual(t, rep.Roots, 2*len(registered), "the pass found too few entry methods; it would pass on nothing")
	require.Len(t, rep.Accessors, 15, "FixerStores changed: review the accessors against the guard")

	allow := readFixerStoresAllowlist(t, filepath.Join("testdata", "fixer_stores_allowlist.txt"))

	seenSinks := map[string]bool{}
	for _, s := range rep.StoreSinks {
		key := s.Func
		seenSinks[key] = true
		if _, ok := allow["store-sink"][key]; !ok {
			t.Errorf("fixer code reaches the store accessor %s on a plugin store, not the shell's, in %s (%s).\n"+
				"  call path: %s\n"+
				"  Read it from the FixerStores of ctx (p.fixerStores(ctx)), or, if the call is safe (the receiver is the shell's, or the path is not real), add\n"+
				"  `store-sink %s # reason` to testdata/fixer_stores_allowlist.txt",
				s.Accessor, s.Func, s.Pos, strings.Join(s.Path, " -> "), key)
		}
	}
	for key, reason := range allow["store-sink"] {
		if !seenSinks[key] {
			t.Errorf("stale allowlist entry `store-sink %s # %s`: the pass no longer reports it; delete the line", key, reason)
		}
	}

	seenDeps := map[string]bool{}
	for _, d := range rep.Deps {
		seenDeps[d.Method] = true
		if _, ok := allow["dep"][d.Method]; !ok {
			t.Errorf("fixer code reaches the plugin dep %s, which is not a FixerStores store (called from %s).\n"+
				"  Add `dep %s # reason` to testdata/fixer_stores_allowlist.txt once someone has decided it may stay outside the shell",
				d.Method, strings.Join(d.Callers, ", "), d.Method)
		}
	}
	for key, reason := range allow["dep"] {
		if !seenDeps[key] {
			t.Errorf("stale allowlist entry `dep %s # %s`: the pass no longer reports it; delete the line", key, reason)
		}
	}

	// What the pass could not follow is not an error (an argument that is a
	// parameter or a field has no body to look at); it is in the log so a
	// reviewer sees how much of the graph the pass took on trust.
	for _, u := range rep.Unresolved {
		t.Logf("unresolved func argument: %s passes one to %s (%s)", u.Caller, u.Callee, u.Pos)
	}
}

// fixerStoresReport is the JSON tools/fixerstores prints.
type fixerStoresReport struct {
	Fixers     []string `json:"fixers"`
	Roots      int      `json:"roots"`
	Reachable  int      `json:"reachable"`
	Accessors  []string `json:"accessors"`
	StoreSinks []struct {
		Func     string   `json:"func"`
		Accessor string   `json:"accessor"`
		Pos      string   `json:"pos"`
		Path     []string `json:"path"`
	} `json:"storeSinks"`
	Deps []struct {
		Method  string   `json:"method"`
		Callers []string `json:"callers"`
	} `json:"deps"`
	Unresolved []struct {
		Caller string `json:"caller"`
		Callee string `json:"callee"`
		Pos    string `json:"pos"`
	} `json:"unresolved"`
}

func runFixerStoresPass(t *testing.T, root string) *fixerStoresReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "-C", filepath.Join(root, "tools", "fixerstores"), "run", ".", "-repo", root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "tools/fixerstores failed:\n%s", stderr.String())
	var rep fixerStoresReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep), "tools/fixerstores printed something that is not its report")
	t.Logf("fixer-stores pass: %d fixer types, %d entry methods, %d reachable functions, %d store sinks, %d deps",
		len(rep.Fixers), rep.Roots, rep.Reachable, len(rep.StoreSinks), len(rep.Deps))
	return &rep
}

// readFixerStoresAllowlist reads `kind name # reason` lines. A line with no
// reason, an unknown kind or a repeated entry fails the test: the reason is
// the review.
func readFixerStoresAllowlist(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	out := map[string]map[string]string{"store-sink": {}, "dep": {}}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, reason, ok := strings.Cut(line, " # ")
		require.Truef(t, ok && strings.TrimSpace(reason) != "", "%s:%d: an entry needs a reason (`kind name # reason`)", path, n)
		kind, name, ok := strings.Cut(strings.TrimSpace(entry), " ")
		require.Truef(t, ok, "%s:%d: want `kind name # reason`", path, n)
		kind, name = strings.TrimSpace(kind), strings.TrimSpace(name)
		set, known := out[kind]
		require.Truef(t, known, "%s:%d: unknown kind %q (want store-sink or dep)", path, n, kind)
		_, dup := set[name]
		require.Falsef(t, dup, "%s:%d: %s %s is listed twice", path, n, kind, name)
		set[name] = strings.TrimSpace(reason)
	}
	require.NoError(t, sc.Err())
	return out
}
