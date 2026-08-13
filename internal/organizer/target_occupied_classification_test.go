// file: internal/organizer/target_occupied_classification_test.go
// version: 1.0.0
// guid: 2d7f4a19-8b06-4c35-91ea-3f70c852bd64
// last-edited: 2026-08-13

package organizer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// An occupied target has three possible meanings and they were, until now, one
// byte-identical error string. A production survey of 19,519 occupied-target
// log lines could therefore say only that they happened — not how many needed
// dedup and how many needed file cleanup, which are opposite remedies.
//
//	ByBook   — another book row owns the target. Two rows expand to one name.
//	           Remedy: a dedup candidate.
//	ByOrphan — a file sits there and NO book row claims it. Remedy: delete or
//	           quarantine the file. Opening a dedup candidate is meaningless;
//	           there is no second book.
//	Unknown  — the ownership question was never answered.
//
// The third case is the one that makes the other two worth having. "Orphan" is
// a POSITIVE finding: the database was asked, and answered that nobody owns the
// path. A lookup that never ran — no store wired, or the query itself failed —
// also has a nil occupant. Folding the two together would manufacture orphans
// out of database errors and point cleanup at files that a book may well own.
// These tests exist mostly to hold that line.

// occupiedFixture builds two different source files whose metadata expands to
// the same target, organizes the first, and returns the organizer plus the
// second book, which is now guaranteed to collide.
func occupiedFixture(t *testing.T, store database.Store) (*Organizer, *database.Book) {
	t.Helper()

	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "source")
	dstDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	srcA := filepath.Join(srcDir, "a.m4b")
	srcB := filepath.Join(srcDir, "b.m4b")
	if err := os.WriteFile(srcA, []byte("content A"), 0644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(srcB, []byte("content B - different bytes"), 0644); err != nil {
		t.Fatalf("write B: %v", err)
	}

	org := NewOrganizer(&config.Config{
		RootDir:              dstDir,
		FolderNamingPattern:  "{author}",
		FileNamingPattern:    "{title}",
		OrganizationStrategy: "copy",
	})

	// Organize A with no store, so the fixture's own setup cannot depend on
	// the lookup behaviour under test.
	bookA := &database.Book{
		ID:       "book-a",
		Title:    "Foundation",
		FilePath: srcA,
		Author:   &database.Author{Name: "Asimov"},
	}
	if _, _, err := org.OrganizeBook(bookA); err != nil {
		t.Fatalf("fixture setup: organizing A must succeed, got %v", err)
	}

	if store != nil {
		org.SetStore(store)
	}
	return org, &database.Book{
		ID:       "book-b",
		Title:    "Foundation",
		FilePath: srcB,
		Author:   &database.Author{Name: "Asimov"},
	}
}

// assertExactlyOneKind pins that the three sub-sentinels are mutually
// exclusive. Without this an implementation could tag an error with two of them
// and satisfy every individual errors.Is assertion while making the counts
// derived from them sum to more than the number of failures.
func assertExactlyOneKind(t *testing.T, err error, want error) {
	t.Helper()

	if !errors.Is(err, ErrTargetOccupied) {
		t.Errorf("every occupied-target error must still satisfy errors.Is(err, ErrTargetOccupied); got %v", err)
	}

	kinds := []struct {
		name string
		err  error
	}{
		{"ByBook", ErrTargetOccupiedByBook},
		{"ByOrphan", ErrTargetOccupiedByOrphan},
		{"Unknown", ErrTargetOccupantUnknown},
	}
	matched := 0
	for _, k := range kinds {
		if errors.Is(err, k.err) {
			matched++
			if k.err != want {
				t.Errorf("error was tagged %s, want %v; full error: %v", k.name, want, err)
			}
		}
	}
	if matched != 1 {
		t.Errorf("expected exactly one of ByBook/ByOrphan/Unknown to match, %d did: %v", matched, err)
	}
}

// TestTargetOccupied_ByAnotherBook — the dedup-actionable case.
func TestTargetOccupied_ByAnotherBook(t *testing.T) {
	occupantID := "book-occupant-01"
	store := &database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) {
			return &database.Book{ID: occupantID, Title: "Foundation", FilePath: path}, nil
		},
	}

	org, bookB := occupiedFixture(t, store)
	_, _, err := org.OrganizeBook(bookB)
	if err == nil {
		t.Fatal("expected an occupied-target error, got nil")
	}
	assertExactlyOneKind(t, err, ErrTargetOccupiedByBook)

	// The occupant's ID has to reach the message. Knowing "another book owns
	// it" without knowing WHICH book leaves the dedup candidate unbuildable,
	// which is the entire point of distinguishing this case.
	if !strings.Contains(err.Error(), occupantID) {
		t.Errorf("error must name the occupying book %q so a dedup candidate can be built; got %v",
			occupantID, err)
	}
}

// TestTargetOccupied_ByOrphanFile — the cleanup-actionable case. The lookup
// runs and succeeds, and reports that no book row owns the path.
func TestTargetOccupied_ByOrphanFile(t *testing.T) {
	store := &database.MockStore{
		GetBookByFilePathFunc: func(string) (*database.Book, error) {
			return nil, nil
		},
	}

	org, bookB := occupiedFixture(t, store)
	_, _, err := org.OrganizeBook(bookB)
	if err == nil {
		t.Fatal("expected an occupied-target error, got nil")
	}
	assertExactlyOneKind(t, err, ErrTargetOccupiedByOrphan)
}

// TestTargetOccupied_FailedLookupIsNotAnOrphan is the load-bearing test.
//
// A failed lookup and a successful "nobody owns it" both yield a nil occupant.
// If the classifier keys off the occupant pointer alone, every database error
// during an organize run is reported as an orphan file — and orphan is the
// finding whose remedy is deleting the file.
func TestTargetOccupied_FailedLookupIsNotAnOrphan(t *testing.T) {
	store := &database.MockStore{
		GetBookByFilePathFunc: func(string) (*database.Book, error) {
			return nil, errors.New("pebble: iterator error")
		},
	}

	org, bookB := occupiedFixture(t, store)
	_, _, err := org.OrganizeBook(bookB)
	if err == nil {
		t.Fatal("expected an occupied-target error, got nil")
	}
	assertExactlyOneKind(t, err, ErrTargetOccupantUnknown)

	if errors.Is(err, ErrTargetOccupiedByOrphan) {
		t.Error("a FAILED ownership lookup was reported as an orphan — this sends cleanup at a file that may well be owned")
	}
}

// TestTargetOccupied_NoStoreIsUnknown covers the other way the question goes
// unasked. Every pre-existing collision test in this package constructs the
// organizer without a store, so this is also the case they all now produce.
func TestTargetOccupied_NoStoreIsUnknown(t *testing.T) {
	org, bookB := occupiedFixture(t, nil)
	_, _, err := org.OrganizeBook(bookB)
	if err == nil {
		t.Fatal("expected an occupied-target error, got nil")
	}
	assertExactlyOneKind(t, err, ErrTargetOccupantUnknown)
}

// TestTargetOccupied_SelfOwnedTargetIsStillANoOp guards the case that must NOT
// become an error. When the DB says this exact book already owns the target,
// it is a re-organize no-op — the same successful lookup that distinguishes
// ByBook from ByOrphan is the one that has to keep letting this through.
func TestTargetOccupied_SelfOwnedTargetIsStillANoOp(t *testing.T) {
	store := &database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) {
			return &database.Book{ID: "book-b", Title: "Foundation", FilePath: path}, nil
		},
	}

	org, bookB := occupiedFixture(t, store)
	target, _, err := org.OrganizeBook(bookB)
	if err != nil {
		t.Fatalf("a book that already owns the target must be a no-op, got %v", err)
	}
	if target == "" {
		t.Error("no-op must still report the target path")
	}
}
