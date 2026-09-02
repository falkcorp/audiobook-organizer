// file: internal/dedup/field_locks_test.go
// version: 1.0.0
// guid: 1e6b8d2c-5f93-4a07-b7c4-9d3e2a8f0b61
// last-edited: 2026-09-02

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lockRows(bookID string, keys ...string) func(string) ([]database.MetadataFieldState, error) {
	return func(id string) ([]database.MetadataFieldState, error) {
		if id != bookID {
			return nil, nil
		}
		rows := make([]database.MetadataFieldState, 0, len(keys))
		for _, k := range keys {
			rows = append(rows, database.MetadataFieldState{BookID: id, Field: k, OverrideLocked: true})
		}
		return rows, nil
	}
}

// ── MergeSplitBookCluster: keep.Title ───────────────────────────────────────

// The keep's title is user-locked; the candidate's suggested title must not
// replace it. The unlocked duration recount still lands on the same write.
func TestMergeSplitBookCluster_LockedKeepTitleStands(t *testing.T) {
	const keepID, src1, src2 = "K", "S1", "S2"
	m := newSplitMergeMock(src1, src2, "", map[string]bool{}, map[string]bool{})
	m.GetMetadataFieldStatesFunc = lockRows(keepID, database.FieldKeyTitle)
	m.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case src1:
			return []database.BookFile{{ID: "f1", BookID: src1, Duration: 100}}, nil
		case src2:
			return []database.BookFile{{ID: "f2", BookID: src2, Duration: 200}}, nil
		default:
			return []database.BookFile{{ID: "f1", Duration: 100}, {ID: "f2", Duration: 200}}, nil
		}
	}
	var keepWrite *database.Book
	prev := m.UpdateBookFunc
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == keepID {
			cp := *b
			keepWrite = &cp
		}
		return prev(id, b)
	}

	result, err := MergeSplitBookCluster(m, keepID, []string{src1, src2}, "Suggested Title")
	require.NoError(t, err)
	assert.Empty(t, result.Errors)
	assert.True(t, result.TitleKeptLocked, "the result must say the title was kept")
	require.NotNil(t, keepWrite, "the keep must still be written for its duration")
	assert.Equal(t, "book K", keepWrite.Title, "the user's title must stand")
	require.NotNil(t, keepWrite.Duration)
	assert.Equal(t, 300, *keepWrite.Duration, "the unlocked duration recount must still land")
}

func TestMergeSplitBookCluster_UnlockedKeepTitleChanges(t *testing.T) {
	const keepID, src1, src2 = "K", "S1", "S2"
	m := newSplitMergeMock(src1, src2, "", map[string]bool{}, map[string]bool{})
	var keepWrite *database.Book
	prev := m.UpdateBookFunc
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == keepID {
			cp := *b
			keepWrite = &cp
		}
		return prev(id, b)
	}

	result, err := MergeSplitBookCluster(m, keepID, []string{src1, src2}, "Suggested Title")
	require.NoError(t, err)
	assert.False(t, result.TitleKeptLocked)
	require.NotNil(t, keepWrite)
	assert.Equal(t, "Suggested Title", keepWrite.Title, "fixture cannot observe the bug if the unlocked title does not change")
}

func TestMergeSplitBookCluster_LockReadErrorKeepsTitle(t *testing.T) {
	const keepID, src1, src2 = "K", "S1", "S2"
	m := newSplitMergeMock(src1, src2, "", map[string]bool{}, map[string]bool{})
	m.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: closed")
	}
	var keepWrite *database.Book
	prev := m.UpdateBookFunc
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		if id == keepID {
			cp := *b
			keepWrite = &cp
		}
		return prev(id, b)
	}

	result, err := MergeSplitBookCluster(m, keepID, []string{src1, src2}, "Suggested Title")
	require.NoError(t, err)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], database.ErrFieldLocksUnavailable.Error())
	require.NotNil(t, keepWrite)
	assert.Equal(t, "book K", keepWrite.Title, "fail closed: an unreadable lock set keeps the title")
}

// ── DedupSeries: SeriesID ───────────────────────────────────────────────────

// withRefCounts makes the fixture answer the unfiltered reference-count
// question from its own books. Without it MockStore returns an empty map, the
// refuse-to-delete guard computes 0 - moved and never fires, and a test cannot
// observe whether a series a locked book still points at was deleted.
func withRefCounts(store *dedupFixtureStore) *dedupFixtureStore {
	store.GetAllSeriesBookRefCountsFunc = func() (map[int]int, error) {
		out := map[int]int{}
		for _, b := range store.books {
			if b.SeriesID != nil {
				out[*b.SeriesID]++
			}
		}
		return out, nil
	}
	return store
}

// BOOK1's series name is user-locked as "foundation" (series 2). The canonical
// for the group is series 1 "Foundation" -- a different spelling -- so BOOK1
// stays on series 2 and series 2 is kept alive for it. BOOK2 (unlocked) moves.
func TestDedupSeries_LockedSeriesNameIsNotRepointedToADifferentSpelling(t *testing.T) {
	store := withRefCounts(newSeriesDedupFixture())
	store.GetMetadataFieldStatesFunc = lockRows("BOOK1", database.FieldKeySeriesName)

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)

	require.NotNil(t, store.books["BOOK1"].SeriesID)
	assert.Equal(t, 2, *store.books["BOOK1"].SeriesID, "the locked book must stay on its series")
	require.NotNil(t, store.books["BOOK2"].SeriesID)
	assert.Equal(t, 1, *store.books["BOOK2"].SeriesID, "the unlocked sibling must still move")
	assert.Contains(t, store.series, 2, "a series a locked book still points at must not be deleted")
	assert.Equal(t, 1, result.TotalBooksReassigned)
	assert.Equal(t, 0, result.TotalMerged, "the duplicate could not be merged away")
	require.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], "user-locked")
}

// The same lock on a group whose members are spelled IDENTICALLY is not a
// reason to refuse: the repoint preserves the user's value byte for byte.
func TestDedupSeries_LockedSeriesNameMovesWhenSpellingIsIdentical(t *testing.T) {
	two := 2
	store := withRefCounts(newDedupFixtureStore(
		[]database.Series{{ID: 1, Name: "Foundation"}, {ID: 2, Name: "Foundation"}},
		[]database.Book{{ID: "BOOK1", SeriesID: &two}},
	))
	store.GetMetadataFieldStatesFunc = lockRows("BOOK1", database.FieldKeySeriesName)

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)
	assert.Empty(t, result.Errors)
	require.NotNil(t, store.books["BOOK1"].SeriesID)
	assert.Equal(t, 1, *store.books["BOOK1"].SeriesID)
	assert.NotContains(t, store.series, 2)
}

func TestDedupSeries_LockReadErrorKeepsTheBookAndItsSeries(t *testing.T) {
	store := withRefCounts(newSeriesDedupFixture())
	store.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: closed")
	}

	result, err := DedupSeries(context.Background(), store, nil, false)
	require.NoError(t, err)
	assert.Equal(t, 2, *store.books["BOOK1"].SeriesID)
	assert.Equal(t, 2, *store.books["BOOK2"].SeriesID)
	assert.Contains(t, store.series, 2)
	assert.Equal(t, 0, result.TotalBooksReassigned)
	require.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], database.ErrFieldLocksUnavailable.Error())
}
