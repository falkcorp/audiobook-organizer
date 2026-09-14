// file: internal/metadata/write_protected_fallback_test.go
// version: 1.0.0
// guid: 7a3e9c15-4b6d-4f28-8e01-c5d92b7f4a36
// last-edited: 2026-09-14

package metadata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

type dirProtected struct{ dir string }

func (d dirProtected) IsProtected(path string) bool {
	return strings.HasPrefix(path, d.dir+string(os.PathSeparator))
}

// The guard ran only inside the native taglib writer. Its refusal was treated
// as an ordinary native failure, and WriteMetadataToFile fell back to the CLI
// writers (AtomicParsley --overWrite, ffmpeg) on the ORIGINAL protected path.
// The refusal must come back as tagger.ErrProtectedPathWrite, with no fallback
// and the file untouched.
func TestWriteMetadataToFile_ProtectedRefusalDoesNotFallBackToCLI(t *testing.T) {
	prev := packageSafeWriteDeps
	t.Cleanup(func() { packageSafeWriteDeps = prev })

	seeding := filepath.Join(t.TempDir(), "seeding")
	if err := os.MkdirAll(seeding, 0o755); err != nil {
		t.Fatal(err)
	}
	SetSafeWriteDeps(tagger.SafeWriteDeps{ProtectedCache: dirProtected{dir: seeding}})

	for _, name := range []string{"book.mp3", "book.m4b"} {
		f := filepath.Join(seeding, name)
		content := []byte("not real audio")
		if err := os.WriteFile(f, content, 0o644); err != nil {
			t.Fatal(err)
		}
		err := WriteMetadataToFile(f, map[string]any{"title": "T"}, fileops.OperationConfig{})
		if !errors.Is(err, tagger.ErrProtectedPathWrite) {
			t.Errorf("%s: err = %v, want one wrapping tagger.ErrProtectedPathWrite (no CLI fallback)", name, err)
		}
		got, readErr := os.ReadFile(f)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, content) {
			t.Errorf("%s: protected file changed", name)
		}
	}
}
