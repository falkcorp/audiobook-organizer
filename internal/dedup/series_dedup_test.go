// file: internal/dedup/series_dedup_test.go
// version: 1.5.0
// guid: f6a7b8c9-d0e1-2345-fabc-456789012345
// last-edited: 2026-08-30

package dedup

import (
	"context"
	"errors"
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

// --- TASK-044: the unfiltered ref-count guard -------------------------------
//
// GetBooksBySeriesIDCore hides trashed and non-primary rows. The merge loop
// reassigns what that getter returns and then deletes the series, so any hidden
// row is stranded on a series ID that no longer resolves. database/series_bookref.go
// records the production result of exactly this shape: 6,893 phantom series IDs
// held by 13,322 live books.
//
// These tests seed the divergence directly -- one VISIBLE book, three total
// references -- because a fixture where the two counts agree passes with or
// without the guard.

// seriesRefStore is a MockStore that can also answer the unfiltered question.
// MockStore does implement SeriesBookRefStore, but its zero value returns an
// empty map, which reads as "nothing references anything" and would let every
// delete through -- so the counts must be supplied explicitly.
type seriesRefStore struct {
	*database.MockStore
	refCounts map[int]int
}

func (s seriesRefStore) GetAllSeriesBookRefCounts() (map[int]int, error) {
	return s.refCounts, nil
}

// newSeriesMergeFixture builds the two-same-name-series setup shared below.
// Series 2 merges into series 1 and has exactly ONE book visible to the
// filtered getter.
func newSeriesMergeFixture(t *testing.T, refCounts map[int]int) (seriesRefStore, *[]int) {
	t.Helper()
	deleted := &[]int{}
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Foundation"}, {ID: 2, Name: "Foundation"}}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 2 {
			return []database.BookCore{{ID: "VISIBLE1", SeriesID: &id}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 2
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(_ string, b *database.Book) (*database.Book, error) { return b, nil }
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return seriesRefStore{MockStore: mock, refCounts: refCounts}, deleted
}

func TestDedupSeries_RefusesDeleteThatWouldStrandHiddenBooks(t *testing.T) {
	// One book visible to the filtered getter, THREE rows actually pointing at
	// series 2. The other two are trashed or non-primary -- invisible to the
	// reassignment, and orphaned if the row is deleted.
	store, deleted := newSeriesMergeFixture(t, map[int]int{2: 3})

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)

	assert.NotContains(t, *deleted, 2, "series 2 must survive: two rows still reference it that the merge could not reassign")
	assert.Equal(t, 0, result.TotalMerged, "a refused delete is not a completed merge")
	require.Len(t, result.Errors, 1, "the refusal must be reported, not silently skipped")
	assert.Contains(t, result.Errors[0], "still reference it")

	// The visible book IS still reassigned. Moving it is strictly an
	// improvement and is deliberately not rolled back -- only the row removal
	// is refused.
	assert.Equal(t, 1, result.TotalBooksReassigned)
}

func TestDedupSeries_StillDeletesWhenNothingHiddenReferencesIt(t *testing.T) {
	// POSITIVE CONTROL. Without this, a guard that refuses every delete passes
	// the test above and the op silently stops doing its job.
	store, deleted := newSeriesMergeFixture(t, map[int]int{2: 1})

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)

	assert.Contains(t, *deleted, 2, "the one reference was reassigned, so the row is genuinely unreferenced and must be deleted")
	assert.Equal(t, 1, result.TotalMerged)
	assert.Empty(t, result.Errors)
}

func TestDedupSeries_DryRunMakesTheSameRefusalAsApply(t *testing.T) {
	// The guard sits BEFORE the dryRun branch so preview and apply cannot
	// disagree. A dry run that reports a merge the real run would refuse is
	// worse than no dry run, because it will be trusted -- and TASK-029 is
	// queued to edit this same loop.
	store, deleted := newSeriesMergeFixture(t, map[int]int{2: 3})

	result, err := DedupSeries(context.Background(), store, nil, true)
	require.NoError(t, err)

	assert.True(t, result.DryRun)
	assert.Empty(t, *deleted, "a dry run must never delete")
	assert.Equal(t, 0, result.TotalMerged, "the preview must predict the refusal, not report a merge that will not happen")
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "still reference it")
}

// --- Review follow-ups to TASK-044 ------------------------------------------

// TestDedupSeries_RefusesDeleteWhenAReassignmentFailed pins the defect two
// reviewers found in the first cut of this guard: the subtrahend was
// len(books) -- what the loop ATTEMPTED -- rather than what it actually moved.
//
// Series 2 has two books visible to the filtered getter and an unfiltered count
// of 2, so nothing is hidden and the old arithmetic computed 2-2 == 0 and
// deleted the row. But book B's UpdateBook fails, so B is still pointing at
// series 2 when it is deleted: a phantom series ID produced BY the guard, on
// the failure path the guard exists to cover.
func TestDedupSeries_RefusesDeleteWhenAReassignmentFailed(t *testing.T) {
	deleted := []int{}
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Foundation"}, {ID: 2, Name: "Foundation"}}, nil
	}
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == 2 {
			return []database.BookCore{{ID: "A"}, {ID: "B"}}, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := 2
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "B" {
			return nil, errors.New("pebble write failed")
		}
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}
	// Unfiltered count agrees with the filtered getter: 2 books, nothing hidden.
	store := seriesRefStore{MockStore: mock, refCounts: map[int]int{2: 2}}

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)

	assert.NotContains(t, deleted, 2,
		"book B's reassignment failed, so B still references series 2 — deleting it strands B")
	assert.Equal(t, 1, result.TotalBooksReassigned, "only A moved")
	assert.Equal(t, 0, result.TotalMerged, "a refused delete is not a completed merge")
}

// TestDedupSeries_RefusesToRunWithoutTheUnfilteredCount pins the fail-closed
// claim itself, which had no test at all. A store that cannot answer the
// unfiltered question must abort the whole op — never fall back to the filtered
// count, which is the original bug.
func TestDedupSeries_RefusesToRunWithoutTheUnfilteredCount(t *testing.T) {
	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{{ID: 1, Name: "Foundation"}, {ID: 2, Name: "Foundation"}}, nil
	}
	mock.DeleteSeriesFunc = func(int) error {
		t.Fatal("must not delete anything when the unfiltered count is unavailable")
		return nil
	}
	// noRefCounts hides MockStore's own GetAllSeriesBookRefCounts so the
	// capability lookup genuinely fails. Embedding the interface rather than
	// the struct is what removes the promoted method.
	store := noRefCountStore{Store: mock}

	_, err := DedupSeries(context.Background(), store, nil, false)
	require.Error(t, err, "a store that cannot count unfiltered references must abort the op, not proceed")
	assert.Contains(t, err.Error(), "unfiltered reference counts")
}

// noRefCountStore is a database.Store that deliberately does NOT satisfy
// SeriesBookRefStore. Embedding the interface (not *MockStore) drops the
// promoted GetAllSeriesBookRefCounts, which is the only way to model a store
// that cannot answer.
type noRefCountStore struct{ database.Store }

// --- TASK-029: the non-primary half of the orphaning hazard ------------------

// TestDedupSeries_RelinksNonPrimaryVersionBooks is the regression gate for the
// change that switched the merge loop from GetBooksBySeriesIDCore to
// GetBooksBySeriesIDAllVersions.
//
// The hazard it pins: a NON-PRIMARY version whose only series link is the one
// about to be deleted. The listing getter hides it precisely because it
// duplicates a book already in the list — but "duplicate for display" and
// "safe to strand" are different claims, and the merge loop conflated them. It
// reassigned only what the filtered getter returned and then called
// DeleteSeries, leaving the hidden row pointing at an ID that no longer
// resolves. That is the shape behind the 6,893 phantom series IDs held by
// 13,322 live books in database/series_bookref.go.
//
// The fixture makes the two getters DISAGREE — Core sees one book, AllVersions
// sees two — because a fixture where they agree passes with or without the
// change. It asserts the positive outcome (the row is RELINKED) rather than
// only that the delete was refused: refusing to delete would also avoid the
// orphan, but it would leave the duplicate series in place forever and quietly
// stop the op from doing its job.
func TestDedupSeries_RelinksNonPrimaryVersionBooks(t *testing.T) {
	const (
		primaryBookID    = "PRIMARY1"
		nonPrimaryBookID = "NONPRIMARY1"
		keepSeriesID     = 1
		mergeSeriesID    = 2
	)

	deleted := []int{}
	// seriesAfterUpdate records what each book's SeriesID actually became, so
	// the assertions below read the WRITE rather than inferring it from a call
	// count. A count cannot tell "relinked to the kept series" apart from
	// "written back unchanged".
	seriesAfterUpdate := map[string]int{}

	mock := &database.MockStore{}
	mock.GetAllSeriesFunc = func() ([]database.Series, error) {
		return []database.Series{
			{ID: keepSeriesID, Name: "Foundation"},
			{ID: mergeSeriesID, Name: "Foundation"},
		}, nil
	}

	// The LISTING getter hides the non-primary version — this is correct
	// behaviour for it, and it is what the merge loop used to consult.
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeSeriesID {
			sid := id
			return []database.BookCore{{ID: primaryBookID, SeriesID: &sid}}, nil
		}
		return nil, nil
	}
	// The COMPLETE getter sees both. Set explicitly rather than relying on the
	// mock's fallback to the Core stub: this test exists to prove the merge
	// loop reads THIS one, so the two must return different sets.
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeSeriesID {
			sid := id
			return []database.BookCore{
				{ID: primaryBookID, SeriesID: &sid},
				{ID: nonPrimaryBookID, SeriesID: &sid},
			}, nil
		}
		return nil, nil
	}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := mergeSeriesID
		primary := id == primaryBookID
		return &database.Book{ID: id, SeriesID: &sid, IsPrimaryVersion: &primary}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			seriesAfterUpdate[id] = *b.SeriesID
		}
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		deleted = append(deleted, id)
		return nil
	}

	// TWO live rows reference series 2. The unfiltered guard only lets the
	// delete through once BOTH have been reassigned, so this count is also
	// what makes the delete assertion below meaningful.
	store := seriesRefStore{MockStore: mock, refCounts: map[int]int{mergeSeriesID: 2}}

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)

	// The point of the whole task: the hidden row is RELINKED, not orphaned.
	require.Contains(t, seriesAfterUpdate, nonPrimaryBookID,
		"the non-primary version was never written — the merge loop is still reading the "+
			"filtered listing getter, and DeleteSeries will strand this row")
	assert.Equal(t, keepSeriesID, seriesAfterUpdate[nonPrimaryBookID],
		"the non-primary version must be repointed at the KEPT series")
	assert.Equal(t, keepSeriesID, seriesAfterUpdate[primaryBookID],
		"the primary book must still be repointed — the switch must not regress the "+
			"rows that already worked")

	// Positive control. Refusing the delete would also prevent the orphan, so
	// without this a guard that never deletes would pass everything above
	// while silently disabling the op.
	assert.Contains(t, deleted, mergeSeriesID,
		"both referencing rows were reassigned, so series 2 is genuinely unreferenced "+
			"and must be deleted")
	assert.Equal(t, 1, result.TotalMerged)
	assert.Equal(t, 2, result.TotalBooksReassigned,
		"both rows count as reassigned, including the one the listing getter hides")
	assert.Empty(t, result.Errors)
}

// --- SERIES-DELETE-UNGUARDED (#2908): MergeSeries's unfiltered ref guard -----
//
// MergeSeries had NO reference guard at all: it enumerated with
// GetBooksBySeriesIDAllVersions, appended every reassignment failure to
// result.Errors, and then called DeleteSeries(mergeID) UNCONDITIONALLY -- after
// a reassignment it had just recorded as failed, and after an enumeration that
// returned nothing because every referencing row is trashed.
//
// The fixtures below all seed the FILTERED/UNFILTERED asymmetry that is the bug:
// what GetBooksBySeriesIDAllVersions returns and what the unfiltered counter
// says DISAGREE. A fixture where the two agree passes with or without the guard.

// newMergeSeriesFixture builds a keep+merge pair whose merged-away series
// enumerates `visible` and whose unfiltered reference count is refCounts.
func newMergeSeriesFixture(
	t *testing.T,
	visible []database.BookCore,
	refCounts map[int]int,
) (seriesRefStore, *[]int, *map[string]int) {
	t.Helper()
	const (
		keepID  = 10
		mergeID = 20
	)
	deleted := &[]int{}
	repointed := &map[string]int{}
	*repointed = map[string]int{}

	mock := &database.MockStore{}
	mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		switch id {
		case keepID:
			return &database.Series{ID: keepID, Name: "Original"}, nil
		case mergeID:
			return &database.Series{ID: mergeID, Name: "Duplicate"}, nil
		}
		return nil, nil
	}
	// Set EXPLICITLY, both getters. MockStore.GetBooksBySeriesIDAllVersions
	// falls back to GetBooksBySeriesIDCoreFunc when its own stub is nil, so
	// leaving this unset would silently model a different store than the one
	// MergeSeries actually reads.
	mock.GetBooksBySeriesIDCoreFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return visible, nil
		}
		return nil, nil
	}
	mock.GetBooksBySeriesIDAllVersionsFunc = func(id int) ([]database.BookCore, error) {
		if id == mergeID {
			return visible, nil
		}
		return nil, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		sid := mergeID
		return &database.Book{ID: id, SeriesID: &sid}, nil
	}
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if b.SeriesID != nil {
			(*repointed)[id] = *b.SeriesID
		}
		return b, nil
	}
	mock.DeleteSeriesFunc = func(id int) error {
		*deleted = append(*deleted, id)
		return nil
	}
	return seriesRefStore{MockStore: mock, refCounts: refCounts}, deleted, repointed
}

// TestMergeSeries_RefusesDeleteWhenEveryReferencingBookIsTrashed is the
// headline case from #2908.
//
// Both series getters skip soft-deleted books, so a series whose books are ALL
// TRASHED enumerates EMPTY. The old loop reassigned nothing, recorded no error,
// and deleted the row anyway -- leaving two trashed rows holding a series ID
// that no longer resolves. This is not an empty fixture: two books genuinely
// reference series 20, they are simply invisible to the getter.
func TestMergeSeries_RefusesDeleteWhenEveryReferencingBookIsTrashed(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, nil, map[int]int{20: 2})

	result, err := MergeSeries(context.Background(), store, "op-trashed", 10, []int{20}, "", nil)
	require.NoError(t, err)

	assert.NotContains(t, *deleted, 20,
		"series 20 must survive: two trashed rows still reference it and cannot be repointed")
	assert.Equal(t, 0, result.MergedCount, "a refused delete is not a completed merge")
	require.Len(t, result.Errors, 1, "the refusal must be reported, not silently skipped")
	assert.Contains(t, result.Errors[0], "still reference it")
	assert.Empty(t, *repointed, "nothing was visible to repoint")
}

// TestMergeSeries_StillDeletesWhenNothingHiddenReferencesIt is the POSITIVE
// CONTROL. Without it, a guard that refuses EVERY delete passes every test
// above while silently turning the merge op into a no-op.
func TestMergeSeries_StillDeletesWhenNothingHiddenReferencesIt(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t,
		[]database.BookCore{{ID: "BOOK_X"}}, map[int]int{20: 1})

	result, err := MergeSeries(context.Background(), store, "op-normal", 10, []int{20}, "", nil)
	require.NoError(t, err)

	assert.Contains(t, *deleted, 20,
		"the one reference was reassigned, so series 20 is genuinely unreferenced and must be deleted")
	assert.Equal(t, 1, result.MergedCount)
	assert.Empty(t, result.Errors)
	assert.Equal(t, 10, (*repointed)["BOOK_X"])
}

// TestMergeSeries_RefusesDeleteWhenAReassignmentFailed pins the subtrahend.
//
// Nothing is hidden here -- the unfiltered count is 2 and the getter returns 2
// -- so a guard that subtracts len(books) (what was ATTEMPTED) computes 2-2 == 0
// and deletes the row while book B is still pointing at it. Only subtracting
// what actually MOVED catches it. This is the mutation gate for `moved`.
func TestMergeSeries_RefusesDeleteWhenAReassignmentFailed(t *testing.T) {
	store, deleted, _ := newMergeSeriesFixture(t,
		[]database.BookCore{{ID: "A"}, {ID: "B"}}, map[int]int{20: 2})
	store.MockStore.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == "B" {
			return nil, errors.New("pebble write failed")
		}
		return b, nil
	}

	result, err := MergeSeries(context.Background(), store, "op-failwrite", 10, []int{20}, "", nil)
	require.NoError(t, err)

	assert.NotContains(t, *deleted, 20,
		"book B's reassignment failed, so B still references series 20 — deleting it strands B")
	assert.Equal(t, 0, result.MergedCount, "a refused delete is not a completed merge")
	require.Len(t, result.Errors, 2, "the failed write AND the refusal must both be reported")
	assert.Contains(t, strings.Join(result.Errors, "; "), "still reference it")
}

// TestMergeSeries_RefusesDeleteWhenABookCannotBeHydrated covers the one branch
// in the loop that was entirely SILENT: GetBookByID returning (nil, nil).
//
// A row the series getter listed but a point-get cannot hydrate is
// indistinguishable from a row that still holds mergeID, so it must surface as
// an error AND must not count toward what was moved. It used to be a bare
// continue that fell straight through to the delete.
func TestMergeSeries_RefusesDeleteWhenABookCannotBeHydrated(t *testing.T) {
	store, deleted, _ := newMergeSeriesFixture(t,
		[]database.BookCore{{ID: "GHOST"}}, map[int]int{20: 1})
	store.MockStore.GetBookByIDFunc = func(string) (*database.Book, error) { return nil, nil }

	result, err := MergeSeries(context.Background(), store, "op-ghost", 10, []int{20}, "", nil)
	require.NoError(t, err)

	assert.NotContains(t, *deleted, 20,
		"GHOST could not be hydrated, so it may still reference series 20")
	assert.Equal(t, 0, result.MergedCount)
	require.Len(t, result.Errors, 2, "the unhydratable row AND the refusal must both be reported")
	assert.Contains(t, strings.Join(result.Errors, "; "), "vanished between the series scan and hydration")
	assert.Contains(t, strings.Join(result.Errors, "; "), "still reference it")
}

// TestMergeSeries_RefusesToRunWithoutTheUnfilteredCount pins the fail-closed
// claim. A store that cannot answer the unfiltered question must abort the
// merge — never fall back to the filtered count, which is the original bug.
func TestMergeSeries_RefusesToRunWithoutTheUnfilteredCount(t *testing.T) {
	mock := &database.MockStore{}
	mock.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		return &database.Series{ID: id, Name: "Whatever"}, nil
	}
	mock.DeleteSeriesFunc = func(int) error {
		t.Fatal("must not delete anything when the unfiltered count is unavailable")
		return nil
	}
	store := noRefCountStore{Store: mock}

	_, err := MergeSeries(context.Background(), store, "op-noref", 10, []int{20}, "", nil)
	require.Error(t, err, "a store that cannot count unfiltered references must abort the merge")
	assert.Contains(t, err.Error(), "unfiltered reference counts")
}
