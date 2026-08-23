// file: internal/dedup/series_dedup_test.go
// version: 1.2.0
// guid: f6a7b8c9-d0e1-2345-fabc-456789012345
// last-edited: 2026-08-23

package dedup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── ExtractSeriesNameForDedup ─────────────────────────────────────────────────

func TestExtractSeriesNameForDedup(t *testing.T) {
	tests := []struct {
		input     string
		want      string
		wantMatch bool
	}{
		// Colon pattern: after-part is shorter → it's the series
		{"The Great War: Darkness", "Darkness", true},
		// Colon pattern: before-part is shorter → it's the series
		// "Long" (4 chars) < "A Very Long Subtitle That Goes On And On" so "Long" wins
		{"Long: A Very Long Subtitle That Goes On And On", "Long", true},
		// Comma-book pattern
		{"Shadow of the Wind, Book 2", "Shadow of the Wind", true},
		// Comma-vol pattern
		{"Discworld, Vol 5", "Discworld", true},
		// Comma-hash pattern (", #" in the list)
		{"Farseer, #3", "Farseer", true},
		// No pattern → false
		{"Just A Plain Series Name", "", false},
		// Too short after-colon part (≤3 chars)
		{"Something: No", "", false},
	}
	for _, tt := range tests {
		got, ok := ExtractSeriesNameForDedup(tt.input)
		if tt.wantMatch {
			assert.True(t, ok, "expected match for %q", tt.input)
			assert.Equal(t, tt.want, got, "input %q", tt.input)
		} else {
			assert.False(t, ok, "expected no match for %q", tt.input)
		}
	}
}

// ── isGarbageSeries ───────────────────────────────────────────────────────────

func TestIsGarbageSeries(t *testing.T) {
	assert.True(t, isGarbageSeries(""))
	assert.True(t, isGarbageSeries("   "))
	assert.True(t, isGarbageSeries("1234"))
	assert.False(t, isGarbageSeries("The Expanse"))
	assert.False(t, isGarbageSeries("Book 1"))
}

// ── ScanSeriesDuplicates ─────────────────────────────────────────────────────

func TestScanSeriesDuplicates_ExactDuplicates(t *testing.T) {
	// Two series with exactly the same name → one "exact" group.
	// NormalizeString only trims/lowercases, so names must be literally equal
	// (after trim+lowercase) to land in the same bucket.
	seriesA := database.Series{ID: 1, Name: "The Expanse"}
	seriesB := database.Series{ID: 2, Name: "the expanse"} // same after NormalizeString

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{seriesA, seriesB}, nil
	}
	mock.GetAllAuthorsFunc = func() ([]database.Author, error) { return nil, nil }

	result, err := ScanSeriesDuplicates(context.Background(), mock, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, result.TotalSeries)
	require.Len(t, result.Groups, 1, "one exact-duplicate group expected")
	assert.Equal(t, "exact", result.Groups[0].MatchType)
	assert.Equal(t, 2, result.Groups[0].Count)
}

func TestScanSeriesDuplicates_GarbageFiltered(t *testing.T) {
	// Numeric-only series names should be excluded from grouping entirely.
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "1234"},
			{ID: 2, Name: "5678"},
		}, nil
	}
	mock.GetAllAuthorsFunc = func() ([]database.Author, error) { return nil, nil }

	result, err := ScanSeriesDuplicates(context.Background(), mock, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Groups, "garbage series should produce no duplicate groups")
}

func TestScanSeriesDuplicates_SubseriesPattern(t *testing.T) {
	// "Shadow of the Wind, Book 2" should detect "Shadow of the Wind" as a
	// parent and group with a series named exactly "Shadow of the Wind".
	parent := database.Series{ID: 1, Name: "Shadow of the Wind"}
	child := database.Series{ID: 2, Name: "Shadow of the Wind, Book 2"}

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{parent, child}, nil
	}
	mock.GetAllAuthorsFunc = func() ([]database.Author, error) { return nil, nil }

	result, err := ScanSeriesDuplicates(context.Background(), mock, nil)
	require.NoError(t, err)
	// Either an exact group or a subseries group should be produced.
	assert.NotEmpty(t, result.Groups, "subseries should form at least one group")
}

// ── DedupSeries ──────────────────────────────────────────────────────────────

func TestDedupSeries_MergesDuplicates(t *testing.T) {
	// Series 1 and 2 have the same normalised name → 2 should be merged into 1.
	seriesA := database.Series{ID: 1, Name: "Foundation"}
	seriesB := database.Series{ID: 2, Name: "Foundation"}

	var deletedIDs []int
	var updatedBooks []database.Book

	heavyDesc := "heavy description that must survive the series reassign"

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{seriesA, seriesB}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 2 {
			return []database.BookCore{{ID: "BOOK1", SeriesID: &id}}, nil
		}
		return nil, nil
	}
	// DedupSeries hydrates the full row via GetBookByID before writing, so the
	// heavy Description field is present at write time and must be preserved.
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 2
		return &database.Book{ID: id, SeriesID: &sid, Description: strPtr(heavyDesc)}, nil
	}
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		updatedBooks = append(updatedBooks, *book)
		return book, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	}

	// dryRun=false: the anti-over-suppression case. The new guard must not
	// become the only path — a real run still deletes and still reassigns.
	result, err := DedupSeries(context.Background(), mock, nil, false)
	require.NoError(t, err)
	assert.False(t, result.DryRun)
	assert.Equal(t, 1, result.TotalMerged)
	assert.Equal(t, 1, result.TotalBooksReassigned)
	assert.Empty(t, result.Errors)
	assert.Contains(t, deletedIDs, 2)
	// The book should have been reassigned to series 1.
	require.Len(t, updatedBooks, 1)
	require.NotNil(t, updatedBooks[0].SeriesID)
	assert.Equal(t, 1, *updatedBooks[0].SeriesID)
	// Regression: the hydrate-mutate-update path must NOT wipe heavy fields.
	require.NotNil(t, updatedBooks[0].Description)
	assert.Equal(t, heavyDesc, *updatedBooks[0].Description)
}

func TestDedupSeries_NoDuplicates(t *testing.T) {
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: 1, Name: "Alpha"},
			{ID: 2, Name: "Beta"},
		}, nil
	}

	// Edge case from the brief: dryRun=true over zero duplicate groups is
	// TotalMerged=0 and no error, not a failure.
	result, err := DedupSeries(context.Background(), mock, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 0, result.TotalMerged)
	assert.Equal(t, 0, result.TotalBooksReassigned)
	assert.True(t, result.DryRun)
	assert.Empty(t, result.Errors)
}

// ── DedupSeries dry run ───────────────────────────────────────────────────────

// dedupFixtureStore is a MockStore whose series and book rows are REAL mutable
// state, not recorded call arguments. The dry-run test has to assert that the
// store is unchanged, and a mock that only records calls cannot tell the
// difference between "the write was skipped" and "the write was applied to
// nothing".
type dedupFixtureStore struct {
	*database.MockStore
	series map[int]database.Series
	books  map[string]database.Book
	writes int // UpdateBook + DeleteSeries calls actually reaching the store
}

func newDedupFixtureStore(series []database.Series, books []database.Book) *dedupFixtureStore {
	f := &dedupFixtureStore{
		MockStore: &database.MockStore{},
		series:    make(map[int]database.Series, len(series)),
		books:     make(map[string]database.Book, len(books)),
	}
	for _, s := range series {
		f.series[s.ID] = s
	}
	for _, b := range books {
		f.books[b.ID] = b
	}

	f.GetAllSeriesFunc = func() ([]database.Series, error) {
		out := make([]database.Series, 0, len(f.series))
		for _, s := range f.series {
			out = append(out, s)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	f.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		var out []database.BookCore
		for _, b := range f.books {
			if b.SeriesID != nil && *b.SeriesID == id {
				sid := *b.SeriesID
				out = append(out, database.BookCore{ID: b.ID, SeriesID: &sid})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	f.GetBookByIDFunc = func(id string) (*database.Book, error) {
		b, ok := f.books[id]
		if !ok {
			return nil, nil
		}
		clone := b
		if b.SeriesID != nil {
			sid := *b.SeriesID
			clone.SeriesID = &sid
		}
		return &clone, nil
	}
	f.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		f.writes++
		clone := *book
		if book.SeriesID != nil {
			sid := *book.SeriesID
			clone.SeriesID = &sid
		}
		f.books[id] = clone
		return book, nil
	}
	f.DeleteSeriesFunc = func(id int) error {
		f.writes++
		delete(f.series, id)
		return nil
	}
	return f
}

// snapshot renders every series row and every book→series association in a
// stable order. Two snapshots comparing equal means nothing in the store moved.
func (f *dedupFixtureStore) snapshot() string {
	var sb strings.Builder
	ids := make([]int, 0, len(f.series))
	for id := range f.series {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		s := f.series[id]
		fmt.Fprintf(&sb, "series %d name=%q author=%v\n", s.ID, s.Name, s.AuthorID)
	}
	bids := make([]string, 0, len(f.books))
	for id := range f.books {
		bids = append(bids, id)
	}
	sort.Strings(bids)
	for _, id := range bids {
		b := f.books[id]
		sid := "nil"
		if b.SeriesID != nil {
			sid = fmt.Sprintf("%d", *b.SeriesID)
		}
		desc := "nil"
		if b.Description != nil {
			desc = *b.Description
		}
		fmt.Fprintf(&sb, "book %s series=%s desc=%q\n", b.ID, sid, desc)
	}
	return sb.String()
}

func newSeriesDedupFixture() *dedupFixtureStore {
	two := 2
	three := 3
	return newDedupFixtureStore(
		[]database.Series{
			{ID: 1, Name: "Foundation"},
			{ID: 2, Name: "foundation"}, // duplicate of 1 after NormalizeString
			{ID: 3, Name: "Dune"},       // no duplicate — must be untouched
		},
		[]database.Book{
			{ID: "BOOK1", SeriesID: &two, Description: strPtr("heavy one")},
			{ID: "BOOK2", SeriesID: &two, Description: strPtr("heavy two")},
			{ID: "BOOK3", SeriesID: &three, Description: strPtr("heavy three")},
		},
	)
}

// TestDedupSeries_DryRunMakesNoChanges is the test this whole parameter exists
// for. It asserts two things, in this order and on the SAME store:
//
//  1. a dry run mutates NOTHING — the snapshot is identical afterwards, and no
//     write call reached the store at all;
//  2. the apply that follows does EXACTLY what the dry run predicted.
//
// (2) is what stops the two paths drifting. A dry run whose report differs
// from the apply is worse than no dry run, because it will be trusted. Order
// matters: dry-first-then-apply is the only order that proves the prediction
// was made against the un-merged state the apply then acted on.
func TestDedupSeries_DryRunMakesNoChanges(t *testing.T) {
	store := newSeriesDedupFixture()
	before := store.snapshot()

	dry, err := DedupSeries(context.Background(), store, nil, true)
	require.NoError(t, err)
	assert.Empty(t, dry.Errors)
	assert.True(t, dry.DryRun, "result must echo the mode it ran in")

	// (1) nothing moved.
	assert.Equal(t, before, store.snapshot(),
		"dry run must leave the store byte-for-byte unchanged")
	assert.Zero(t, store.writes,
		"dry run must not reach the store with a single UpdateBook or DeleteSeries")
	assert.Len(t, store.series, 3, "all three series rows must survive a dry run")
	require.Contains(t, store.series, 2, "the duplicate series must NOT be deleted")
	require.NotNil(t, store.books["BOOK1"].SeriesID)
	assert.Equal(t, 2, *store.books["BOOK1"].SeriesID, "no book may be reassigned")

	// The preview still has to be informative, not merely harmless.
	assert.Equal(t, 1, dry.TotalMerged, "one series WOULD be merged away")
	assert.Equal(t, 2, dry.TotalBooksReassigned, "two books WOULD move to series 1")

	// (2) the apply matches the prediction, on the same fixture.
	applied, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)
	assert.Empty(t, applied.Errors)
	assert.False(t, applied.DryRun)
	assert.Equal(t, dry.TotalMerged, applied.TotalMerged,
		"apply merged a different number of series than the dry run predicted")
	assert.Equal(t, dry.TotalBooksReassigned, applied.TotalBooksReassigned,
		"apply reassigned a different number of books than the dry run predicted")

	// And the prediction matches the store, not just the other report.
	after := store.snapshot()
	assert.NotEqual(t, before, after, "the real run must actually change something")
	assert.NotContains(t, store.series, 2, "series 2 must be gone after the apply")
	assert.Contains(t, store.series, 1)
	assert.Contains(t, store.series, 3)
	assert.Equal(t, dry.TotalBooksReassigned, store.writes-1,
		"observed book writes (total writes minus the one DeleteSeries) must equal the prediction")
	for _, id := range []string{"BOOK1", "BOOK2"} {
		require.NotNil(t, store.books[id].SeriesID, "book %s lost its series", id)
		assert.Equal(t, 1, *store.books[id].SeriesID, "book %s must move to series 1", id)
		require.NotNil(t, store.books[id].Description,
			"book %s lost its heavy fields in the reassign", id)
	}
	require.NotNil(t, store.books["BOOK3"].SeriesID)
	assert.Equal(t, 3, *store.books["BOOK3"].SeriesID, "the non-duplicate book must be untouched")

	// Idempotency: a dry run after a successful apply reports nothing pending.
	post, err := DedupSeries(context.Background(), store, nil, true)
	require.NoError(t, err)
	assert.Equal(t, 0, post.TotalMerged, "re-running dry after apply must report 0 pending")
	assert.Equal(t, after, store.snapshot())
}

// ── MergeSeries ───────────────────────────────────────────────────────────────

func TestMergeSeries_BasicMerge(t *testing.T) {
	keepID := 10
	mergeID := 20

	keepSeries := &database.Series{ID: keepID, Name: "Original"}
	mergeSeries := &database.Series{ID: mergeID, Name: "Duplicate"}

	var deletedIDs []int
	var updatedBooks []database.Book

	mock := &database.MockStore{}
	mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		switch id {
		case keepID:
			return keepSeries, nil
		case mergeID:
			return mergeSeries, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return []database.BookCore{{ID: "BOOK_X", SeriesID: &id}}, nil
		}
		return nil, nil
	}
	// MergeSeries hydrates the full row via GetBookByID before writing, so the
	// heavy Description field is present at write time and must be preserved.
	heavyDesc := "heavy description that must survive the merge"
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid, Description: strPtr(heavyDesc)}, nil
	}
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		updatedBooks = append(updatedBooks, *book)
		return book, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		deletedIDs = append(deletedIDs, id)
		return nil
	}

	result, err := MergeSeries(context.Background(), mock, "op1", keepID, []int{mergeID}, "", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.MergedCount)
	assert.Empty(t, result.Errors)
	assert.Contains(t, deletedIDs, mergeID)
	require.Len(t, updatedBooks, 1)
	assert.Equal(t, keepID, *updatedBooks[0].SeriesID)
	// Regression: the hydrate-mutate-update path must NOT wipe heavy fields.
	require.NotNil(t, updatedBooks[0].Description)
	assert.Equal(t, heavyDesc, *updatedBooks[0].Description)
}

func TestMergeSeries_CustomRename(t *testing.T) {
	keepID := 10
	keepSeries := &database.Series{ID: keepID, Name: "Old Name"}

	var renamedTo string
	mock := &database.MockStore{}
	mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		return keepSeries, nil
	}
	mock.UpdateSeriesNameFunc = func(id int, name string) error {
		renamedTo = name
		return nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) { return nil, nil }
	mock.DeleteSeriesFunc = func(id int) error { return nil }

	result, err := MergeSeries(context.Background(), mock, "op2", keepID, []int{}, "New Name", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.MergedCount, "no series to merge, just rename")
	assert.Equal(t, "New Name", renamedTo)
}
