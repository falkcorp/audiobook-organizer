// file: internal/server/library_sizes_boundary_test.go
// version: 1.0.0
// guid: 2d6f8b13-c4e7-4a90-9f35-81b0e6a2c7d4
// last-edited: 2026-09-12

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// expireLibrarySizeCache forces the next calculateLibrarySizes call to walk
// the disk instead of returning a cached result from an earlier call.
func expireLibrarySizeCache(t *testing.T) {
	t.Helper()
	cacheLock.Lock()
	cachedSizeComputedAt = time.Time{}
	cacheLock.Unlock()
}

// An import folder that is a SIBLING of the library root ("<base>/lib2" next to
// "<base>/lib") is not under the root, so its files count toward the import
// size. A bare prefix check skipped them as if they were library files.
func TestCalculateLibrarySizes_SiblingImportFolderIsCounted(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "lib")
	sibling := filepath.Join(base, "lib2")
	for _, d := range []string{root, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.m4b"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "b.m4b"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}

	expireLibrarySizeCache(t)
	t.Cleanup(func() { expireLibrarySizeCache(t) })
	_, importSize := calculateLibrarySizes(root, []database.ImportPath{{ID: 1, Path: sibling, Enabled: true}})
	if importSize <= 0 {
		t.Fatalf("importSize = %d, want > 0: %s is a sibling of %s, not inside it", importSize, sibling, root)
	}

	// Control: an import folder really inside the root is still skipped.
	inner := filepath.Join(root, "incoming")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "c.m4b"), make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}
	expireLibrarySizeCache(t)
	if _, got := calculateLibrarySizes(root, []database.ImportPath{{ID: 2, Path: inner, Enabled: true}}); got != 0 {
		t.Fatalf("importSize for an import folder inside the root = %d, want 0", got)
	}
}
