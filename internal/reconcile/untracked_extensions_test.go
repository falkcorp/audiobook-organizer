// file: internal/reconcile/untracked_extensions_test.go
// version: 1.0.0
// guid: d5dc58db-5747-4bec-a312-7673efd9e2a3
// last-edited: 2026-09-01

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// withEmptyExtensionConfig points RootDir at a temp dir and leaves
// SupportedExtensions NIL — the state of any process that has not run
// InitConfig, and the state a user produces by writing
// `supported_extensions: []`. It also zeroes every field appdirs.FromConfig
// reads, because a synthesized backups directory would exclude part of the
// tree and confuse the counts.
func withEmptyExtensionConfig(t *testing.T, exts []string) string {
	t.Helper()
	root := t.TempDir()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) {
		c.RootDir = root
		c.SupportedExtensions = exts
		c.BackupDir, c.OpenLibraryDumpDir = "", ""
		c.DatabasePath, c.PlaylistDir = "", ""
		c.ITunes.LibraryReadPath = ""
	})
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	return root
}

func writeAudio(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, "Author", "Book", n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// 🔴 The fail-open case at a real call site. With SupportedExtensions nil, the
// untracked-file scan must still find audio. If it resolved the nil straight
// through it would find NOTHING and report a library with zero untracked files
// — a clean, wrong, entirely silent answer, and the reconcile UI would show an
// empty import-candidate list for a directory full of books.
func TestFindUntrackedFilesFailsOpenOnNilExtensionConfig(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "one.m4b", "two.aax", "three.aiff", "cover.jpg")

	got, err := FindUntrackedFiles(appDirStore{}, map[string]bool{})
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("found %d untracked files (%v), want 3 — the nil config must fall "+
			"back to the compiled-in default set, not match nothing", len(got), got)
	}
}

func TestFindUntrackedFilesFailsOpenOnEmptyExtensionConfig(t *testing.T) {
	root := withEmptyExtensionConfig(t, []string{})
	writeAudio(t, root, "one.m4b", "two.aaxc")

	got, err := FindUntrackedFiles(appDirStore{}, map[string]bool{})
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d untracked files (%v), want 2", len(got), got)
	}
}

// The default set must reach the extensions the old inline literal covered but
// that a .mp3-only fixture cannot observe.
func TestFindUntrackedFilesCoversTheLongTailExtensions(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "a.aax", "b.aaxc", "c.aiff", "d.aif", "e.mka", "f.oga", "g.wav", "h.wma")

	got, err := FindUntrackedFiles(appDirStore{}, map[string]bool{})
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("found %d untracked files (%v), want all 8", len(got), got)
	}
}

func TestFindUntrackedFilesNarrowsWithTheConfig(t *testing.T) {
	root := withEmptyExtensionConfig(t, []string{".m4b"})
	writeAudio(t, root, "keep.m4b", "drop.aax")

	got, err := FindUntrackedFiles(appDirStore{}, map[string]bool{})
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "keep.m4b" {
		t.Fatalf("got %v, want only the configured .m4b", got)
	}
}
