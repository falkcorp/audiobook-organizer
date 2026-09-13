// file: internal/scanner/path_containment_boundary_test.go
// version: 1.0.0
// guid: e6f1a3c8-5d24-4b07-9a6e-3c8b2f7d10a5
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// saveBookToDatabase decides "is this copy in the library" from RootDir in four
// places: the organized-hash stamp, the promote-vs-link choice for a whole-file
// hash duplicate, the primary election of that link, and the primary election
// of a multi-file segment match. A sibling directory whose name starts with the
// root ("<base>/lib2" next to "<base>/lib") is outside the library in all four.

type boundaryEnv struct {
	store *database.PebbleStore
	base  string
	root  string
}

func setupBoundaryScanner(t *testing.T) boundaryEnv {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})
	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	base := t.TempDir()
	root := filepath.Join(base, "lib")
	config.AppConfig.RootDir = root
	return boundaryEnv{store: store, base: base, root: root}
}

func mustBookAt(t *testing.T, store *database.PebbleStore, path string) *database.Book {
	t.Helper()
	b, err := store.GetBookByFilePath(path)
	if err != nil || b == nil {
		t.Fatalf("no book row at %s: %v", path, err)
	}
	return b
}

func isPrimary(b *database.Book) bool { return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion }

func TestSaveBook_SiblingOfRootGetsNoOrganizedHash(t *testing.T) {
	env := setupBoundaryScanner(t)
	path := filepath.Join(env.base, "lib2", "a.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: path, Title: "A", Author: "Auth", Format: ".m4b", FileHash: "hash-sibling-organized"}); err != nil {
		t.Fatal(err)
	}
	if b := mustBookAt(t, env.store, path); b.OrganizedFileHash != nil && *b.OrganizedFileHash != "" {
		t.Errorf("OrganizedFileHash = %q for a book outside %s", *b.OrganizedFileHash, env.root)
	}

	// Control: inside the root the organized hash is stamped.
	in := filepath.Join(env.root, "b.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: in, Title: "B", Author: "Auth", Format: ".m4b", FileHash: "hash-inside-organized"}); err != nil {
		t.Fatal(err)
	}
	if b := mustBookAt(t, env.store, in); b.OrganizedFileHash == nil || *b.OrganizedFileHash != "hash-inside-organized" {
		t.Errorf("control: OrganizedFileHash not stamped inside the root")
	}
}

// Whole-file hash duplicate: existing copy outside, new copy in a sibling of
// the root. Neither is in the library, so they are linked with the existing
// copy primary. A bare prefix check promoted the sibling instead (site 1) or
// made it the primary (site 4).
func TestSaveBook_HashDuplicateInSiblingIsLinkedNotPromoted(t *testing.T) {
	env := setupBoundaryScanner(t)
	hash := "hash-dup-sibling-new"
	existingPath := filepath.Join(env.base, "src", "a.m4b")
	existing, err := env.store.CreateBook(&database.Book{Title: "A", FilePath: existingPath, FileHash: &hash, OriginalFileHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(env.base, "lib2", "a.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: newPath, Title: "A", Author: "Auth", Format: ".m4b", FileHash: hash}); err != nil {
		t.Fatal(err)
	}

	created := mustBookAt(t, env.store, newPath)
	if created.ID == existing.ID {
		t.Fatalf("the existing row was promoted to %s instead of a new linked row being created", newPath)
	}
	if isPrimary(created) {
		t.Errorf("new copy at %s (outside %s) was elected primary", newPath, env.root)
	}
	old, _ := env.store.GetBookByID(existing.ID)
	if old == nil || !isPrimary(old) || old.VersionGroupID == nil || *old.VersionGroupID == "" {
		t.Errorf("existing copy should be the linked primary, got %+v", old)
	}
}

// Whole-file hash duplicate: existing copy in a sibling of the root, new copy
// inside the root. The new copy is the only one in the library, so the
// existing row is promoted (no version group). A bare prefix check counted the
// sibling as "in the library" and linked the two instead (site 2).
func TestSaveBook_HashDuplicateExistingInSiblingIsPromoted(t *testing.T) {
	env := setupBoundaryScanner(t)
	hash := "hash-dup-sibling-existing"
	existing, err := env.store.CreateBook(&database.Book{Title: "A", FilePath: filepath.Join(env.base, "lib2", "a.m4b"), FileHash: &hash, OriginalFileHash: &hash})
	if err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(env.root, "a.m4b")
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: newPath, Title: "A", Author: "Auth", Format: ".m4b", FileHash: hash}); err != nil {
		t.Fatal(err)
	}
	old, _ := env.store.GetBookByID(existing.ID)
	if old != nil && old.VersionGroupID != nil && *old.VersionGroupID != "" {
		t.Errorf("existing copy in %s2 was version-linked as if it were in the library; want the promote branch", env.root)
	}
}

func writeBoundarySegments(t *testing.T, dir string, contents ...string) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, c := range contents {
		p := filepath.Join(dir, string(rune('a'+i))+".mp3")
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func seedSegmentBook(t *testing.T, env boundaryEnv, dir string, contents ...string) *database.Book {
	t.Helper()
	segs := writeBoundarySegments(t, dir, contents...)
	b, err := env.store.CreateBook(&database.Book{Title: "Seg " + filepath.Base(dir), FilePath: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range segs {
		h, err := ComputeFileHash(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.store.CreateBookFile(&database.BookFile{ID: b.ID + "-" + string(rune('a'+i)), BookID: b.ID, FilePath: p, FileHash: h}); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func saveSegmentBook(t *testing.T, dir, wholeHash string, contents ...string) {
	t.Helper()
	segs := writeBoundarySegments(t, dir, contents...)
	if err := saveBookToDatabase(context.Background(), &Book{FilePath: dir, Title: "New " + filepath.Base(dir), Author: "Auth", Format: ".mp3", FileHash: wholeHash, SegmentFiles: segs}); err != nil {
		t.Fatal(err)
	}
}

// Multi-file match, matched book in a sibling of the root, new copy inside the
// root: the new copy is the library copy and must be primary (site 3).
func TestSaveBook_SegmentMatchInSiblingIsNotTheLibraryCopy(t *testing.T) {
	env := setupBoundaryScanner(t)
	matched := seedSegmentBook(t, env, filepath.Join(env.base, "lib2", "Book"), "seg-one-A", "seg-two-A")
	newDir := filepath.Join(env.root, "Book")
	saveSegmentBook(t, newDir, "whole-hash-A", "seg-one-A", "seg-two-A")

	if created := mustBookAt(t, env.store, newDir); !isPrimary(created) {
		t.Errorf("copy inside %s should be primary", env.root)
	}
	if m, _ := env.store.GetBookByID(matched.ID); m == nil || isPrimary(m) {
		t.Errorf("matched copy in %s2 should not be primary", env.root)
	}
}

// Multi-file match, matched book outside, new copy in a sibling of the root:
// neither is in the library, so the matched book stays primary (site 4).
func TestSaveBook_SegmentMatchNewCopyInSiblingIsNotPrimary(t *testing.T) {
	env := setupBoundaryScanner(t)
	matched := seedSegmentBook(t, env, filepath.Join(env.base, "src", "Book"), "seg-one-B", "seg-two-B")
	newDir := filepath.Join(env.base, "lib2", "Book")
	saveSegmentBook(t, newDir, "whole-hash-B", "seg-one-B", "seg-two-B")

	if created := mustBookAt(t, env.store, newDir); isPrimary(created) {
		t.Errorf("copy in %s2 should not be primary", env.root)
	}
	if m, _ := env.store.GetBookByID(matched.ID); m == nil || !isPrimary(m) {
		t.Errorf("matched copy outside the root should stay primary")
	}
}
