// file: internal/merge/itunes_guard_symlink_test.go
// version: 1.0.0
// guid: 9f1d4b62-2e7a-4c85-b3f0-6a8c1e5d7b24
// last-edited: 2026-09-13

package merge

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A path outside books/itunes/ that reaches it through a symlink is the same
// files, and is refused -- including a target that does not exist yet.
func TestCheckITunesPath_SymlinkIntoITunesTreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "books", "itunes", "Author")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lib", "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, p := range []string{
		link,
		filepath.Join(link, "Book", "not-yet-written.m4b"),
	} {
		err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: p}}, nil)
		if !errors.Is(err, ErrITunesProtected) {
			t.Errorf("path %s: err = %v, want ErrITunesProtected", p, err)
		}
	}

	plain := filepath.Join(dir, "lib", "plain", "book.m4b")
	if err := GuardITunesProtectedLoaded([]*database.Book{{ID: "b", FilePath: plain}}, nil); err != nil {
		t.Fatalf("plain path refused: %v", err)
	}
}
