// file: internal/database/pebble_store_stats.go
// version: 1.1.0
// guid: 8643a893-1898-4098-8e69-c312531d962c
// last-edited: 2026-07-06

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// CountFiles returns the total number of audio files across all books.
// Books with active segments count their segments; books without segments count as 1 file each.
// Uses two range scans instead of per-book GetBookFiles calls to avoid N+1 queries.
func (p *PebbleStore) CountFiles() (int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().CountFiles()
	}
	// Pass 1: collect IDs of all primary, non-deleted books (key scan + JSON decode)
	primaryBookIDs := make(map[string]struct{})
	bookIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return 0, err
	}
	for bookIter.First(); bookIter.Valid(); bookIter.Next() {
		key := string(bookIter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var book Book
		if err := json.Unmarshal(bookIter.Value(), &book); err != nil {
			return 0, err
		}
		if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
			continue
		}
		if book.IsPrimaryVersion != nil && !*book.IsPrimaryVersion {
			continue
		}
		primaryBookIDs[book.ID] = struct{}{}
	}
	bookIter.Close()

	// Pass 2: single range scan over book_file: space — count active files per book
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err != nil {
		return 0, err
	}
	defer fileIter.Close()

	bookActiveFiles := make(map[string]int, len(primaryBookIDs))
	for fileIter.First(); fileIter.Valid(); fileIter.Next() {
		// Primary keys are book_file:<bookID>:<fileID> (3 colon-delimited segments).
		// The upper bound book_file; already excludes secondary indexes (book_file_pid:, etc.)
		// but SplitN guards against any edge cases.
		parts := strings.SplitN(string(fileIter.Key()), ":", 4)
		if len(parts) != 3 {
			continue
		}
		bookID := parts[1]
		if _, ok := primaryBookIDs[bookID]; !ok {
			continue
		}
		var f BookFile
		if err := json.Unmarshal(fileIter.Value(), &f); err != nil {
			continue
		}
		if !f.Missing {
			bookActiveFiles[bookID]++
		}
	}

	total := 0
	for id := range primaryBookIDs {
		if n := bookActiveFiles[id]; n > 0 {
			total += n
		} else {
			total++ // no file records or all missing → count as 1
		}
	}
	return total, nil
}

func (p *PebbleStore) CountAuthors() (int, error) {
	count := 0
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("author:0"),
		UpperBound: []byte("author:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}
		count++
	}
	return count, nil
}

func (p *PebbleStore) CountSeries() (int, error) {
	count := 0
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("series:0"),
		UpperBound: []byte("series:;"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}
		count++
	}
	return count, nil
}

// GetBookCountsByLocation counts primary, non-deleted books whose file path
// starts with rootDir (organized) vs. those that don't (import/unorganized).
// When rootDir is empty, all books are counted as unorganized.
func (p *PebbleStore) GetBookCountsByLocation(rootDir string) (library, import_ int, err error) {
	books, err := p.GetAllBooks(0, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range books {
		if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
			library++
		} else {
			import_++
		}
	}
	return library, import_, nil
}

// GetBookSizesByLocation sums file sizes for books in the library root vs. outside.
// When rootDir is empty, all sizes go to the import (unorganized) bucket.
func (p *PebbleStore) GetBookSizesByLocation(rootDir string) (librarySize, importSize int64, err error) {
	books, err := p.GetAllBooks(0, 0)
	if err != nil {
		return 0, 0, err
	}
	for _, b := range books {
		var sz int64
		if b.FileSize != nil {
			sz = *b.FileSize
		}
		if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
			librarySize += sz
		} else {
			importSize += sz
		}
	}
	return librarySize, importSize, nil
}

// GetDashboardStats returns LibraryStats, serving from the PebbleDB cache when fresh.
// Even if the cache is marked dirty via InvalidateLibraryStats, returns the cached value
// if it was recomputed within the min-interval (default 10 minutes) to prevent thrashing
// during background operations like fingerprinting.
//
// Only when a cache miss (TTL expiry, or recent compute + dirty but outside min-interval)
// requires recompute, a per-process mutex gates the work to prevent concurrent stampedes.
func (p *PebbleStore) GetDashboardStats() (*DashboardStats, error) {
	// Stale-while-revalidate. ANY cached value — even a week-old one —
	// is returned immediately; if stale beyond the min-interval, kick a
	// background recompute for next time. This eliminates the cold-start
	// 87s spike where memdb wasn't warm yet and computeLibraryStats fell
	// through to a slow Pebble scan blocking the dashboard.
	cached := p.readCachedLibraryStats()

	if cached != nil {
		ageSec := time.Since(cached.ComputedAt).Seconds()
		minIntervalSec := float64(getLibraryCountsMinIntervalSeconds())
		if ageSec >= minIntervalSec {
			// Stale: kick off background recompute. TryLock so we don't
			// queue duplicate recomputes — one in flight is enough.
			if p.libraryCountsRecomputeMu.TryLock() {
				go func() {
					defer p.libraryCountsRecomputeMu.Unlock()
					start := time.Now()
					stats, err := p.computeLibraryStats()
					if err != nil {
						slog.Warn("library_counts background recompute failed",
							"component", "library_counts_cache", "error", err)
						return
					}
					p.writeCachedLibraryStats(stats)
					slog.Info("library_counts cache recomputed (background)",
						"component", "library_counts_cache",
						"total_books", stats.TotalBooks,
						"duration_ms", time.Since(start).Milliseconds(),
						"reason", "stale-while-revalidate",
					)
				}()
			}
		} else {
			slog.Debug("library_counts cache hit (fresh)",
				"component", "library_counts_cache",
				"age_seconds", ageSec,
				"min_interval_seconds", minIntervalSec,
			)
		}
		return cached, nil
	}

	// No cache at all (first boot or post-Invalidate restart). Block on
	// recompute — nothing to serve in the meantime.
	p.libraryCountsRecomputeMu.Lock()
	defer p.libraryCountsRecomputeMu.Unlock()

	// Double-check: a peer goroutine may have populated the cache while
	// we waited for the lock.
	if cached := p.readCachedLibraryStats(); cached != nil {
		return cached, nil
	}

	start := time.Now()
	stats, err := p.computeLibraryStats()
	if err != nil {
		return nil, err
	}
	p.writeCachedLibraryStats(stats)
	slog.Info("library_counts cache recomputed (cold)",
		"component", "library_counts_cache",
		"total_books", stats.TotalBooks,
		"organized_books", stats.OrganizedBooks,
		"unorganized_books", stats.UnorganizedBooks,
		"broken_files", stats.BrokenFiles,
		"duration_ms", time.Since(start).Milliseconds(),
		"reason", "cold-cache",
	)
	return stats, nil
}

// computeLibraryStats builds a fresh LibraryStats in two sequential range scans.
// Pass 1 (book:): counts/sums all fields, splits organized vs unorganized, per-import-path counts.
// Pass 2 (book_file:): counts active files without any per-book point lookups.
func (p *PebbleStore) computeLibraryStats() (*LibraryStats, error) {
	// Fast path: when memdb is published, aggregate from RAM. ~150× faster
	// than the Pebble scan below (no JSON unmarshal, no disk I/O). Memdb
	// can't see the book_file_errors_by_book: index, so we still need a
	// short Pebble call for BrokenFiles.
	if mem := p.mem(); mem != nil {
		importPaths, _ := p.GetAllImportPaths()
		stats, err := mem.ComputeLibraryStats(p.rootDir, importPaths)
		if err == nil {
			if booksWithErrors, berr := p.ListBooksWithFileErrors(); berr == nil {
				stats.BrokenFiles = len(booksWithErrors)
			}
			return stats, nil
		}
		// Fall through to Pebble scan on memdb error (shouldn't happen).
		slog.Warn("memdb ComputeLibraryStats failed, falling back to Pebble scan",
			"error", err)
	}

	stats := &LibraryStats{
		StateDistribution:  make(map[string]int),
		FormatDistribution: make(map[string]int),
		BooksByImportPath:  make(map[int]int),
		SizeByImportPath:   make(map[int]int64),
		ComputedAt:         time.Now(),
	}

	// Load import paths upfront (typically just a handful, not worth a separate scan).
	importPaths, _ := p.GetAllImportPaths()

	// Pass 1: book: range
	primaryBookIDs := make(map[string]struct{}, 12000)
	bookIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	for bookIter.First(); bookIter.Valid(); bookIter.Next() {
		key := string(bookIter.Key())
		if strings.Contains(key, ":path:") || strings.Contains(key, ":series:") ||
			strings.Contains(key, ":author:") {
			continue
		}
		var b Book
		if err := json.Unmarshal(bookIter.Value(), &b); err != nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		stats.TotalBooks++
		if b.Duration != nil {
			stats.TotalDuration += int64(*b.Duration)
		}
		size := int64(0)
		if b.FileSize != nil {
			size = *b.FileSize
			stats.TotalSize += size
		}
		state := "imported"
		if b.LibraryState != nil {
			state = *b.LibraryState
		}
		stats.StateDistribution[state]++
		codec := "unknown"
		if b.Codec != nil {
			codec = *b.Codec
		}
		stats.FormatDistribution[codec]++

		// Organized vs unorganized + per-import-path (primary versions only)
		if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
			primaryBookIDs[b.ID] = struct{}{}
			if p.rootDir != "" && strings.HasPrefix(b.FilePath, p.rootDir) {
				stats.OrganizedBooks++
				stats.OrganizedSize += size
			} else {
				stats.UnorganizedBooks++
				stats.UnorganizedSize += size
				for _, ip := range importPaths {
					if strings.HasPrefix(b.FilePath, ip.Path) {
						stats.BooksByImportPath[ip.ID]++
						stats.SizeByImportPath[ip.ID] += size
						break
					}
				}
			}
		}
	}
	bookIter.Close()

	// Pass 2: book_file: range — active file count + fingerprint coverage per primary book.
	// This path only runs as a rare fallback when memdb is unavailable (the fast-path
	// branch at the top of this function returns early otherwise), so the added
	// per-file JSON.Unmarshal cost (needed to call GetAcoustIDSeg0()) is acceptable —
	// unlike before, we can no longer do a pure key-only scan here.
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:"),
		UpperBound: []byte("book_file;"),
	})
	if err == nil {
		bookActiveFiles := make(map[string]int, len(primaryBookIDs))
		bookFingerprintedFiles := make(map[string]int, len(primaryBookIDs))
		for fileIter.First(); fileIter.Valid(); fileIter.Next() {
			parts := strings.SplitN(string(fileIter.Key()), ":", 4)
			if len(parts) != 3 {
				continue
			}
			bookID := parts[1]
			if _, ok := primaryBookIDs[bookID]; !ok {
				continue
			}
			bookActiveFiles[bookID]++
			var bf BookFile
			if err := json.Unmarshal(fileIter.Value(), &bf); err != nil {
				continue
			}
			if bf.GetAcoustIDSeg0() != "" {
				bookFingerprintedFiles[bookID]++
			}
		}
		fileIter.Close()
		for id := range primaryBookIDs {
			if n := bookActiveFiles[id]; n > 0 {
				stats.TotalFiles += n
			} else {
				stats.TotalFiles++ // no file records → count as 1
			}
			// Classify fingerprint coverage: none/partial/complete, mirroring the
			// semantics of fingerprint.ComputeFingerprintFields without importing
			// it (that function takes a []FileWithFingerprint slice, which would
			// mean building a throwaway slice per book for no benefit).
			switch fp := bookFingerprintedFiles[id]; {
			case fp == 0:
				stats.UnfingerprintedBooks++
			case fp == bookActiveFiles[id]:
				stats.FingerprintedBooks++
			default:
				stats.PartiallyFingerprintedBooks++
			}
		}
	}

	// Key-only scans — no JSON deserialization
	if ac, err := p.CountAuthors(); err == nil {
		stats.TotalAuthors = ac
	}
	if sc, err := p.CountSeries(); err == nil {
		stats.TotalSeries = sc
	}

	// Pass 3: book_file_errors_by_book: key-only scan — count distinct books with errors.
	// Reuses the secondary index written by RecordFileError so no JSON deserialization needed.
	if booksWithErrors, err := p.ListBooksWithFileErrors(); err == nil {
		stats.BrokenFiles = len(booksWithErrors)
	}

	if stats.TotalBooks > 0 {
		stats.FingerprintCoveragePercent = stats.FingerprintedBooks * 100 / stats.TotalBooks
	}

	return stats, nil
}

// SaveLibraryFingerprint stores or updates the fingerprint for an iTunes library file.
func (p *PebbleStore) SaveLibraryFingerprint(path string, size int64, modTime time.Time, crc32val uint32) error {
	rec := LibraryFingerprintRecord{
		Path:      path,
		Size:      size,
		ModTime:   modTime,
		CRC32:     crc32val,
		UpdatedAt: time.Now(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("itunes:fingerprint:%s", path))
	return p.db.Set(key, data, pebble.Sync)
}

// GetLibraryFingerprint retrieves the stored fingerprint for an iTunes library file.
func (p *PebbleStore) GetLibraryFingerprint(path string) (*LibraryFingerprintRecord, error) {
	key := []byte(fmt.Sprintf("itunes:fingerprint:%s", path))
	data, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var rec LibraryFingerprintRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// GetDuplicateFilesByHash returns groups of BookFiles sharing the same
// OriginalFileHash. Groups are ordered by file count descending. limit=0
// defaults to 50.
func (p *PebbleStore) GetDuplicateFilesByHash(limit int) ([]DuplicateFileGroup, error) {
	if limit <= 0 {
		limit = 50
	}
	files, err := p.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("GetDuplicateFilesByHash: %w", err)
	}

	// Build book title map for annotating results.
	books, _ := p.GetAllBooks(0, 0)
	bookTitles := make(map[string]string, len(books))
	bookPaths := make(map[string]string, len(books))
	for _, b := range books {
		bookTitles[b.ID] = b.Title
		bookPaths[b.ID] = b.FilePath
	}

	type group struct {
		files []BookFileCore
	}
	byHash := make(map[string]*group)
	for i := range files {
		h := files[i].OriginalFileHash
		if h == "" {
			continue
		}
		if byHash[h] == nil {
			byHash[h] = &group{}
		}
		byHash[h].files = append(byHash[h].files, files[i])
	}

	var result []DuplicateFileGroup
	for hash, g := range byHash {
		if len(g.files) < 2 {
			continue
		}
		bookIDs := make(map[string]struct{})
		var infos []DuplicateFileInfo
		var totalSize int64
		for _, f := range g.files {
			bookIDs[f.BookID] = struct{}{}
			totalSize += f.FileSize
			infos = append(infos, DuplicateFileInfo{
				BookFileID: f.ID,
				BookID:     f.BookID,
				BookTitle:  bookTitles[f.BookID],
				FilePath:   f.FilePath,
				BookPath:   bookPaths[f.BookID],
				FileSize:   f.FileSize,
			})
		}
		result = append(result, DuplicateFileGroup{
			Hash:      hash,
			FileCount: len(g.files),
			BookCount: len(bookIDs),
			TotalSize: totalSize,
			Files:     infos,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FileCount > result[j].FileCount
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// GetBookFileHashStats scans all book_file primary records in memory and
// returns aggregate hash-coverage statistics, including a per-library breakdown
// derived from each file's source_import_path on its parent book.
func (p *PebbleStore) GetBookFileHashStats() (*BookFileHashStats, error) {
	files, err := p.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("GetBookFileHashStats: %w", err)
	}

	stats := &BookFileHashStats{TotalBookFiles: len(files)}
	for _, f := range files {
		if f.FileHash != "" {
			stats.WithFileHash++
		}
		if f.OriginalFileHash != "" {
			stats.WithOriginalHash++
		}
	}
	stats.MissingFileHash = stats.TotalBookFiles - stats.WithFileHash

	// Gather per-library stats by grouping files under their parent book's source_import_path.
	// Build a bookID → source_import_path map first.
	allBooks, berr := p.GetAllBooks(0, 0)
	if berr == nil {
		bookPaths := make(map[string]string, len(allBooks))
		stats.TotalBooks = len(allBooks)
		for _, b := range allBooks {
			if b.SourceImportPath != nil && *b.SourceImportPath != "" {
				bookPaths[b.ID] = *b.SourceImportPath
			} else {
				bookPaths[b.ID] = ""
			}
		}
		// Check books without any files
		bookHasFile := make(map[string]bool, len(allBooks))
		for _, f := range files {
			bookHasFile[f.BookID] = true
		}
		for _, b := range allBooks {
			if !bookHasFile[b.ID] {
				stats.BooksWithNoFiles++
			}
		}

		libMap := make(map[string]*BookFileHashStatsByLib)
		for _, f := range files {
			lib := bookPaths[f.BookID]
			if lib == "" {
				continue
			}
			if _, ok := libMap[lib]; !ok {
				libMap[lib] = &BookFileHashStatsByLib{Path: lib}
			}
			libMap[lib].TotalFiles++
			if f.FileHash != "" {
				libMap[lib].WithHash++
			}
		}
		for _, row := range libMap {
			row.MissingHash = row.TotalFiles - row.WithHash
			stats.ByLibrary = append(stats.ByLibrary, *row)
		}
	}
	return stats, nil
}

// GetBookMetadataHashStats returns metadata_source_hash coverage across all books.
func (p *PebbleStore) GetBookMetadataHashStats() (*BookMetadataHashStats, error) {
	allBooks, err := p.GetAllBooks(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetBookMetadataHashStats: %w", err)
	}

	stats := &BookMetadataHashStats{TotalBooks: len(allBooks)}
	libMap := make(map[string]*BookMetadataHashStatsByLib)

	for _, b := range allBooks {
		hasHash := b.MetadataSourceHash != nil && *b.MetadataSourceHash != ""
		hasID := (b.ASIN != nil && *b.ASIN != "") ||
			(b.ISBN13 != nil && *b.ISBN13 != "") ||
			(b.ISBN10 != nil && *b.ISBN10 != "")

		if hasHash {
			stats.WithMetadataHash++
		} else {
			stats.MissingMetadataHash++
		}
		if hasID {
			stats.WithASINOrISBN++
			if !hasHash {
				stats.MissingHashHasID++
			}
		}

		lib := ""
		if b.SourceImportPath != nil {
			lib = *b.SourceImportPath
		}
		if lib == "" {
			continue
		}
		if _, ok := libMap[lib]; !ok {
			libMap[lib] = &BookMetadataHashStatsByLib{Path: lib}
		}
		libMap[lib].TotalBooks++
		if hasHash {
			libMap[lib].WithMetadataHash++
		} else {
			libMap[lib].MissingMetadataHash++
			if hasID {
				libMap[lib].MissingHashHasID++
			}
		}
	}

	for _, row := range libMap {
		stats.ByLibrary = append(stats.ByLibrary, *row)
	}
	return stats, nil
}

// GetAcoustIDStats returns fingerprint coverage across all book files, grouped by
// library root (the parent book's source_import_path).
//
// Uses getAllBookFilesPebbleScan (Pebble-direct) rather than
// GetAllBookFilesCore so that the hasFP check reads the full AcoustIDSeg0..6
// fields from storage.
// After fable5 T019 those fields are stripped from memdb projections; reading
// them from memdb would return all-zeros and undercount fingerprinted files.
func (p *PebbleStore) GetAcoustIDStats() (*AcoustIDStats, error) {
	files, err := p.getAllBookFilesPebbleScan()
	if err != nil {
		return nil, fmt.Errorf("GetAcoustIDStats: %w", err)
	}

	// Build bookID → source_import_path for library grouping.
	//
	// Read books pebble-direct (not via GetAllBooks) for the same reason the file
	// scan above is pebble-direct: this method must not depend on the async memdb
	// warmup. While memdb is unpublished (or transiently empty during the warmup
	// window), GetAllBooks would return no books and every file would collapse into
	// the "(unknown)" library bucket. Reading pebble directly keeps the grouping
	// consistent with the authoritative store regardless of memdb state.
	// (FLAKY-DB-TESTS-2026-06-17 root cause.)
	allBooks, _ := p.getAllBooksPebbleScan()
	bookLib := make(map[string]string, len(allBooks))
	for _, b := range allBooks {
		if b.SourceImportPath != nil && *b.SourceImportPath != "" {
			bookLib[b.ID] = *b.SourceImportPath
		}
	}

	byLib := make(map[string]*AcoustIDStatsByLibrary)
	stats := &AcoustIDStats{}

	for _, f := range files {
		stats.TotalFiles++
		// T020: segs are no longer stored; check whole-file fp first (preferred),
		// then fall back to legacy seg fields for rows not yet swept by T020.
		hasFP := len(f.AcoustIDFingerprint) > 0 ||
			f.AcoustIDSeg0 != "" || f.AcoustIDSeg1 != "" || f.AcoustIDSeg2 != "" ||
			f.AcoustIDSeg3 != "" || f.AcoustIDSeg4 != "" || f.AcoustIDSeg5 != "" || f.AcoustIDSeg6 != ""
		if hasFP {
			stats.WithFingerprint++
		}

		root := bookLib[f.BookID]
		if root == "" {
			root = "(unknown)"
		}
		lib := byLib[root]
		if lib == nil {
			lib = &AcoustIDStatsByLibrary{LibraryRoot: root}
			byLib[root] = lib
		}
		lib.TotalFiles++
		if hasFP {
			lib.WithFingerprint++
		}
	}

	for _, v := range byLib {
		stats.ByLibrary = append(stats.ByLibrary, *v)
	}
	sort.Slice(stats.ByLibrary, func(i, j int) bool {
		return stats.ByLibrary[i].LibraryRoot < stats.ByLibrary[j].LibraryRoot
	})
	return stats, nil
}

// GetFilesWithFingerprintFailures scans all book_files and returns those where
// FingerprintFailedAt is set, optionally filtered to a specific reason string.
func (p *PebbleStore) GetFilesWithFingerprintFailures(reason string, limit, offset int) ([]BookFile, int64, error) {
	// Bypass memdb deliberately: memdb-resident BookFiles have their
	// fingerprint-diagnostic fields stripped (see stripBookFileForMemdb),
	// so the Failed/Reason/Detail/Diagnostic columns this endpoint
	// surfaces are only available from Pebble.
	allFiles, err := p.getAllBookFilesPebbleScan()
	if err != nil {
		return nil, 0, fmt.Errorf("GetFilesWithFingerprintFailures: %w", err)
	}
	var matched []BookFile
	for _, f := range allFiles {
		if f.FingerprintFailedAt == nil {
			continue
		}
		if reason != "" && (f.FingerprintFailureReason == nil || *f.FingerprintFailureReason != reason) {
			continue
		}
		matched = append(matched, f)
	}
	total := int64(len(matched))
	if offset >= len(matched) {
		return nil, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total, nil
}
