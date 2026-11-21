// file: internal/arch/typedload_copy_test.go
// version: 1.0.0
// guid: 3b9d6e14-7a52-4c08-8f31-e5a0c2d47b69
// last-edited: 2026-11-21

package arch

import (
	"os"
	"strings"
	"testing"
)

// TestTypedloadCopyMatches: tools/fixerstores is a module of its own, so it
// carries a copy of core/archtest/typedload (the serial type-checking loader
// every typed packages.Load goes through; see its doc for golang/go#81122).
// From the package clause on, the copy is byte for byte the original: a fix to
// one must be made in both, and this fails when they drift.
func TestTypedloadCopyMatches(t *testing.T) {
	const orig = "../../core/archtest/typedload/load.go"
	const dup = "../../tools/fixerstores/typedload/load.go"
	body := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		i := strings.Index(s, "\npackage typedload\n")
		if i < 0 {
			t.Fatalf("%s has no package typedload clause", path)
		}
		return s[i:]
	}
	if body(orig) != body(dup) {
		t.Errorf("%s differs from %s after the package clause; make the same change in both", dup, orig)
	}
}
