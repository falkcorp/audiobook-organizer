// file: internal/database/pebble_store_scancache.go
// version: 3.0.0
// guid: 5737e19f-0c4c-4762-a8ea-928619a02862
// last-edited: 2026-08-24

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"
)

// GetScanCacheMap returns a map of file_path -> ScanCacheEntry built from
// **book_file** rows.
//
// It used to be built from BOOK rows, and that was the bug. The scan cache is
// consulted per FILE, during the walk, before any book is known; keying it per
// BOOK only works while the two grains coincide, which they do for a single-file
// book and stop doing the moment createBookFilesForBook normalizes
// Book.FilePath to the containing directory. After that the walk looks up a
// segment path and the map holds a directory path, so every lookup misses.
//
// The consequence was arithmetic rather than mysterious: an 80-file audiobook
// has ONE book row, so at most one of its files could ever be represented, and
// the other 79 were re-read and re-hashed on every scan forever. Measured on
// prod 2026-08-24: "436 of 500 scan-cache write-backs skipped because no book
// row exists at the path".
//
// Reading book_file rows also fixes the VALUE grain in the same move: each entry
// now carries the mtime and size of that file, not of the directory inode the
// book row pointed at (128 bytes observed), so the size comparison in
// classifySkipFile can succeed.
//
// A nil LastScanMtime means never scanned and is skipped -- distinct from a row
// scanned and measured at zero, which is why these fields are pointers.
//
// IMPORTANT (see docs/plans/2026-08-24-per-file-scan-cache-design.md): this
// function must never read Book.FilePath. That is the property which keeps the
// eventual option B -- stop normalizing Book.FilePath at all -- cheap, by
// leaving the scan/skip layer untouched by it.
func (p *PebbleStore) GetScanCacheMap() (map[string]ScanCacheEntry, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	result := make(map[string]ScanCacheEntry)
	for iter.First(); iter.Valid(); iter.Next() {
		var bf BookFile
		if err := json.Unmarshal(iter.Value(), &bf); err != nil {
			continue
		}
		if bf.FilePath == "" || bf.LastScanMtime == nil {
			continue
		}
		result[bf.FilePath] = ScanCacheEntry{
			Mtime:       derefInt64(bf.LastScanMtime),
			Size:        derefInt64(bf.LastScanSize),
			NeedsRescan: derefBool(bf.NeedsRescan),
		}
	}
	return result, nil
}

// UpdateBookFileScanCache stamps the per-file scan cache for the row at
// filePath, so the next incremental scan can skip that file.
//
// Keyed by PATH because that is what the caller has: the scanner has just
// processed a file and holds its path and its os.Stat, and no book. It
// deliberately does NOT look a book up -- that lookup is the entire failure
// class this change removes.
//
// A path with no book_file row is not an error. It returns nil and reports
// false so the caller can count it without treating it as a store failure.
func (p *PebbleStore) UpdateBookFileScanCache(filePath string, mtime, size int64) (bool, error) {
	if filePath == "" {
		return false, nil
	}
	bf, err := p.GetBookFileByPath(filePath)
	if err != nil {
		return false, err
	}
	if bf == nil {
		return false, nil
	}
	f := false
	if err := p.stampFileScanCache(bf.BookID, bf.ID, func(row *BookFile) {
		row.LastScanMtime = &mtime
		row.LastScanSize = &size
		row.NeedsRescan = &f
	}); err != nil {
		return false, err
	}
	return true, nil
}

// BackfillBookFileScanCacheResult reports what a backfill pass did.
type BackfillBookFileScanCacheResult struct {
	BooksScanned   int `json:"books_scanned"`
	Seeded         int `json:"seeded"`
	SkippedMulti   int `json:"skipped_multi_file"`
	SkippedNoStamp int `json:"skipped_book_never_scanned"`
	SkippedPathMis int `json:"skipped_path_mismatch"`
	Errors         int `json:"errors"`

	// CreatedRows counts single-file books that had NO book_file row at all and
	// were given the one row they should always have had. The scan never creates
	// rows for a genuinely single-file book -- see the comment at
	// internal/server/server.go:1208 -- so until now those rows appeared only if
	// the book happened to pass through auto-organize. A file-keyed scan cache
	// cannot see a book with no file row, so without this the switch to per-file
	// keying would turn that population's cache hits into permanent misses.
	CreatedRows int `json:"created_rows"`
	// SkippedNotAFile counts books whose FilePath does not stat as a regular file
	// (missing, unreadable, or a directory). A directory book with no rows is a
	// DIFFERENT problem and must not be papered over with one row pointing at the
	// folder -- the same refusal ensureSingleFileBookFile makes.
	SkippedNotAFile int `json:"skipped_not_a_regular_file"`
	// CreateErrors counts rows that failed to write. Separate from Errors so a
	// creation problem is never read as a seeding problem.
	CreateErrors int `json:"create_errors"`

	DryRun bool `json:"dry_run"`
}

// BackfillBookFileScanCache seeds the new per-file scan cache from the existing
// book-level one, for SINGLE-FILE books only.
//
// Without this, every book_file row reads as "never scanned" the moment
// GetScanCacheMap starts reading them, and the first scan after deploy is a
// whole-library re-read -- on a library that already takes 4-6 hours, the exact
// opposite of the intent. This is option 1 of the design's two migration
// choices, and it is the recommended one.
//
// Only single-file books are seeded, and only when the file's path equals the
// book's path. That is precisely the population where the old key AND the old
// value were both already correct, so copying them forward is sound. A
// multi-file book's stamp describes a directory inode and must NOT be copied
// onto its members -- it would assert a size every member fails to match, or
// worse, one it accidentally does.
//
// The multi-file population cold-starts. That costs nothing new: it is exactly
// the population being re-read on every scan TODAY.
func (p *PebbleStore) BackfillBookFileScanCache(dryRun bool) (*BackfillBookFileScanCacheResult, error) {
	res := &BackfillBookFileScanCacheResult{DryRun: dryRun}

	// One pass over book_file rows, grouped by book, so this is O(files) rather
	// than a GetBookFiles call per book.
	byBook := make(map[string][]BookFile)
	fiter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return nil, err
	}
	for fiter.First(); fiter.Valid(); fiter.Next() {
		var bf BookFile
		if err := json.Unmarshal(fiter.Value(), &bf); err != nil {
			continue
		}
		if bf.BookID == "" {
			continue
		}
		byBook[bf.BookID] = append(byBook[bf.BookID], bf)
	}
	fiter.Close()

	biter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer biter.Close()

	// Books that own no book_file row, collected for the concurrent creation pass.
	var missingRow []Book

	for biter.First(); biter.Valid(); biter.Next() {
		key := string(biter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(biter.Value(), &book); err != nil {
			continue
		}
		if book.ID == "" {
			continue
		}
		res.BooksScanned++

		if book.FilePath == "" {
			res.SkippedNoStamp++
			continue
		}

		files := byBook[book.ID]
		if len(files) == 0 {
			// No file row at all. Defer to the creation pass below rather than
			// deciding here: creating one requires an os.Stat, and doing 61k of
			// those inline would make this migration serial on NAS latency.
			// Note this is deliberately NOT gated on the book having a stamp --
			// the row should exist either way; only SEEDING it needs a stamp.
			missingRow = append(missingRow, book)
			continue
		}

		if book.LastScanMtime == nil {
			res.SkippedNoStamp++
			continue
		}
		if len(files) != 1 {
			res.SkippedMulti++
			continue
		}
		if files[0].FilePath != book.FilePath {
			// The book's stamp does not describe this file. Cold-start it
			// rather than assert a measurement that was never taken here.
			res.SkippedPathMis++
			continue
		}
		if files[0].LastScanMtime != nil {
			continue // already seeded; idempotent
		}
		if dryRun {
			res.Seeded++
			continue
		}
		bf := files[0]
		if err := p.stampFileScanCache(bf.BookID, bf.ID, func(row *BookFile) {
			row.LastScanMtime = book.LastScanMtime
			row.LastScanSize = book.LastScanSize
			row.NeedsRescan = book.NeedsRescan
		}); err != nil {
			res.Errors++
			continue
		}
		res.Seeded++
	}

	if err := p.createMissingSingleFileRows(missingRow, dryRun, res); err != nil {
		return nil, err
	}

	slog.Info("book_file scan-cache backfill complete",
		"dry_run", dryRun, "books_scanned", res.BooksScanned, "seeded", res.Seeded,
		"skipped_multi_file", res.SkippedMulti, "skipped_never_scanned", res.SkippedNoStamp,
		"skipped_path_mismatch", res.SkippedPathMis, "errors", res.Errors,
		"created_rows", res.CreatedRows, "skipped_not_a_regular_file", res.SkippedNotAFile,
		"create_errors", res.CreateErrors)
	return res, nil
}

// stampFileScanCache applies a scan-cache-only mutation to one book_file row.
//
// It exists because UpdateBookFile is far too heavy for this write. Per call that
// method deletes and rebuilds every secondary index, invalidates library stats,
// marks quick-queries dirty, and calls notifyBookFileChange, which RECOMPUTES the
// book's Duration/FileSize aggregates. A scan stamp changes none of the inputs to
// any of that: the secondary indexes cover ID, ITunesPersistentID, FilePath,
// FileHash, OriginalFileHash and the fingerprint segments, and not one of
// LastScanMtime/LastScanSize/NeedsRescan appears among them. Paying an aggregate
// recompute to record an mtime would put avoidable work on the scan's hottest
// path, on a library where a full scan already runs 4-6 hours.
//
// It reads the AUTHORITATIVE stored row rather than accepting a caller's struct,
// which is the reason this does not simply take a *BookFile. The memdb projection
// nils AcoustIDFingerprint (~230 KB/file) and IntroTranscription, so writing back
// a slim struct would silently erase them. UpdateBookFile carries preserve-on-nil
// guards for exactly that hazard; by re-reading, this path cannot create it.
//
// The mutation is confined to a callback so that no caller can quietly widen it
// into a general row write -- doing so would reintroduce the index-staleness this
// deliberately skips.
func (p *PebbleStore) stampFileScanCache(bookID, fileID string, apply func(*BookFile)) error {
	old, err := p.getBookFileByID(bookID, fileID)
	if err != nil {
		return err
	}
	if old == nil {
		return nil // non-fatal: row went away under us; the next scan re-reads it
	}

	apply(old)
	old.UpdatedAt = time.Now()

	data, err := marshalBookFileDropSegs(old)
	if err != nil {
		return err
	}

	// Primary row only. No index churn: every indexed field is untouched above, so
	// the existing index entries still point at this row and still hold true.
	key := []byte(fmt.Sprintf("book_file:%s:%s", old.BookID, old.ID))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}
	p.UpsertBookFileToMemDB(old)
	return nil
}

// bookStampDescribesExactlyOneFile returns the single book_file row that a
// BOOK-level scan stamp legitimately describes, or nil when there is none.
//
// A book-level stamp measures whatever Book.FilePath points at. That is a real
// file only when the book owns exactly one file AND that file's path IS the
// book's path. Once createBookFilesForBook normalizes Book.FilePath to the
// containing directory the stamp describes a directory inode (128 bytes observed
// on prod) and therefore describes no file at all; copying it onto a member would
// mark that file scanned at a measurement nobody took, and every later scan would
// skip it -- including the scan that should have noticed it changed.
//
// The backfill applies the same rule. They must stay identical: the backfill is
// just this rule replayed over history, so a divergence would mean a row could be
// seeded by one path and refused by the other.
func (p *PebbleStore) bookStampDescribesExactlyOneFile(book *Book) (*BookFile, error) {
	if book == nil || book.FilePath == "" {
		return nil, nil
	}
	files, err := p.GetBookFiles(book.ID)
	if err != nil {
		return nil, err
	}
	if len(files) != 1 || files[0].FilePath != book.FilePath {
		return nil, nil
	}
	return &files[0], nil
}

// UpdateScanCache sets LastScanMtime, LastScanSize, and clears NeedsRescan for a
// book, and MIRRORS the stamp onto the book's file row when the book owns exactly
// one file at the book's own path.
//
// The mirror is what keeps this change self-consistent. GetScanCacheMap now reads
// book_file rows, but the only production writer is the scanner's
// writeBackScanCache, which is still book-keyed (the per-file writer is a separate
// change). Without the mirror the reader and the writer would sit at different
// grains: the backfill would seed a row once and nothing would ever refresh it, so
// a single-file book that changed even once would miss the cache forever after --
// a regression against today's behaviour, where those books do get hits.
//
// The failure direction is safe either way (a stale stamp never MATCHES a changed
// file, so the scan re-reads rather than wrongly skipping), but "safe" is not the
// bar: this population gets cache hits today and must keep getting them.
//
// Multi-file books are deliberately not mirrored here. Their stamp describes a
// directory, so there is nothing correct to write; they cold-start until the
// per-file writer lands, which is exactly what they already do today.
func (p *PebbleStore) UpdateScanCache(bookID string, mtime int64, size int64) error {
	book, err := p.GetBookByID(bookID)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	book.LastScanMtime = &mtime
	book.LastScanSize = &size
	f := false
	book.NeedsRescan = &f
	if _, err = p.UpdateBook(bookID, book); err != nil {
		return err
	}

	bf, err := p.bookStampDescribesExactlyOneFile(book)
	if err != nil {
		// Non-fatal: the book row is already stamped. Losing the mirror costs a
		// re-read next scan, never a wrong skip.
		slog.Warn("scan cache: could not mirror book stamp onto its file row",
			"book_id", bookID, "error", err)
		return nil
	}
	if bf == nil {
		return nil
	}
	return p.stampFileScanCache(bf.BookID, bf.ID, func(row *BookFile) {
		row.LastScanMtime = &mtime
		row.LastScanSize = &size
		row.NeedsRescan = &f
	})
}

// MarkNeedsRescan sets NeedsRescan = true for the given book, and mirrors it onto
// the book's file row under the same single-file rule as UpdateScanCache.
//
// The mirror is REQUIRED, not symmetry for its own sake. classifySkipFile reads
// NeedsRescan out of the scan-cache entry (scanner.go:549) and that entry is now
// built from the book_file row. The scanner's rescan-age re-arm calls this
// function right after UpdateScanCache has cleared the flag (scanner.go:726), so
// without the mirror the clear would land on the file row and the re-arm on the
// book row -- the re-arm would become inert, and a file still inside the
// rescan-age window would be treated as settled. Marking a book for rescan by
// hand would silently do nothing too.
//
// Multi-file books need no mirror: they have no scan-cache entry at all until the
// per-file writer lands, so classifySkipFile reports cacheMiss and re-reads them
// regardless, which is the same thing NeedsRescan would have forced.
func (p *PebbleStore) MarkNeedsRescan(bookID string) error {
	book, err := p.GetBookByID(bookID)
	if err != nil {
		return err
	}
	if book == nil {
		return nil // non-fatal: book not found
	}
	t := true
	book.NeedsRescan = &t
	if _, err = p.UpdateBook(bookID, book); err != nil {
		return err
	}

	bf, err := p.bookStampDescribesExactlyOneFile(book)
	if err != nil {
		// Non-fatal, but note the direction differs from UpdateScanCache's mirror:
		// losing THIS write means a file that should be re-read may be skipped, so
		// it is logged at Warn with the book id to make it findable.
		slog.Warn("scan cache: could not mirror NeedsRescan onto the file row; "+
			"a file inside the rescan-age window may be treated as settled",
			"book_id", bookID, "error", err)
		return nil
	}
	if bf == nil {
		return nil
	}
	return p.stampFileScanCache(bf.BookID, bf.ID, func(row *BookFile) {
		row.NeedsRescan = &t
	})
}

// GetDirtyBookFolders returns a deduplicated list of parent directories for all
// books that have NeedsRescan = true.
func (p *PebbleStore) GetDirtyBookFolders() ([]string, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	seen := make(map[string]struct{})
	var dirs []string
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(iter.Value(), &book); err != nil {
			continue
		}
		if book.FilePath == "" || !derefBool(book.NeedsRescan) {
			continue
		}
		dir := filepath.Dir(book.FilePath)
		if _, ok := seen[dir]; !ok {
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

// RecordPathChange stores a path change record in PebbleDB.
// Key format: path_history:<book_id>:<timestamp>
func (p *PebbleStore) RecordPathChange(change *BookPathChange) error {
	ts := time.Now().UnixNano()
	change.CreatedAt = time.Now()
	change.ID = int(ts)
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("path_history:%s:%019d", change.BookID, ts))
	return p.db.Set(key, data, pebble.Sync)
}

// GetBookPathHistory returns all path changes for a book, newest first.
func (p *PebbleStore) GetBookPathHistory(bookID string) ([]BookPathChange, error) {
	prefix := []byte(fmt.Sprintf("path_history:%s:", bookID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []BookPathChange
	for iter.First(); iter.Valid(); iter.Next() {
		var c BookPathChange
		if err := json.Unmarshal(iter.Value(), &c); err != nil {
			continue
		}
		results = append(results, c)
	}
	// Reverse for newest-first
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results, nil
}

// createMissingSingleFileRows gives each single-file book that owns NO book_file
// row the one row it should always have had, and seeds that row's scan stamp from
// the book in the same write.
//
// WHY THIS EXISTS. The scan never creates book_file rows for a genuinely
// single-file book -- internal/server/server.go:1208 says so outright -- so those
// rows appear only when a book happens to pass through auto-organize, which calls
// ensureSingleFileBookFile. A book-keyed scan cache did not care, because it read
// the book row. A FILE-keyed cache cannot see such a book at all, so switching the
// reader without this pass would take the population that caches correctly today
// and make it re-read on every scan forever -- the exact failure being fixed for
// multi-file books, reintroduced on the other side.
//
// The row it writes is the same honest row ensureSingleFileBookFile writes: this
// book has exactly this one file. It deliberately does NOT go through
// createBookFilesForBook, which would also normalize Book.FilePath to the
// containing directory -- right for a multi-file book, wrong here, and not a side
// effect a migration should have.
//
// It stats every candidate, so it is a whole-library loop doing real per-item I/O
// against a NAS. Per CLAUDE.md that is written concurrent from the start rather
// than bolted on: a bounded errgroup at NumCPU, never unbounded fan-out over an
// unbounded collection. The work partitions cleanly -- one book, one file path,
// one new row -- so no two workers can touch the same row and no lock is needed
// beyond the atomic counters.
//
// A stat failure is a SKIP, never a create. A missing or unreadable path means we
// do not know what the file is, and a directory means the book is a different
// problem entirely; inventing a row pointing at a folder would hand the scan
// cache a directory inode's size, which is the value-grain bug this whole change
// exists to remove.
func (p *PebbleStore) createMissingSingleFileRows(books []Book, dryRun bool, res *BackfillBookFileScanCacheResult) error {
	if len(books) == 0 {
		return nil
	}

	var created, skippedNotAFile, createErrs int64

	g := new(errgroup.Group)
	g.SetLimit(runtime.NumCPU())

	for i := range books {
		book := books[i] // copy: the loop variable must not be shared across workers
		g.Go(func() error {
			info, statErr := os.Stat(book.FilePath)
			if statErr != nil || info.IsDir() {
				atomic.AddInt64(&skippedNotAFile, 1)
				return nil
			}

			if dryRun {
				atomic.AddInt64(&created, 1)
				return nil
			}

			ext := strings.ToLower(filepath.Ext(book.FilePath))
			bf := &BookFile{
				BookID:           book.ID,
				FilePath:         book.FilePath,
				OriginalFilename: filepath.Base(book.FilePath),
				Format:           strings.TrimPrefix(ext, "."),
				FileSize:         info.Size(),
				TrackNumber:      1,
			}

			// Seed the stamp in the SAME write. This is the
			// single-file-at-the-book's-own-path case, which is exactly the
			// population where the old book-level key and value were both already
			// correct, so copying the stamp asserts nothing that was not already
			// true.
			//
			// Unconditional on purpose: a book with no stamp has nil here, and
			// copying nil yields nil, so it gets a stampless row and cold-starts.
			// An `if book.LastScanMtime != nil` guard around this was an
			// equivalent mutation -- it read as though it were protecting
			// something while changing no outcome, which is worse than no guard.
			bf.LastScanMtime = book.LastScanMtime
			bf.LastScanSize = book.LastScanSize
			bf.NeedsRescan = book.NeedsRescan

			if err := p.CreateBookFile(bf); err != nil {
				// Best-effort per row: one book that cannot be written must not
				// abort a 61k-row migration and leave it half-applied. It is
				// counted separately from seeding errors so the two cannot be
				// confused in the summary.
				slog.Warn("scan-cache backfill: could not create book_file row",
					"book_id", book.ID, "path", book.FilePath, "error", err)
				atomic.AddInt64(&createErrs, 1)
				return nil
			}
			atomic.AddInt64(&created, 1)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}

	res.CreatedRows = int(created)
	res.SkippedNotAFile = int(skippedNotAFile)
	res.CreateErrors = int(createErrs)
	return nil
}
