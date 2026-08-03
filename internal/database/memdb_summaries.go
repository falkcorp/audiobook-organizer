// file: internal/database/memdb_summaries.go
// version: 1.3.1
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000008
// last-edited: 2026-07-05

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

	// SortBy selects which memdb index drives iteration. Empty string means
	// "any order" (currently equivalent to ID-sorted). Only "title" is
	// honored today — other sort keys fall through to the service-level
	// in-memory sort path. The memdb title index is a sorted radix tree, so
	// iterating it is O(offset+limit), not O(n).
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
			Next() interface{}
		}
		err error
	)
	switch {
	case f.SortBy == "title":
		if f.SortAscending {
			iter, err = txn.Get(memTableBooks, memIdxTitle)
		} else {
			iter, err = txn.GetReverse(memTableBooks, memIdxTitle)
		}
	case f.IsPrimaryVersion != nil:
		iter, err = txn.Get(memTableBooks, memIdxIsPrimaryVersion, *f.IsPrimaryVersion)
	default:
		iter, err = txn.Get(memTableBooks, memIdxID)
	}
	if err != nil {
		return nil, fmt.Errorf("memdb book_summaries scan: %w", err)
	}

	// When iterating by title, IsPrimary becomes an in-loop predicate.
	primaryFilter := f.SortBy == "title" && f.IsPrimaryVersion != nil
	wantPrimary := false
	if primaryFilter {
		wantPrimary = *f.IsPrimaryVersion
	}

	// excludeDeleted: by default we drop marked_for_deletion=true rows
	// (mirrors getAllBookSummariesFull). An explicit filter overrides.
	excludeDeleted := true
	requireDeleted := false
	if f.MarkedForDeletion != nil {
		excludeDeleted = false
		requireDeleted = *f.MarkedForDeletion
	}

	cap0 := limit
	if cap0 > 4096 {
		cap0 = 4096
	}
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
		if excludeDeleted {
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
		} else {
			isDel := b.MarkedForDeletion != nil && *b.MarkedForDeletion
			if isDel != requireDeleted {
				continue
			}
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
	return out, nil
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
		iter interface{ Next() interface{} }
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
	excludeDeleted := true
	requireDeleted := false
	if f.MarkedForDeletion != nil {
		excludeDeleted = false
		requireDeleted = *f.MarkedForDeletion
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
		if excludeDeleted {
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
		} else {
			isDel := b.MarkedForDeletion != nil && *b.MarkedForDeletion
			if isDel != requireDeleted {
				continue
			}
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
