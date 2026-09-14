// file: internal/metafetch/writeback_protected_test.go
// version: 1.0.0
// guid: 0d5a8e37-b6c2-4f91-8a3e-c47f1d209b65
// last-edited: 2026-09-14

package metafetch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

type dirChecker struct{ dir string }

func (d dirChecker) IsProtected(path string) bool {
	return strings.HasPrefix(path, d.dir+string(os.PathSeparator))
}

// Write-back's per-file write went straight to fileops.WriteTagsSafe on the
// path it was given. Its only protection was mfs.isProtectedPath (import roots
// and the iTunes library), so a file under a Deluge save_path was rewritten
// in place while it was seeding. The write must go through the same guard as
// every other tag write: refused with tagger.ErrProtectedPathWrite (no
// importer to make a library copy here), the writer never called. An
// unprotected path is written as before.
func TestWriteFileTagsSafe_ProtectedPathIsRefusedNotWritten(t *testing.T) {
	seeding := filepath.Join(t.TempDir(), "seeding")
	protectedFile := filepath.Join(seeding, "book.m4b")
	libraryFile := filepath.Join(t.TempDir(), "book.m4b")

	var written []string
	svc := &Service{
		safeWriteDeps: tagger.SafeWriteDeps{ProtectedCache: dirChecker{dir: seeding}},
		fileTagWrite: func(path string, _ map[string]any) error {
			written = append(written, path)
			return nil
		},
	}
	tags := map[string]any{"title": "T"}

	err := svc.writeFileTagsSafe(protectedFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{})
	if !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want one wrapping tagger.ErrProtectedPathWrite", err)
	}
	if err := svc.writeFileTagsSafe(libraryFile, tags, fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}); err != nil {
		t.Fatalf("unprotected write: %v", err)
	}
	if len(written) != 1 || written[0] != libraryFile {
		t.Errorf("writer called for %v, want only the unprotected %s", written, libraryFile)
	}
}
