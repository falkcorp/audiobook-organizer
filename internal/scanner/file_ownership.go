// file: internal/scanner/file_ownership.go
// version: 1.0.0
// guid: f938af2f-e090-48ab-b6b0-c89267a7adbf
// last-edited: 2026-09-28

package scanner

import (
	"fmt"
	"os"
	"slices"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fragmentSkipCount counts scanned books that were NOT saved because their
// files already belong to another book's book_file rows. Reported once per run
// in the scan summary.
var fragmentSkipCount atomic.Int64

// fileOwnershipVerdict is checkFileOwnership's answer.
type fileOwnershipVerdict struct {
	// skip is true when saving this scanned book would mint a NEW book over
	// files another book already owns.
	skip bool
	// owners are the live books that own at least one scanned file, sorted,
	// for the log line.
	owners []string
	// reason says which shape was refused.
	reason string
}

// scannedFilesOf returns the files a scanned Book would claim: its segment
// list, or its own path for a single-file book. A directory-shaped book (no
// segment list, FilePath is a folder) returns nil: its files are enumerated
// only later, by createBookFilesForBook, and it is found by its directory path.
func scannedFilesOf(book *Book) []string {
	if len(book.SegmentFiles) > 0 {
		return book.SegmentFiles
	}
	if book.FilePath == "" {
		return nil
	}
	if info, err := os.Stat(book.FilePath); err == nil && info.IsDir() {
		return nil
	}
	return []string{book.FilePath}
}

// checkFileOwnership decides whether saveBookToDatabase may treat book as a
// book at all, by asking who already owns its files.
//
// Until 2026-09-28 the only "already known?" question the scan asked was
// GetBookByFilePath, which reads the book-path index and nothing else. A
// chapter file of a multi-file book is NOT at any book's path -- it is one of
// that book's book_file rows -- so a scan that emitted the chapter on its own
// (a flat directory over the size cap, a leading-number-only consolidation
// miss) imported it as a brand-new book. Auto-organize then moved the file to
// the new book's target path, and the owning book's row was left pointing at
// a file that no longer existed. Prod held ~26,600 such fragment books.
//
// The decision, in order:
//
//  1. A book already at book.FilePath is an UPDATE of that book: proceed, the
//     upsert below handles it exactly as before.
//  2. Otherwise collect the live owners of every scanned file. Proceed only if
//     no file is owned (a genuinely new book), or the scanned files are
//     exactly one owner's present (non-Missing) rows -- the rescan of a
//     multi-file book whose row was normalized to its directory, which the
//     hash lookup below re-finds.
//  3. Everything else is a fragment and is skipped: files owned by a
//     different book, a mix of owned and unowned files, files owned by
//     several books. Creating a book there is exactly the bleed.
//
// A row whose book no longer exists is dangling, not an owner. A store error
// is returned, never read as "unowned": the caller fails closed, the same way
// an undeterminable hash lookup does (H5).
func checkFileOwnership(book *Book) (fileOwnershipVerdict, error) {
	store := getStore()
	if store == nil || book == nil {
		return fileOwnershipVerdict{}, nil
	}
	if atPath, err := store.GetBookByFilePath(book.FilePath); err != nil {
		return fileOwnershipVerdict{}, fmt.Errorf("book lookup for %s: %w", book.FilePath, err)
	} else if atPath != nil {
		return fileOwnershipVerdict{}, nil
	}

	files := scannedFilesOf(book)
	if len(files) == 0 {
		return fileOwnershipVerdict{}, nil
	}

	scanned := make(map[string]struct{}, len(files))
	for _, f := range files {
		scanned[f] = struct{}{}
	}

	live := make(map[string]bool) // bookID -> the book exists
	owned := make(map[string]int) // bookID -> scanned files it owns
	unowned := 0
	for f := range scanned {
		rows, err := database.BookFileRowsAtPath(store, f)
		if err != nil {
			return fileOwnershipVerdict{}, fmt.Errorf("book_file lookup for %s: %w", f, err)
		}
		fileOwners := 0
		seen := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			if _, dup := seen[r.BookID]; dup {
				continue
			}
			seen[r.BookID] = struct{}{}
			exists, known := live[r.BookID]
			if !known {
				b, err := store.GetBookByID(r.BookID)
				if err != nil {
					return fileOwnershipVerdict{}, fmt.Errorf("owner lookup %s for %s: %w", r.BookID, f, err)
				}
				exists = b != nil
				live[r.BookID] = exists
			}
			if !exists {
				continue // dangling row: its book is gone, so it owns nothing
			}
			owned[r.BookID]++
			fileOwners++
		}
		if fileOwners == 0 {
			unowned++
		}
	}

	if len(owned) == 0 {
		return fileOwnershipVerdict{}, nil
	}
	owners := make([]string, 0, len(owned))
	for id := range owned {
		owners = append(owners, id)
	}
	slices.Sort(owners)

	switch {
	case len(owners) > 1:
		return fileOwnershipVerdict{skip: true, owners: owners, reason: "files are owned by several books"}, nil
	case unowned > 0:
		return fileOwnershipVerdict{skip: true, owners: owners, reason: "some files are owned by another book and some are not"}, nil
	}

	// One owner holds every scanned file. It is this same book only when every
	// one of its PRESENT rows is among the scanned files: a row flagged Missing
	// may be a file that has just reappeared, and one the scan did not emit at
	// all is expected to be missing. Any present row outside the scanned set
	// means the scanned files are a piece of a larger book.
	ownerID := owners[0]
	rows, err := store.GetBookFiles(ownerID)
	if err != nil {
		return fileOwnershipVerdict{}, fmt.Errorf("book_files of owner %s: %w", ownerID, err)
	}
	for _, r := range rows {
		if r.Missing {
			continue
		}
		if _, ok := scanned[r.FilePath]; !ok {
			return fileOwnershipVerdict{skip: true, owners: owners,
				reason: "the files are part of a larger book that owns other files too"}, nil
		}
	}
	return fileOwnershipVerdict{}, nil
}
