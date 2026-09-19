// file: internal/audiobooks/revert_service_organize_test.go
// version: 1.1.0
// guid: 4f8c2a1d-5e9b-4f70-a3c6-8d1e0f2b9a47
// last-edited: 2026-09-19

package audiobooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestRevertChange_OrganizeRename verifies that organize_rename change
// types are reversible by the RevertService — the same file_move
// reversal path, just with the change_type the organize service writes.
func TestRevertChange_OrganizeRename(t *testing.T) {
	tmpDir := t.TempDir()
	oldPath := filepath.Join(tmpDir, "old", "Book.m4b")
	newPath := filepath.Join(tmpDir, "new", "Author - Book.m4b")

	if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
		t.Fatalf("mkdir old: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir new: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	mockStore := &stubStoreForRevert{
		book: &database.Book{ID: "b1", FilePath: newPath},
	}
	rs := NewRevertService(mockStore)

	change := &database.OperationChange{
		ID:         "c1",
		BookID:     "b1",
		ChangeType: "organize_rename",
		FieldName:  "file_path",
		OldValue:   oldPath,
		NewValue:   newPath,
	}

	if err := rs.revertChange(change); err != nil {
		t.Fatalf("revertChange failed: %v", err)
	}

	// File moved back
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("expected file at %s after revert, got %v", oldPath, err)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Errorf("expected file gone from %s after revert, got %v", newPath, err)
	}

	// Book updated
	if mockStore.book.FilePath != oldPath {
		t.Errorf("Book.FilePath = %q, want %q", mockStore.book.FilePath, oldPath)
	}
}

// TestRevertChange_OrganizeNonMutating verifies that organize_failed,
// organize_skipped, and organize_summary types are treated as no-ops
// by the revert engine.
func TestRevertChange_OrganizeNonMutating(t *testing.T) {
	rs := NewRevertService(nil)
	for _, ct := range []string{"organize_failed", "organize_skipped", "organize_summary"} {
		change := &database.OperationChange{ChangeType: ct}
		if err := rs.revertChange(change); err != nil {
			t.Errorf("revertChange(%s) returned error %v, want nil", ct, err)
		}
	}
}

// stubStoreForRevert satisfies database.Store for the two methods the
// file_move revert path calls. Returning a non-nil error from the
// method we care about is enough for the happy-path test; the rest
// panic if called, which would surface a regression.
type stubStoreForRevert struct {
	database.Store
	book *database.Book
}

// GetBookByID returns an independent copy per read, as PebbleStore does, so
// the caller's edits never reach s.book except through ModifyBook/UpdateBook.
func (s *stubStoreForRevert) GetBookByID(id string) (*database.Book, error) {
	if s.book == nil {
		return nil, nil
	}
	return database.SnapshotBook(s.book)
}

func (s *stubStoreForRevert) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	s.book = b
	return b, nil
}

// GetBookFiles: the book has no book_file rows to repoint.
func (s *stubStoreForRevert) GetBookFiles(string) ([]database.BookFile, error) { return nil, nil }

func (s *stubStoreForRevert) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.book == nil {
		return nil, nil
	}
	cp := *s.book
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return &cp, nil
		}
		return nil, err
	}
	s.book = &cp
	return &cp, nil
}
