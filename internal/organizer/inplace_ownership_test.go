// file: internal/organizer/inplace_ownership_test.go
// version: 1.0.0
// guid: 1e5bf761-ee82-4110-865a-948c137494ee
// last-edited: 2026-09-28

package organizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestReOrganizeInPlace_RefusesUnsafeMoves pins each refusal
// refuseUnsafeInPlaceMove makes, plus the move that must still go ahead. Every
// refused file must still be where it was.
func TestReOrganizeInPlace_RefusesUnsafeMoves(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the book to organize and the files that must not move.
		setup        func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string)
		wantCategory string // "" = the move goes ahead
	}{
		{
			name: "chapter owned by another book's book_file row",
			setup: func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string) {
				dir := filepath.Join(root, "Christopher Paolini", "Eldest")
				parent := addInPlaceBook(t, store, "parent", "Eldest", filepath.Join(dir, "97.mp3"), filled(100, 1), nil, 0)
				ch := filepath.Join(dir, "98.mp3")
				if err := os.WriteFile(ch, filled(110, 2), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := store.CreateBookFile(&database.BookFile{ID: "parent-98", BookID: parent.ID, FilePath: ch}); err != nil {
					t.Fatal(err)
				}
				// The fragment book the scanner minted from the chapter, with
				// its own row at the same path.
				frag := addInPlaceBook(t, store, "frag", "98", ch, nil, nil, 0)
				return frag, []string{ch}
			},
			wantCategory: OutcomeOwnedByOtherBook,
		},
		{
			name: "file in the frozen iTunes tree",
			setup: func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string) {
				p := filepath.Join(root, "books", "itunes", "iTunes Media", "Audiobooks", "Someone", "Thing.m4b")
				b := addInPlaceBook(t, store, "itunes", "Thing", p, filled(120, 3), nil, 0)
				return b, []string{p}
			},
			wantCategory: OutcomeFrozenITunes,
		},
		{
			name: "multi-file book whose path is one of its files",
			setup: func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string) {
				dir := filepath.Join(root, "incoming", "Scattered Suns")
				b := addInPlaceBook(t, store, "multi", "Scattered Suns", filepath.Join(dir, "01.mp3"), filled(130, 4), nil, 0)
				second := filepath.Join(dir, "02.mp3")
				if err := os.WriteFile(second, filled(140, 5), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := store.CreateBookFile(&database.BookFile{ID: "multi-2", BookID: b.ID, FilePath: second}); err != nil {
					t.Fatal(err)
				}
				return b, []string{b.FilePath, second}
			},
			wantCategory: OutcomePartialMultiFile,
		},
		{
			name: "single-file book nobody else owns still moves",
			setup: func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string) {
				b := addInPlaceBook(t, store, "solo", "Solo", filepath.Join(root, "incoming", "solo.m4b"), filled(150, 6), nil, 0)
				return b, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, root := setupInPlace(t)
			book, pinned := tc.setup(t, store, root)
			before := book.FilePath

			newPath, err := svc.ReOrganizeInPlace(book, &noopLogger{})
			if tc.wantCategory == "" {
				if err != nil {
					t.Fatalf("move refused: %v", err)
				}
				if newPath == before {
					t.Fatalf("book did not move")
				}
				return
			}
			var conflict *DestinationConflictError
			if !errors.As(err, &conflict) || conflict.Category != tc.wantCategory {
				t.Fatalf("err = %v, want a %s refusal", err, tc.wantCategory)
			}
			for _, p := range pinned {
				if _, statErr := os.Stat(p); statErr != nil {
					t.Fatalf("%s moved or vanished: %v", p, statErr)
				}
			}
			if got := getInPlaceBook(t, store, book.ID); got.FilePath != before {
				t.Fatalf("book path rewritten to %q on a refused move", got.FilePath)
			}
		})
	}
}

// TestOrganizeBooks_OwnedChapterIsSkippedNotFailed: the refusal is a counted
// skip in an organize run, the same as a declined destination conflict.
func TestOrganizeBooks_OwnedChapterIsSkippedNotFailed(t *testing.T) {
	svc, store, root := setupInPlace(t)
	dir := filepath.Join(root, "Kevin J. Anderson", "Scattered Suns")
	parent := addInPlaceBook(t, store, "parent", "Scattered Suns", filepath.Join(dir, "01.mp3"), filled(100, 1), nil, 0)
	ch := filepath.Join(dir, "02.mp3")
	if err := os.WriteFile(ch, filled(110, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "parent-02", BookID: parent.ID, FilePath: ch}); err != nil {
		t.Fatal(err)
	}
	frag := addInPlaceBook(t, store, "frag", "02", ch, nil, nil, 0)

	stats := svc.organizeBooks(context.Background(), []database.Book{*frag}, nil, &noopLogger{}, "")
	if stats.Collisions[OutcomeOwnedByOtherBook] != 1 || stats.Failed != 0 {
		t.Fatalf("want one owned_by_other_book skip and no failure, got %+v", stats)
	}
	mustContent(t, ch, filled(110, 2))
}

// TestCommitLanding_InPlaceMoveRecordsTheRealOldPath: an in-place move used
// to be recorded as organize_skipped with old == new, because
// reOrganizeInPlace had already rewritten book.FilePath by the time
// CommitLanding read it. The record undo reads (organize_rename) must carry
// the path the file was moved FROM.
func TestCommitLanding_InPlaceMoveRecordsTheRealOldPath(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root, "incoming", "solo.m4b")
	book := addInPlaceBook(t, store, "solo", "Solo", src, filled(150, 6), nil, 0)

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), book, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if landing.SourcePath != src {
		t.Fatalf("landing.SourcePath = %q, want %q", landing.SourcePath, src)
	}
	const opID = "op-inplace"
	outcome, _, err := svc.CommitLanding(book, landing, opID, &noopLogger{})
	if err != nil {
		t.Fatalf("CommitLanding: %v", err)
	}
	if outcome != LandingRenamed {
		t.Fatalf("outcome = %v, want LandingRenamed", outcome)
	}
	changes, err := store.GetOperationChanges(opID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range changes {
		if c.ChangeType == "organize_skipped" {
			t.Fatalf("a real move was recorded as organize_skipped: %+v", c)
		}
		if c.ChangeType == "organize_rename" {
			found = true
			if c.OldValue != src || c.NewValue != landing.Path || c.OldValue == c.NewValue {
				t.Fatalf("organize_rename old=%q new=%q, want old=%q new=%q", c.OldValue, c.NewValue, src, landing.Path)
			}
		}
	}
	if !found {
		t.Fatalf("no organize_rename recorded; changes: %+v", changes)
	}
}
