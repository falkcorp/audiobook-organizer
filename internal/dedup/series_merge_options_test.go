// file: internal/dedup/series_merge_options_test.go
// version: 1.0.1
// guid: d27a4e90-6b13-4c85-9f0e-a31c58b72d64
// last-edited: 2026-10-13

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These pin the two protections the Authors & series lane's merges carry.
// Merge All used to be one dedup.series-dedup op, which stood the scanner down
// and honoured series_name locks; it is now N dedup.series-merge ops, and a
// protection the replacement lacks is one the change removed.

const lockOpID = "01JSERIESMERGEOPTIONS0000"

// lockSeriesName makes every book's series_name a user-locked field.
func lockSeriesName(s seriesRefStore) {
	s.MockStore.GetMetadataFieldStatesFunc = func(bookID string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: bookID, Field: database.FieldKeySeriesName, OverrideLocked: true}}, nil
	}
}

func TestMergeSeries_HonorLocksLeavesALockedBookAndKeepsItsSeries(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_LOCKED"}}, map[int]int{20: 1})
	lockSeriesName(store)

	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil,
		MergeSeriesOptions{HonorLocks: true})
	require.NoError(t, err)

	assert.Empty(t, *repointed, "a locked book stays on its series")
	assert.NotContains(t, *deleted, 20, "the series it still references must not be deleted")
	assert.Equal(t, 0, result.MergedCount)
	assert.Equal(t, []int{20}, result.LeftIDs, "the series is reported as left behind")
	require.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], "BOOK_LOCKED")
	assert.Contains(t, result.Errors[0], "user-locked", "the reason is the lock, not a generic refusal")
}

func TestMergeSeries_HonorLocksStillMergesAnUnlockedBook(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_FREE"}}, map[int]int{20: 1})

	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil,
		MergeSeriesOptions{HonorLocks: true})
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"BOOK_FREE": 10}, *repointed)
	assert.Equal(t, []int{20}, *deleted)
	assert.Equal(t, 1, result.MergedCount)
	assert.Empty(t, result.LeftIDs)
	assert.Empty(t, result.Errors)
}

// Positive control for the test above: without the option the lock is ignored,
// which is the original MergeSeries contract (a user's explicit action).
func TestMergeSeries_IgnoresLocksWithoutTheOption(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_LOCKED"}}, map[int]int{20: 1})
	lockSeriesName(store)

	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil, MergeSeriesOptions{})
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"BOOK_LOCKED": 10}, *repointed)
	assert.Equal(t, []int{20}, *deleted)
	assert.Equal(t, 1, result.MergedCount)
}

// A lock protects the name the owner chose. Moving a book onto a series spelled
// the same changes nothing the lock guards, so the lock is not consulted.
func TestMergeSeries_HonorLocksIgnoresALockWhenTheNamesMatch(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_LOCKED"}}, map[int]int{20: 1})
	lockSeriesName(store)

	// The kept series is renamed to the merged-from series' own name.
	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "Duplicate", nil,
		MergeSeriesOptions{HonorLocks: true})
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"BOOK_LOCKED": 10}, *repointed)
	assert.Equal(t, []int{20}, *deleted)
	assert.Equal(t, 1, result.MergedCount)
}

func TestMergeSeries_AnUnreadableLockMovesNothing(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_X"}}, map[int]int{20: 1})
	store.MockStore.GetMetadataFieldStatesFunc = func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("store unavailable")
	}

	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil,
		MergeSeriesOptions{HonorLocks: true})
	require.NoError(t, err)

	assert.Empty(t, *repointed, "a lock that cannot be read is not a lock that is absent")
	assert.Empty(t, *deleted)
	assert.Equal(t, []int{20}, result.LeftIDs)
}

func TestMergeSeries_StandsTheScannerDownForTheMerge(t *testing.T) {
	store, deleted, _ := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_FREE"}}, map[int]int{20: 1})
	scan := newFakeScanController()

	_, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil,
		MergeSeriesOptions{Scan: scan})
	require.NoError(t, err)

	assert.Equal(t, []string{lockOpID}, scan.acquires, "acquired once, keyed by the op id")
	assert.Equal(t, 1, scan.releases, "and released when the merge ended")
	assert.Equal(t, 1, scan.renews, "renewed once per merged-from series")
	assert.Equal(t, []int{20}, *deleted)
}

func TestMergeSeries_RefusesWhenTheScannerCannotBeStoodDown(t *testing.T) {
	store, deleted, repointed := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_FREE"}}, map[int]int{20: 1})
	scan := newFakeScanController()
	scan.acquireErr = errors.New("library.scan will not park")

	_, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "New Name", nil,
		MergeSeriesOptions{Scan: scan})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to apply while a library.scan is running")
	assert.Empty(t, *deleted)
	assert.Empty(t, *repointed)
}

func TestMergeSeries_RefusesWithoutAnOperationIDWhenGated(t *testing.T) {
	store, deleted, _ := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_FREE"}}, map[int]int{20: 1})

	_, err := MergeSeries(context.Background(), store, "", 10, []int{20}, "", nil,
		MergeSeriesOptions{Scan: newFakeScanController()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "without an operation id")
	assert.Empty(t, *deleted)
}

func TestMergeSeries_ALostLeaseAbortsAndReportsIt(t *testing.T) {
	store, deleted, _ := newMergeSeriesFixture(t, []database.BookCore{{ID: "BOOK_FREE"}}, map[int]int{20: 1})
	scan := newFakeScanController()
	scan.renewOK = false

	result, err := MergeSeries(context.Background(), store, lockOpID, 10, []int{20}, "", nil,
		MergeSeriesOptions{Scan: scan})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stand-down lease lost")
	assert.Equal(t, []int{20}, *deleted, "what was written before the lease lapsed stays written")
	assert.NotEmpty(t, result.Errors)
	assert.Equal(t, 1, scan.releases)
}
