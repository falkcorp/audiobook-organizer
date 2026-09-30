// file: internal/scanner/scan_lock_order_test.go
// version: 1.0.0
// guid: 5f2c8e71-0a4d-4b36-8d9e-c3a17b6f2e05
// last-edited: 2026-09-30

package scanner

import (
	"os/exec"
	"strings"
	"testing"
)

// R3 of internal/scanlock's lock order: the scanner never takes an L1 key.
//
// L1 is the server's writeBackPathLocks table. It is package-private to
// internal/server and reaches other code only as lockers wired into three
// packages: metafetch (SetPathLocker), organizer (VersionGroupLocker) and
// audiobooks (SetRevertPathLocker). The scanner holds L0 keys across its tag
// read and merge; if it could reach any of those lockers while holding them,
// an apply holding L1 and waiting for L0 would deadlock against it. So R3 is
// a property of the import graph, and this test pins it there: the scanner
// package must not depend, directly or transitively, on any package that can
// hold an L1 locker.
//
// Auto-organize, which does take L1, runs from the server's hook after
// ProcessBooksParallel has returned and every L0 key is released.
func TestScannerCannotReachTheL1Lockers(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	forbidden := []string{
		"/internal/server",
		"/internal/metafetch",
		"/internal/organizer",
		"/internal/audiobooks",
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, f := range forbidden {
			if strings.HasSuffix(dep, f) || strings.Contains(dep, f+"/") {
				t.Errorf("the scanner depends on %s, which can hold an L1 (writeBackPathLocks) locker; R3 says the scanner never takes L1", dep)
			}
		}
	}
}
