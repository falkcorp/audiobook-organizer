// file: internal/organizer/inplace_ownership_test.go
// version: 1.1.0
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
			// S5: a soft-deleted book still holding a row at this book's own
			// file does not block the move -- this live book's claim is the
			// one that matters.
			name: "soft-deleted co-owner of the book's own file does not block",
			setup: func(t *testing.T, store *database.PebbleStore, root string) (*database.Book, []string) {
				p := filepath.Join(root, "incoming", "solo.m4b")
				b := addInPlaceBook(t, store, "solo", "Solo", p, filled(150, 6), nil, 0)
				marked := true
				if _, err := store.CreateBook(&database.Book{ID: "gone", Title: "Gone", FilePath: p + ".old", MarkedForDeletion: &marked}); err != nil {
					t.Fatal(err)
				}
				if err := store.CreateBookFile(&database.BookFile{ID: "gone-f", BookID: "gone", FilePath: p}); err != nil {
					t.Fatal(err)
				}
				return b, nil
			},
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
	imported := "imported"
	book.LibraryState = &imported

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
	var found, stateFound bool
	for _, c := range changes {
		if c.ChangeType == "metadata_update" && c.FieldName == "library_state" {
			stateFound = true
			// reOrganizeInPlace stamps the in-memory book "organized" as it
			// moves it; the record must carry the state from before the move.
			if c.OldValue != "imported" || c.NewValue != "organized" {
				t.Fatalf("library_state record old=%q new=%q, want imported -> organized", c.OldValue, c.NewValue)
			}
		}
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
	if !found || !stateFound {
		t.Fatalf("organize_rename recorded=%v, library_state recorded=%v; changes: %+v", found, stateFound, changes)
	}
}

// TestReOrganizeInPlace_MultiFileBookMovesEveryFile is S3 of the 2026-09-28
// review: a multi-file book whose path is one of its files (the scanner's
// sub-grouped books keep FilePath on their first file) used to be refused as
// partial_multi_file forever, so it stayed library_state=imported and ABS hid
// it. Every present file now moves into the book's target directory, every
// row follows its file, a Missing row stays where it was, and no row is lost.
func TestReOrganizeInPlace_MultiFileBookMovesEveryFile(t *testing.T) {
	svc, store, root := setupInPlace(t)
	dir := filepath.Join(root, "incoming", "Scattered Suns")
	b := addInPlaceBook(t, store, "multi", "Scattered Suns", filepath.Join(dir, "01.mp3"), filled(130, 4), nil, 0)
	second := filepath.Join(dir, "02.mp3")
	if err := os.WriteFile(second, filled(140, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-2", BookID: b.ID, FilePath: second}); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "03.mp3")
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-3", BookID: b.ID, FilePath: gone, Missing: true}); err != nil {
		t.Fatal(err)
	}
	// A different book in the same shared folder must not move.
	other := addInPlaceBook(t, store, "other", "Other", filepath.Join(dir, "Other.mp3"), filled(90, 7), nil, 0)
	imported := "imported"
	b.LibraryState = &imported

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if !landing.MultiFile || len(landing.FileMoves) != 2 {
		t.Fatalf("landing = %+v, want a multi-file landing with 2 moves", landing)
	}
	targetDir := landing.Path
	mustContent(t, filepath.Join(targetDir, "01.mp3"), filled(130, 4))
	mustContent(t, filepath.Join(targetDir, "02.mp3"), filled(140, 5))
	mustContent(t, other.FilePath, filled(90, 7))

	got := getInPlaceBook(t, store, b.ID)
	if got.FilePath != targetDir || got.LibraryState == nil || *got.LibraryState != "organized" {
		t.Fatalf("book path=%q state=%v, want %q organized", got.FilePath, got.LibraryState, targetDir)
	}
	rows, err := store.GetBookFiles(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, r := range rows {
		paths[r.ID] = r.FilePath
	}
	want := map[string]string{
		"multi-f": filepath.Join(targetDir, "01.mp3"),
		"multi-2": filepath.Join(targetDir, "02.mp3"),
		"multi-3": gone,
	}
	if len(paths) != len(want) {
		t.Fatalf("rows = %v, want %v (a row was lost or added)", paths, want)
	}
	for id, p := range want {
		if paths[id] != p {
			t.Fatalf("row %s at %q, want %q", id, paths[id], p)
		}
	}

	const opID = "op-multi"
	if outcome, _, err := svc.CommitLanding(b, landing, opID, &noopLogger{}); err != nil || outcome != LandingRenamed {
		t.Fatalf("CommitLanding = %v, %v", outcome, err)
	}
	changes, err := store.GetOperationChanges(opID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.ChangeType]++
	}
	if counts["organize_rename"] != 0 || counts["book_file_move"] != 2 || counts["book_path_update"] != 1 {
		t.Fatalf("recorded %v; want 2 book_file_move + 1 book_path_update and no organize_rename", counts)
	}
}

// TestReOrganizeInPlace_MultiFileRefusesBeforeMovingAnything: one file of
// the book owned by a different book refuses the WHOLE move, before any file
// moves.
func TestReOrganizeInPlace_MultiFileRefusesBeforeMovingAnything(t *testing.T) {
	svc, store, root := setupInPlace(t)
	dir := filepath.Join(root, "incoming", "Eldest")
	b := addInPlaceBook(t, store, "multi", "Eldest", filepath.Join(dir, "01.mp3"), filled(130, 4), nil, 0)
	second := filepath.Join(dir, "02.mp3")
	if err := os.WriteFile(second, filled(140, 5), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "multi-2", BookID: b.ID, FilePath: second}); err != nil {
		t.Fatal(err)
	}
	addInPlaceBook(t, store, "frag", "02", second, nil, nil, 0)

	_, err := svc.ReOrganizeInPlace(b, &noopLogger{})
	var conflict *DestinationConflictError
	if !errors.As(err, &conflict) || conflict.Category != OutcomeOwnedByOtherBook {
		t.Fatalf("err = %v, want owned_by_other_book", err)
	}
	mustContent(t, b.FilePath, filled(130, 4))
	mustContent(t, second, filled(140, 5))
}

// TestReOrganizeInPlace_OwnershipUnverifiedDuringWarmup is S4: while the
// complete ownership index is unavailable the organizer must not act on the
// single-row fallback. The move is skipped (retried later), never made.
func TestReOrganizeInPlace_OwnershipUnverifiedDuringWarmup(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root, "incoming", "solo.m4b")
	b := addInPlaceBook(t, store, "solo", "Solo", src, filled(150, 6), nil, 0)
	store.UseMemDB = false // memdb not serving: the warmup window

	stats := svc.organizeBooks(context.Background(), []database.Book{*b}, nil, &noopLogger{}, "")
	if stats.Collisions[OutcomeOwnershipUnverified] != 1 || stats.Failed != 0 || stats.Skipped != 1 {
		t.Fatalf("want one ownership_unverified skip, got %+v", stats)
	}
	mustContent(t, src, filled(150, 6))
}
