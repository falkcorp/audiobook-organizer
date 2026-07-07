// file: internal/dedup/series_dedup_test.go
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-456789012345
// last-edited: 2026-07-07

package dedup

import (
	"context"
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

	result, err := DedupSeries(context.Background(), mock, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalMerged)
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

	result, err := DedupSeries(context.Background(), mock, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, result.TotalMerged)
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
