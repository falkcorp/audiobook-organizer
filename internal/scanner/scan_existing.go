// file: internal/scanner/scan_existing.go
// version: 1.6.0
// guid: b05dd330-3c2a-4793-b0ef-88229676313b
// last-edited: 2026-10-26
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
	// list whose chapter order was re-sorted (or whose first file's row is
	// marked Missing). A renamed chapter has no row, so the ownership check
	// reads it as an arrival or a fragment, never as this.
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

	// retired is set when nothing above found a row -- or the row found by
	// path or organizer ID is a retired shell, which is not the book -- and
	// the scanned content is a retired version's content
	// (lookupRetiredContent): the library already holds these bytes as a
	// version another book keeps out of its live files, so the import is
	// skipped. The holder is never offered as byHash, bySegments or row():
	// that would read the retired version as the book's own file and
	// version-link a new row to it.
	retired *retiredContentHit
}

// retiredContentHit names the retired version a scanned file or segment list
// matched by content. matched of total hashed files matched it (a single file
// is 1 of 1).
type retiredContentHit struct {
	holder    string
	versionID string
	matched   int
	total     int
}

// asError is the skip the save returns: the same errFileOwnedByOtherBook the
// ownership check raises, so every caller already skips the book's rows,
// chapters, scan-cache stamp and AI nomination, and the run's ownership-skip
// log carries the holder and the reason.
func (h *retiredContentHit) asError(path string) error {
	return &ownershipSkipError{Path: path, Owners: []string{h.holder},
		Reason: fmt.Sprintf("content of a retired version of book %s (version %s): %d of %d file(s) match",
			h.holder, h.versionID, h.matched, h.total)}
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
		// The row named by path or organizer ID is normally the book and ends
		// the lookup. A retired SHELL named that way is not: its files live on
		// as retired rows under another book, so a copy of that content
		// (the shell's own file rescanned after a change of mtime or size, or a
		// file still tagged with the shell's organizer ID) is retired content,
		// and saving it onto the shell would mint a live row beside the
		// retired one. Judge the content before the shell is accepted.
		for _, named := range []*database.Book{e.byPath, e.byOrgID} {
			if named == nil {
				continue
			}
			shell, serr := isRetiredShell(store, named.ID)
			if serr != nil {
				return nil, fmt.Errorf("skipping import of %s: retired-version check undeterminable: %w", book.FilePath, serr)
			}
			if !shell {
				continue
			}
			// A segment list is judged by the hashes kept on
			// book.SegmentHashes, and hashSegments is their only writer
			// besides the vote this branch skips. Without it a tagged copy of
			// a retired multi-file version has no hashes, reads as no match,
			// and relinks the shell onto the copy with live rows of its own.
			hashSegments(book)
			if err := e.lookupRetiredContent(book, fileHash); err != nil {
				return nil, err
			}
			break
		}
		return e, nil
	}
	if err := e.lookupHashes(book, fileHash); err != nil {
		return nil, err
	}
	if e.byHash == nil {
		e.lookupSegments(book)
	}
	if e.row() == nil {
		// About to be a new book: last check that its content is not a
		// retired version's. A hit through a retired row counts only when no
		// non-retired row holds the content too (those are the lookups above).
		if err := e.lookupRetiredContent(book, fileHash); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// lookupRetiredContent sets e.retired when the scanned content matches a
// retired book_file row and no non-retired row. GetBookBySegmentFileHash and
// the book-level hash lookups never answer for a retired row, so without this
// a copy of a retired version's bytes (the same file dropped in again, or a
// re-download) was imported as a brand new book. A single file is judged by
// its hash; a segment list by the segments' hashes (kept on book.SegmentHashes
// by lookupSegments) at the same 80% bar the live segment vote uses.
//
// database.RetiredHoldersOfHash is the lookup: it needs no memdb while no
// retired version exists and reads Pebble while memdb is warming. It fails
// only for a store that cannot say either way, and that is returned: the book
// cannot be proven not to be a retired version, so the import is skipped (the
// file is untouched and imports on a later scan once the store recovers), the
// same rule as a failing duplicate-hash lookup.
func (e *scanExisting) lookupRetiredContent(book *Book, fileHash *string) error {
	store := getStore()
	if len(book.SegmentFiles) > 1 {
		threshold := int(math.Ceil(float64(len(book.SegmentFiles)) * 0.8))
		var first *retiredContentHit
		matched := 0
		for _, f := range book.SegmentFiles {
			h := book.SegmentHashes[f]
			if h == "" {
				continue
			}
			hit, err := retiredHolderOfHash(store, h)
			if err != nil {
				return fmt.Errorf("skipping import of %s: retired-version check undeterminable: %w", book.FilePath, err)
			}
			if hit == nil {
				continue
			}
			matched++
			if first == nil {
				first = hit
			}
		}
		if first != nil && matched >= threshold {
			first.matched, first.total = matched, len(book.SegmentFiles)
			e.retired = first
		}
		return nil
	}
	if fileHash == nil || *fileHash == "" {
		return nil
	}
	hit, err := retiredHolderOfHash(store, *fileHash)
	if err != nil {
		return fmt.Errorf("skipping import of %s: retired-version check undeterminable: %w", book.FilePath, err)
	}
	if hit != nil {
		hit.matched, hit.total = 1, 1
		e.retired = hit
	}
	return nil
}

// retiredHolderOfHash returns the retired version holding hash, or nil when no
// retired row has it or any non-retired row does (a live copy is the live
// lookups' business, not a retired-content skip).
//
// It asks database.RetiredHoldersOfHash, which answers "none" without memdb
// while no retired version exists and falls back to a Pebble-only read while
// memdb is warming, so a new import is never refused for warmup alone.
func retiredHolderOfHash(store any, hash string) (*retiredContentHit, error) {
	retired, liveHolds, err := database.RetiredHoldersOfHash(store, hash)
	if err != nil {
		return nil, err
	}
	if liveHolds || len(retired) == 0 {
		return nil, nil
	}
	return &retiredContentHit{holder: retired[0].BookID, versionID: retired[0].VersionID}, nil
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

// hashSegments hashes every segment file of a multi-file book into
// book.SegmentHashes, skipping files already hashed. A file that cannot be read
// gets no entry, so it casts no vote and counts toward no retired-content match.
// It is the only writer of SegmentHashes: the live segment vote
// (lookupSegments) and the retired-content check (lookupRetiredContent) both
// read what it leaves, and createBookFilesForBookWithAudio reuses it instead of
// hashing again.
func hashSegments(book *Book) {
	if len(book.SegmentFiles) <= 1 {
		return
	}
	for _, segFile := range book.SegmentFiles {
		if book.SegmentHashes[segFile] != "" {
			continue
		}
		h, herr := ComputeFileHash(segFile)
		if herr != nil || h == "" {
			continue
		}
		if book.SegmentHashes == nil {
			book.SegmentHashes = make(map[string]string)
		}
		book.SegmentHashes[segFile] = h
	}
}

// lookupSegments runs the multi-file vote once: every segment file is hashed
// (the hashes are kept on book.SegmentHashes so createBookFilesForBookWithAudio does
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
	hashSegments(book)
	bookVotes := make(map[string]int)
	bookCandidates := make(map[string]*database.Book)
	for _, segFile := range book.SegmentFiles {
		h := book.SegmentHashes[segFile]
		if h == "" {
			continue
		}
		candidate, lerr := store.GetBookBySegmentFileHash(h)
		if lerr != nil {
			// A failed lookup casts no vote, so it can only lower a book's count:
			// the vote carries on with what it has, but the failure is counted
			// (into the scan's duplicate-lookup error summary) and logged rather
			// than read as "no match".
			warnSampled(&dupLookupErrCount, defaultLog, "duplicate-detection segment hash lookup failed for %s: %v", segFile, lerr)
			continue
		}
		if candidate == nil {
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
