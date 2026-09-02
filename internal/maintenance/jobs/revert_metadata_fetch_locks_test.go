// file: internal/maintenance/jobs/revert_metadata_fetch_locks_test.go
// version: 1.0.0
// guid: 8b3e6f2d-1c47-4a9e-b5d0-6e2f8a1c4d97
// last-edited: 2026-09-02

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rmfLockFixture is a book whose title AND publisher were fetched. The user
// then edited the title to "My Title" and locked it. Reverting the fetch must
// put the publisher back and leave the user's title exactly where it is.
func rmfLockFixture(t *testing.T, locked func(string) ([]database.MetadataFieldState, error)) (*database.MockStore, **database.Book, *int) {
	t.Helper()
	started := time.Now().Add(-time.Hour)
	prevTitle, _ := json.Marshal("Original Title")
	prevTitleStr := string(prevTitle)
	prevPub, _ := json.Marshal("Original Press")
	prevPubStr := string(prevPub)
	fetchedPub := "Fetched Press"

	var updated *database.Book
	authorLookups := 0
	store := &database.MockStore{
		GetOperationByIDFunc: func(id string) (*database.Operation, error) {
			return &database.Operation{ID: id, Type: "bulk_metadata_fetch", CreatedAt: started, StartedAt: &started}, nil
		},
		GetOperationResultsFunc: func(opID string) ([]database.OperationResult, error) {
			return []database.OperationResult{{OperationID: opID, BookID: "book-1", Status: "updated"}}, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "My Title", Publisher: &fetchedPub}, nil
		},
		GetBookChangeHistoryFunc: func(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
			return []database.MetadataChangeRecord{
				{BookID: bookID, Field: "title", ChangeType: "fetched", ChangedAt: time.Now(), PreviousValue: &prevTitleStr},
				{BookID: bookID, Field: "publisher", ChangeType: "fetched", ChangedAt: time.Now(), PreviousValue: &prevPubStr},
			}, nil
		},
		GetAuthorByNameFunc: func(string) (*database.Author, error) {
			authorLookups++
			return nil, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updated = b
			return b, nil
		},
		GetMetadataFieldStatesFunc: locked,
	}
	return store, &updated, &authorLookups
}

func runRevert(t *testing.T, store *database.MockStore) error {
	t.Helper()
	j, err := maintenance.Get("revert-metadata-fetch")
	require.NoError(t, err)
	ctx := maintenance.WithOperationID(context.Background(), "op-revert-1")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(`{"fetch_op_ids":["fetch-op-a"]}`))
	return j.Run(ctx, store, &noopReporter{}, false)
}

func TestRevertMetadataFetchJob_LockedTitleStaysWhilePublisherReverts(t *testing.T) {
	store, updated, _ := rmfLockFixture(t, func(id string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeyTitle, OverrideLocked: true}}, nil
	})
	require.NoError(t, runRevert(t, store))
	require.NotNil(t, *updated, "the unlocked publisher must still be reverted")
	assert.Equal(t, "My Title", (*updated).Title, "the user's locked title must not be reverted")
	require.NotNil(t, (*updated).Publisher)
	assert.Equal(t, "Original Press", *(*updated).Publisher)
}

func TestRevertMetadataFetchJob_UnlockedTitleReverts(t *testing.T) {
	store, updated, _ := rmfLockFixture(t, nil)
	require.NoError(t, runRevert(t, store))
	require.NotNil(t, *updated)
	assert.Equal(t, "Original Title", (*updated).Title, "fixture cannot observe the lock if the unlocked title does not revert")
}

func TestRevertMetadataFetchJob_LockReadErrorWritesNothing(t *testing.T) {
	store, updated, _ := rmfLockFixture(t, func(string) ([]database.MetadataFieldState, error) {
		return nil, errors.New("pebble: closed")
	})
	require.NoError(t, runRevert(t, store))
	assert.Nil(t, *updated, "fail closed: an unreadable lock set must not be treated as unlocked")
}

// A locked author_name must be dropped BEFORE the switch, or the job resolves
// an author row for a value it is not going to write.
func TestRevertMetadataFetchJob_LockedAuthorSkipsTheLookup(t *testing.T) {
	store, _, lookups := rmfLockFixture(t, func(id string) ([]database.MetadataFieldState, error) {
		return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeyAuthorName, OverrideLocked: true}}, nil
	})
	prev, _ := json.Marshal("Old Author")
	prevStr := string(prev)
	store.GetBookChangeHistoryFunc = func(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
		return []database.MetadataChangeRecord{
			{BookID: bookID, Field: "author_name", ChangeType: "fetched", ChangedAt: time.Now(), PreviousValue: &prevStr},
		}, nil
	}
	require.NoError(t, runRevert(t, store))
	assert.Equal(t, 0, *lookups)
}
