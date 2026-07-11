// file: internal/database/memdb_reads.go
// version: 1.13.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000006
// last-edited: 2026-07-10

package database

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Read-side implementations for the queries previously handled by Chai SQL.
//
// All methods on MemStore here are read-only and use snapshot transactions,
// which means they never block writers and never return errors related to
// concurrency. The signatures intentionally mirror the existing
// `*_Pebble` counterparts on PebbleStore so swap-in is a one-line
// delegation change.

// GetAllSeries returns every Series sorted by Name (case-insensitive).
func (m *MemStore) GetAllSeries() ([]Series, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableSeries, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb series scan: %w", err)
	}
	var out []Series
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		out = append(out, *(obj.(*Series)))
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// GetAllAuthors returns every Author sorted by Name (case-insensitive).
func (m *MemStore) GetAllAuthors() ([]Author, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb authors scan: %w", err)
	}
	var out []Author
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		out = append(out, *(obj.(*Author)))
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// GetAllImportPaths returns every ImportPath sorted by Name.
func (m *MemStore) GetAllImportPaths() ([]ImportPath, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableImportPaths, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb import_paths scan: %w", err)
	}
	var out []ImportPath
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		out = append(out, *(obj.(*ImportPath)))
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// GetAllAuthorAliases returns every AuthorAlias sorted by AuthorID then AliasName.
func (m *MemStore) GetAllAuthorAliases() ([]AuthorAlias, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableAuthorAliases, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb author_aliases scan: %w", err)
	}
	var out []AuthorAlias
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		out = append(out, *(obj.(*AuthorAlias)))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthorID != out[j].AuthorID {
			return out[i].AuthorID < out[j].AuthorID
		}
		return strings.ToLower(out[i].AliasName) < strings.ToLower(out[j].AliasName)
	})
	return out, nil
}

// GetAllWorks is intentionally NOT implemented on MemStore — Works are not
// resident in memdb (dropped to save ~120MB heap across 211K rows). Callers
// hit PebbleStore.GetAllWorks_Pebble directly via the routing in
// PebbleStore.GetAllWorks.

// CountFiles returns the total number of audio files across all primary,
// non-deleted books. Books with no BookFile records count as 1 each —
// matching the Pebble scan behaviour in PebbleStore.CountFiles.
func (m *MemStore) CountFiles() (int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	// Count non-missing book files per book.
	fileIter, err := txn.Get(memTableBookFiles, memIdxMissing, false)
	if err != nil {
		return 0, fmt.Errorf("memdb book_files count: %w", err)
	}
	bookFileCounts := make(map[string]int)
	for obj := fileIter.Next(); obj != nil; obj = fileIter.Next() {
		f := obj.(*BookFile)
		bookFileCounts[f.BookID]++
	}

	// Iterate primary, non-deleted books. For each, use its file count or 1.
	bookIter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return 0, fmt.Errorf("memdb books scan: %w", err)
	}
	total := 0
	for obj := bookIter.Next(); obj != nil; obj = bookIter.Next() {
		b := obj.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		n := bookFileCounts[b.ID]
		if n == 0 {
			total++ // no file records → count the book as 1 file
		} else {
			total += n
		}
	}
	return total, nil
}

// GetAllSeriesBookCounts returns map[seriesID] → count of primary, not-deleted
// books that belong to that series.
func (m *MemStore) GetAllSeriesBookCounts() (map[int]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	out := make(map[int]int)
	// Scan only primary books to cut the candidate set in half.
	iter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.SeriesID == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		out[*b.SeriesID]++
	}
	return out, nil
}

// GetAllAuthorBookCounts returns map[authorID] → count of primary, not-deleted
// books for which the author appears (either via the book_authors junction table
// or the legacy AuthorID field on the Book row).
func (m *MemStore) GetAllAuthorBookCounts() (map[int]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	out := make(map[int]int)

	// Pass 1: scan book_authors junction table (multi-author support).
	// Track which bookIDs have junction entries to avoid double-counting legacy.
	bookHasJunction := make(map[string]bool)
	baIter, err := txn.Get(memTableBookAuthors, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_authors scan: %w", err)
	}
	for obj := baIter.Next(); obj != nil; obj = baIter.Next() {
		ba := obj.(*BookAuthor)
		// Check that the book is primary and not deleted.
		raw, bErr := txn.First(memTableBooks, memIdxID, ba.BookID)
		if bErr != nil || raw == nil {
			continue
		}
		b := raw.(*Book)
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		bookHasJunction[ba.BookID] = true
		out[ba.AuthorID]++
	}

	// Pass 2: scan books for the legacy AuthorID field (books without junction entries).
	bIter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		b := obj.(*Book)
		if bookHasJunction[b.ID] {
			continue // already counted via junction
		}
		if b.AuthorID == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		out[*b.AuthorID]++
	}
	return out, nil
}

// GetAllWorkBookCounts returns map[workID] → count of primary, not-deleted
// books for which the work is the WorkID on the Book row. Mirrors
// GetAllAuthorBookCounts; built with a single memdb iteration so callers
// avoid N+1 GetBooksByWorkID lookups on a 50K-work corpus.
func (m *MemStore) GetAllWorkBookCounts() (map[string]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	out := make(map[string]int)
	iter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.WorkID == nil || *b.WorkID == "" {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		out[*b.WorkID]++
	}
	return out, nil
}

// GetAllSeriesFileCounts returns map[seriesID] → count of non-missing
// book_files that belong to a primary book in that series. Books with no
// files count as 1 (to ensure series with only unscanned books have a
// non-zero file count).
func (m *MemStore) GetAllSeriesFileCounts() (map[int]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	// Pass 1: build bookID → seriesID for primary books only.
	// Also initialise every book's count to 0 so we can detect zero-file books.
	bookToSeries := make(map[string]int, 0)
	bookFileCounts := make(map[string]int, 0)
	bIter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		b := obj.(*Book)
		if b.SeriesID == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		bookToSeries[b.ID] = *b.SeriesID
		bookFileCounts[b.ID] = 0
	}

	// Pass 2: count non-missing files for those books.
	fIter, err := txn.Get(memTableBookFiles, memIdxMissing, false)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	for obj := fIter.Next(); obj != nil; obj = fIter.Next() {
		bf := obj.(*BookFile)
		if _, ok := bookToSeries[bf.BookID]; ok {
			bookFileCounts[bf.BookID]++
		}
	}

	// Pass 3: aggregate to series, using max(count, 1) per book.
	out := make(map[int]int)
	for bookID, seriesID := range bookToSeries {
		n := bookFileCounts[bookID]
		if n == 0 {
			n = 1
		}
		out[seriesID] += n
	}
	return out, nil
}

// GetAllAuthorFileCounts returns map[authorID] → count of non-missing
// book_files belonging to primary books for each author. Books with no
// files count as 1. Looks up both the legacy AuthorID field and the
// book_authors junction table.
func (m *MemStore) GetAllAuthorFileCounts() (map[int]int, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	// bookToAuthor: bookID → authorID (from legacy field, primary only).
	bookToAuthor := make(map[string]int, 0)
	bookFileCounts := make(map[string]int, 0)
	bIter, err := txn.Get(memTableBooks, memIdxIsPrimaryVersion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		b := obj.(*Book)
		if b.AuthorID == nil {
			continue
		}
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		bookToAuthor[b.ID] = *b.AuthorID
		bookFileCounts[b.ID] = 0
	}

	// Count actual non-missing files.
	fIter, err := txn.Get(memTableBookFiles, memIdxMissing, false)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	for obj := fIter.Next(); obj != nil; obj = fIter.Next() {
		bf := obj.(*BookFile)
		if _, ok := bookToAuthor[bf.BookID]; ok {
			bookFileCounts[bf.BookID]++
		}
	}

	// Aggregate with min-1 per book.
	out := make(map[int]int)
	for bookID, authorID := range bookToAuthor {
		n := bookFileCounts[bookID]
		if n == 0 {
			n = 1
		}
		out[authorID] += n
	}
	return out, nil
}

// GetFolderDuplicatesCore is the MemStore twin of
// PebbleStore.GetFolderDuplicatesCore's memdb-delegation branch
// (pebble_store.go) — see that method's doc comment for full bucketing
// semantics (normalizedTitle + single-parent-dir, >=2 members per group,
// deleted/non-primary/empty-title/multi-dir books silently skipped). Walks
// the memdb books table once via the ID index, resolving each qualifying
// book's parent dir from the memdb book_files table (memIdxBookID) — a
// pointer walk, no JSON unmarshal, no Pebble disk scan — then defers to the
// shared bucketFolderDuplicates helper so the two backends can never drift
// in bucketing behavior.
func (m *MemStore) GetFolderDuplicatesCore() ([][]BookCore, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb folder duplicates scan: %w", err)
	}

	var entries []folderDupEntry
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		if strings.TrimSpace(b.Title) == "" {
			continue
		}

		fIter, ferr := txn.Get(memTableBookFiles, memIdxBookID, b.ID)
		if ferr != nil {
			return nil, fmt.Errorf("memdb book_files for %s: %w", b.ID, ferr)
		}
		var paths []string
		for fo := fIter.Next(); fo != nil; fo = fIter.Next() {
			if bf, ok := fo.(*BookFile); ok {
				paths = append(paths, bf.FilePath)
			}
		}
		dir, ok := singleParentDir(paths)
		if !ok {
			continue
		}

		entries = append(entries, folderDupEntry{
			book:      b.Core(),
			normTitle: util.NormalizeTitle(b.Title),
			dir:       dir,
		})
	}

	return bucketFolderDuplicates(entries), nil
}

// GetBooksBySeriesIDCore returns primary, not-deleted books for a series
// with pagination. Sort order is series_sequence (nulls last) then title,
// but the comparator pre-lowercases titles ONCE per row instead of on every
// compare to avoid O(n log n) string allocations.
//
// Core-typed (STOREFID W4): the return type is BookCore, not Book — memdb
// rows never carry the nine heavy fields (Description, VersionNotes,
// BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
// BookSigCoveragePct, Author, Series) in the first place, so projecting via
// .Core() here just makes that guarantee visible in the type system. Unlike
// GetBooksByAuthorID (kept []Book because it is shared by two PebbleStore
// callers), this method has exactly one caller
// (PebbleStore.GetBooksBySeriesIDCore), so retyping it directly is the
// smaller change. See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (m *MemStore) GetBooksBySeriesIDCore(seriesID int, limit, offset int) ([]BookCore, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxSeriesID, seriesID)
	if err != nil {
		return nil, fmt.Errorf("memdb books by series: %w", err)
	}

	all := make([]Book, 0, 32)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		all = append(all, *b)
	}
	if len(all) > 1 {
		keys := make([]string, len(all))
		for i := range all {
			keys[i] = strings.ToLower(all[i].Title)
		}
		sort.SliceStable(all, func(i, j int) bool {
			si, sj := all[i].SeriesSequence, all[j].SeriesSequence
			switch {
			case si == nil && sj == nil:
				return keys[i] < keys[j]
			case si == nil:
				return false
			case sj == nil:
				return true
			case *si != *sj:
				return *si < *sj
			default:
				return keys[i] < keys[j]
			}
		})
	}
	paged := paginate(all, limit, offset)
	cores := make([]BookCore, len(paged))
	for i := range paged {
		cores[i] = paged[i].Core()
	}
	return cores, nil
}

// GetBooksByAuthorID returns primary, not-deleted books for an author with
// pagination. Checks both the book_authors junction table and the legacy
// AuthorID field on the Book row. No default sort — matches the Pebble path
// (which returned books in key/ULID order) and avoids per-call sort cost.
// Callers that need a specific order should sort the slice themselves.
//
// Intentionally NOT renamed/retyped to *Core (STOREFID P3-W2 / P3-W2b): this
// helper is shared by two PebbleStore callers, both of which now map its
// result through .Core() at their own boundary —
// PebbleStore.GetBooksByAuthorIDCore and
// PebbleStore.GetBooksByAuthorIDWithRoleCore (STOREFID P3-W2b). Retyping
// this MemStore method itself to return []BookCore would ripple beyond what
// either caller needs; the smaller, sufficient change is to keep this method
// []Book and let each PebbleStore-layer caller project to Core. See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (m *MemStore) GetBooksByAuthorID(authorID int, limit, offset int) ([]Book, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	bookIDSet := make(map[string]struct{})

	// Pass 1: collect bookIDs from book_authors junction table.
	baIter, err := txn.Get(memTableBookAuthors, memIdxAuthorID, authorID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_authors by author: %w", err)
	}
	for obj := baIter.Next(); obj != nil; obj = baIter.Next() {
		ba := obj.(*BookAuthor)
		bookIDSet[ba.BookID] = struct{}{}
	}

	// Pass 2: collect bookIDs from legacy AuthorID field.
	legacyIter, err := txn.Get(memTableBooks, memIdxAuthorID, authorID)
	if err != nil {
		return nil, fmt.Errorf("memdb books by author: %w", err)
	}
	for obj := legacyIter.Next(); obj != nil; obj = legacyIter.Next() {
		b := obj.(*Book)
		bookIDSet[b.ID] = struct{}{}
	}

	// Pass 3: fetch the actual book objects for all collected IDs.
	all := make([]Book, 0, len(bookIDSet))
	for bookID := range bookIDSet {
		raw, bErr := txn.First(memTableBooks, memIdxID, bookID)
		if bErr != nil || raw == nil {
			continue
		}
		b := raw.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			continue
		}
		all = append(all, *b)
	}

	return paginate(all, limit, offset), nil
}

// GetAllBooksCore returns books with optional filters and pagination,
// Core-typed (STOREFID W5a/W5z): the return type is BookCore, not Book —
// memdb rows never carry the nine heavy fields (Description, VersionNotes,
// BookSigV1, BookSigV1Mask, BookSigSegments, BookSigBuiltAt,
// BookSigCoveragePct, Author, Series) in the first place, so projecting via
// .Core() here just makes that guarantee visible in the type system. Filter
// keys: "is_primary_version" (bool), "marked_for_deletion" (bool),
// "series_id" (int), "author_id" (int), "version_group_id" (string).
//
// No default sort. The Pebble path iterated in key (ULID) order without
// sorting; matching that here keeps allocation cost flat. Sorting 68K
// books by lowercase title on every page-load was the prod regression
// that caused 340MB allocations per call and severe GC pressure — never
// again. See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (m *MemStore) GetAllBooksCore(limit, offset int, filters map[string]interface{}) ([]BookCore, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	var (
		iter interface {
			Next() interface{}
		}
		err error
	)

	switch {
	case filters["series_id"] != nil:
		iter, err = txn.Get(memTableBooks, memIdxSeriesID, filters["series_id"])
	case filters["author_id"] != nil:
		iter, err = txn.Get(memTableBooks, memIdxAuthorID, filters["author_id"])
	case filters["version_group_id"] != nil:
		iter, err = txn.Get(memTableBooks, memIdxVersionGroupID, filters["version_group_id"])
	case filters["is_primary_version"] != nil:
		iter, err = txn.Get(memTableBooks, memIdxIsPrimaryVersion, filters["is_primary_version"])
	default:
		iter, err = txn.Get(memTableBooks, memIdxID)
	}
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}

	cap0 := limit
	if cap0 <= 0 || cap0 > 100_000 {
		cap0 = 1024
	}
	all := make([]Book, 0, cap0)

	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if v, ok := filters["is_primary_version"].(bool); ok {
			eff := true
			if b.IsPrimaryVersion != nil {
				eff = *b.IsPrimaryVersion
			}
			if eff != v {
				continue
			}
		}
		if v, ok := filters["marked_for_deletion"].(bool); ok {
			eff := false
			if b.MarkedForDeletion != nil {
				eff = *b.MarkedForDeletion
			}
			if eff != v {
				continue
			}
		}
		if v, ok := filters["series_id"].(int); ok {
			if b.SeriesID == nil || *b.SeriesID != v {
				continue
			}
		}
		if v, ok := filters["author_id"].(int); ok {
			if b.AuthorID == nil || *b.AuthorID != v {
				continue
			}
		}
		if v, ok := filters["version_group_id"].(string); ok {
			if b.VersionGroupID == nil || *b.VersionGroupID != v {
				continue
			}
		}
		all = append(all, *b)
	}
	paged := paginate(all, limit, offset)
	cores := make([]BookCore, len(paged))
	for i := range paged {
		cores[i] = paged[i].Core()
	}
	return cores, nil
}

// ListBookIDs returns the IDs of all non-deleted books. Walks the memdb
// books table via the ID index and reads only b.ID off each pointer — no
// struct copy, no JSON unmarshal. Used by callers that only need the ID
// set (e.g., diff'ing against another set of IDs). Saves ~50x memory vs
// GetAllBooksCore(0,0).
func (m *MemStore) ListBookIDs() ([]string, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb list book ids: %w", err)
	}

	ids := make([]string, 0, 1024)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		ids = append(ids, b.ID)
	}
	return ids, nil
}

// ListSoftDeletedBooks returns books with MarkedForDeletion=true, with optional
// age filter (olderThan: only books whose MarkedForDeletionAt is on/before this
// time). Uses the marked_for_deletion index so cost is O(deleted_count), not
// O(total_books) — the soft-deleted set is typically tiny relative to 393K
// total books, so this is orders of magnitude faster than the Pebble full-scan.
func (m *MemStore) ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxMarkedForDeletion, true)
	if err != nil {
		return nil, fmt.Errorf("memdb soft-deleted books: %w", err)
	}

	matched := make([]Book, 0, 32)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if olderThan != nil && b.MarkedForDeletionAt != nil && b.MarkedForDeletionAt.After(*olderThan) {
			continue
		}
		matched = append(matched, *b)
	}
	// Stable sort by MarkedForDeletionAt desc (most recent first), nil last —
	// matches user expectation in the UI ("most recently deleted on top").
	sort.SliceStable(matched, func(i, j int) bool {
		ai, aj := matched[i].MarkedForDeletionAt, matched[j].MarkedForDeletionAt
		switch {
		case ai == nil && aj == nil:
			return matched[i].ID < matched[j].ID
		case ai == nil:
			return false
		case aj == nil:
			return true
		default:
			return ai.After(*aj)
		}
	})
	return paginate(matched, limit, offset), nil
}

// CountBooksByPathPrefix returns the number of (non-deleted) books whose
// SourceImportPath (or FilePath, if SourceImportPath is nil) starts with prefix.
// Falls back to a full memdb scan — no path-prefix index exists — but a memdb
// scan over 393K rows is still ~200× faster than the equivalent Pebble scan
// because the books are in RAM and don't need JSON unmarshal.
func (m *MemStore) CountBooksByPathPrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, nil
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return 0, fmt.Errorf("memdb books scan: %w", err)
	}
	count := 0
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
			continue
		}
		if b.SourceImportPath != nil && *b.SourceImportPath != "" {
			if strings.HasPrefix(*b.SourceImportPath, prefix) {
				count++
			}
			continue
		}
		if strings.HasPrefix(b.FilePath, prefix) {
			count++
		}
	}
	return count, nil
}

// ComputeLibraryStats aggregates per-book and per-file statistics from memdb,
// mirroring PebbleStore.computeLibraryStats but without any JSON unmarshal cost
// (everything is already in RAM as typed structs). rootDir is the configured
// library root (organized vs unorganized classification); importPaths is the
// resolved import-path list used for per-folder counts.
//
// Caller must populate stats.BrokenFiles separately — that count lives in the
// Pebble book_file_errors_by_book: secondary index, not in memdb.
func (m *MemStore) ComputeLibraryStats(rootDir string, importPaths []ImportPath) (*LibraryStats, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	stats := &LibraryStats{
		StateDistribution:  make(map[string]int),
		FormatDistribution: make(map[string]int),
		BooksByImportPath:  make(map[int]int),
		SizeByImportPath:   make(map[int]int64),
		ComputedAt:         time.Now(),
	}

	// Pass 1: books
	primaryBookIDs := make(map[string]struct{}, 16384)
	bIter, err := txn.Get(memTableBooks, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb books scan: %w", err)
	}
	for obj := bIter.Next(); obj != nil; obj = bIter.Next() {
		b := obj.(*Book)
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

		isPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
		if !isPrimary {
			continue
		}
		primaryBookIDs[b.ID] = struct{}{}
		if rootDir != "" && strings.HasPrefix(b.FilePath, rootDir) {
			stats.OrganizedBooks++
			stats.OrganizedSize += size
			continue
		}
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

	// Pass 2: files-per-primary-book.
	// Matches the Pebble semantics: count all files for primary books; books
	// with no file rows still count as 1 (legacy single-file-no-row case).
	bookActiveFiles := make(map[string]int, len(primaryBookIDs))
	// bookFingerprintedFiles tallies, per primary book, how many of its active
	// book_files have a fingerprint per BookFile.GetAcoustIDSeg0() (which
	// already falls back to the memdb-safe AcoustIDFingerprintDurationSec
	// proxy when AcoustIDSeg0 has been stripped — see bookfile_fingerprint.go).
	bookFingerprintedFiles := make(map[string]int, len(primaryBookIDs))
	fIter, err := txn.Get(memTableBookFiles, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	for obj := fIter.Next(); obj != nil; obj = fIter.Next() {
		bf := obj.(*BookFile)
		if _, ok := primaryBookIDs[bf.BookID]; !ok {
			continue
		}
		bookActiveFiles[bf.BookID]++
		if bf.GetAcoustIDSeg0() != "" {
			bookFingerprintedFiles[bf.BookID]++
		}
	}
	for id := range primaryBookIDs {
		if n := bookActiveFiles[id]; n > 0 {
			stats.TotalFiles += n
		} else {
			stats.TotalFiles++
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

	// Authors / Series totals come straight from memdb tables (cheap).
	aIter, err := txn.Get(memTableAuthors, memIdxID)
	if err == nil {
		for obj := aIter.Next(); obj != nil; obj = aIter.Next() {
			stats.TotalAuthors++
		}
	}
	sIter, err := txn.Get(memTableSeries, memIdxID)
	if err == nil {
		for obj := sIter.Next(); obj != nil; obj = sIter.Next() {
			stats.TotalSeries++
		}
	}

	if stats.TotalBooks > 0 {
		stats.FingerprintCoveragePercent = stats.FingerprintedBooks * 100 / stats.TotalBooks
	}

	return stats, nil
}

// GetBookFilesForIDsCore returns book files grouped by bookID, as the
// BookFileCore projection, using the memdb book_id index —
// O(sum-of-files-for-IDs), NOT O(all 308K book_files) like the Pebble
// full-scan implementation. For a 500-book page query, this drops from ~15s
// to <5ms.
//
// Returns an empty map for empty input. Caller-supplied IDs absent from
// memdb appear as missing keys in the result (caller filters as needed).
//
// Core-typed (STOREFID): memdb rows are already the stripped BookFile
// projection (stripBookFileForMemdb), so .Core() here is a lossless
// re-projection onto BookFileCore — no additional fields are dropped beyond
// what memdb already strips. See
// docs/specs/2026-07-05-store-getter-fidelity-unification.md.
func (m *MemStore) GetBookFilesForIDsCore(bookIDs []string) (map[string][]BookFileCore, error) {
	result := make(map[string][]BookFileCore, len(bookIDs))
	if len(bookIDs) == 0 {
		return result, nil
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	for _, id := range bookIDs {
		iter, err := txn.Get(memTableBookFiles, memIdxBookID, id)
		if err != nil {
			return nil, fmt.Errorf("memdb book_files for %s: %w", id, err)
		}
		for obj := iter.Next(); obj != nil; obj = iter.Next() {
			bf, ok := obj.(*BookFile)
			if !ok {
				continue
			}
			result[id] = append(result[id], bf.Core())
		}
	}
	return result, nil
}

// GetAllBookFilesCore returns the BookFileCore projection of every BookFile
// in the memdb book_files table by iterating the primary ID index. O(N)
// pointer walk over the in-memory table — no JSON unmarshal, no Pebble disk
// scan. For 308K book_files this is roughly two orders of magnitude faster
// than the Pebble full-scan fallback.
func (m *MemStore) GetAllBookFilesCore() ([]BookFileCore, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	iter, err := txn.Get(memTableBookFiles, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb book_files scan: %w", err)
	}
	var files []BookFileCore
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		bf, ok := obj.(*BookFile)
		if !ok {
			continue
		}
		files = append(files, bf.Core())
	}
	return files, nil
}

// GetBookFilesNeedingDelugeImportCore returns BookFileCores that have a
// non-empty DelugeHash AND have not yet been imported (ImportedFromDelugeAt
// is nil). Core-typed (STOREFID W6) — see the interface doc comment.
//
// Walks the sparse memdb deluge_hash index — only rows with a non-empty
// DelugeHash exist in that index — then post-filters on the
// ImportedFromDelugeAt nil check. This is O(deluge-hash-present rows), not
// O(308K), which mirrors the GetAllBookFiles fastpath from PR #1166 but
// trims the working set to the deluge-relevant subset for the discovery
// handler and centralization plugin (H2 + H8).
func (m *MemStore) GetBookFilesNeedingDelugeImportCore() ([]BookFileCore, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	iter, err := txn.Get(memTableBookFiles, memIdxDelugeHash)
	if err != nil {
		return nil, fmt.Errorf("memdb deluge_hash scan: %w", err)
	}
	var out []BookFileCore
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		bf, ok := obj.(*BookFile)
		if !ok {
			continue
		}
		if bf.ImportedFromDelugeAt != nil {
			continue
		}
		out = append(out, bf.Core())
	}
	return out, nil
}

// ListBooksByITunesPID returns books whose ITunesPersistentID is non-nil and
// non-empty, paginated. Walks the itunes_persistent_id memdb index — the
// nullableStringFieldIndex skips nil/empty rows, so the iterator only sees
// books that actually have an iTunes mapping. Order is the index's natural
// byte order on the PID; not sorted further because the iTunes handlers
// don't care about ordering (results are joined back to the iTunes library
// XML by PID lookup).
//
// Returns the full match-set when limit <= 0 (matches the GetAllBooks
// "fetch all" convention used by the iTunes writeback-preview handler).
func (m *MemStore) ListBooksByITunesPID(limit, offset int) ([]Book, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBooks, memIdxITunesPID)
	if err != nil {
		return nil, fmt.Errorf("memdb books by itunes pid: %w", err)
	}

	all := make([]Book, 0, 256)
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		// Defensive: the index already skips nil/empty, but a non-nil
		// pointer to an empty string would only be skipped by the indexer
		// itself — keep the check so callers can rely on the postcondition.
		if b.ITunesPersistentID == nil || *b.ITunesPersistentID == "" {
			continue
		}
		all = append(all, *b)
	}
	return paginate(all, limit, offset), nil
}

// GetBookFileByAcoustIDFuzzy walks memdb book_files (in-RAM, no Pebble I/O)
// and returns the first row whose any of AcoustIDSeg0..6 is fuzzy-similar
// to fp at or above minSimilarity. This replaces a full Pebble prefix scan
// of all book_file:* keys (308K+) — wedge point for the AcoustIDScan op.
//
// Iteration order is arbitrary (memdb's id index is hash-based). The dedup
// engine only needs ANY match, not a deterministic one, so this is fine.
func (m *MemStore) GetBookFileByAcoustIDFuzzy(fp string, minSimilarity float64) (*BookFile, error) {
	if fp == "" {
		return nil, nil
	}
	txn := m.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(memTableBookFiles, memIdxID)
	if err != nil {
		return nil, fmt.Errorf("memdb fuzzy acoustid scan: %w", err)
	}
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		bf := obj.(*BookFile)
		segs := [7]string{
			bf.AcoustIDSeg0, bf.AcoustIDSeg1, bf.AcoustIDSeg2, bf.AcoustIDSeg3,
			bf.AcoustIDSeg4, bf.AcoustIDSeg5, bf.AcoustIDSeg6,
		}
		for _, seg := range segs {
			if seg == "" {
				continue
			}
			sim, simErr := fingerprint.HammingSimilarity(fp, seg)
			if simErr != nil {
				continue
			}
			if sim >= minSimilarity {
				cp := *bf
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func paginate[T any](in []T, limit, offset int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return nil
	}
	end := len(in)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return in[offset:end]
}
