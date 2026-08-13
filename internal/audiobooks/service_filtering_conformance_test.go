// file: internal/audiobooks/service_filtering_conformance_test.go
// version: 1.0.0
// guid: 6d2a9f14-7c85-4b30-a1e9-3f8c5b26d047
// last-edited: 2026-08-13

package audiobooks

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// This file pins the distinction that the library-list pushdown got wrong:
// "can you filter?" is not "did you filter?".
//
// summariesPushdownFiltered reports didPushdown=true when the store satisfies
// filteredSummaryStore, and its callers in service_query.go skip their own
// post-filter pass on the strength of that report. When conformance was
// inferred from the presence of GetAllBookSummariesFiltered alone, a store
// that applied a subset of the filter satisfied the assertion exactly as well
// as one that applied all of it — and the skipped post-filter pass was the
// only thing that would have caught the difference. That is how a filtered
// library query returned ~63,870 rows during the startup warmup.
//
// The marker method makes the fast path opt-in, so the failure mode of an
// unaware implementer is "slow but correct" rather than "fast and wrong".

// partialFilterStore has the filter methods but never declares conformance —
// the shape of a store that applies some predicates and not others. It must
// NOT satisfy either pushdown interface.
type partialFilterStore struct{}

func (partialFilterStore) GetAllBookSummariesFiltered(limit, offset int, f database.BookSummaryFilter) ([]database.BookSummary, error) {
	return nil, nil
}

func (partialFilterStore) CountBookSummariesFiltered(f database.BookSummaryFilter) (int, error) {
	return 0, nil
}

// conformingFilterStore is partialFilterStore plus the explicit declaration.
// It is the positive control: the marker is what flips satisfaction, so a
// test that only checked the negative case would also pass if the interfaces
// were unsatisfiable by anything at all.
type conformingFilterStore struct{ partialFilterStore }

func (conformingFilterStore) HonorsEveryBookSummaryFilter() {}

func TestFilteredSummaryStore_MethodAloneIsNotConformance(t *testing.T) {
	var partial any = partialFilterStore{}

	_, ok := partial.(filteredSummaryStore)
	require.False(t, ok,
		"a store exposing GetAllBookSummariesFiltered without declaring "+
			"HonorsEveryBookSummaryFilter must NOT satisfy filteredSummaryStore; "+
			"the caller skips its post-filter pass whenever this assertion succeeds")

	_, ok = partial.(countingFilteredStore)
	require.False(t, ok,
		"a store exposing CountBookSummariesFiltered without declaring "+
			"HonorsEveryBookSummaryFilter must NOT satisfy countingFilteredStore; "+
			"its number is returned as the pagination total with no correction available")
}

func TestFilteredSummaryStore_MarkerGrantsConformance(t *testing.T) {
	var conforming any = conformingFilterStore{}

	_, ok := conforming.(filteredSummaryStore)
	require.True(t, ok,
		"declaring HonorsEveryBookSummaryFilter must satisfy filteredSummaryStore")

	_, ok = conforming.(countingFilteredStore)
	require.True(t, ok,
		"declaring HonorsEveryBookSummaryFilter must satisfy countingFilteredStore")
}

// TestPebbleStoreDeclaresFilterConformance guards the production path. If
// PebbleStore ever loses the marker, every library query silently drops to
// the fetch-everything-and-post-filter path — correct, but a full-corpus
// fetch per request. That regression is invisible in output and shows up only
// as latency, so assert it directly.
func TestPebbleStoreDeclaresFilterConformance(t *testing.T) {
	var store any = &database.PebbleStore{}

	_, ok := store.(filteredSummaryStore)
	require.True(t, ok, "PebbleStore must satisfy filteredSummaryStore")

	_, ok = store.(countingFilteredStore)
	require.True(t, ok, "PebbleStore must satisfy countingFilteredStore")
}
