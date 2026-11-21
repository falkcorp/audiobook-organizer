// file: core/kernel/restore_staging_cleanup_test.go
// version: 0.1.0
// guid: 03e16f2b-6d6b-4280-8160-87d99fc248f5
// last-edited: 2026-11-21

package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestRemoveEmptyStagingKeepsFiles: an applied restore removes a staging
// root's staging/ leaf only when it holds directories alone, then the root
// only when that left it empty. A leaf that still holds a file keeps it (and
// its root), and a root that also holds previous/ (the only undo) keeps
// previous/ and itself.
func TestRemoveEmptyStagingKeepsFiles(t *testing.T) {
	mk := func(t *testing.T, paths ...string) string {
		t.Helper()
		root := filepath.Join(t.TempDir(), ".mediacore-restore", "r1")
		for _, p := range paths {
			full := filepath.Join(root, p)
			if filepath.Ext(p) == ".file" {
				if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("robin"), 0o600); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(full, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	t.Run("empty leaf and root go", func(t *testing.T) {
		root := mk(t, "staging/state")
		if err := removeEmptyStaging(vfs.Default, root); err != nil {
			t.Fatal(err)
		}
		if exists(root) {
			t.Fatalf("%s is still there, want the emptied root removed", root)
		}
	})
	t.Run("a file in the leaf is kept", func(t *testing.T) {
		root := mk(t, "staging/state/alfred_key.file")
		if err := removeEmptyStaging(vfs.Default, root); err != nil {
			t.Fatal(err)
		}
		if f := filepath.Join(root, "staging/state/alfred_key.file"); !exists(f) {
			t.Fatalf("%s was removed, want a staged file kept", f)
		}
	})
	t.Run("previous/ keeps the root", func(t *testing.T) {
		root := mk(t, "staging/state", "previous/main.pebble/MANIFEST.file")
		if err := removeEmptyStaging(vfs.Default, root); err != nil {
			t.Fatal(err)
		}
		if exists(filepath.Join(root, "staging")) {
			t.Fatalf("the emptied staging leaf of %s is still there", root)
		}
		if !exists(filepath.Join(root, "previous/main.pebble/MANIFEST.file")) {
			t.Fatalf("previous/ under %s was removed", root)
		}
	})
	t.Run("no staging leaf is success", func(t *testing.T) {
		root := mk(t, "previous")
		if err := removeEmptyStaging(vfs.Default, root); err != nil {
			t.Fatalf("removeEmptyStaging of a root with no staging leaf = %v, want nil", err)
		}
	})
}
