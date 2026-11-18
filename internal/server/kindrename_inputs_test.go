// file: internal/server/kindrename_inputs_test.go
// version: 1.0.0
// guid: 5e2b9a74-0c3d-4f18-9b6e-d17a43c8f205
// last-edited: 2026-11-18

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The registry dump M4's kind-rename generator reads (F5-16; 05-tasks §5T.8.2,
// "Inputs"). The generator is a plain program: it cannot boot a server, and
// the canonical-ID-to-alias table (rule 3) exists only in a booted registry.
// This test boots one the way TestOpIDs_NoRenameWithoutAlias does and checks
// the registry against the checked-in dump, so the dump cannot drift from the
// registry any more than the map can drift from the dump
// (kindrename.TestKindRenameMap_UpToDate checks that half).
//
// The dump lists every canonical ID ("id <ID>") and every alias
// ("alias <OLD> <CANONICAL>"), sorted. Regenerate it with
// UPDATE_KINDRENAME_REGISTRY=1 go test ./internal/server -run
// TestKindRenameInputs_RegistryDump; the header comment lines are kept.
const kindRenameRegistryPath = "../books/cutover/kindrename/testdata/head_registry.golden"

const kindRenameRegistryHeader = `# file: internal/books/cutover/kindrename/testdata/head_registry.golden
# version: 1.0.0
# guid: 0b6d3f81-72a4-4c59-8e1d-93f5a2c7b640
# last-edited: 2026-11-18
#
# HEAD's registered operation IDs and FormerIDs aliases, as a booted registry
# reports them (internal/server/kindrename_inputs_test.go writes and checks
# it). Read by the kind-rename generator (internal/books/cutover/kindrename/gen).
# Regenerate: UPDATE_KINDRENAME_REGISTRY=1 go test ./internal/server -run TestKindRenameInputs_RegistryDump
`

// kindRenameRegistryBody renders the dump's body from a booted registry.
func kindRenameRegistryBody(registered map[string]bool, aliases map[string]string) string {
	var lines []string
	for id := range registered {
		// maintenance_dryrun_default_test.go registers probe jobs from test
		// code; they never exist in a production build (the ledger guard
		// skips them for the same reason).
		if strings.Contains(id, ".test-probe-") {
			continue
		}
		lines = append(lines, "id "+id)
	}
	for old, canon := range aliases {
		lines = append(lines, fmt.Sprintf("alias %s %s", old, canon))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// dumpBody is a golden file's body: every line that is not a "#" comment.
func dumpBody(raw string) string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		if strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}

func TestKindRenameInputs_RegistryDump(t *testing.T) {
	_, registered, aliases := bootRegisteredOpIDs(t)
	want := kindRenameRegistryBody(registered, aliases)
	path := filepath.FromSlash(kindRenameRegistryPath)

	if os.Getenv("UPDATE_KINDRENAME_REGISTRY") == "1" {
		header := kindRenameRegistryHeader
		if prev, err := os.ReadFile(path); err == nil {
			var keep []string
			for _, l := range strings.Split(string(prev), "\n") {
				if !strings.HasPrefix(l, "#") {
					break
				}
				keep = append(keep, l)
			}
			header = strings.Join(keep, "\n") + "\n"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(header+want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with UPDATE_KINDRENAME_REGISTRY=1)", path, err)
	}
	if got := dumpBody(string(raw)); got != want {
		t.Fatalf("%s no longer matches the booted registry; regenerate it with UPDATE_KINDRENAME_REGISTRY=1 and re-run go generate ./internal/books/cutover/kindrename/\n--- file\n%s\n--- registry\n%s", path, got, want)
	}
}
