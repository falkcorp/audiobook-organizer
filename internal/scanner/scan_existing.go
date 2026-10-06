// file: internal/scanner/scan_existing.go
// version: 1.0.0
// guid: b05dd330-3c2a-4793-b0ef-88229676313b
// last-edited: 2026-10-06
//
// Resolves, once, the stored row a scanned book already is.

package scanner

import (
	"fmt"
	"math"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// scanExisting is every way saveBookToDatabase finds the stored row a scanned
// book already is, looked up ONCE before any author, series or work row is
// resolved or created. Until 2026-10-06 the folder-parse hold
// (holdFolderFieldsForExisting) looked at the path alone and ran before the
// organizer-ID relink and the hash lookups, so a moved or renamed book read
// as new: the new parse minted author, series and work rows for it, and the
// relink then returned without using them.
//
// The fields keep the lookups' own order and conditions -- the save flow
// reads them where it used to query -- and row() is the one the hold uses.
type scanExisting struct {
	// byPath is the row at book.FilePath.
	byPath *database.Book
	// byOrgID is the row the file's AUDIOBOOK_ORGANIZER_ID tag names, when
	// it sits at another path (a tagged move: the relink repoints it).
	// orgErr is that lookup's failure, logged by the relink.
	byOrgID *database.Book
	orgErr  error
	// byOwner is the live book that owns every scanned file
	// (fileOwnershipVerdict.sameBook) when no row is at the path: a segment
	// list whose first file was renamed or re-sorted.
	byOwner *database.Book

	// byHash is the first row a file/original/organized hash lookup found
	// (lookupHashes; hashDone once run).
	hashDone bool
	byHash   *database.Book
	// bySegments is the book the per-segment hash vote picked
	// (lookupSegments; segDone once run), with its vote and the 80% bar.
	segDone      bool
	bySegments   *database.Book
	segCount     int
	segThreshold int
}

// lookupScanExisting resolves book's existing row: path, organizer ID, the
// owner of its files, then (only when none of those found one) the content
// hashes and the per-segment vote. A store error on the path or owner read,
// or a hash lookup that failed with nothing found, is returned: the book
// cannot be proven new, and the caller fails closed before creating rows.
func lookupScanExisting(book *Book, verdict fileOwnershipVerdict, fileHash *string) (*scanExisting, error) {
	store := getStore()
	e := &scanExisting{}
	var err error
	if e.byPath, err = store.GetBookByFilePath(book.FilePath); err != nil {
		return nil, fmt.Errorf("book lookup failed: %w", err)
	}
	if book.BookOrganizerID != "" {
		b, oerr := store.GetBookByID(book.BookOrganizerID)
		switch {
		case oerr != nil:
			e.orgErr = oerr
		case b != nil && b.FilePath != book.FilePath:
			e.byOrgID = b
		}
	}
	if e.byPath == nil && verdict.sameBook != "" {
		b, oerr := store.GetBookByID(verdict.sameBook)
		if oerr != nil {
			return nil, fmt.Errorf("owner lookup %s failed: %w", verdict.sameBook, oerr)
		}
		if b != nil && !b.IsSoftDeleted() {
			e.byOwner = b
		}
	}
	if e.byPath != nil || e.byOrgID != nil {
		return e, nil
	}
	if err := e.lookupHashes(book, fileHash); err != nil {
		return nil, err
	}
	if e.byHash == nil {
		e.lookupSegments(book)
	}
	return e, nil
}

// row is the existing row the folder-parse hold keeps the stored identity
// of: the organizer-ID row (the relink makes it this path's row), the path's
// row, the files' owner, a content-hash match, or a segment vote that
// cleared its bar; nil for a new book.
func (e *scanExisting) row() *database.Book {
	switch {
	case e == nil:
		return nil
	case e.byOrgID != nil:
		return e.byOrgID
	case e.byPath != nil:
		return e.byPath
	case e.byOwner != nil:
		return e.byOwner
	case e.byHash != nil:
		return e.byHash
	case e.bySegments != nil && e.segCount >= e.segThreshold:
		return e.bySegments
	}
	return nil
}

// lookupHashes runs the file/original/organized hash lookups once. Not-found
// is (nil, nil) on every store; a failing lookup with no match is returned,
// because the file cannot be proven NOT to be a duplicate (audit 2026-07-17
// H5): the file is untouched on disk and imports on the next scan once the
// store recovers.
func (e *scanExisting) lookupHashes(book *Book, fileHash *string) error {
	if e.hashDone {
		return nil
	}
	e.hashDone = true
	if fileHash == nil || *fileHash == "" {
		return nil
	}
	store := getStore()
	hashLookups := []func(string) (*database.Book, error){
		store.GetBookByFileHash,
		store.GetBookByOriginalHash,
		store.GetBookByOrganizedHash,
	}
	lookupErrs := 0
	var firstLookupErr error
	for _, lookup := range hashLookups {
		candidate, lerr := lookup(*fileHash)
		if lerr != nil {
			lookupErrs++
			if firstLookupErr == nil {
				firstLookupErr = lerr
			}
			warnSampled(&dupLookupErrCount, defaultLog, "duplicate-detection hash lookup failed for %s: %v", book.FilePath, lerr)
			continue
		}
		if candidate != nil {
			e.byHash = candidate
			return nil
		}
	}
	if lookupErrs > 0 {
		dupLookupSkipCount.Add(1)
		return fmt.Errorf("skipping import of %s: duplicate status undeterminable (%d/%d hash lookups failed, first error: %w)",
			book.FilePath, lookupErrs, len(hashLookups), firstLookupErr)
	}
	return nil
}

// lookupSegments runs the multi-file vote once: every segment file is hashed
// (the hashes are kept on book.SegmentHashes so createBookFilesForBook does
// not re-hash) and each match votes for its parent book. The book with the
// most votes is kept with its count and the 80% bar it must clear. This
// handles a book whose first file was damaged or replaced, which the
// single-hash lookup misses, and partial damage (bit rot).
func (e *scanExisting) lookupSegments(book *Book) {
	if e.segDone {
		return
	}
	e.segDone = true
	if len(book.SegmentFiles) <= 1 {
		return
	}
	store := getStore()
	bookVotes := make(map[string]int)
	bookCandidates := make(map[string]*database.Book)
	for _, segFile := range book.SegmentFiles {
		h, herr := ComputeFileHash(segFile)
		if herr != nil || h == "" {
			continue
		}
		if book.SegmentHashes == nil {
			book.SegmentHashes = make(map[string]string)
		}
		book.SegmentHashes[segFile] = h
		candidate, lerr := store.GetBookBySegmentFileHash(h)
		if lerr != nil || candidate == nil {
			continue
		}
		bookVotes[candidate.ID]++
		bookCandidates[candidate.ID] = candidate
	}
	bestID, bestCount := "", 0
	for id, count := range bookVotes {
		if count > bestCount {
			bestCount, bestID = count, id
		}
	}
	if bestID == "" {
		return
	}
	e.bySegments = bookCandidates[bestID]
	e.segCount = bestCount
	e.segThreshold = int(math.Ceil(float64(len(book.SegmentFiles)) * 0.8))
}
