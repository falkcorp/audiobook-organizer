// file: internal/database/memdb_summaries.go
// version: 1.8.1
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000008
// last-edited: 2026-09-02

package database

import (
	"fmt"
	"strings"
)

// BookSummaryFilter narrows a summary query without forcing the caller to
// materialize a full Book slice. Each non-nil field becomes a predicate
// applied during memdb iteration; nil means "no constraint".
//
// IsPrimaryVersion is the hot one — the UI sends it on every library page
// load, so pushing it down lets memdb iterate only the ~38K primary rows
// instead of the full ~68K.
type BookSummaryFilter struct {
	IsPrimaryVersion  *bool
	MarkedForDeletion *bool // nil → exclude deleted (default behavior)

	// SortBy selects which sorted index drives iteration. Empty string means
	// "any order" (currently equivalent to ID-sorted). "title" always
	// streams; every other field streams only while its index is enabled
	// (sortIndexEnabled, mirroring CanPushDownSort) and otherwise falls
	// through to a materialise-and-sort path. A memdb sort index is a sorted
	// radix tree, so iterating it is O(offset+limit), not O(n).
	//
	// Stores answering this filter must honor SortBy or decline the
	// HonorsEveryBookSummaryFilter marker — ordering is a predicate, and the
	// service layer skips its own sort when a store claims the page is final.
	SortBy string
	// SortAscending controls iteration direction when SortBy is set.
	// True (or zero value with SortBy=="title") = A→Z; false = Z→A.
	SortAscending bool

	// ExcludeQuarantined, when true, drops rows whose QuarantinedAt is set.
	// Applied in-loop BEFORE offset/limit so pagination counts the post-
	// quarantine set — a page of N returns N non-quarantined books, and the
	// matching count (CountBookSummaries) agrees. The HTTP layer sets this from
	// the inverse of ?show_quarantined.
	ExcludeQuarantined bool

	// LibraryState, if non-empty, restricts to books with this LibraryState
	// (case-sensitive equality, e.g. "organized" / "imported" / "suspicious").
	// Applied as an in-loop predicate during iteration — cheap because we walk
	// pointers, not copies, and stop at limit+offset matches.
	LibraryState string

	// ReviewStatus, if non-empty, restricts to books whose MetadataReviewStatus
	// equals this value (e.g. "matched" / "no_match"). Applied in-loop.
	ReviewStatus string

	// RestrictToIDs, if non-nil, restricts iteration to books whose ID is in
	// the set. Used for tag-set intersection: the caller resolves
	// tag → []book_id via store.GetBooksByTag, builds a set, passes it here.
	// Empty set means "no books match" (fast empty return).
	RestrictToIDs map[string]struct{}

	// Predicate is an optional in-loop predicate called per row with a
	// *Book pointer (no copy). Return true to keep, false to skip. Use to
	// push down arbitrary filter logic (FieldFilters, PerUserFilters,
	// fingerprint coverage, etc.) without forcing the database package to
	// know about service-layer filter shapes.
	//
	// IMPORTANT: must be read-only. Mutating *b or the memdb txn from inside
	// the predicate is undefined behavior.
	Predicate func(*Book) bool
}

// GetBookSummaries returns a paginated slice of BookSummary records,
// projecting from Book in-place during iteration. Key differences vs.
// "fetch all Books then project":
//
//   - Iterates the most selective index given the filter.
//   - Projects Book → BookSummary inside the loop (no full-Book copies).
//   - Stops as soon as `limit` summaries have been collected past `offset`.
//
// For the typical library list query (is_primary_version=true, limit=50,
// offset=0) this performs ~50 BookSummary allocations and ~50 index node
// reads instead of 68K Book copies + 68K BookSummary projections.
func (m *MemStore) GetBookSummaries(limit, offset int, f BookSummaryFilter) ([]BookSummary, error) {
	if limit <= 0 {
		limit = 1_000_000
	}
	if offset < 0 {
		offset = 0
	}
	// Empty RestrictToIDs means "no books are eligible" — short-circuit
	// before opening a txn. nil means "no restriction".
	if f.RestrictToIDs != nil && len(f.RestrictToIDs) == 0 {
		return []BookSummary{}, nil
	}

	txn := m.db.Txn(false)
	defer txn.Abort()

	// Index selection priority:
	//   1. SortBy=="title" → iterate title index in order (asc/desc). IsPrimary
	//      is applied as an in-loop filter — title index is the sorted radix
	//      tree we need, and the rejected rows are cheap to skip.
	//   2. IsPrimary set, no SortBy → use the IsPrimary index (most selective
	//      for the typical library page query).
	//   3. Otherwise → ID-ordered scan.
	var (
		iter interface {
			Next() any
		}
		err error
	)
	// sortIdx is the sorted index serving this request, or "" when the sort
	// cannot be streamed and the caller falls back to materialise-and-sort.
	// title predates the generic map and keeps its own name.
	//
	// The enabled check is load-bearing: sortIndexForField lists every field
	// that COULD be indexed, but indexes are opt-in, so mapping straight
	// through it would hand txn.Get an index name that was never registered
	// and error at runtime. sortIndexEnabled is the same predicate
	// CanPushDownSort uses, so the query service and the store cannot
	// disagree about which sorts stream.
	sortIdx := ""
	if f.SortBy == "title" {
		sortIdx = memIdxTitle
	} else if sortIndexEnabled(f.SortBy) {
		sortIdx = sortIndexForField[f.SortBy]
	}

	// Ordering is a predicate too. When no index can stream f.SortBy but
	// SortBooks understands it, the page has to be chosen from the ORDERED
	// match set — so collect every match first and sort before paginating.
	//
	// The walk below is index order, not sort order, so consuming offset/limit
	// during it picks an arbitrary window. Whoever sorts that window afterwards
	// gets a page that looks perfectly ordered and holds the wrong rows: "the
	// 50 shortest books" comes back as 50 books in correct duration order that
	// are not the 50 shortest, 200 OK, no error surface.
	//
	// This is NOT something the caller can do instead. The service receives
	// []BookSummary, and BookSummary drops most of what is sortable — author,
	// series, year, genre, language, publisher, codec, quality, edition,
	// bitrate and sample_rate are all absent from it (see BookSummary and
	// audiobooks.bookSummaryToBook). Sorting there compares "" against "" for
	// every row and returns the input untouched. Measured before this change:
	// 13 of the 23 keys in bookSortComparators ordered nothing at all through
	// GetAudiobooks. Do NOT move this back up the stack — see
	// docs/audits/2026-08-25-author-series-sort-degenerate.md.
	materialise := sortIdx == "" && CanSortBooksBy(f.SortBy)
	var matches []Book

	switch {
	case sortIdx != "":
		if f.SortAscending {
			iter, err = txn.Get(memTableBooks, sortIdx)
		} else {
			iter, err = txn.GetReverse(memTableBooks, sortIdx)
		}
	case f.IsPrimaryVersion != nil:
		iter, err = txn.Get(memTableBooks, memIdxIsPrimaryVersion, *f.IsPrimaryVersion)
	default:
		iter, err = txn.Get(memTableBooks, memIdxID)
	}
	if err != nil {
		return nil, fmt.Errorf("memdb book_summaries scan: %w", err)
	}

	// When iterating a sorted index, IsPrimary becomes an in-loop predicate:
	// the ordered walk is the thing we need, and rejected rows are cheap to
	// skip.
	primaryFilter := sortIdx != "" && f.IsPrimaryVersion != nil
	wantPrimary := false
	if primaryFilter {
		wantPrimary = *f.IsPrimaryVersion
	}

	// CodeQL go/uncontrolled-allocation-size (alert #966): only the INITIAL
	// capacity passed to make() is bounded here — cap0 is clamped to <= 4096
	// regardless of the caller-supplied limit (limit<=0 becomes 1_000_000
	// above, but that value is then clamped right here). The slice can still
	// GROW past 4096 via append below: the loop's only stop condition is
	// `len(out) >= limit` (see the `break` after the append), so the actual
	// peak size is min(limit, matching rows in the corpus) — up to 1,000,000
	// if the caller passes limit<=0 and that many rows match. That peak is
	// bounded by the corpus size, not by any caller-supplied value, which is
	// why this is a false positive: the allocation is data-bounded, not
	// attacker-controlled — but it is NOT a hard 4096-element ceiling.
	// Re-verified 2026-08-23: dismissed via the code-scanning API citing the
	// cap0 clamp; this comment additionally documents the actual (larger)
	// peak so a future reader does not mistake cap0's bound for a ceiling on
	// len(out).
	cap0 := min(limit, 4096)
	out := make([]BookSummary, 0, cap0)
	skipped := 0

	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)

		// When sorting by title, IsPrimary is enforced here (not by index).
		// nil IsPrimaryVersion on the row counts as "primary" per the
		// historical Pebble fallback semantics in
		// GetAllBookSummariesFiltered.
		if primaryFilter {
			eff := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
			if eff != wantPrimary {
				continue
			}
		}

		// Apply filters before pagination so offset/limit match the
		// post-filter set, not the pre-filter set.
		//
		// f.MarkedForDeletion is the tri-state: nil excludes the trash
		// (the default library view), &true requires it, &false requires
		// live rows. The policy itself lives in includeByDeletionState so
		// that GetAllBooksCore cannot spell it differently.
		if !includeByDeletionState(bookIsSoftDeleted(b), f.MarkedForDeletion) {
			continue
		}

		// In-loop filter pushdowns. Each predicate is O(1) per row; the
		// loop body short-circuits on the first miss, so adding filters
		// can only reduce work, never add it.
		if f.ExcludeQuarantined && b.QuarantinedAt != nil {
			continue
		}
		if f.LibraryState != "" {
			ls := ""
			if b.LibraryState != nil {
				ls = *b.LibraryState
			}
			if ls != f.LibraryState {
				continue
			}
		}
		if f.ReviewStatus != "" {
			rs := ""
			if b.MetadataReviewStatus != nil {
				rs = *b.MetadataReviewStatus
			}
			if !strings.EqualFold(rs, f.ReviewStatus) {
				continue
			}
		}
		if f.RestrictToIDs != nil {
			if _, ok := f.RestrictToIDs[b.ID]; !ok {
				continue
			}
		}
		if f.Predicate != nil && !f.Predicate(b) {
			continue
		}

		// Materialising: take every match, unpaginated. The sort below
		// decides which rows the page holds.
		if materialise {
			matches = append(matches, *b)
			continue
		}

		if skipped < offset {
			skipped++
			continue
		}

		out = append(out, BookSummary{
			ID:                   b.ID,
			Title:                b.Title,
			AuthorID:             b.AuthorID,
			SeriesID:             b.SeriesID,
			SeriesSequence:       b.SeriesSequence,
			FilePath:             b.FilePath,
			Format:               b.Format,
			Duration:             b.Duration,
			OriginalFilename:     b.OriginalFilename,
			FileSize:             b.FileSize,
			FileHash:             b.FileHash,
			OriginalFileHash:     b.OriginalFileHash,
			OrganizedFileHash:    b.OrganizedFileHash,
			LibraryState:         b.LibraryState,
			QuarantinedAt:        b.QuarantinedAt,
			QuarantineReason:     b.QuarantineReason,
			CoverURL:             b.CoverURL,
			Narrator:             b.Narrator,
			NarratorsJSON:        b.NarratorsJSON,
			TranscribedTitle:     b.TranscribedTitle,
			CreatedAt:            b.CreatedAt,
			UpdatedAt:            b.UpdatedAt,
			MetadataUpdatedAt:    b.MetadataUpdatedAt,
			IsPrimaryVersion:     b.IsPrimaryVersion,
			VersionGroupID:       b.VersionGroupID,
			MetadataReviewStatus: b.MetadataReviewStatus,
		})

		if len(out) >= limit {
			break
		}
	}

	if materialise {
		// author/series sort on a name the row does not carry: memdb strips
		// both pointers (stripBookForMemdb) so every book would compare as "".
		// Resolve them from the same txn before sorting.
		if err := hydrateSortNames(txn, matches, f.SortBy); err != nil {
			return nil, err
		}
		SortBooks(matches, f.SortBy, f.SortAscending)
		if offset >= len(matches) {
			return []BookSummary{}, nil
		}
		end := len(matches)
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		page := matches[offset:end]
		sorted := make([]BookSummary, len(page))
		for i := range page {
			sorted[i] = bookToSummary(&page[i])
		}
		return sorted, nil
	}

	return out, nil
}

// memFirstLookup is the one memdb operation hydrateSortNames needs. Taking an
// interface keeps this file free of a memdb import for a single call.
type memFirstLookup interface {
	First(table, index string, args ...any) (any, error)
}

// hydrateSortNames fills in the Author/Series pointer that stripBookForMemdb
// cleared, but only for the sort that actually reads it. Every other sort key
// reads a field the memdb row still carries, so this is a no-op for them.
//
// Sorting by author is the reason this exists: the memdb sort index for author
// is built by an indexer that receives only *Book, and the *Book in memdb has
// Author=nil, so the index orders every row under the same empty key. A name
// can only be resolved by something holding the txn — which is this function,
// not the indexer.
func hydrateSortNames(txn memFirstLookup, books []Book, sortBy string) error {
	switch sortBy {
	case "author":
		cache := make(map[int]*Author)
		for i := range books {
			id := books[i].AuthorID
			if id == nil {
				continue
			}
			a, seen := cache[*id]
			if !seen {
				obj, err := txn.First(memTableAuthors, memIdxID, *id)
				if err != nil {
					return fmt.Errorf("memdb author lookup for sort: %w", err)
				}
				if av, ok := obj.(*Author); ok {
					a = av
				}
				cache[*id] = a
			}
			books[i].Author = a
		}
	case "series":
		cache := make(map[int]*Series)
		for i := range books {
			id := books[i].SeriesID
			if id == nil {
				continue
			}
			sv, seen := cache[*id]
			if !seen {
				obj, err := txn.First(memTableSeries, memIdxID, *id)
				if err != nil {
					return fmt.Errorf("memdb series lookup for sort: %w", err)
				}
				if s2, ok := obj.(*Series); ok {
					sv = s2
				}
				cache[*id] = sv
			}
			books[i].Series = sv
		}
	}
	return nil
}

// CountBookSummaries returns the number of rows that would be returned by
// GetBookSummaries with the same filter (and unbounded limit/offset). Shares
// the exact iteration + predicate logic, but never allocates BookSummary
// projections — just increments a counter. Cost: O(matches) for the
// allocation-free portion, O(corpus) iteration in the worst case where most
// rows fail the predicate.
//
// Use for pagination totals when the unfiltered Pebble count is wrong.
func (m *MemStore) CountBookSummaries(f BookSummaryFilter) (int, error) {
	if f.RestrictToIDs != nil && len(f.RestrictToIDs) == 0 {
		return 0, nil
	}

	txn := m.db.Txn(false)
	defer txn.Abort()

	var (
		iter interface{ Next() any }
		err  error
	)
	switch {
	case f.SortBy == "title":
		// No need to sort for a count, but using the title index is fine;
		// it's the same set of pointers in different order.
		iter, err = txn.Get(memTableBooks, memIdxTitle)
	case f.IsPrimaryVersion != nil:
		iter, err = txn.Get(memTableBooks, memIdxIsPrimaryVersion, *f.IsPrimaryVersion)
	default:
		iter, err = txn.Get(memTableBooks, memIdxID)
	}
	if err != nil {
		return 0, fmt.Errorf("memdb book_summaries count: %w", err)
	}

	primaryFilter := f.SortBy == "title" && f.IsPrimaryVersion != nil
	wantPrimary := false
	if primaryFilter {
		wantPrimary = *f.IsPrimaryVersion
	}
	n := 0
	for obj := iter.Next(); obj != nil; obj = iter.Next() {
		b := obj.(*Book)
		if primaryFilter {
			eff := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
			if eff != wantPrimary {
				continue
			}
		}
		// Same tri-state policy as the listing pass above. This is a COUNT
		// of what that pass would return, so the two must agree by
		// construction, not by two people writing the same if/else.
		if !includeByDeletionState(bookIsSoftDeleted(b), f.MarkedForDeletion) {
			continue
		}
		if f.ExcludeQuarantined && b.QuarantinedAt != nil {
			continue
		}
		if f.LibraryState != "" {
			ls := ""
			if b.LibraryState != nil {
				ls = *b.LibraryState
			}
			if ls != f.LibraryState {
				continue
			}
		}
		if f.ReviewStatus != "" {
			rs := ""
			if b.MetadataReviewStatus != nil {
				rs = *b.MetadataReviewStatus
			}
			if !strings.EqualFold(rs, f.ReviewStatus) {
				continue
			}
		}
		if f.RestrictToIDs != nil {
			if _, ok := f.RestrictToIDs[b.ID]; !ok {
				continue
			}
		}
		if f.Predicate != nil && !f.Predicate(b) {
			continue
		}
		n++
	}
	return n, nil
}
