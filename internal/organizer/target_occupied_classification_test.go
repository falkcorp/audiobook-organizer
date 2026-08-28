// file: internal/organizer/target_occupied_classification_test.go
// version: 2.0.0
// guid: 2d7f4a19-8b06-4c35-91ea-3f70c852bd64
// last-edited: 2026-08-28

package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestNextAvailableTargetPathSkipsExistingCopies(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Foundation.m4b")
	for _, path := range []string{
		target,
		filepath.Join(dir, "Foundation_copy1.m4b"),
	} {
		if err := os.WriteFile(path, []byte("already organized"), 0644); err != nil {
			t.Fatalf("write existing target %q: %v", path, err)
		}
	}

	got, err := nextAvailableTargetPath(target)
	if err != nil {
		t.Fatalf("nextAvailableTargetPath() error: %v", err)
	}
	want := filepath.Join(dir, "Foundation_copy2.m4b")
	if got != want {
		t.Errorf("nextAvailableTargetPath() = %q, want %q", got, want)
	}
}

func TestOrganizeBook_SelfOwnedTargetIsStillANoOp(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "source")
	dstDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	src := filepath.Join(srcDir, "Foundation.m4b")
	if err := os.WriteFile(src, []byte("source bytes"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	org := NewOrganizer(&config.Config{
		RootDir:              dstDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	})
	book := &database.Book{
		ID:       "book-b",
		Title:    "Foundation",
		FilePath: src,
		Author:   &database.Author{Name: "Asimov"},
	}
	target, _, err := org.OrganizeBook(book)
	if err != nil {
		t.Fatalf("organize setup: %v", err)
	}

	org.SetStore(&database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) {
			return &database.Book{ID: book.ID, FilePath: path}, nil
		},
	})
	got, method, err := org.OrganizeBook(book)
	if err != nil {
		t.Fatalf("self-owned target must be a no-op: %v", err)
	}
	if got != target || method != "" {
		t.Errorf("self-owned target = (%q, %q), want (%q, \"\")", got, method, target)
	}
}
