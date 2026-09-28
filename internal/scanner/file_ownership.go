// file: internal/scanner/file_ownership.go
// version: 1.0.0
// guid: f938af2f-e090-48ab-b6b0-c89267a7adbf
// last-edited: 2026-09-28

package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
// list, its own path for a single-file book, or -- for a directory-shaped book
// (no segment list, FilePath is a folder) -- the audio files directly in that
// folder, enumerated the way createBookFilesForBook will enumerate them when it
// mints the book's rows.
func scannedFilesOf(book *Book) ([]string, error) {
	if len(book.SegmentFiles) > 0 {
		return book.SegmentFiles, nil
	}
	if book.FilePath == "" {
		return nil, nil
	}
	info, err := os.Stat(book.FilePath)
	if err != nil || !info.IsDir() {
		return []string{book.FilePath}, nil
	}
	entries, err := os.ReadDir(book.FilePath)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", book.FilePath, err)
	}
	audioExts := make(map[string]bool, len(config.AppConfig.SupportedExtensions))
	for _, ext := range config.AppConfig.SupportedExtensions {
		audioExts[ext] = true
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, filepath.Join(book.FilePath, e.Name()))
		}
	}
	return files, nil
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
//  1. A single-file or directory-shaped book already at book.FilePath is an
//     update of that book: proceed, the upsert below handles it exactly as
//     before. A segment-list book gets no such shortcut, because its FilePath
//     is only its first file: the hit can be fragment #1 of a folder whose
//     chapters were imported as separate books, and the new grouping must not
//     be overlaid onto it.
//  2. Collect the live owners of every scanned file. Proceed only if no file is
//     owned (a genuinely new book), or exactly one book owns every scanned file
//     and all of that book's present (non-Missing) rows are among them -- the
//     rescan of the same book, whether or not its row was normalized to its
//     directory.
//  3. Everything else is a fragment and is skipped: files owned by a
//     different book, a mix of owned and unowned files, files owned by
//     several books. Creating (or overlaying) a book there is exactly the
//     bleed.
//
// A row whose book no longer exists is dangling, not an owner. A store error
// is returned, never read as "unowned": the caller fails closed, the same way
// an undeterminable hash lookup does (H5).
func checkFileOwnership(book *Book) (fileOwnershipVerdict, error) {
	store := getStore()
	if store == nil || book == nil {
		return fileOwnershipVerdict{}, nil
	}
	// A segment list is the one shape whose FilePath is merely its first file;
	// every other shape's FilePath names the whole book.
	if len(book.SegmentFiles) <= 1 {
		if atPath, err := store.GetBookByFilePath(book.FilePath); err != nil {
			return fileOwnershipVerdict{}, fmt.Errorf("book lookup for %s: %w", book.FilePath, err)
		} else if atPath != nil {
			return fileOwnershipVerdict{}, nil
		}
	}
	files, err := scannedFilesOf(book)
	if err != nil {
		return fileOwnershipVerdict{}, err
	}
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
