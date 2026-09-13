// file: internal/organizer/cleanup_parents_boundary_test.go
// version: 1.0.0
// guid: 4e1b7c92-6a3d-4f05-b8e2-9d0c5a7f13b8
// last-edited: 2026-09-12

package organizer

import (
	"os"
	"path/filepath"
	"testing"
)

// cleanupEmptyParents must never walk a sibling of stopAt: with stopAt
// "<tmp>/lib", an empty tree under "<tmp>/lib2" is outside the library and must
// be left alone. A bare strings.HasPrefix matched it and removed it.
func TestCleanupEmptyParents_SiblingOfStopAtIsUntouched(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "lib")
	sibling := filepath.Join(tmp, "lib2", "a", "b")
	inside := filepath.Join(root, "a", "b")
	for _, d := range []string{sibling, inside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	svc := &Service{}
	svc.cleanupEmptyParents(sibling, root, &noopLogger{})
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("sibling dir %s was removed: %v", sibling, err)
	}

	// Inside the root, the empty tree is still cleaned up to (not including) root.
	svc.cleanupEmptyParents(inside, root, &noopLogger{})
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatalf("empty dirs under root should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root itself must remain: %v", err)
	}

	// dir == stopAt: nothing to do, root stays.
	svc.cleanupEmptyParents(root, root, &noopLogger{})
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root removed when dir == stopAt: %v", err)
	}
}
